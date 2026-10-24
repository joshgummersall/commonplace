package store

import (
	"path/filepath"
	"testing"
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

	mustExec(`UPDATE highlights SET note = 'stoic obstacle' WHERE id = 1`)
	if n := match("obstacle"); n != 1 {
		t.Errorf("note match after update = %d, want 1", n)
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
