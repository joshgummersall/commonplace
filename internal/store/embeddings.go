package store

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// EmbedJob is a highlight whose embedding is missing or was made by
// another model.
type EmbedJob struct {
	ID   int64
	Text string
}

const staleEmbeddings = `FROM highlights h LEFT JOIN embeddings e ON e.highlight_id = h.id
	WHERE e.highlight_id IS NULL OR e.model != ?`

// StaleEmbeddings returns up to limit highlights needing a (new) embedding
// for model, oldest first.
func (s *Store) StaleEmbeddings(model string, limit int) ([]EmbedJob, error) {
	rows, err := s.db.Query(`SELECT h.id, h.text `+staleEmbeddings+` ORDER BY h.id LIMIT ?`, model, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmbedJob
	for rows.Next() {
		var j EmbedJob
		if err := rows.Scan(&j.ID, &j.Text); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CountStaleEmbeddings counts highlights needing a (new) embedding.
func (s *Store) CountStaleEmbeddings(model string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) `+staleEmbeddings, model).Scan(&n)
	return n, err
}

// SaveEmbeddings stores vectors for highlights, replacing any older ones.
// A highlight deleted meanwhile (merged by a concurrent import) is
// skipped.
func (s *Store) SaveEmbeddings(model string, ids []int64, vecs [][]float32, now time.Time) error {
	if len(ids) != len(vecs) {
		return fmt.Errorf("save embeddings: %d ids for %d vectors", len(ids), len(vecs))
	}
	return s.InTx(func(t *Tx) error {
		for i, id := range ids {
			_, err := t.tx.Exec(`INSERT OR REPLACE INTO embeddings (highlight_id, model, dims, vector, created_at)
				SELECT id, ?, ?, ?, ? FROM highlights WHERE id = ?`,
				model, len(vecs[i]), EncodeVector(vecs[i]), formatTime(now), id)
			if err != nil {
				return fmt.Errorf("save embedding for %d: %w", id, err)
			}
		}
		return nil
	})
}

// Vector is a stored embedding.
type Vector struct {
	HighlightID int64
	BookID      int64
	Vec         []float32
}

// Vectors loads every embedding made by model, with each highlight's book
// (for preferring other books in similar-highlight lists).
func (s *Store) Vectors(model string) ([]Vector, error) {
	rows, err := s.db.Query(`SELECT e.highlight_id, h.book_id, e.vector FROM embeddings e
		JOIN highlights h ON h.id = e.highlight_id
		WHERE e.model = ? ORDER BY e.highlight_id`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vector
	for rows.Next() {
		var (
			v    Vector
			blob []byte
		)
		if err := rows.Scan(&v.HighlightID, &v.BookID, &blob); err != nil {
			return nil, err
		}
		if v.Vec, err = DecodeVector(blob); err != nil {
			return nil, fmt.Errorf("embedding for %d: %w", v.HighlightID, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// EncodeVector stores a vector as little-endian float32s.
func EncodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// DecodeVector reverses EncodeVector.
func DecodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector blob is %d bytes, not a multiple of 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
