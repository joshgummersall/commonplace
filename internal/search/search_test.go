package search

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/store"
)

// conceptEmbedder maps text onto three axes by counting words from small
// vocabularies, so "fortune" lands near "luck" without sharing a word,
// like a real model would place them.
type conceptEmbedder struct{ down bool }

var concepts = [][]string{
	{"luck", "fortune", "chance", "random"},
	{"sea", "ocean", "waves", "fish"},
	{"manager", "meeting", "team", "leverage"},
}

func (c conceptEmbedder) Model() string { return "test/concepts" }

func (c conceptEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if c.down {
		return nil, fmt.Errorf("%w: Ollama is not reachable at http://test", embedding.ErrUnavailable)
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(concepts)+1)
		v[len(concepts)] = 0.1 // keep every vector nonzero
		for _, w := range strings.Fields(strings.ToLower(t)) {
			w = strings.Trim(w, ".,;:!?")
			for axis, vocab := range concepts {
				if slices.Contains(vocab, w) {
					v[axis]++
				}
			}
		}
		embedding.Normalize(v)
		out[i] = v
	}
	return out, nil
}

func (c conceptEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vs, err := c.EmbedDocuments(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

func newSearcher(t *testing.T, e embedding.Embedder, texts map[string][]string) (*Searcher, map[string]int64) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clips []clippings.Clipping
	loc := 10
	for book, ts := range texts {
		for _, text := range ts {
			clips = append(clips, clippings.Clipping{Kind: clippings.Highlight, Title: book, Author: "A",
				LocStart: loc, LocEnd: loc + 1, AddedAt: at, Text: text})
			loc += 10
		}
	}
	if _, err := importer.Import(s, clips, at); err != nil {
		t.Fatal(err)
	}
	if _, err := embedding.Backfill(context.Background(), s, conceptEmbedder{}, nil); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64)
	rows, _ := s.DB().Query(`SELECT id, text FROM highlights`)
	for rows.Next() {
		var id int64
		var text string
		rows.Scan(&id, &text)
		ids[text] = id
	}
	rows.Close()
	return &Searcher{Store: s, Embedder: e, Index: NewIndex(s, conceptEmbedder{}.Model())}, ids
}

var library = map[string][]string{
	"Fooled by Randomness": {
		"Fortune favors the prepared, but chance decides the rest.",
		"Most success is random noise mistaken for skill.",
	},
	"The Old Man and the Sea": {
		"The ocean was calm and the fish ran deep.",
		"He was an old man who fished alone in a skiff.",
	},
	"High Output Management": {
		"A manager should seek leverage in every meeting.",
		"Luck plays a part, but process makes a team.",
	},
}

func texts(p Page) []string {
	var out []string
	for _, r := range p.Results {
		out = append(out, r.Text)
	}
	return out
}

func TestRunModes(t *testing.T) {
	sr, _ := newSearcher(t, conceptEmbedder{}, library)
	ctx := context.Background()
	q := Query{Text: "luck"}

	kw, err := sr.Run(ctx, q, ModeKeyword)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(kw); len(got) != 1 || !strings.HasPrefix(got[0], "Luck plays") {
		t.Errorf("keyword = %q, want only the passage containing the word", got)
	}

	sem, err := sr.Run(ctx, q, ModeSemantic)
	if err != nil {
		t.Fatal(err)
	}
	got := texts(sem)
	// Semantic finds the luck passages without the word: "fortune",
	// "chance", "random".
	if len(got) < 3 || !strings.Contains(strings.Join(got[:3], "|"), "Fortune favors") ||
		!strings.Contains(strings.Join(got[:3], "|"), "random noise") {
		t.Errorf("semantic top 3 = %q", got[:min(3, len(got))])
	}
	if sem.Fallback != "" {
		t.Errorf("unexpected fallback %q", sem.Fallback)
	}

	hyb, err := sr.Run(ctx, q, ModeHybrid)
	if err != nil {
		t.Fatal(err)
	}
	got = texts(hyb)
	// The passage matching both ways ranks first, and keeps its marks.
	if !strings.HasPrefix(got[0], "Luck plays") {
		t.Errorf("hybrid first = %q", got[0])
	}
	if !hasMatch(hyb.Results[0].Snippet) {
		t.Error("hybrid lost the keyword snippet marks")
	}
	if !strings.Contains(strings.Join(got[:3], "|"), "Fortune favors") {
		t.Errorf("hybrid top 3 = %q, want meaning matches too", got[:3])
	}
	if hyb.Total != len(got) {
		t.Errorf("Total = %d for %d results on one page", hyb.Total, len(got))
	}
}

func TestRunFallsBackToKeyword(t *testing.T) {
	ctx := context.Background()

	t.Run("embedder down", func(t *testing.T) {
		sr, _ := newSearcher(t, conceptEmbedder{down: true}, library)
		p, err := sr.Run(ctx, Query{Text: "luck"}, ModeHybrid)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Results) != 1 || p.Fallback != "Ollama is not reachable at http://test" {
			t.Errorf("results %q, fallback %q", texts(p), p.Fallback)
		}
	})

	t.Run("no embedder", func(t *testing.T) {
		sr, _ := newSearcher(t, nil, library)
		sr.Embedder, sr.Index = nil, nil
		p, err := sr.Run(ctx, Query{Text: "luck"}, ModeSemantic)
		if err != nil || len(p.Results) != 1 || p.Fallback == "" {
			t.Errorf("results %q, fallback %q, err %v", texts(p), p.Fallback, err)
		}
	})

	t.Run("nothing embedded yet", func(t *testing.T) {
		sr, _ := newSearcher(t, conceptEmbedder{}, library)
		if _, err := sr.Store.DB().Exec(`DELETE FROM embeddings`); err != nil {
			t.Fatal(err)
		}
		p, err := sr.Run(ctx, Query{Text: "luck"}, ModeHybrid)
		if err != nil || len(p.Results) != 1 || p.Fallback != "no highlights are embedded yet" {
			t.Errorf("results %q, fallback %q, err %v", texts(p), p.Fallback, err)
		}
	})

	t.Run("real errors still fail", func(t *testing.T) {
		sr, _ := newSearcher(t, failing{}, library)
		if _, err := sr.Run(ctx, Query{Text: "luck"}, ModeHybrid); err == nil || errors.Is(err, embedding.ErrUnavailable) {
			t.Errorf("err = %v, want the embedder's own error", err)
		}
	})
}

type failing struct{ conceptEmbedder }

func (failing) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, errors.New("model crashed")
}

func TestRunFiltersAndPaging(t *testing.T) {
	sr, ids := newSearcher(t, conceptEmbedder{}, library)
	ctx := context.Background()

	var hom int64
	sr.Store.DB().QueryRow(`SELECT id FROM books WHERE title = 'High Output Management'`).Scan(&hom)
	p, err := sr.Run(ctx, Query{Text: "luck", BookID: hom}, ModeSemantic)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Results {
		if r.BookID != hom {
			t.Errorf("book filter let through %q", r.Text)
		}
	}

	fav := ids["Most success is random noise mistaken for skill."]
	sr.Store.SetFavorite(fav, true)
	p, _ = sr.Run(ctx, Query{Text: "luck", Favorites: true}, ModeHybrid)
	if len(p.Results) != 1 || p.Results[0].ID != fav {
		t.Errorf("favorites filter = %q", texts(p))
	}

	// Pages of 2 cover the semantic ranking without overlap.
	all, _ := sr.Run(ctx, Query{Text: "luck", Limit: 100}, ModeHybrid)
	var paged []string
	for off := 0; ; off += 2 {
		p, err := sr.Run(ctx, Query{Text: "luck", Limit: 2, Offset: off}, ModeHybrid)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, texts(p)...)
		if !p.More {
			break
		}
	}
	if strings.Join(paged, "|") != strings.Join(texts(all), "|") {
		t.Errorf("paged %q\nall   %q", paged, texts(all))
	}
}

func TestFuse(t *testing.T) {
	// 3 is in both lists, so it beats 1 and 7, which top one list each.
	got := Fuse([]int64{1, 3, 5}, []int64{7, 3, 9})
	want := []int64{3, 1, 7, 5, 9}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Fuse = %v, want %v", got, want)
	}
	if got := Fuse(nil, []int64{4}); fmt.Sprint(got) != "[4]" {
		t.Errorf("Fuse with an empty list = %v", got)
	}
}

func TestIndex(t *testing.T) {
	sr, ids := newSearcher(t, conceptEmbedder{}, library)
	ix := sr.Index
	fortune := ids["Fortune favors the prepared, but chance decides the rest."]

	other, same, ok, err := ix.Similar(fortune, 2)
	if err != nil || !ok {
		t.Fatalf("Similar: ok %v, err %v", ok, err)
	}
	if len(other) == 0 || other[0].ID != ids["Luck plays a part, but process makes a team."] {
		t.Errorf("nearest from other books = %+v", other)
	}
	if len(same) != 1 || same[0].ID != ids["Most success is random noise mistaken for skill."] {
		t.Errorf("same book = %+v", same)
	}
	for _, h := range append(other, same...) {
		if h.ID == fortune {
			t.Error("a highlight is similar to itself")
		}
	}

	// A new embedding is picked up without restarting.
	before, _ := ix.Size()
	if _, err := sr.Store.DB().Exec(`DELETE FROM embeddings WHERE highlight_id = ?`, fortune); err != nil {
		t.Fatal(err)
	}
	if n, _ := ix.Size(); n != before-1 {
		t.Errorf("index size %d after a delete, want %d", n, before-1)
	}
	if _, _, ok, _ := ix.Similar(fortune, 2); ok {
		t.Error("Similar ok for a highlight without a vector")
	}
	embedding.Backfill(context.Background(), sr.Store, conceptEmbedder{}, nil)
	if n, _ := ix.Size(); n != before {
		t.Errorf("index size %d after backfill, want %d", n, before)
	}
}

func TestRunReportsWhy(t *testing.T) {
	sr, ids := newSearcher(t, conceptEmbedder{}, library)
	p, err := sr.Run(context.Background(), Query{Text: "luck", Limit: 100}, ModeHybrid)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]Result{}
	for _, r := range p.Results {
		byID[r.ID] = r
	}
	both := byID[ids["Luck plays a part, but process makes a team."]]
	if fmt.Sprint(both.Matched) != "[text]" || !both.Similar {
		t.Errorf("word and meaning match: Matched %v, Similar %v", both.Matched, both.Similar)
	}
	meaning := byID[ids["Fortune favors the prepared, but chance decides the rest."]]
	if len(meaning.Matched) != 0 || !meaning.Similar {
		t.Errorf("meaning-only match: Matched %v, Similar %v", meaning.Matched, meaning.Similar)
	}
	kw, _ := sr.Run(context.Background(), Query{Text: "luck"}, ModeKeyword)
	if kw.Results[0].Similar {
		t.Error("keyword mode claimed a meaning match")
	}
}

func TestFTSQueryAny(t *testing.T) {
	tests := []struct{ in, want string }{
		{"luck versus skill", `"luck" OR "versus" OR "skill"*`},
		{"the luck of the draw", `"luck" OR "draw"*`},
		{"the", `"the"*`}, // all common: keep them rather than match nothing
		{`"the old man" sea`, `"the old man" OR "sea"*`},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ftsQueryAny(tt.in); got != tt.want {
			t.Errorf("ftsQueryAny(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// No highlight has all of "luck", "fish", and "meeting", but hybrid
// search still surfaces the ones with any of them, explains how, and
// counts honestly.
func TestHybridPartialWordMatches(t *testing.T) {
	sr, _ := newSearcher(t, conceptEmbedder{}, library)
	ctx := context.Background()
	q := Query{Text: "luck fish meeting", Limit: 100}

	kw, err := sr.Run(ctx, q, ModeKeyword)
	if err != nil {
		t.Fatal(err)
	}
	if len(kw.Results) != 0 {
		t.Errorf("keyword mode should still require every word: %q", texts(kw))
	}

	p, err := sr.Run(ctx, q, ModeHybrid)
	if err != nil {
		t.Fatal(err)
	}
	words := 0
	for _, r := range p.Results {
		if slices.Contains(r.Matched, FieldText) {
			words++
		}
	}
	// luck, meeting, fish, and (stemmed) fished: four passages.
	if words != 4 || p.WordMatches != 4 {
		t.Errorf("word matches: %d marked, WordMatches %d; want 4", words, p.WordMatches)
	}
	if p.WordMatches+p.MeaningOnly != p.Total {
		t.Errorf("counts don't add up: %d words + %d meaning != %d total", p.WordMatches, p.MeaningOnly, p.Total)
	}
	if p.MeaningOnly > SemanticK {
		t.Errorf("MeaningOnly %d exceeds the cap", p.MeaningOnly)
	}
}
