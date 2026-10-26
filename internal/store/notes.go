package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// UserSource marks notes written in the web app.
const UserSource = "user"

// Note is a dated note on a highlight.
type Note struct {
	ID          int64
	HighlightID int64
	Source      string // "kindle", "crosspoint", or UserSource
	Body        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AddDeviceNote stores a note imported from a device, unless this
// highlight already has (or had, before an edit or delete) a note with the
// same original text. It reports whether a note was added. at is when the
// note was written on the device (now if unknown); updated_at starts equal
// to it, so only a real edit marks the note as edited.
func (t *Tx) AddDeviceNote(highlightID int64, source, text string, at, now time.Time) (bool, error) {
	text = strings.TrimSpace(text)
	if at.IsZero() {
		at = now
	}
	res, err := t.tx.Exec(`INSERT OR IGNORE INTO notes
		(highlight_id, source, body, device_text, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		highlightID, source, text, text, formatTime(at), formatTime(at))
	if err != nil {
		return false, fmt.Errorf("add device note: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

const noteColumns = `id, highlight_id, source, body, created_at, updated_at`

// Note returns one live note, or ErrNotFound.
func (s *Store) Note(id int64) (Note, error) {
	rows, err := s.db.Query(`SELECT `+noteColumns+` FROM notes WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return Note{}, err
	}
	ns, err := scanNotes(rows)
	if err != nil {
		return Note{}, err
	}
	if len(ns) == 0 {
		return Note{}, ErrNotFound
	}
	return ns[0], nil
}

// Notes returns a highlight's notes, oldest first.
func (s *Store) Notes(highlightID int64) ([]Note, error) {
	rows, err := s.db.Query(`SELECT `+noteColumns+`
		FROM notes WHERE highlight_id = ? AND deleted_at IS NULL
		ORDER BY created_at, id`, highlightID)
	if err != nil {
		return nil, err
	}
	return scanNotes(rows)
}

func scanNotes(rows *sql.Rows) ([]Note, error) {
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var (
			n                  Note
			created, updatedAt sql.NullString
		)
		err := rows.Scan(&n.ID, &n.HighlightID, &n.Source, &n.Body, &created, &updatedAt)
		if err != nil {
			return nil, err
		}
		if n.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if n.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddNote adds a note written by the user.
func (s *Store) AddNote(highlightID int64, body string, now time.Time) (Note, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Note{}, invalid("note is empty")
	}
	res, err := s.db.Exec(`INSERT INTO notes (highlight_id, source, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, highlightID, UserSource, body, formatTime(now), formatTime(now))
	if err != nil {
		return Note{}, fmt.Errorf("add note: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Note{}, err
	}
	now = now.UTC().Truncate(time.Second)
	return Note{ID: id, HighlightID: highlightID, Source: UserSource, Body: body, CreatedAt: now, UpdatedAt: now}, nil
}

// UpdateNote replaces a note's body. Device notes can be edited too; the
// original text is kept aside so re-imports don't restore it.
func (s *Store) UpdateNote(id int64, body string, now time.Time) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return invalid("note is empty")
	}
	return s.execOne(`UPDATE notes SET body = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		body, formatTime(now), id)
}

// DeleteNote hides a note. The row stays so that re-importing doesn't
// bring a deleted device note back.
func (s *Store) DeleteNote(id int64, now time.Time) error {
	return s.execOne(`UPDATE notes SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, formatTime(now), id)
}

// RestoreNote undoes DeleteNote and returns the note's highlight id.
func (s *Store) RestoreNote(id int64) (int64, error) {
	var hid int64
	err := s.db.QueryRow(`UPDATE notes SET deleted_at = NULL
		WHERE id = ? AND deleted_at IS NOT NULL RETURNING highlight_id`, id).Scan(&hid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return hid, err
}

// NoteCounts returns how many live notes each highlight has.
func (s *Store) NoteCounts(ids []int64) (map[int64]int, error) {
	out := make(map[int64]int)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT highlight_id, count(*) FROM notes
		WHERE deleted_at IS NULL AND highlight_id IN (`+placeholders(len(ids))+`)
		GROUP BY highlight_id`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
