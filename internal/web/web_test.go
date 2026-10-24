package web

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/store"
)

var now = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

type harness struct {
	t     *testing.T
	store *store.Store
	srv   *httptest.Server
}

func newHarness(t *testing.T, clips ...clippings.Clipping) *harness {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if len(clips) > 0 {
		if _, err := importer.Import(s, clips, now); err != nil {
			t.Fatal(err)
		}
	}
	w, err := New(s, Options{Now: func() time.Time { return now }, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(w.Handler())
	t.Cleanup(srv.Close)
	return &harness{t: t, store: s, srv: srv}
}

func clip(title, author string, loc int, text string) clippings.Clipping {
	return clippings.Clipping{Kind: clippings.Highlight, Dialect: clippings.Kindle, Title: title, Author: author,
		LocStart: loc, LocEnd: loc, AddedAt: now.Add(-time.Duration(loc) * time.Hour), Text: text}
}

// do sends a request the way htmx would (same origin, HX-Request header
// when htmx is true) and returns status and body.
func (h *harness) do(method, path string, form url.Values, htmx bool) (int, string) {
	h.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, h.srv.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (h *harness) get(path string) string {
	h.t.Helper()
	status, body := h.do("GET", path, nil, false)
	if status != http.StatusOK {
		h.t.Fatalf("GET %s = %d: %s", path, status, body)
	}
	return body
}

func (h *harness) firstID() int64 {
	h.t.Helper()
	var id int64
	if err := h.store.DB().QueryRow(`SELECT min(id) FROM highlights`).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("response missing %q", w)
		}
	}
}

func mustNotContain(t *testing.T, body string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(body, n) {
			t.Errorf("response contains %q", n)
		}
	}
}

func TestDaily(t *testing.T) {
	h := newHarness(t,
		clip("Meditations", "Marcus Aurelius", 10, "The impediment to action advances action."),
		clip("1984", "Orwell, George", 20, "We are the dead."),
	)
	body := h.get("/")
	mustContain(t, body, "The impediment to action advances action.", "We are the dead.",
		"Meditations", "Saturday, October 24", `aria-current="page"`)

	var seen int
	if err := h.store.DB().QueryRow(`SELECT count(*) FROM highlights WHERE last_seen IS NOT NULL`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Errorf("%d highlights marked seen, want 2", seen)
	}
}

func TestDailyEmpty(t *testing.T) {
	h := newHarness(t)
	mustContain(t, h.get("/"), "Nothing to resurface yet")
}

// Highlight text and notes come from files and forms; they must render as
// text, never as markup.
func TestEscapesUserContent(t *testing.T) {
	h := newHarness(t, clip(`<b>Bold</b> "Title"`, "A", 1, `<script>alert("x")</script> & more`))
	id := h.firstID()
	if _, err := h.store.AddNote(id, `<img src=x onerror=alert(1)>`, now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", fmt.Sprintf("/h/%d", id), "/search?q=alert"} {
		body := h.get(path)
		mustNotContain(t, body, `<script>alert`, `<img src=x`, `<b>Bold</b>`)
		mustContain(t, body, "&lt;script&gt;")
	}
}

func TestSearch(t *testing.T) {
	h := newHarness(t,
		clip("Meditations", "Marcus Aurelius", 10, "The impediment to action advances action."),
		clip("1984", "Orwell, George", 20, "We are the dead."),
	)

	full := h.get("/search?q=impediment")
	mustContain(t, full, "<!doctype html>", `value="impediment"`, "<mark>impediment</mark>", "1 result")
	mustNotContain(t, full, "We are the dead.")

	status, frag := h.do("GET", "/search?q=dead", nil, true)
	if status != http.StatusOK {
		t.Fatalf("htmx search = %d", status)
	}
	mustContain(t, frag, "<mark>dead</mark>")
	mustNotContain(t, frag, "<!doctype html>", "<select")

	mustContain(t, h.get("/search"), "Search your highlights")
	mustContain(t, h.get("/search?q=xylophone"), "No highlights match.")

	var bookID int64
	if err := h.store.DB().QueryRow(`SELECT id FROM books WHERE title = '1984'`).Scan(&bookID); err != nil {
		t.Fatal(err)
	}
	byBook := h.get(fmt.Sprintf("/search?book=%d", bookID))
	mustContain(t, byBook, "We are the dead.", "selected")
	mustNotContain(t, byBook, "impediment")

	if status, _ := h.do("GET", "/search?book=abc", nil, false); status != http.StatusBadRequest {
		t.Errorf("bad book id = %d, want 400", status)
	}
}

func TestFavoriteAndBury(t *testing.T) {
	h := newHarness(t, clip("Meditations", "Marcus Aurelius", 10, "The impediment to action."))
	id := h.firstID()
	path := fmt.Sprintf("/h/%d", id)

	status, body := h.do("POST", path+"/favorite", nil, true)
	if status != http.StatusOK {
		t.Fatalf("favorite = %d", status)
	}
	mustContain(t, body, `aria-pressed="true"`, "★")
	_, body = h.do("POST", path+"/favorite", nil, true)
	mustContain(t, body, `aria-pressed="false"`)

	_, body = h.do("POST", path+"/bury", nil, true)
	mustContain(t, body, "is-buried", "Buried: hidden from Daily", "Unbury")
	if got := h.get("/"); !strings.Contains(got, "Nothing to resurface") {
		t.Error("buried highlight still in the daily shuffle")
	}
	// Buried highlights stay searchable.
	mustContain(t, h.get("/search?q=impediment"), "is-buried")

	_, body = h.do("POST", path+"/unbury", nil, true)
	mustNotContain(t, body, "is-buried")
	mustContain(t, body, ">Bury<")

	if status, _ := h.do("POST", "/h/9999/favorite", nil, true); status != http.StatusNotFound {
		t.Errorf("favorite missing highlight = %d, want 404", status)
	}
	if status, _ := h.do("GET", "/h/abc", nil, false); status != http.StatusNotFound {
		t.Errorf("non-numeric id = %d, want 404", status)
	}
}

func TestNotes(t *testing.T) {
	h := newHarness(t, clip("Meditations", "Marcus Aurelius", 10, "The impediment to action."))
	id := h.firstID()
	page := fmt.Sprintf("/h/%d", id)
	mustContain(t, h.get(page), "No notes yet.")

	_, body := h.do("POST", page+"/notes", url.Values{"body": {"Re-read after the reorg.\nStill true."}}, true)
	mustContain(t, body, `id="notes"`, "<p>Re-read after the reorg.</p>", "<p>Still true.</p>", ">You<")
	mustNotContain(t, body, "edited")

	// An empty note keeps the form and explains why.
	_, body = h.do("POST", page+"/notes", url.Values{"body": {"  "}}, true)
	mustContain(t, body, "note is empty", `role="alert"`)

	notes, err := h.store.Notes(id)
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes = %v, %v", notes, err)
	}
	noteURL := fmt.Sprintf("/notes/%d", notes[0].ID)

	_, body = h.do("GET", noteURL+"/edit", nil, true)
	mustContain(t, body, "<textarea", "Re-read after the reorg.")

	_, body = h.do("PUT", noteURL, url.Values{"body": {"Edited thought."}}, true)
	mustContain(t, body, "Edited thought.")
	if status, _ := h.do("PUT", noteURL, url.Values{"body": {""}}, true); status != http.StatusBadRequest {
		t.Errorf("empty edit = %d, want 400", status)
	}

	// The card on the daily page counts notes and the search finds them.
	mustContain(t, h.get("/"), "1 note")
	mustContain(t, h.get("/search?q=edited"), "<mark>Edited</mark>")

	status, body := h.do("DELETE", noteURL, nil, true)
	if status != http.StatusOK {
		t.Fatalf("delete = %d", status)
	}
	mustContain(t, body, "Note deleted.", noteURL+"/restore")
	if status, _ := h.do("DELETE", noteURL, nil, true); status != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", status)
	}
	mustContain(t, h.get(page), "No notes yet.")

	// Undo brings it back and re-renders the whole notes section.
	_, body = h.do("POST", noteURL+"/restore", nil, true)
	mustContain(t, body, `id="notes"`, "Edited thought.")
	mustContain(t, h.get(page), "Edited thought.")
	if status, _ := h.do("POST", noteURL+"/restore", nil, true); status != http.StatusNotFound {
		t.Errorf("restoring a live note = %d, want 404", status)
	}
}

// Tags were removed; their routes must be gone too.
func TestNoTagRoutes(t *testing.T) {
	h := newHarness(t, clip("Meditations", "Marcus Aurelius", 10, "The impediment to action."))
	path := fmt.Sprintf("/h/%d/tags", h.firstID())
	if status, _ := h.do("POST", path, url.Values{"tag": {"x"}}, true); status != http.StatusMethodNotAllowed && status != http.StatusNotFound {
		t.Errorf("POST %s = %d, want 404 or 405", path, status)
	}
	mustNotContain(t, h.get("/search"), `name="tag"`)
}

// The server listens on localhost; a page on another site must not be able
// to change data through it.
func TestRejectsCrossOriginWrites(t *testing.T) {
	h := newHarness(t, clip("Meditations", "Marcus Aurelius", 10, "The impediment to action."))
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/h/%d/favorite", h.srv.URL, h.firstID()), nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", resp.StatusCode)
	}
	var fav bool
	h.store.DB().QueryRow(`SELECT favorite FROM highlights`).Scan(&fav)
	if fav {
		t.Error("cross-site POST changed data")
	}
}

func TestStatic(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/static/app.css", "/static/app.js", "/static/htmx-2.0.4.min.js"} {
		if status, _ := h.do("GET", p, nil, false); status != http.StatusOK {
			t.Errorf("GET %s = %d", p, status)
		}
	}
}

func TestSearchFavorites(t *testing.T) {
	h := newHarness(t,
		clip("Meditations", "Marcus Aurelius", 10, "The impediment to action advances action."),
		clip("1984", "Orwell, George", 20, "We are the dead."),
		clip("1984", "Orwell, George", 30, "Freedom is the freedom to say two plus two make four."),
	)
	mustContain(t, h.get("/search?fav=1"), "No favorites yet.")

	var dead, freedom int64
	db := h.store.DB()
	db.QueryRow(`SELECT id FROM highlights WHERE text LIKE 'We are%'`).Scan(&dead)
	db.QueryRow(`SELECT id FROM highlights WHERE text LIKE 'Freedom%'`).Scan(&freedom)
	for _, id := range []int64{dead, freedom} {
		if status, _ := h.do("POST", fmt.Sprintf("/h/%d/favorite", id), nil, true); status != http.StatusOK {
			t.Fatalf("favorite %d = %d", id, status)
		}
	}

	all := h.get("/search?fav=1")
	mustContain(t, all, "2 results", "We are the dead.", "Freedom is the freedom", `id="fav" type="checkbox" name="fav" value="1" checked`)
	mustNotContain(t, all, "impediment")
	mustContain(t, all, `<a href="/search?fav=1" aria-current="page">Favorites</a>`, "<title>Favorites · Commonplace</title>")
	mustNotContain(t, all, `<a href="/search" aria-current="page">`)

	// Combined with words, only matching favorites come back.
	_, frag := h.do("GET", "/search?q=freedom&fav=1", nil, true)
	mustContain(t, frag, "1 result", "<mark>Freedom</mark>")
	mustNotContain(t, frag, "We are the dead.")
	// Without the filter, words match everything.
	mustContain(t, h.get("/search?q=the"), "3 results")
}

func TestSearchLoadMore(t *testing.T) {
	var clips []clippings.Clipping
	for i := range 120 {
		clips = append(clips, clip("Siddhartha", "Hesse", 10*(i+1), fmt.Sprintf("Passage %03d about the river.", i)))
	}
	h := newHarness(t, clips...)

	first := h.get("/search?q=river")
	mustContain(t, first, "120 results", "data-load-more", `hx-trigger="revealed, click"`)
	if n := strings.Count(first, `data-item="quote"`); n != 50 {
		t.Errorf("first page has %d quotes, want 50", n)
	}
	next := regexp.MustCompile(`hx-get="(/search\?[^"]+)"`).FindStringSubmatch(first)
	if next == nil {
		t.Fatal("no Load more URL")
	}
	nextURL := html.UnescapeString(next[1])
	if !strings.Contains(nextURL, "offset=50") || !strings.Contains(nextURL, "q=river") {
		t.Errorf("next URL = %s", nextURL)
	}

	_, second := h.do("GET", nextURL, nil, true)
	mustNotContain(t, second, "<!doctype html>", `class="count"`)
	if n := strings.Count(second, `data-item="quote"`); n != 50 {
		t.Errorf("second page has %d quotes, want 50", n)
	}
	mustContain(t, second, "offset=100")

	_, last := h.do("GET", strings.Replace(nextURL, "offset=50", "offset=100", 1), nil, true)
	if n := strings.Count(last, `data-item="quote"`); n != 20 {
		t.Errorf("last page has %d quotes, want 20", n)
	}
	mustNotContain(t, last, "data-load-more")

	// Pages never repeat a quote.
	ids := regexp.MustCompile(`id="h-(\d+)"`)
	seen := map[string]bool{}
	for _, body := range []string{first, second, last} {
		for _, m := range ids.FindAllStringSubmatch(body, -1) {
			if seen[m[1]] {
				t.Errorf("quote %s on two pages", m[1])
			}
			seen[m[1]] = true
		}
	}
	if len(seen) != 120 {
		t.Errorf("saw %d distinct quotes, want 120", len(seen))
	}

	// A full page load ignores offset and starts from the top.
	reload := h.get("/search?q=river&offset=50")
	mustContain(t, reload, "120 results")
	if a, b := ids.FindStringSubmatch(first), ids.FindStringSubmatch(reload); a[1] != b[1] {
		t.Errorf("reload with offset starts at quote %s, want %s", b[1], a[1])
	}
	if status, _ := h.do("GET", "/search?q=river&offset=-1", nil, true); status != http.StatusBadRequest {
		t.Errorf("negative offset = %d, want 400", status)
	}
	// Few results: no button at all.
	mustNotContain(t, h.get("/search?q=%22passage+007%22"), "data-load-more")
}

func TestSearchExplainsWhy(t *testing.T) {
	h := newHarness(t,
		clip("Meditations", "Marcus Aurelius", 10, "The impediment to action advances action."),
		clip("1984", "Orwell, George", 20, "We are the dead."),
	)
	mustContain(t, h.get("/search?q=impediment"), "Listed because: </span>Words in the passage")
	mustContain(t, h.get("/search?q=orwell"), "Words in the author")
	// Without an embedder the app says why meaning wasn't used.
	mustContain(t, h.get("/search?q=dead"), "Keyword matches only:")
	h.store.SetFavorite(h.firstID(), true)
	mustNotContain(t, h.get("/search?fav=1"), "Listed because")
}

// wordEmbedder puts texts sharing the word "luck" or "sea" near each other.
type wordEmbedder struct{}

func (wordEmbedder) Model() string { return "test/words" }
func (wordEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := []float32{0.1, 0.1, 0.1}
		if strings.Contains(strings.ToLower(t), "luck") {
			v[0] = 1
		}
		if strings.Contains(strings.ToLower(t), "sea") {
			v[1] = 1
		}
		embedding.Normalize(v)
		out[i] = v
	}
	return out, nil
}
func (e wordEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vs, err := e.EmbedDocuments(ctx, []string{text})
	return vs[0], err
}

func TestSimilarPanel(t *testing.T) {
	clips := []clippings.Clipping{
		clip("Fooled by Randomness", "Taleb", 10, "Luck is the reason for extreme success."),
		clip("Fooled by Randomness", "Taleb", 20, "Never mistake luck for skill."),
		clip("The Drunkard's Walk", "Mlodinow", 30, "Luck shapes outcomes more than we admit."),
		clip("The Old Man and the Sea", "Hemingway", 40, "The sea was calm that night."),
	}

	// Without an embedder the panel says what's needed.
	plain := newHarness(t, clips...)
	_, body := plain.do("GET", fmt.Sprintf("/h/%d/similar", plain.firstID()), nil, true)
	mustContain(t, body, "need an embedding model")

	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := importer.Import(s, clips, now); err != nil {
		t.Fatal(err)
	}
	w, err := New(s, Options{Now: func() time.Time { return now }, Embedder: wordEmbedder{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(w.Handler())
	defer srv.Close()
	h := &harness{t: t, store: s, srv: srv}

	var luck int64
	s.DB().QueryRow(`SELECT id FROM highlights WHERE text LIKE 'Never mistake%'`).Scan(&luck)

	// Not embedded yet.
	_, body = h.do("GET", fmt.Sprintf("/h/%d/similar", luck), nil, true)
	mustContain(t, body, "embedded yet. Similar highlights appear")

	if _, err := embedding.Backfill(context.Background(), s, wordEmbedder{}, nil); err != nil {
		t.Fatal(err)
	}
	_, body = h.do("GET", fmt.Sprintf("/h/%d/similar", luck), nil, true)
	mustContain(t, body, "From other books", "Luck shapes outcomes", "From this book", "Luck is the reason")
	other := body[strings.Index(body, "From other books"):strings.Index(body, "From this book")]
	if strings.Index(other, "Luck shapes") > strings.Index(other, "The sea was calm") {
		t.Error("the unrelated passage ranked above the related one")
	}
	mustNotContain(t, body, "Never mistake luck") // not similar to itself

	// The quote page loads the panel lazily and offers More like this.
	page := h.get(fmt.Sprintf("/h/%d", luck))
	mustContain(t, page, fmt.Sprintf(`hx-get="/h/%d/similar"`, luck), `href="#similar"`)
	if status, _ := h.do("GET", "/h/9999/similar", nil, true); status != http.StatusNotFound {
		t.Errorf("similar for a missing highlight = %d, want 404", status)
	}

	// Hybrid search: the word match and the meaning match both explain themselves.
	results := h.get("/search?q=skill")
	mustContain(t, results, "Words in the passage")
}

func TestAssetsAreVersioned(t *testing.T) {
	h := newHarness(t)
	page := h.get("/")
	m := regexp.MustCompile(`href="(/static/app\.css\?v=[0-9a-f]{8})"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("stylesheet link has no content hash")
	}
	resp, err := http.Get(h.srv.URL + m[1])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset Cache-Control = %q", cc)
	}
}

func TestSearchCountLine(t *testing.T) {
	clips := []clippings.Clipping{
		clip("Fooled by Randomness", "Taleb", 10, "Luck is the reason for extreme success."),
		clip("The Old Man and the Sea", "Hemingway", 20, "The sea was calm that night."),
		clip("The Old Man and the Sea", "Hemingway", 30, "He fished the sea alone."),
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	importer.Import(s, clips, now)
	embedding.Backfill(context.Background(), s, wordEmbedder{}, nil)
	w, _ := New(s, Options{Now: func() time.Time { return now }, Embedder: wordEmbedder{}, Logger: slog.New(slog.DiscardHandler)})
	srv := httptest.NewServer(w.Handler())
	defer srv.Close()
	h := &harness{t: t, store: s, srv: srv}

	// "luck" is in one passage; the sea passages come in by meaning (the
	// test embedder makes everything at least a little similar).
	mustContain(t, h.get("/search?q=luck"), "1 with your words · 2 more similar in meaning")
	// Without the embedder's help, a plain count.
	mustContain(t, newHarness(t, clips...).get("/search?q=luck"), "1 result<")
}
