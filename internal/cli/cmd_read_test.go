package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/store"
)

// readDB imports the Kindle fixture (10 highlights) into a new database.
func readDB(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "c.db")
	fixture := filepath.Join("..", "..", "testdata", "kindle", "clippings.txt")
	if _, _, err := runCLIWith(t, unitEmbedder{}, "--db", db, "import", fixture); err != nil {
		t.Fatal(err)
	}
	return db
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	return mustRunWith(t, unavailable{}, args...)
}

func mustRunWith(t *testing.T, e embedding.Embedder, args ...string) string {
	t.Helper()
	out, errOut, err := runCLIWith(t, e, args...)
	if err != nil {
		t.Fatalf("cmpl %s: %v\n%s", strings.Join(args, " "), err, errOut)
	}
	return out
}

func decode[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	return v
}

func mustContainAll(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("output missing %q:\n%s", w, s)
		}
	}
}

func TestDailyCommand(t *testing.T) {
	db := readDB(t)
	hs := decode[[]highlightJSON](t, mustRun(t, "--db", db, "daily"))
	if len(hs) != 10 {
		t.Fatalf("daily returned %d highlights, want all 10", len(hs))
	}
	// The set is fixed for the day.
	again := decode[[]highlightJSON](t, mustRun(t, "--db", db, "daily"))
	for i := range hs {
		if hs[i].ID != again[i].ID {
			t.Fatalf("daily changed within the day: %d vs %d", hs[i].ID, again[i].ID)
		}
	}
	// Reading it here doesn't mark anything seen.
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var seen int
	s.DB().QueryRow(`SELECT count(*) FROM highlights WHERE last_seen IS NOT NULL`).Scan(&seen)
	if seen != 0 {
		t.Errorf("%d highlights marked seen by cmpl daily", seen)
	}
}

func TestListCommand(t *testing.T) {
	db := readDB(t)
	p := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--limit", "4"))
	if p.Total != 10 || len(p.Results) != 4 || p.NextOffset == nil || *p.NextOffset != 4 {
		t.Fatalf("first page: total %d, %d results, next %v", p.Total, len(p.Results), p.NextOffset)
	}
	for i := 1; i < len(p.Results); i++ {
		if p.Results[i-1].HighlightedAt.Before(*p.Results[i].HighlightedAt) {
			t.Errorf("not newest first at %d", i)
		}
	}
	last := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--limit", "4", "--offset", "8"))
	if len(last.Results) != 2 || last.NextOffset != nil {
		t.Errorf("last page: %d results, next %v", len(last.Results), last.NextOffset)
	}

	// --book takes part of a title or an id.
	gtd := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--book", "getting things"))
	if gtd.Total == 0 {
		t.Fatal("no highlights for --book 'getting things'")
	}
	for _, h := range gtd.Results {
		if h.Title != "Getting Things Done" {
			t.Errorf("--book returned %q", h.Title)
		}
	}
	byID := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--book", fmt.Sprint(gtd.Results[0].BookID)))
	if byID.Total != gtd.Total {
		t.Errorf("--book by id: %d, by title: %d", byID.Total, gtd.Total)
	}
}

func TestListErrors(t *testing.T) {
	db := readDB(t)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"list", "--book", "zzz"}, `no book title contains "zzz"`},
		{[]string{"list", "--book", "999"}, "no book with id 999"},
		{[]string{"list", "--book", "the"}, "pass an id instead"},
		{[]string{"list", "--limit", "0"}, "--limit must be at least 1"},
		{[]string{"show", "999"}, "no highlight with id 999"},
		{[]string{"show", "abc"}, "is not a highlight id"},
		{[]string{"similar", "999"}, "no highlight with id 999"},
	}
	for _, tt := range tests {
		_, _, err := runCLI(t, append([]string{"--db", db}, tt.args...)...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("cmpl %s: err = %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}

func TestSearchCommand(t *testing.T) {
	db := readDB(t)
	// The embedder is down in runCLI, so search matches words and says so.
	p := decode[pageJSON](t, mustRun(t, "--db", db, "search", "courage"))
	if p.Total != 1 || p.Results[0].Why[0] != "words in the passage" || p.Fallback == "" {
		t.Fatalf("search courage: %+v", p)
	}
	none := decode[pageJSON](t, mustRun(t, "--db", db, "search", "xylophone"))
	if none.Total != 0 || none.Results == nil || none.NextOffset != nil {
		t.Errorf("search xylophone: %+v", none)
	}
}

func TestShowCommand(t *testing.T) {
	db := readDB(t)
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	s.DB().QueryRow(`SELECT id FROM highlights WHERE text LIKE 'Courage%'`).Scan(&id)
	if _, err := s.AddNote(id, "Say it plainly.", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Search shows the matching part of a note, and --notes whole notes.
	p := decode[pageJSON](t, mustRun(t, "--db", db, "search", "plainly"))
	if p.Total != 1 || p.Results[0].ID != id || p.Results[0].NoteMatch != "Say it plainly." || p.Results[0].Notes != nil {
		t.Fatalf("search plainly: %+v", p)
	}
	all := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--notes"))
	for _, h := range all.Results {
		if want := map[bool]int{true: 1}[h.ID == id]; len(h.Notes) != want {
			t.Errorf("list --notes: #%d has %d notes, want %d", h.ID, len(h.Notes), want)
		}
	}
	hs := decode[[]highlightJSON](t, mustRun(t, "--db", db, "show", fmt.Sprint(id)))
	if len(hs) != 1 || len(hs[0].Notes) != 1 || hs[0].Notes[0].Body != "Say it plainly." || hs[0].Notes[0].Source != "user" {
		t.Fatalf("show: %+v", hs)
	}
	// Ids may carry the # that the web app shows.
	if hs := decode[[]highlightJSON](t, mustRun(t, "--db", db, "show", fmt.Sprintf("#%d", id))); len(hs) != 1 || hs[0].ID != id {
		t.Errorf("show #%d: %+v", id, hs)
	}
}

func TestSimilarCommand(t *testing.T) {
	db := readDB(t)
	// Similar reads the vectors stored for the configured model.
	first := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--limit", "1")).Results[0]
	out := decode[struct {
		OtherBooks []highlightJSON `json:"other_books"`
		SameBook   []highlightJSON `json:"same_book"`
	}](t, mustRunWith(t, unitEmbedder{}, "--db", db, "similar", fmt.Sprint(first.ID), "--limit", "2"))
	if len(out.OtherBooks) != 2 || out.OtherBooks[0].Score == nil {
		t.Fatalf("similar: %+v", out)
	}
	for _, h := range out.OtherBooks {
		if h.BookID == first.BookID || h.ID == first.ID {
			t.Errorf("other_books has %+v", h)
		}
	}
}

func TestBooksCommand(t *testing.T) {
	db := readDB(t)
	books := decode[[]struct {
		ID         int64  `json:"id"`
		Title      string `json:"title"`
		Highlights int    `json:"highlights"`
	}](t, mustRun(t, "--db", db, "books"))
	total := 0
	for _, b := range books {
		total += b.Highlights
	}
	if total != 10 {
		t.Errorf("books count %d highlights, want 10", total)
	}
}

func TestEmptyLibrary(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	for _, c := range []string{"daily", "books"} {
		if out := mustRun(t, "--db", db, c); strings.TrimSpace(out) != "[]" {
			t.Errorf("empty %s = %q", c, out)
		}
	}
}

// The root help is the map an agent starts from: it names every command
// and how to learn more.
func TestRootHelp(t *testing.T) {
	out := mustRun(t, "help")
	mustContainAll(t, out, "Read highlights:", "Manage the library:", "prints JSON", "cmpl help <command>")
	for _, c := range []string{"daily", "list", "search", "show", "similar", "books", "import", "serve", "backup"} {
		mustContainAll(t, out, "\n  "+c+" ")
		help := mustRun(t, "help", c)
		mustContainAll(t, help, "Examples:")
	}
}

func TestThemesCommand(t *testing.T) {
	db := readDB(t)
	type themesOut struct {
		RefreshedAt *time.Time `json:"refreshed_at"`
		Unthemed    int        `json:"unthemed"`
		Areas       []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Highlights int    `json:"highlights"`
			Themes     []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"themes"`
		} `json:"areas"`
	}

	// Before any refresh.
	empty := decode[themesOut](t, mustRun(t, "--db", db, "themes"))
	if empty.RefreshedAt != nil || empty.Areas == nil || len(empty.Areas) != 0 || empty.Unthemed != 10 {
		t.Errorf("before a refresh: %+v", empty)
	}
	if _, _, err := runCLIWith(t, unavailable{}, "--db", db, "list", "--theme", "1"); err == nil ||
		!strings.Contains(err.Error(), "no themes yet") {
		t.Errorf("--theme before a refresh: %v", err)
	}

	// One area of six highlights holding one theme of four.
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	rows, _ := s.DB().Query(`SELECT id FROM highlights ORDER BY id LIMIT 6`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	area := store.Area{Theme: store.Theme{Name: "Getting work done", Members: ids}}
	area.Themes = []store.Theme{{Name: "Weekly review", Members: ids[:4]}}
	if err := s.ReplaceThemes([]store.Area{area}, store.ThemeRefresh{At: time.Now(), Highlights: 10}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	got := decode[themesOut](t, mustRun(t, "--db", db, "themes"))
	if got.RefreshedAt == nil || got.Unthemed != 4 || len(got.Areas) != 1 || got.Areas[0].Highlights != 6 ||
		len(got.Areas[0].Themes) != 1 || got.Areas[0].Themes[0].Name != "Weekly review" {
		t.Fatalf("themes = %+v", got)
	}
	themeID := got.Areas[0].Themes[0].ID

	// --theme takes an id or part of a name, and highlights name their theme.
	byID := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--theme", fmt.Sprint(themeID)))
	byName := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--theme", "weekly"))
	if byID.Total != 4 || byName.Total != 4 {
		t.Errorf("--theme: %d by id, %d by name, want 4", byID.Total, byName.Total)
	}
	for _, h := range byID.Results {
		if h.Theme == nil || h.Theme.ID != themeID {
			t.Errorf("highlight %d theme = %+v, want %d", h.ID, h.Theme, themeID)
		}
	}
	if area := decode[pageJSON](t, mustRun(t, "--db", db, "list", "--theme", "getting work")); area.Total != 6 {
		t.Errorf("--theme for the area: %d, want 6", area.Total)
	}

	for _, tt := range []struct{ arg, want string }{
		{"999", "no theme with id 999"},
		{"astronomy", `no theme name contains "astronomy"`},
		{"e", `"e" matches 2 themes`},
	} {
		if _, _, err := runCLIWith(t, unavailable{}, "--db", db, "list", "--theme", tt.arg); err == nil ||
			!strings.Contains(err.Error(), tt.want) {
			t.Errorf("--theme %s: %v, want %q", tt.arg, err, tt.want)
		}
	}
}
