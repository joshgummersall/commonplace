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

// ErrNotFound is returned when a row to change does not exist.
var ErrNotFound = errors.New("not found")

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
// same original text. It reports whether a note was added.
func (t *Tx) AddDeviceNote(highlightID int64, source, text string, at, now time.Time) (bool, error) {
	text = strings.TrimSpace(text)
	if at.IsZero() {
		at = now
	}
	res, err := t.tx.Exec(`INSERT OR IGNORE INTO notes
		(highlight_id, source, body, device_text, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		highlightID, source, text, text, formatTime(at), formatTime(now))
	if err != nil {
		return false, fmt.Errorf("add device note: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Notes returns a highlight's notes, oldest first.
func (s *Store) Notes(highlightID int64) ([]Note, error) {
	rows, err := s.db.Query(`SELECT id, highlight_id, source, body, created_at, updated_at
		FROM notes WHERE highlight_id = ? AND deleted_at IS NULL
		ORDER BY created_at, id`, highlightID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var (
			n                  Note
			created, updatedAt sql.NullString
		)
		if err := rows.Scan(&n.ID, &n.HighlightID, &n.Source, &n.Body, &created, &updatedAt); err != nil {
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
		return Note{}, errors.New("note is empty")
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
		return errors.New("note is empty")
	}
	return s.execOne(`UPDATE notes SET body = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		body, formatTime(now), id)
}

// DeleteNote hides a note. The row stays so that re-importing doesn't
// bring a deleted device note back.
func (s *Store) DeleteNote(id int64, now time.Time) error {
	return s.execOne(`UPDATE notes SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, formatTime(now), id)
}

func (s *Store) execOne(q string, args ...any) error {
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
