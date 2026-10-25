package store

import (
	"database/sql"
	"fmt"
	"time"
)

// DailyCandidate is what the daily shuffle weighs about a highlight.
type DailyCandidate struct {
	ID       int64
	Weight   float64
	Favorite bool
	LastSeen time.Time // zero if never seen
}

// DailyCandidates lists every highlight that isn't buried, by id.
func (s *Store) DailyCandidates() ([]DailyCandidate, error) {
	rows, err := s.db.Query(`SELECT id, weight, favorite, last_seen FROM highlights
		WHERE buried = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyCandidate
	for rows.Next() {
		var (
			c    DailyCandidate
			seen sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.Weight, &c.Favorite, &seen); err != nil {
			return nil, err
		}
		if c.LastSeen, err = parseTime(seen); err != nil {
			return nil, fmt.Errorf("highlight %d last_seen: %w", c.ID, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DailyPicks returns the highlights chosen for day (YYYY-MM-DD), in
// order, or none if the day has no set yet.
func (s *Store) DailyPicks(day string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT highlight_id FROM daily_picks WHERE day = ? ORDER BY position`, day)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// SaveDailyPicks stores day's set unless one is already stored, and
// returns whichever set the day has. INSERT OR IGNORE on the (day,
// position) key means a concurrent first request loses quietly.
func (s *Store) SaveDailyPicks(day string, ids []int64) ([]int64, error) {
	err := s.InTx(func(t *Tx) error {
		for i, id := range ids {
			if _, err := t.tx.Exec(`INSERT OR IGNORE INTO daily_picks (day, position, highlight_id) VALUES (?, ?, ?)`,
				day, i, id); err != nil {
				return fmt.Errorf("store daily picks: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.DailyPicks(day)
}

// MarkSeen sets last_seen on highlights that were shown.
func (s *Store) MarkSeen(ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(`UPDATE highlights SET last_seen = ? WHERE id IN (`+placeholders(len(ids))+`)`,
		append([]any{formatTime(now)}, int64Args(ids)...)...)
	return err
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
