package store

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

// SetFavorite marks or unmarks a highlight as a favorite.
func (s *Store) SetFavorite(id int64, favorite bool) error {
	return s.execOne(`UPDATE highlights SET favorite = ? WHERE id = ?`, favorite, id)
}

// SetBuried hides a highlight from the daily shuffle, or brings it back.
// It stays searchable either way.
func (s *Store) SetBuried(id int64, buried bool) error {
	return s.execOne(`UPDATE highlights SET buried = ? WHERE id = ?`, buried, id)
}

// BookCount is a book and how many highlights it has.
type BookCount struct {
	ID     int64
	Title  string
	Author string
	Count  int
}

// Books lists books that have highlights, by title.
func (s *Store) Books() ([]BookCount, error) {
	rows, err := s.db.Query(`SELECT b.id, b.title, b.author, count(h.id)
		FROM books b JOIN highlights h ON h.book_id = b.id
		GROUP BY b.id ORDER BY b.title COLLATE NOCASE, b.author`)
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

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}
