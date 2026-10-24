package daily

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/store"
)

func seedStore(t *testing.T, n int) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	err = s.InTx(func(tx *store.Tx) error {
		bookID, err := tx.BookID("Book", "Author")
		if err != nil {
			return err
		}
		for i := range n {
			h := &store.Highlight{BookID: bookID, Text: fmt.Sprintf("highlight %d", i),
				ImportedAt: time.Now(), Hash: fmt.Sprint(i)}
			if err := tx.InsertHighlight(h); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ids(es []store.Entry) []int64 {
	var out []int64
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func mustToday(t *testing.T, s *store.Store, now time.Time) []store.Entry {
	t.Helper()
	es, err := Today(s, now, DefaultSize)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestTodayIsStableWithinADay(t *testing.T) {
	s := seedStore(t, 100)
	morning := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)

	first := mustToday(t, s, morning)
	if len(first) != DefaultSize {
		t.Fatalf("got %d highlights, want %d", len(first), DefaultSize)
	}
	// Viewing marks them seen, which changes every weight; the day's set
	// must not change.
	if err := MarkSeen(s, ids(first), morning); err != nil {
		t.Fatal(err)
	}
	evening := morning.Add(12 * time.Hour)
	if got := ids(mustToday(t, s, evening)); fmt.Sprint(got) != fmt.Sprint(ids(first)) {
		t.Errorf("evening = %v, morning = %v", got, ids(first))
	}

	tomorrow := ids(mustToday(t, s, morning.AddDate(0, 0, 1)))
	seen := make(map[int64]bool)
	for _, id := range ids(first) {
		seen[id] = true
	}
	for _, id := range tomorrow {
		if seen[id] {
			t.Errorf("highlight %d shown again the next day despite 90 unseen alternatives", id)
		}
	}
}

func TestTodayExcludesBuried(t *testing.T) {
	s := seedStore(t, 12)
	if _, err := s.DB().Exec(`UPDATE highlights SET buried = 1 WHERE id <= 4`); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)
	got := mustToday(t, s, day)
	if len(got) != 8 {
		t.Fatalf("got %d, want the 8 unburied", len(got))
	}
	for _, e := range got {
		if e.ID <= 4 {
			t.Errorf("buried highlight %d picked", e.ID)
		}
	}

	// Burying a highlight after it was picked drops it from the day.
	if _, err := s.DB().Exec(`UPDATE highlights SET buried = 1 WHERE id = ?`, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if again := mustToday(t, s, day); len(again) != 7 {
		t.Errorf("got %d after burying one, want 7", len(again))
	}
}

func TestTodayEmpty(t *testing.T) {
	s := seedStore(t, 0)
	if got := mustToday(t, s, time.Now()); len(got) != 0 {
		t.Errorf("got %d from an empty library", len(got))
	}
}

func TestScore(t *testing.T) {
	now := time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		c    candidate
		want float64
	}{
		{"never seen", candidate{weight: 1}, 366},
		{"seen just now", candidate{weight: 1, lastSeen: now}, 1},
		{"seen 9 days ago", candidate{weight: 1, lastSeen: now.AddDate(0, 0, -9)}, 10},
		{"seen 3 years ago is capped", candidate{weight: 1, lastSeen: now.AddDate(-3, 0, 0)}, 366},
		{"favorite", candidate{weight: 1, favorite: true, lastSeen: now.AddDate(0, 0, -9)}, 30},
		{"weight column", candidate{weight: 0.5, lastSeen: now.AddDate(0, 0, -9)}, 5},
		{"seen in the future (clock skew)", candidate{weight: 1, lastSeen: now.Add(time.Hour)}, 1},
	}
	for _, tt := range tests {
		if got := tt.c.score(now); got != tt.want {
			t.Errorf("%s: score = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Over many days, picks follow the weights: a favorite unseen for 30 days
// (score 93) shows up far more often than a highlight seen yesterday
// (score 2), and never-seen ones (366) most of all.
func TestSampleFollowsWeights(t *testing.T) {
	now := time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)
	var cands []candidate
	for i := range 30 {
		cands = append(cands, candidate{id: int64(i), weight: 1, lastSeen: now.AddDate(0, 0, -1)})
	}
	cands = append(cands,
		candidate{id: 100, weight: 1, favorite: true, lastSeen: now.AddDate(0, 0, -30)},
		candidate{id: 200, weight: 1},
	)
	counts := make(map[int64]int)
	const days = 2000
	for d := range days {
		for _, id := range sample(cands, 1, now, uint64(d)) {
			counts[id]++
		}
	}
	yesterday := counts[0]
	for i := range 30 {
		yesterday = max(yesterday, counts[int64(i)])
	}
	if !(counts[200] > counts[100] && counts[100] > 10*yesterday) {
		t.Errorf("never seen %d, favorite %d, max seen-yesterday %d", counts[200], counts[100], yesterday)
	}
}

func TestSampleIsDeterministic(t *testing.T) {
	now := time.Now()
	var cands []candidate
	for i := range 50 {
		cands = append(cands, candidate{id: int64(i), weight: 1})
	}
	a := sample(cands, 10, now, seed("2026-10-24"))
	b := sample(cands, 10, now, seed("2026-10-24"))
	c := sample(cands, 10, now, seed("2026-10-25"))
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("same seed differs: %v vs %v", a, b)
	}
	if fmt.Sprint(a) == fmt.Sprint(c) {
		t.Errorf("different days gave the same shuffle: %v", a)
	}
	if got := sample(cands, 100, now, 1); len(got) != 50 {
		t.Errorf("size above candidate count returned %d", len(got))
	}
}
