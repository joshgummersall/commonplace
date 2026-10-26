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

	"github.com/joshgummersall/commonplace/internal/autotag"
	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/store"
)

// topicNamer names a group after the word its passages share, and can
// be made to fail.
type topicNamer struct {
	mu  sync.Mutex
	err error
}

func (n *topicNamer) Model() string { return "test-namer" }

func (n *topicNamer) Name(_ context.Context, passages []string) (string, error) {
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
	return name, nil
}

// tagLibrary is 36 invented highlights over four books: 15 about luck,
// 15 about the sea, and 6 about neither.
func tagLibrary() []clippings.Clipping {
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

func newTagHarness(t *testing.T, namer autotag.Namer) (*harness, *Server) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := importer.Import(s, tagLibrary(), now); err != nil {
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

func TestTagsNeedOllama(t *testing.T) {
	h := newHarness(t)
	mustContain(t, h.get("/tags"), "needs Ollama", "No tags yet")
	if status, _ := h.do("POST", "/tags/seed", url.Values{}, true); status != http.StatusBadRequest {
		t.Errorf("suggesting without Ollama = %d, want 400", status)
	}
}

// reloads reports whether an htmx request tells the page to reload.
func reloads(t *testing.T, h *harness, method, path string, form url.Values) bool {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Origin", h.srv.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d", method, path, resp.StatusCode)
	}
	return resp.Header.Get("HX-Refresh") == "true"
}

func TestTagsSeedRenameMergeDelete(t *testing.T) {
	h, w := newTagHarness(t, &topicNamer{})

	page := h.get("/tags")
	mustContain(t, page, "No tags yet", "Suggest tags", "test-namer in Ollama", `hx-post="/tags/seed"`)

	_, body := h.do("POST", "/tags/seed", url.Values{}, true)
	mustContain(t, body, `id="seed"`)
	w.seeder.Wait()
	// The poll after the run ends tells htmx to reload the page.
	if !reloads(t, h, "GET", "/tags/seed", nil) {
		t.Error("a finished run did not ask the page to reload")
	}

	page = h.get("/tags")
	mustContain(t, page, "Suggested", "Luck", "The sea", `data-item="tag"`, "36 highlights have suggested tags awaiting")
	mustNotContain(t, page, "No tags yet", "failed")

	luck, sea := tagID(t, page, "Luck"), tagID(t, page, "The sea")
	form := h.get(fmt.Sprintf("/tags/%d/rename", luck))
	mustContain(t, form, `name="name"`, "Save name", `data-action="cancel"`)
	_, body = h.do("PUT", fmt.Sprintf("/tags/%d", luck), url.Values{"name": {"   "}}, true)
	mustContain(t, body, "a tag needs a name", `name="name"`)
	_, body = h.do("PUT", fmt.Sprintf("/tags/%d", luck), url.Values{"name": {"Chance & fortune"}}, true)
	mustContain(t, body, "Chance &amp; fortune", `data-item="tag"`)

	results := h.get(fmt.Sprintf("/search?tag=%d", luck))
	mustContain(t, results, "<h1>Chance &amp; fortune</h1>", "Luck decides", `class="tag is-suggested"`,
		`<select name="tag"`, fmt.Sprintf(`<option value="%d" selected>`, luck))
	mustNotContain(t, results, "The sea kept")
	_, body = h.do("GET", "/search?tag=", nil, true)
	mustNotContain(t, body, "tag-filter")
	if status, _ := h.do("GET", "/search?tag=9999", nil, false); status != http.StatusNotFound {
		t.Errorf("missing tag = %d, want 404", status)
	}

	// Renaming onto another tag's name merges the two and reloads the page.
	if !reloads(t, h, "PUT", fmt.Sprintf("/tags/%d", sea), url.Values{"name": {"chance & FORTUNE"}}) {
		t.Error("a merge did not ask the page to reload")
	}
	page = h.get("/tags")
	mustContain(t, page, "Chance &amp; fortune")
	mustNotContain(t, page, "The sea")

	// Merging picks the other tag from a menu.
	h.do("PUT", fmt.Sprintf("/tags/%d", luck), url.Values{"name": {"Chance"}}, true)
	_, body = h.do("PUT", "/h/"+fmt.Sprint(quoteID(t, h.get("/search?q=luck")))+"/tags", url.Values{"tags": {"Chance, Fortune"}}, true)
	fortune := tagID(t, h.get("/tags"), "Fortune")
	_, body = h.do("GET", fmt.Sprintf("/tags/%d/merge", fortune), nil, true)
	mustContain(t, body, `<select name="into"`, fmt.Sprintf(`<option value="%d">Chance (36)</option>`, luck))
	mustNotContain(t, body, `>Fortune (`)
	if !reloads(t, h, "POST", fmt.Sprintf("/tags/%d/merge", fortune), url.Values{"into": {fmt.Sprint(luck)}}) {
		t.Error("a merge did not ask the page to reload")
	}
	mustNotContain(t, h.get("/tags"), ">Fortune<")
	h.do("PUT", fmt.Sprintf("/tags/%d", luck), url.Values{"name": {"Chance & fortune"}}, true)

	_, body = h.do("GET", fmt.Sprintf("/tags/%d/delete", luck), nil, true)
	mustContain(t, body, "Delete “Chance &amp; fortune” from 36 highlights?", "is-confirming")
	_, body = h.do("DELETE", fmt.Sprintf("/tags/%d", luck), nil, true)
	mustContain(t, body, "Deleted “Chance &amp; fortune”")
	mustContain(t, h.get("/tags"), "No tags yet")
}

func TestTagsOnAQuote(t *testing.T) {
	h, w := newTagHarness(t, &topicNamer{})
	h.do("POST", "/tags/seed", url.Values{}, true)
	w.seeder.Wait()
	id := quoteID(t, h.get("/search?q=luck"))
	tagsURL := fmt.Sprintf("/h/%d/tags", id)

	quote := h.get(fmt.Sprintf("/h/%d", id))
	mustContain(t, quote, `class="tag is-suggested"`, ">Luck<", `data-action="confirm-tags"`, "Edit tags")

	_, body := h.do("POST", tagsURL+"/confirm", nil, true)
	mustContain(t, body, `class="tag"`, ">Luck<")
	mustNotContain(t, body, "is-suggested", "confirm-tags")

	_, body = h.do("GET", tagsURL+"/edit", nil, true)
	mustContain(t, body, `value="Luck, "`, `data-tag-names="[&#34;Luck&#34;,&#34;The sea&#34;]"`, `<button type="submit">Save</button>`)

	_, body = h.do("PUT", tagsURL, url.Values{"tags": {"luck, Fortune,"}}, true)
	mustContain(t, body, ">Fortune<", ">Luck<")
	_, body = h.do("PUT", tagsURL, url.Values{"tags": {"a, " + strings.Repeat("x", 81)}}, true)
	mustContain(t, body, "under 80 characters", `name="tags"`)

	// Clearing every tag leaves the card with a way to add one.
	_, body = h.do("PUT", tagsURL, url.Values{"tags": {""}}, true)
	mustContain(t, body, "Add tags")
	mustNotContain(t, body, `class="tag`)
	if status, _ := h.do("PUT", "/h/9999/tags", url.Values{"tags": {"x"}}, true); status != http.StatusNotFound {
		t.Errorf("tagging a missing quote = %d, want 404", status)
	}
}

func TestTagsFailedSeedSaysSo(t *testing.T) {
	namer := &topicNamer{err: fmt.Errorf("%w: Ollama is not reachable at http://localhost:11434", autotag.ErrUnavailable)}
	h, w := newTagHarness(t, namer)
	h.do("POST", "/tags/seed", url.Values{}, true)
	w.seeder.Wait()
	mustContain(t, h.get("/tags"), "Suggesting tags failed", "Ollama is not reachable")
}

// tagID finds the id of the first tag row whose name starts with name.
func tagID(t *testing.T, page, name string) int64 {
	t.Helper()
	m := regexp.MustCompile(`href="/search\?tag=(\d+)" data-action="open">` + regexp.QuoteMeta(name)).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no tag named %q", name)
	}
	var id int64
	fmt.Sscan(m[1], &id)
	return id
}

// quoteID finds the id of the first quote on a page.
func quoteID(t *testing.T, page string) int64 {
	t.Helper()
	m := regexp.MustCompile(`data-item="quote" data-id="(\d+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no quote on the page")
	}
	var id int64
	fmt.Sscan(m[1], &id)
	return id
}
