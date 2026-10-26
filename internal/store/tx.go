package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

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

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// prefixed qualifies each column in a comma-separated list.
func prefixed(prefix, columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
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
