package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/joshgummersall/commonplace/internal/themes"
)

// topicNamer names a group after the word its passages share, and can
// be made to fail.
type topicNamer struct {
	mu  sync.Mutex
	err error
}

func (n *topicNamer) Model() string { return "test-namer" }

func (n *topicNamer) Name(_ context.Context, passages []string, broad bool) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return "", n.err
	}
	name := "Odds and ends"
	switch {
	case strings.Contains(strings.ToLower(passages[0]), "luck"):
		name = "Luck"
	case strings.Contains(strings.ToLower(passages[0]), "sea"):
		name = "The sea"
	}
	if broad {
		name += " (area)"
	}
	return name, nil
}

// themeLibrary is 36 invented highlights over four books: 15 about luck,
// 15 about the sea, and 6 about neither.
func themeLibrary() []clippings.Clipping {
	books := []string{"Book One", "Book Two", "Book Three", "Book Four"}
	var clips []clippings.Clipping
	for i := range 15 {
		clips = append(clips, clip(books[i%4], "Author", 10+i, fmt.Sprintf("Luck decides more than we think, case %d.", i)))
		clips = append(clips, clip(books[(i+1)%4], "Author", 100+i, fmt.Sprintf("The sea kept its own counsel, morning %d.", i)))
	}
	for i := range 6 {
		clips = append(clips, clip(books[i%4], "Author", 200+i, fmt.Sprintf("Plain remark number %d.", i)))
	}
	return clips
}

func newThemeHarness(t *testing.T, namer themes.Namer) (*harness, *Server) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := importer.Import(s, themeLibrary(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := embedding.Backfill(context.Background(), s, wordEmbedder{}, nil); err != nil {
		t.Fatal(err)
	}
	w, err := New(s, Options{Now: func() time.Time { return now }, Embedder: wordEmbedder{}, Namer: namer,
		Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(w.Handler())
	t.Cleanup(srv.Close)
	return &harness{t: t, store: s, srv: srv}, w
}

func TestThemesNeedOllama(t *testing.T) {
	h := newHarness(t)
	mustContain(t, h.get("/themes"), "needs Ollama", "No themes yet")
	if status, _ := h.do("POST", "/themes/refresh", url.Values{}, true); status != http.StatusBadRequest {
		t.Errorf("refresh without Ollama = %d, want 400", status)
	}
}

func TestThemesRefreshRenameAndFilter(t *testing.T) {
	h, w := newThemeHarness(t, &topicNamer{})

	page := h.get("/themes")
	mustContain(t, page, "No themes yet", "Find themes", "test-namer in Ollama", `hx-post="/themes/refresh"`)

	_, body := h.do("POST", "/themes/refresh", url.Values{}, true)
	mustContain(t, body, `id="refresh"`)
	w.themes.Wait()

	// The poll after the refresh ends tells htmx to reload the page.
	req, _ := http.NewRequest("GET", h.srv.URL+"/themes/refresh", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("HX-Refresh") != "true" {
		t.Error("a finished refresh did not ask the page to reload")
	}

	page = h.get("/themes")
	mustContain(t, page, "Refreshed", "Luck", "The sea", "Refresh themes", `data-item="theme"`)
	mustNotContain(t, page, "No themes yet", "Refresh failed")

	id := themeID(t, page, "Luck")
	form := h.get(fmt.Sprintf("/themes/%d/rename", id))
	mustContain(t, form, `name="name"`, "Save name", `data-action="cancel"`)

	_, body = h.do("PUT", fmt.Sprintf("/themes/%d", id), url.Values{"name": {"   "}}, true)
	mustContain(t, body, "a theme needs a name", `name="name"`)
	_, body = h.do("PUT", fmt.Sprintf("/themes/%d", id), url.Values{"name": {"Chance & fortune"}}, true)
	mustContain(t, body, "Chance &amp; fortune", `data-item="theme"`)

	results := h.get(fmt.Sprintf("/search?theme=%d", id))
	mustContain(t, results, "<h1>Chance &amp; fortune</h1>", "Luck decides", "Theme: <a",
		`<select name="theme"`, fmt.Sprintf(`<option value="%d" selected>`, id))
	mustNotContain(t, results, "The sea kept")

	// Picking a theme from the menu swaps the results and their header.
	_, body = h.do("GET", fmt.Sprintf("/search?theme=%d", id), nil, true)
	mustContain(t, body, "<h1>Chance &amp; fortune</h1>", "Luck decides")
	_, body = h.do("GET", "/search?theme=", nil, true)
	mustNotContain(t, body, "theme-filter")
	if status, _ := h.do("GET", "/search?theme=9999", nil, false); status != http.StatusNotFound {
		t.Errorf("missing theme = %d, want 404", status)
	}

	// Cards elsewhere link to their theme.
	mustContain(t, h.get("/search?q=luck"), fmt.Sprintf(`href="/search?theme=`))
}

func TestThemesFailedRefreshSaysSo(t *testing.T) {
	namer := &topicNamer{err: fmt.Errorf("%w: Ollama is not reachable at http://localhost:11434", themes.ErrUnavailable)}
	h, w := newThemeHarness(t, namer)
	h.do("POST", "/themes/refresh", url.Values{}, true)
	w.themes.Wait()
	page := h.get("/themes")
	mustContain(t, page, "Refresh failed", "Ollama is not reachable", "Your themes are unchanged", "Find themes")
}

// themeID finds the id of the first theme row whose name starts with name.
func themeID(t *testing.T, page, name string) int64 {
	t.Helper()
	m := regexp.MustCompile(`href="/search\?theme=(\d+)" data-action="open">` + regexp.QuoteMeta(name)).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no theme named %q", name)
	}
	var id int64
	fmt.Sscan(m[1], &id)
	return id
}
