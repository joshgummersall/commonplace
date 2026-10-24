package store

import (
	"database/sql"
	"strings"
)

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

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// prefixed qualifies each column in a comma-separated list.
func prefixed(prefix, columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}
