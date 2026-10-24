package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/store"
)

func TestFTSQuery(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"obstacle", `"obstacle"*`},
		{"obstacle way", `"obstacle" "way"*`},
		{"obstacle way ", `"obstacle" "way"`},
		{`"the obstacle" way`, `"the obstacle" "way"*`},
		{`way "the obstacle"`, `"way" "the obstacle"`},
		{`"unclosed phrase`, `"unclosed phrase"`},
		{"AND OR NOT NEAR", `"AND" "OR" "NOT" "NEAR"*`},
		{"-minus col:on star*", `"-minus" "col:on" "star*"*`},
		{"ctrl\x01char", `"ctrlchar"*`},
	}
	for _, tt := range tests {
		if got := ftsQuery(tt.in); got != tt.want {
			t.Errorf("ftsQuery(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestSegments(t *testing.T) {
	got := segments("…a " + markOpen + "b" + markClose + " c " + markOpen + "d" + markClose)
	want := []Segment{{"…a ", false}, {"b", true}, {" c ", false}, {"d", true}}
	if len(got) != len(want) {
		t.Fatalf("segments = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func seed(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, name := range []string{"clippings.txt", "edge-cases.txt"} {
		f, err := os.Open(filepath.Join("..", "..", "testdata", "kindle", name))
		if err != nil {
			t.Fatal(err)
		}
		clips, _, err := clippings.Parse(f, clippings.Kindle, time.UTC)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := importer.Import(s, clips, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func titles(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.BookTitle)
	}
	return out
}

func mustSearch(t *testing.T, s *store.Store, q Query) []Result {
	t.Helper()
	rs, err := Keyword(s, q)
	if err != nil {
		t.Fatalf("Keyword(%+v): %v", q, err)
	}
	return rs
}

func TestKeyword(t *testing.T) {
	s := seed(t)

	t.Run("text match with snippet", func(t *testing.T) {
		rs := mustSearch(t, s, Query{Text: "perceptible"})
		if len(rs) != 1 || rs[0].BookTitle != "1984" {
			t.Fatalf("results = %v", titles(rs))
		}
		if !hasMatch(rs[0].Snippet) {
			t.Errorf("snippet has no match: %+v", rs[0].Snippet)
		}
	})

	t.Run("prefix while typing", func(t *testing.T) {
		if rs := mustSearch(t, s, Query{Text: "percept"}); len(rs) != 1 {
			t.Errorf("results = %v", titles(rs))
		}
	})

	t.Run("stemming", func(t *testing.T) {
		// "advances" in the text; porter stems both to "advanc".
		if rs := mustSearch(t, s, Query{Text: "advancing "}); len(rs) != 1 {
			t.Errorf("results = %v", titles(rs))
		}
	})

	t.Run("author match falls back to full text", func(t *testing.T) {
		rs := mustSearch(t, s, Query{Text: "orwell"})
		if len(rs) != 2 {
			t.Fatalf("results = %v", titles(rs))
		}
		if len(rs[0].Snippet) != 1 || rs[0].Snippet[0].Text != rs[0].Text {
			t.Errorf("snippet without a text match should be the full text: %+v", rs[0].Snippet)
		}
	})

	t.Run("note match", func(t *testing.T) {
		rs := mustSearch(t, s, Query{Text: "obstacle"})
		if len(rs) != 1 || rs[0].BookTitle != "Meditations" {
			t.Fatalf("results = %v", titles(rs))
		}
		if !hasMatch(rs[0].NoteSnippet) {
			t.Errorf("NoteSnippet = %+v, want a match", rs[0].NoteSnippet)
		}
	})

	t.Run("words are ANDed", func(t *testing.T) {
		if rs := mustSearch(t, s, Query{Text: "perceptible obstacle"}); len(rs) != 0 {
			t.Errorf("results = %v", titles(rs))
		}
	})

	t.Run("FTS syntax is literal", func(t *testing.T) {
		for _, q := range []string{`NEAR(a b)`, `-`, `"`, `*`, `title:1984`, `a OR`, `—`} {
			mustSearch(t, s, Query{Text: q})
		}
	})

	t.Run("no match", func(t *testing.T) {
		if rs := mustSearch(t, s, Query{Text: "xylophone"}); len(rs) != 0 {
			t.Errorf("results = %v", titles(rs))
		}
	})
}

func TestKeywordFilters(t *testing.T) {
	s := seed(t)
	var gtd int64
	if err := s.DB().QueryRow(`SELECT id FROM books WHERE title = 'Getting Things Done'`).Scan(&gtd); err != nil {
		t.Fatal(err)
	}

	t.Run("book filter", func(t *testing.T) {
		// "the" is in nearly every highlight; the filter must restrict it.
		for _, r := range mustSearch(t, s, Query{Text: "the", BookID: gtd}) {
			if r.BookID != gtd {
				t.Errorf("result from book %d", r.BookID)
			}
		}
	})

	t.Run("browse a book without a query", func(t *testing.T) {
		rs := mustSearch(t, s, Query{BookID: gtd})
		if len(rs) != 2 {
			t.Fatalf("GTD has %d highlights, want 2 after dedupe", len(rs))
		}
		if !rs[0].HighlightedAt.After(rs[1].HighlightedAt) {
			t.Error("browse results not newest first")
		}
	})

	t.Run("limit", func(t *testing.T) {
		if rs := mustSearch(t, s, Query{Limit: 3}); len(rs) != 3 {
			t.Errorf("%d results, want 3", len(rs))
		}
	})
}

// A word in the passage outranks the same word only in a book title.
func TestKeywordRanksTextAboveTitle(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clips := []clippings.Clipping{
		{Kind: clippings.Highlight, Title: "Leverage", Author: "A", LocStart: 1, LocEnd: 1, AddedAt: at,
			Text: "Managers spend their days in meetings."},
		{Kind: clippings.Highlight, Title: "Management", Author: "B", LocStart: 1, LocEnd: 1, AddedAt: at,
			Text: "A manager should seek leverage in every activity."},
	}
	if _, err := importer.Import(s, clips, at); err != nil {
		t.Fatal(err)
	}
	rs := mustSearch(t, s, Query{Text: "leverage "})
	if len(rs) != 2 || rs[0].BookTitle != "Management" {
		t.Errorf("order = %v, want the text match first", titles(rs))
	}
	if got := strings.Join(titles(rs), ","); got != "Management,Leverage" {
		t.Errorf("order = %s", got)
	}
}

func TestKeywordFavorites(t *testing.T) {
	s := seed(t)
	if _, err := s.DB().Exec(`UPDATE highlights SET favorite = 1 WHERE loc_start IN (2452, 2217)`); err != nil {
		t.Fatal(err)
	}
	rs := mustSearch(t, s, Query{Favorites: true})
	if len(rs) != 2 {
		t.Fatalf("favorites = %v, want 2", titles(rs))
	}
	for _, r := range rs {
		if !r.Favorite {
			t.Errorf("non-favorite %d returned", r.ID)
		}
	}
	if rs := mustSearch(t, s, Query{Text: "perceptible", Favorites: true}); len(rs) != 1 || rs[0].BookTitle != "1984" {
		t.Errorf("favorites matching 'perceptible' = %v", titles(rs))
	}
	if rs := mustSearch(t, s, Query{Text: "impediment", Favorites: true}); len(rs) != 0 {
		t.Errorf("non-favorite matched with the filter on: %v", titles(rs))
	}
}

func TestKeywordPaging(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clips []clippings.Clipping
	for i := range 23 {
		// Every highlight matches "river"; repeats vary the bm25 score, and
		// some ties exercise the id tie-break.
		text := fmt.Sprintf("Passage %d about the river.%s", i, strings.Repeat(" river", i%4))
		clips = append(clips, clippings.Clipping{Kind: clippings.Highlight, Title: "Siddhartha", Author: "Hesse",
			LocStart: 10 * (i + 1), LocEnd: 10*(i+1) + 1, AddedAt: at.Add(time.Duration(i%5) * time.Hour), Text: text})
	}
	if _, err := importer.Import(s, clips, at); err != nil {
		t.Fatal(err)
	}

	for _, base := range []Query{{Text: "river"}, {BookID: 1}} {
		all := mustSearch(t, s, Query{Text: base.Text, BookID: base.BookID, Limit: 100})
		if len(all) != 23 {
			t.Fatalf("%+v: %d results, want 23", base, len(all))
		}
		n, err := Count(s, base)
		if err != nil || n != 23 {
			t.Errorf("%+v: Count = %d, %v; want 23", base, n, err)
		}
		var paged []int64
		for off := 0; off < 30; off += 10 {
			q := base
			q.Limit, q.Offset = 10, off
			for _, r := range mustSearch(t, s, q) {
				paged = append(paged, r.ID)
			}
		}
		if len(paged) != len(all) {
			t.Fatalf("%+v: pages returned %d rows, want %d", base, len(paged), len(all))
		}
		for i := range all {
			if paged[i] != all[i].ID {
				t.Errorf("%+v: row %d is %d paged, %d unpaged", base, i, paged[i], all[i].ID)
				break
			}
		}
	}
}
