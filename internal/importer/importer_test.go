package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/store"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func parseFixture(t *testing.T, parts ...string) []clippings.Clipping {
	t.Helper()
	f, err := os.Open(filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	clips, _, err := clippings.Parse(f, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return clips
}

func mustImport(t *testing.T, s *store.Store, clips []clippings.Clipping) Result {
	t.Helper()
	r, err := Import(s, clips, now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func count(t *testing.T, s *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func checkResult(t *testing.T, got, want Result) {
	t.Helper()
	got.Changed, want.Changed = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result = %+v\nwant     %+v", got, want)
	}
}

func TestImportKindleFixture(t *testing.T) {
	s := openStore(t)
	clips := parseFixture(t, "kindle", "clippings.txt")

	// 17 entries: 2 bookmarks, a 2-version extension, a 5-version chain.
	r := mustImport(t, s, clips)
	checkResult(t, r, Result{Added: 10, Merged: 5, Bookmarks: 2})
	if len(r.Changed) != 10 {
		t.Errorf("Changed has %d ids, want 10", len(r.Changed))
	}
	if n := count(t, s, `SELECT count(*) FROM highlights h JOIN books b ON b.id = h.book_id
		WHERE b.title = 'Getting Things Done' AND h.loc_start = 2217`); n != 1 {
		t.Errorf("GTD chain stored %d rows, want 1", n)
	}
	if n := count(t, s, `SELECT length(CAST(text AS BLOB)) FROM highlights WHERE loc_start = 2217`); n != 4571 {
		t.Errorf("GTD chain kept %d bytes, want the final 4571", n)
	}

	r = mustImport(t, s, clips)
	checkResult(t, r, Result{Unchanged: 10, Merged: 5, Bookmarks: 2})
	if len(r.Changed) != 0 {
		t.Errorf("re-import changed %v", r.Changed)
	}
}

func TestImportNotes(t *testing.T) {
	s := openStore(t)
	clips := parseFixture(t, "kindle", "edge-cases.txt")

	r := mustImport(t, s, clips)
	checkResult(t, r, Result{Added: 3, Notes: 1})
	var hid int64
	if err := s.DB().QueryRow(`SELECT id FROM highlights WHERE loc_start = 200`).Scan(&hid); err != nil {
		t.Fatal(err)
	}
	checkNotes := func() {
		t.Helper()
		notes, err := s.Notes(hid)
		if err != nil {
			t.Fatal(err)
		}
		if len(notes) != 1 || notes[0].Body != "obstacle is the way" || notes[0].Source != "kindle" ||
			!notes[0].CreatedAt.Equal(time.Date(2026, 1, 5, 9, 15, 30, 0, time.UTC)) {
			t.Errorf("notes = %+v", notes)
		}
	}
	checkNotes()
	if n := count(t, s, `SELECT count(*) FROM highlights_fts WHERE highlights_fts MATCH 'obstacle'`); n != 1 {
		t.Errorf("note not searchable: %d FTS matches", n)
	}

	r = mustImport(t, s, clips)
	checkResult(t, r, Result{Unchanged: 4})
	checkNotes()
}

func TestImportOrphanNote(t *testing.T) {
	s := openStore(t)
	orphan := clippings.Clipping{Kind: clippings.Note, Dialect: clippings.Kindle, Title: "Meditations",
		LocStart: 900, LocEnd: 900, AddedAt: now, Text: "a thought with no highlight"}

	checkResult(t, mustImport(t, s, []clippings.Clipping{orphan}), Result{Orphans: 1})
	checkResult(t, mustImport(t, s, []clippings.Clipping{orphan}), Result{Unchanged: 1})
}

// A later file carries a newer version of a stored highlight: the stored
// row is updated in place, keeping its id, note, and favorite, and its
// stale embedding is dropped.
func TestImportNewerVersionUpdatesInPlace(t *testing.T) {
	s := openStore(t)
	v1 := clip(10, 10, 1, "the stones remember every flood that moved them.")
	v2 := clip(10, 11, 2, "the stones remember every flood that moved them. we drift.")

	r := mustImport(t, s, []clippings.Clipping{v1})
	id := r.Changed[0]
	db := s.DB()
	if _, err := db.Exec(`UPDATE highlights SET favorite = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(id, "mine", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO embeddings (highlight_id, model, dims, vector, created_at)
		VALUES (?, 'm', 1, x'00000000', '2026-01-01T00:00:00Z')`, id); err != nil {
		t.Fatal(err)
	}

	r = mustImport(t, s, []clippings.Clipping{v2})
	checkResult(t, r, Result{Replaced: 1})
	if len(r.Changed) != 1 || r.Changed[0] != id {
		t.Errorf("Changed = %v, want [%d]", r.Changed, id)
	}
	var (
		text string
		fav  bool
	)
	if err := db.QueryRow(`SELECT text, favorite FROM highlights WHERE id = ?`, id).Scan(&text, &fav); err != nil {
		t.Fatal(err)
	}
	notes, err := s.Notes(id)
	if err != nil {
		t.Fatal(err)
	}
	if text != v2.Text || !fav || len(notes) != 1 || notes[0].Body != "mine" {
		t.Errorf("row = %q, favorite %v, notes %+v; want new text with note and favorite kept", text, fav, notes)
	}
	if n := count(t, s, `SELECT count(*) FROM embeddings WHERE highlight_id = ?`, id); n != 0 {
		t.Error("stale embedding was not deleted")
	}

	// Importing the old file again must not roll the highlight back.
	checkResult(t, mustImport(t, s, []clippings.Clipping{v1}), Result{Older: 1})
}

// Two highlights stored separately, then a newer version that spans both:
// they become one row carrying both rows' tags, notes, and favorite.
func TestImportMergesStoredRows(t *testing.T) {
	s := openStore(t)
	a := clip(10, 10, 1, "the stones remember every flood that moved them.")
	b := clip(11, 11, 2, "we forget the quiet stones that never moved at all.")
	r := mustImport(t, s, []clippings.Clipping{a, b})
	idA, idB := r.Changed[0], r.Changed[1]

	db := s.DB()
	for _, q := range []string{
		`INSERT INTO tags (id, name) VALUES (1, 'risk'), (2, 'bias')`,
		`INSERT INTO highlight_tags VALUES (` + itoa(idA) + `, 1), (` + itoa(idB) + `, 2)`,
		`UPDATE highlights SET favorite = 1, last_seen = '2026-02-01T00:00:00Z' WHERE id = ` + itoa(idB),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := s.AddNote(idA, "note a", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(idB, "note b", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	r = mustImport(t, s, []clippings.Clipping{clip(10, 11, 3, a.Text+" "+b.Text)})
	checkResult(t, r, Result{Replaced: 1})
	if n := count(t, s, `SELECT count(*) FROM highlights`); n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}
	var (
		id   int64
		seen string
		fav  bool
	)
	if err := db.QueryRow(`SELECT id, favorite, last_seen FROM highlights`).Scan(&id, &fav, &seen); err != nil {
		t.Fatal(err)
	}
	if id != idB {
		t.Errorf("kept id %d, want the more recent row %d", id, idB)
	}
	if !fav || seen != "2026-02-01T00:00:00Z" {
		t.Errorf("merged row favorite=%v last_seen=%q", fav, seen)
	}
	notes, err := s.Notes(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Body != "note a" || notes[1].Body != "note b" {
		t.Errorf("merged notes = %+v, want both, oldest first", notes)
	}
	if n := count(t, s, `SELECT count(*) FROM highlight_tags WHERE highlight_id = ?`, id); n != 2 {
		t.Errorf("merged row has %d tags, want 2", n)
	}
}

func TestImportCrossPoint(t *testing.T) {
	s := openStore(t)
	clips := parseFixture(t, "crosspoint", "clippings.txt")
	checkResult(t, mustImport(t, s, clips), Result{Added: 2})
	if n := count(t, s, `SELECT count(*) FROM highlights WHERE source = 'crosspoint' AND page != '' AND loc_start IS NULL`); n != 2 {
		t.Errorf("%d crosspoint rows with page and no location, want 2", n)
	}
	checkResult(t, mustImport(t, s, clips), Result{Unchanged: 2})
}

// TestImportFullExportsIdempotent runs against the untracked full exports,
// if present.
func TestImportFullExportsIdempotent(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "*", "*-full.txt"))
	if len(paths) == 0 {
		t.Skip("no full exports in testdata")
	}
	for _, p := range paths {
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			s := openStore(t)
			clips := parseFixture(t, filepath.Base(filepath.Dir(p)), filepath.Base(p))
			first := mustImport(t, s, clips)
			rows := count(t, s, `SELECT count(*) FROM highlights`)
			second := mustImport(t, s, clips)
			if second.Added != 0 || second.Replaced != 0 || second.Older != 0 || second.Unchanged != first.Added {
				t.Errorf("re-import not idempotent: first %+v, second %+v", first, second)
			}
			if n := count(t, s, `SELECT count(*) FROM highlights`); n != rows {
				t.Errorf("rows %d -> %d", rows, n)
			}
			if n := count(t, s, `SELECT count(*) FROM highlights_fts`); n != rows {
				t.Errorf("fts rows %d, highlights %d", n, rows)
			}
			t.Logf("%d highlights from %d entries", rows, len(clips))
		})
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
