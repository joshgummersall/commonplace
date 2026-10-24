// Package daily picks the day's highlights to resurface.
package daily

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/joshgummersall/commonplace/internal/store"
)

// DefaultSize is how many highlights a day's shuffle holds.
const DefaultSize = 10

// Weighting. A highlight's chance grows with the days since it was last
// seen, capped so a long-neglected highlight doesn't drown everything
// else. Never-seen highlights count as seen maxAgeDays ago, so new imports
// surface first. Favorites get a constant boost. The per-highlight weight
// column multiplies the result.
const (
	maxAgeDays    = 365
	favoriteBoost = 3.0
)

// candidate is what the weighting needs to know about a highlight.
type candidate struct {
	id       int64
	weight   float64
	favorite bool
	lastSeen time.Time
}

func (c candidate) score(now time.Time) float64 {
	days := float64(maxAgeDays)
	if !c.lastSeen.IsZero() {
		days = math.Min(now.Sub(c.lastSeen).Hours()/24, maxAgeDays)
	}
	w := (1 + math.Max(days, 0)) * c.weight
	if c.favorite {
		w *= favoriteBoost
	}
	return w
}

// Today returns the day's shuffle for the local date of now, choosing and
// storing it on the first call of the day. Later calls return the same
// highlights, minus any deleted or buried since.
func Today(s *store.Store, now time.Time, size int) ([]store.Entry, error) {
	day := now.Format(time.DateOnly)
	ids, err := picks(s.DB(), day)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		if ids, err = choose(s.DB(), day, now, size); err != nil {
			return nil, err
		}
	}
	entries, err := s.Entries(ids)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(entries, func(e store.Entry) bool { return e.Buried }), nil
}

// MarkSeen records that highlights were shown.
func MarkSeen(s *store.Store, ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return s.InTx(func(tx *store.Tx) error {
		for _, id := range ids {
			if err := tx.MarkSeen(id, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func picks(db *sql.DB, day string) ([]int64, error) {
	rows, err := db.Query(`SELECT highlight_id FROM daily_picks WHERE day = ? ORDER BY position`, day)
	if err != nil {
		return nil, err
	}
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

// choose samples the day's highlights and stores them. INSERT OR IGNORE on
// the (day, position) key means a concurrent first request loses quietly;
// re-reading afterwards returns whichever set won.
func choose(db *sql.DB, day string, now time.Time, size int) ([]int64, error) {
	cands, err := candidates(db)
	if err != nil {
		return nil, err
	}
	chosen := sample(cands, size, now, seed(day))

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for i, id := range chosen {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO daily_picks (day, position, highlight_id) VALUES (?, ?, ?)`,
			day, i, id); err != nil {
			return nil, fmt.Errorf("store daily picks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return picks(db, day)
}

func candidates(db *sql.DB) ([]candidate, error) {
	rows, err := db.Query(`SELECT id, weight, favorite, last_seen FROM highlights
		WHERE buried = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var (
			c    candidate
			seen sql.NullString
		)
		if err := rows.Scan(&c.id, &c.weight, &c.favorite, &seen); err != nil {
			return nil, err
		}
		if seen.Valid && seen.String != "" {
			if c.lastSeen, err = time.Parse(time.RFC3339, seen.String); err != nil {
				return nil, fmt.Errorf("highlight %d last_seen: %w", c.id, err)
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// seed derives the day's random seed from its date, so a given day and
// database always produce the same shuffle.
func seed(day string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(day))
	return h.Sum64()
}

// sample draws up to size candidates without replacement, each with
// probability proportional to its score (Efraimidis-Spirakis: keep the
// largest u^(1/w), compared in log space as log(u)/w).
func sample(cands []candidate, size int, now time.Time, seed uint64) []int64 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	type keyed struct {
		id  int64
		key float64
	}
	keys := make([]keyed, 0, len(cands))
	for _, c := range cands {
		w := c.score(now)
		if w <= 0 {
			continue
		}
		u := rng.Float64()
		for u == 0 {
			u = rng.Float64()
		}
		keys = append(keys, keyed{c.id, math.Log(u) / w})
	}
	slices.SortFunc(keys, func(a, b keyed) int {
		switch {
		case a.key > b.key:
			return -1
		case a.key < b.key:
			return 1
		default:
			return 0
		}
	})
	if len(keys) > size {
		keys = keys[:size]
	}
	out := make([]int64, len(keys))
	for i, k := range keys {
		out[i] = k.id
	}
	return out
}
