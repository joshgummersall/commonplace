// Package web serves the Commonplace web app: server-rendered pages that
// htmx updates in place.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/search"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/joshgummersall/commonplace/internal/themes"
)

//go:embed templates static
var embedded embed.FS

// Server holds what the handlers need.
type Server struct {
	store    *store.Store
	searcher *search.Searcher
	// embedModel names the embeddings Daily uses to vary its set; "" when
	// there is no embedder.
	embedModel string
	// themes refreshes themes; nil without an embedder and a namer.
	themes *themes.Refresher
	logger *slog.Logger
	now    func() time.Time
	files  fs.FS
	dev    bool

	mu   sync.Mutex
	tmpl *template.Template
}

// Options configures a Server.
type Options struct {
	// DevDir, when set, serves templates and static files from this
	// directory (the internal/web source folder) and re-reads templates
	// on every request, so edits show up without a rebuild.
	DevDir string
	// Embedder enables semantic search and similar highlights. Without
	// it, search is keyword only.
	Embedder embedding.Embedder
	// Namer names themes. With Embedder, it enables refreshing themes.
	Namer  themes.Namer
	Logger *slog.Logger
	Now    func() time.Time
}

// New builds a Server. It parses templates up front so a broken template
// fails at startup rather than on first request.
func New(s *store.Store, opts Options) (*Server, error) {
	srv := &Server{store: s, logger: opts.Logger, now: opts.Now, files: embedded}
	srv.searcher = &search.Searcher{Store: s}
	if opts.Embedder != nil {
		srv.embedModel = opts.Embedder.Model()
		srv.searcher.Embedder = opts.Embedder
		srv.searcher.Index = search.NewIndex(s, opts.Embedder.Model())
	}
	if srv.logger == nil {
		srv.logger = slog.Default()
	}
	if srv.now == nil {
		srv.now = time.Now
	}
	if opts.Embedder != nil && opts.Namer != nil {
		srv.themes = themes.NewRefresher(s, opts.Embedder.Model(), opts.Namer, srv.now, srv.logger)
	}
	if opts.DevDir != "" {
		srv.files = os.DirFS(opts.DevDir)
		srv.dev = true
	}
	if _, err := srv.templates(); err != nil {
		return nil, err
	}
	return srv, nil
}

// Handler returns the app's routes, wrapped in cross-origin protection:
// the server listens on localhost, and without it any web page you visit
// could POST to it.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(s.files, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pages link assets with a ?v= content hash (see asset), so a
		// versioned URL never changes and can be cached for good. In dev
		// mode files change under the same URL, so always revalidate.
		switch {
		case s.dev:
			w.Header().Set("Cache-Control", "no-cache")
		case r.URL.Query().Has("v"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /{$}", s.handle(s.daily))
	mux.HandleFunc("GET /search", s.handle(s.search))
	mux.HandleFunc("GET /h/{id}", s.handle(s.highlight))
	mux.HandleFunc("GET /h/{id}/similar", s.handle(s.similar))
	mux.HandleFunc("POST /h/{id}/favorite", s.handle(s.favorite))
	mux.HandleFunc("POST /h/{id}/bury", s.handle(s.bury))
	mux.HandleFunc("POST /h/{id}/unbury", s.handle(s.unbury))
	mux.HandleFunc("POST /h/{id}/notes", s.handle(s.addNote))
	mux.HandleFunc("GET /notes/{id}", s.handle(s.note))
	mux.HandleFunc("GET /notes/{id}/edit", s.handle(s.editNote))
	mux.HandleFunc("PUT /notes/{id}", s.handle(s.updateNote))
	mux.HandleFunc("DELETE /notes/{id}", s.handle(s.deleteNote))
	mux.HandleFunc("POST /notes/{id}/restore", s.handle(s.restoreNote))
	mux.HandleFunc("GET /themes", s.handle(s.themeList))
	mux.HandleFunc("GET /themes/refresh", s.handle(s.refreshStatus))
	mux.HandleFunc("POST /themes/refresh", s.handle(s.refreshThemes))
	mux.HandleFunc("GET /themes/{id}", s.handle(s.themeRow))
	mux.HandleFunc("GET /themes/{id}/rename", s.handle(s.renameForm))
	mux.HandleFunc("PUT /themes/{id}", s.handle(s.renameTheme))

	return http.NewCrossOriginProtection().Handler(mux)
}

// httpError carries a status code for errors the user caused.
type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return httpError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

// handle adapts an error-returning handler: user errors get their status
// and message, store.ErrNotFound becomes 404, and anything else is logged
// and shown as a generic 500.
func (s *Server) handle(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Responses differ for htmx and full-page requests to one URL.
		w.Header().Add("Vary", "HX-Request")
		err := h(w, r)
		if err == nil {
			return
		}
		var he httpError
		switch {
		case errors.As(err, &he):
			http.Error(w, he.msg, he.status)
		case errors.Is(err, store.ErrNotFound):
			http.Error(w, "Not found", http.StatusNotFound)
		default:
			s.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			http.Error(w, "Something went wrong; see the server log.", http.StatusInternalServerError)
		}
	}
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func (s *Server) templates() (*template.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tmpl != nil && !s.dev {
		return s.tmpl, nil
	}
	fm := template.FuncMap{"asset": s.asset}
	t, err := template.New("").Funcs(funcs).Funcs(fm).ParseFS(s.files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	s.tmpl = t
	return t, nil
}

// asset returns a static file's URL with a short content hash, so browsers
// fetch a file again exactly when it changes.
func (s *Server) asset(name string) (string, error) {
	b, err := fs.ReadFile(s.files, "static/"+name)
	if err != nil {
		return "", fmt.Errorf("asset %s: %w", name, err)
	}
	sum := sha256.Sum256(b)
	return "/static/" + name + "?v=" + hex.EncodeToString(sum[:4]), nil
}

// render executes a named template into a buffer first, so a template
// error yields a clean 500 rather than half a page.
func (s *Server) render(w http.ResponseWriter, name string, data any) error {
	t, err := s.templates()
	if err != nil {
		return err
	}
	var b strings.Builder
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = w.Write([]byte(b.String()))
	return err
}

var funcs = template.FuncMap{
	// paragraphs splits highlight text on the line breaks kept from the
	// device, so multi-paragraph passages render as paragraphs.
	"paragraphs": func(text string) []string {
		return strings.Split(text, "\n")
	},
	"date": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("Jan 2, 2006")
	},
	"sourceName": func(source string) string {
		switch source {
		case store.UserSource:
			return "You"
		case "kindle":
			return "Kindle"
		case "crosspoint":
			return "CrossPoint"
		default:
			return source
		}
	},
	"ago": func(now, t time.Time) string {
		d := now.Sub(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return humanCount(int(d.Minutes()), "minute") + " ago"
		case d < 24*time.Hour:
			return humanCount(int(d.Hours()), "hour") + " ago"
		}
		return "on " + t.Local().Format("Jan 2")
	},
	"percent": func(done, total int) int {
		if total == 0 {
			return 0
		}
		return 100 * done / total
	},
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}

func humanCount(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
