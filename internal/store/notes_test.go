package store

import (
	"errors"
	"testing"
	"time"
)

func seedHighlight(t *testing.T, s *Store) int64 {
	t.Helper()
	var id int64
	err := s.InTx(func(tx *Tx) error {
		bookID, err := tx.BookID("Meditations", "Marcus Aurelius")
		if err != nil {
			return err
		}
		h := &Highlight{BookID: bookID, Source: "kindle", Text: "The impediment to action.",
			ImportedAt: time.Now(), Hash: "h1"}
		if err := tx.InsertHighlight(h); err != nil {
			return err
		}
		id = h.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUserNotes(t *testing.T) {
	s := openTemp(t)
	hid := seedHighlight(t, s)
	t1 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	if _, err := s.AddNote(hid, "   ", t1); err == nil {
		t.Error("AddNote accepted an empty note")
	}
	a, err := s.AddNote(hid, "Re-read after the reorg.", t1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddNote(hid, "Still true.", t2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateNote(a.ID, "Re-read after the reorg. Still true.", t2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNote(b.ID, t2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNote(b.ID, t2); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v, want ErrNotFound", err)
	}
	if err := s.UpdateNote(b.ID, "edit a deleted note", t2); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of deleted note err = %v, want ErrNotFound", err)
	}

	notes, err := s.Notes(hid)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want 1", notes)
	}
	n := notes[0]
	if n.Body != "Re-read after the reorg. Still true." || n.Source != UserSource ||
		!n.CreatedAt.Equal(t1) || !n.UpdatedAt.Equal(t2) {
		t.Errorf("note = %+v", n)
	}
}

// Editing or deleting a device note must survive re-imports.
func TestDeviceNoteEditsSurviveReimport(t *testing.T) {
	s := openTemp(t)
	hid := seedHighlight(t, s)
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	addDevice := func(text string) bool {
		t.Helper()
		var added bool
		err := s.InTx(func(tx *Tx) error {
			var err error
			added, err = tx.AddDeviceNote(hid, "kindle", text, time.Time{}, now)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return added
	}

	if !addDevice("obstacle is the way") {
		t.Fatal("first device note not added")
	}
	if !addDevice("second thought") {
		t.Fatal("second device note not added")
	}
	notes, _ := s.Notes(hid)
	if err := s.UpdateNote(notes[0].ID, "the obstacle is the way", now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNote(notes[1].ID, now); err != nil {
		t.Fatal(err)
	}

	if addDevice("obstacle is the way") || addDevice("second thought") {
		t.Error("re-import restored an edited or deleted device note")
	}
	notes, _ = s.Notes(hid)
	if len(notes) != 1 || notes[0].Body != "the obstacle is the way" {
		t.Errorf("notes = %+v", notes)
	}
}

// Merging two highlights that both carry the same device note keeps one
// copy and moves the rest.
func TestMergeIntoMovesNotes(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	var keep, drop int64
	err := s.InTx(func(tx *Tx) error {
		bookID, err := tx.BookID("Meditations", "Marcus Aurelius")
		if err != nil {
			return err
		}
		for i, id := range []*int64{&keep, &drop} {
			h := &Highlight{BookID: bookID, Text: "text", ImportedAt: now, Hash: string(rune('a' + i))}
			if err := tx.InsertHighlight(h); err != nil {
				return err
			}
			*id = h.ID
			if _, err := tx.AddDeviceNote(h.ID, "kindle", "shared device note", now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(drop, "only on the dropped row", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.InTx(func(tx *Tx) error { return tx.MergeInto(keep, drop) }); err != nil {
		t.Fatal(err)
	}
	notes, err := s.Notes(keep)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Body != "shared device note" || notes[1].Body != "only on the dropped row" {
		t.Errorf("notes = %+v", notes)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM notes WHERE highlight_id = ?`, drop).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d notes left on dropped highlight (err %v)", n, err)
	}
}
