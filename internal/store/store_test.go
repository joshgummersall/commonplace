package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	s := openTemp(t)
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Fatalf("schema version = %d, want %d", v, len(migrations))
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for i := range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		s.Close()
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("Open succeeded on a newer schema, want error")
	}
}

func TestFTSTracksHighlightsAndBooks(t *testing.T) {
	s := openTemp(t)
	db := s.DB()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	match := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM highlights_fts WHERE highlights_fts MATCH ?`, q).Scan(&n); err != nil {
			t.Fatalf("match %q: %v", q, err)
		}
		return n
	}

	mustExec(`INSERT INTO books (id, title, author) VALUES (1, 'Meditations', 'Marcus Aurelius')`)
	mustExec(`INSERT INTO highlights (id, book_id, text, imported_at, hash)
		VALUES (1, 1, 'The impediment to action advances action.', '2026-01-01T00:00:00Z', 'h1')`)

	if n := match("impediment"); n != 1 {
		t.Errorf("text match = %d, want 1", n)
	}
	if n := match("aurelius"); n != 1 {
		t.Errorf("author match = %d, want 1", n)
	}
	// Porter stemming: "advancing" should match "advances".
	if n := match("advancing"); n != 1 {
		t.Errorf("stemmed match = %d, want 1", n)
	}

	mustExec(`INSERT INTO notes (id, highlight_id, source, body, created_at, updated_at)
		VALUES (1, 1, 'user', 'stoic obstacle', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if n := match("obstacle"); n != 1 {
		t.Errorf("note match after insert = %d, want 1", n)
	}
	mustExec(`UPDATE notes SET body = 'stoic hurdle' WHERE id = 1`)
	if n := match("obstacle"); n != 0 {
		t.Errorf("old note text still matches after edit")
	}
	if n := match("hurdle"); n != 1 {
		t.Errorf("edited note match = %d, want 1", n)
	}
	mustExec(`UPDATE notes SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = 1`)
	if n := match("hurdle"); n != 0 {
		t.Errorf("soft-deleted note still matches")
	}
	// Changing the highlight text must keep notes in the FTS row.
	mustExec(`INSERT INTO notes (highlight_id, source, body, created_at, updated_at)
		VALUES (1, 'kindle', 'device thought', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	mustExec(`UPDATE highlights SET text = 'The impediment to action advances action. What stands in the way becomes the way.' WHERE id = 1`)
	if n := match("device"); n != 1 {
		t.Errorf("note lost from FTS after highlight text update")
	}

	mustExec(`UPDATE books SET author = 'Marcus Antoninus' WHERE id = 1`)
	if n := match("aurelius"); n != 0 {
		t.Errorf("old author match after rename = %d, want 0", n)
	}
	if n := match("antoninus"); n != 1 {
		t.Errorf("new author match after rename = %d, want 1", n)
	}

	// Deleting the book cascades to highlights, which must clear FTS rows.
	mustExec(`DELETE FROM books WHERE id = 1`)
	if n := match("impediment"); n != 0 {
		t.Errorf("match after cascade delete = %d, want 0", n)
	}
}

// Migration 0003 moves highlights.note into the notes table and keeps it
// searchable.
func TestMigrateNotesFromV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{db: db}
	if err := old.migrateTo(2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO books (id, title, author) VALUES (1, 'Meditations', 'Marcus Aurelius')`,
		`INSERT INTO highlights (id, book_id, source, text, note, imported_at, hash)
			VALUES (1, 1, 'kindle', 'The impediment to action.', 'obstacle is the way', '2026-01-01T00:00:00Z', 'h1')`,
		`INSERT INTO highlights (id, book_id, source, text, imported_at, hash)
			VALUES (2, 1, 'kindle', 'Waste no more time.', '2026-01-01T00:00:00Z', 'h2')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	notes, err := s.Notes(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Body != "obstacle is the way" || notes[0].Source != "kindle" {
		t.Fatalf("notes = %+v", notes)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM highlights_fts WHERE highlights_fts MATCH 'obstacle'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("migrated note FTS matches = %d, want 1", n)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM highlights_fts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("FTS rows after rebuild = %d, want 2", n)
	}
	// Re-importing the same device note must not duplicate it.
	err = s.InTx(func(tx *Tx) error {
		added, err := tx.AddDeviceNote(1, "kindle", "obstacle is the way", time.Time{}, time.Now())
		if added {
			t.Error("migrated device note was added again")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
