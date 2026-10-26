package store

import (
	"database/sql"
	"errors"
	"fmt"
)

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

// BookCount is a book and how many highlights it has.
type BookCount struct {
	ID     int64
	Title  string
	Author string
	Count  int
}

// Books lists books that have highlights, by title. Without withBuried,
// buried highlights aren't counted, and a book with only buried ones is
// left out.
func (s *Store) Books(withBuried bool) ([]BookCount, error) {
	rows, err := s.db.Query(`SELECT b.id, b.title, b.author, count(h.id)
		FROM books b JOIN highlights h ON h.book_id = b.id
		WHERE ? OR h.buried = 0
		GROUP BY b.id ORDER BY b.title COLLATE NOCASE, b.author`, withBuried)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookCount
	for rows.Next() {
		var b BookCount
		if err := rows.Scan(&b.ID, &b.Title, &b.Author, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
