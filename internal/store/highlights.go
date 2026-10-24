package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Highlight is a row of the highlights table. Zero values stand in for
// NULL: LocStart/LocEnd 0 means no location, and a zero time means unset.
type Highlight struct {
	ID            int64
	BookID        int64
	Source        string
	Text          string
	Location      string
	Page          string
	LocStart      int
	LocEnd        int
	HighlightedAt time.Time
	ImportedAt    time.Time
	Hash          string
	Favorite      bool
	Buried        bool
	LastSeen      time.Time
	Weight        float64
}

// Tx is a write transaction. Get one from Store.InTx.
type Tx struct {
	tx *sql.Tx
}

// InTx runs fn in a transaction, committing if fn returns nil.
func (s *Store) InTx(fn func(*Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(&Tx{tx: tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// BookID returns the id of the book with this title and author, creating
// it if needed.
func (t *Tx) BookID(title, author string) (int64, error) {
	var id int64
	err := t.tx.QueryRow(`SELECT id FROM books WHERE title = ? AND author = ?`, title, author).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := t.tx.Exec(`INSERT INTO books (title, author) VALUES (?, ?)`, title, author)
	if err != nil {
		return 0, fmt.Errorf("insert book %q: %w", title, err)
	}
	return res.LastInsertId()
}

const highlightColumns = `id, book_id, source, text, location, page, loc_start, loc_end,
	highlighted_at, imported_at, hash, favorite, buried, last_seen, weight`

// HighlightByHash returns the book's highlight with this text hash, or nil.
func (t *Tx) HighlightByHash(bookID int64, hash string) (*Highlight, error) {
	rows, err := t.tx.Query(`SELECT `+highlightColumns+` FROM highlights WHERE book_id = ? AND hash = ?`, bookID, hash)
	if err != nil {
		return nil, err
	}
	hs, err := scanHighlights(rows)
	if err != nil || len(hs) == 0 {
		return nil, err
	}
	return &hs[0], nil
}

// HighlightsNear returns the book's highlights whose location range
// overlaps [locStart, locEnd]. When locStart is 0 (no location, as with
// CrossPoint) it matches on page instead.
func (t *Tx) HighlightsNear(bookID int64, locStart, locEnd int, page string) ([]Highlight, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if locStart > 0 {
		rows, err = t.tx.Query(`SELECT `+highlightColumns+` FROM highlights
			WHERE book_id = ? AND loc_start <= ? AND loc_end >= ?
			ORDER BY id`, bookID, locEnd, locStart)
	} else {
		rows, err = t.tx.Query(`SELECT `+highlightColumns+` FROM highlights
			WHERE book_id = ? AND loc_start IS NULL AND page = ?
			ORDER BY id`, bookID, page)
	}
	if err != nil {
		return nil, err
	}
	return scanHighlights(rows)
}

// InsertHighlight inserts h and sets h.ID.
func (t *Tx) InsertHighlight(h *Highlight) error {
	res, err := t.tx.Exec(`INSERT INTO highlights
		(book_id, source, text, location, page, loc_start, loc_end, highlighted_at, imported_at, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.BookID, h.Source, h.Text, h.Location, h.Page, nullInt(h.LocStart), nullInt(h.LocEnd),
		nullTime(h.HighlightedAt), formatTime(h.ImportedAt), h.Hash)
	if err != nil {
		return fmt.Errorf("insert highlight: %w", err)
	}
	h.ID, err = res.LastInsertId()
	return err
}

// ReplaceHighlightText swaps in a newer version of a highlight's text and
// position, keeping its id, notes, favorite, and history.
func (t *Tx) ReplaceHighlightText(id int64, h *Highlight) error {
	_, err := t.tx.Exec(`UPDATE highlights SET
		source = ?, text = ?, location = ?, page = ?, loc_start = ?, loc_end = ?, highlighted_at = ?, hash = ?
		WHERE id = ?`,
		h.Source, h.Text, h.Location, h.Page, nullInt(h.LocStart), nullInt(h.LocEnd),
		nullTime(h.HighlightedAt), h.Hash, id)
	if err != nil {
		return fmt.Errorf("replace highlight %d: %w", id, err)
	}
	return nil
}

// MergeInto folds highlight dropID into keepID and deletes dropID: keepID
// takes dropID's notes, favorite if either was, and the later last_seen.
func (t *Tx) MergeInto(keepID, dropID int64) error {
	stmts := []struct {
		q    string
		args []any
	}{
		// OR IGNORE skips device notes keepID already has; the delete below
		// cascades to them.
		{`UPDATE OR IGNORE notes SET highlight_id = ? WHERE highlight_id = ?`, []any{keepID, dropID}},
		{`UPDATE highlights SET
			favorite = favorite OR (SELECT favorite FROM highlights WHERE id = ?),
			last_seen = max(coalesce(last_seen, ''), coalesce((SELECT last_seen FROM highlights WHERE id = ?), ''))
			WHERE id = ?`, []any{dropID, dropID, keepID}},
		{`UPDATE highlights SET last_seen = NULL WHERE id = ? AND last_seen = ''`, []any{keepID}},
		{`DELETE FROM highlights WHERE id = ?`, []any{dropID}},
	}
	for _, s := range stmts {
		if _, err := t.tx.Exec(s.q, s.args...); err != nil {
			return fmt.Errorf("merge highlight %d into %d: %w", dropID, keepID, err)
		}
	}
	return nil
}

func scanHighlights(rows *sql.Rows) ([]Highlight, error) {
	defer rows.Close()
	var out []Highlight
	for rows.Next() {
		h, err := scanHighlight(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// scanHighlight scans highlightColumns, then any extra destinations.
func scanHighlight(rows *sql.Rows, extra ...any) (Highlight, error) {
	var (
		h                               Highlight
		locStart, locEnd                sql.NullInt64
		highlightedAt, imported, seenAt sql.NullString
	)
	dest := []any{&h.ID, &h.BookID, &h.Source, &h.Text, &h.Location, &h.Page,
		&locStart, &locEnd, &highlightedAt, &imported, &h.Hash, &h.Favorite, &h.Buried,
		&seenAt, &h.Weight}
	if err := rows.Scan(append(dest, extra...)...); err != nil {
		return Highlight{}, err
	}
	h.LocStart, h.LocEnd = int(locStart.Int64), int(locEnd.Int64)
	var err error
	if h.HighlightedAt, err = parseTime(highlightedAt); err != nil {
		return Highlight{}, err
	}
	if h.ImportedAt, err = parseTime(imported); err != nil {
		return Highlight{}, err
	}
	if h.LastSeen, err = parseTime(seenAt); err != nil {
		return Highlight{}, err
	}
	return h, nil
}

// Times are stored as RFC 3339 in UTC so they sort as text.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func parseTime(s sql.NullString) (time.Time, error) {
	if !s.Valid || s.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored time %q: %w", s.String, err)
	}
	return t, nil
}

// MarkSeen sets a highlight's last_seen.
func (t *Tx) MarkSeen(id int64, now time.Time) error {
	_, err := t.tx.Exec(`UPDATE highlights SET last_seen = ? WHERE id = ?`, formatTime(now), id)
	return err
}
