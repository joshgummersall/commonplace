// Package web serves the Commonplace web app: server-rendered pages that
// htmx updates in place.
package web

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joshgummersall/commonplace/internal/store"
)

//go:embed templates static
var embedded embed.FS

// Server holds what the handlers need.
type Server struct {
	store  *store.Store
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
	Logger *slog.Logger
	Now    func() time.Time
}

// New builds a Server. It parses templates up front so a broken template
// fails at startup rather than on first request.
func New(s *store.Store, opts Options) (*Server, error) {
	srv := &Server{store: s, logger: opts.Logger, now: opts.Now, files: embedded}
	if srv.logger == nil {
		srv.logger = slog.Default()
	}
	if srv.now == nil {
		srv.now = time.Now
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
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /{$}", s.handle(s.daily))
	mux.HandleFunc("GET /search", s.handle(s.search))
	mux.HandleFunc("GET /h/{id}", s.handle(s.highlight))
	mux.HandleFunc("POST /h/{id}/favorite", s.handle(s.favorite))
	mux.HandleFunc("POST /h/{id}/bury", s.handle(s.bury))
	mux.HandleFunc("POST /h/{id}/unbury", s.handle(s.unbury))
	mux.HandleFunc("POST /h/{id}/notes", s.handle(s.addNote))
	mux.HandleFunc("GET /notes/{id}", s.handle(s.note))
	mux.HandleFunc("GET /notes/{id}/edit", s.handle(s.editNote))
	mux.HandleFunc("PUT /notes/{id}", s.handle(s.updateNote))
	mux.HandleFunc("DELETE /notes/{id}", s.handle(s.deleteNote))
	mux.HandleFunc("POST /notes/{id}/restore", s.handle(s.restoreNote))

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
	t, err := template.New("").Funcs(funcs).ParseFS(s.files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	s.tmpl = t
	return t, nil
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
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}
