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

// Entry is a highlight with what a reader needs to display it.
type Entry struct {
	Highlight
	BookTitle  string
	BookAuthor string
}

// EntryColumns selects an Entry from highlights h joined to books b. Use
// it with ScanEntry in queries outside this package.
var EntryColumns = prefixed("h.", highlightColumns) + ", b.title, b.author"

// ScanEntry scans EntryColumns, then any extra destinations.
func ScanEntry(rows *sql.Rows, extra ...any) (Entry, error) {
	var e Entry
	h, err := scanHighlight(rows, append([]any{&e.BookTitle, &e.BookAuthor}, extra...)...)
	e.Highlight = h
	return e, err
}

// Entries loads highlights by id, in the order given. Missing ids are
// skipped.
func (s *Store) Entries(ids []int64) ([]Entry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT `+EntryColumns+`
		FROM highlights h JOIN books b ON b.id = h.book_id
		WHERE h.id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	byID, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(ids))
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// Entry loads one highlight, or returns ErrNotFound.
func (s *Store) Entry(id int64) (Entry, error) {
	es, err := s.Entries([]int64{id})
	if err != nil {
		return Entry{}, err
	}
	if len(es) == 0 {
		return Entry{}, ErrNotFound
	}
	return es[0], nil
}

func scanEntries(rows *sql.Rows) (map[int64]Entry, error) {
	defer rows.Close()
	out := make(map[int64]Entry)
	for rows.Next() {
		e, err := ScanEntry(rows)
		if err != nil {
			return nil, err
		}
		out[e.ID] = e
	}
	return out, rows.Err()
}

// CountHighlights counts every highlight.
func (s *Store) CountHighlights() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM highlights`).Scan(&n)
	return n, err
}

// SetFavorite marks or unmarks a highlight as a favorite.
func (s *Store) SetFavorite(id int64, favorite bool) error {
	return s.execOne(`UPDATE highlights SET favorite = ? WHERE id = ?`, favorite, id)
}

// ToggleFavorite flips whether a highlight is a favorite and returns the
// new value, or ErrNotFound.
func (s *Store) ToggleFavorite(id int64) (bool, error) {
	var favorite bool
	err := s.db.QueryRow(`UPDATE highlights SET favorite = NOT favorite
		WHERE id = ? RETURNING favorite`, id).Scan(&favorite)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return favorite, err
}

// SetBuried hides a highlight from the daily shuffle and the web app, or
// brings it back. The CLI lists it either way.
func (s *Store) SetBuried(id int64, buried bool) error {
	return s.execOne(`UPDATE highlights SET buried = ? WHERE id = ?`, buried, id)
}
