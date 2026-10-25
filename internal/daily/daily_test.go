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
	es, err := Today(s, now, DefaultSize, "")
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
		c    store.DailyCandidate
		want float64
	}{
		{"never seen", store.DailyCandidate{Weight: 1}, 366},
		{"seen just now", store.DailyCandidate{Weight: 1, LastSeen: now}, 1},
		{"seen 9 days ago", store.DailyCandidate{Weight: 1, LastSeen: now.AddDate(0, 0, -9)}, 10},
		{"seen 3 years ago is capped", store.DailyCandidate{Weight: 1, LastSeen: now.AddDate(-3, 0, 0)}, 366},
		{"favorite", store.DailyCandidate{Weight: 1, Favorite: true, LastSeen: now.AddDate(0, 0, -9)}, 30},
		{"weight column", store.DailyCandidate{Weight: 0.5, LastSeen: now.AddDate(0, 0, -9)}, 5},
		{"seen in the future (clock skew)", store.DailyCandidate{Weight: 1, LastSeen: now.Add(time.Hour)}, 1},
	}
	for _, tt := range tests {
		if got := score(tt.c, now); got != tt.want {
			t.Errorf("%s: score = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Over many days, picks follow the weights: a favorite unseen for 30 days
// (score 93) shows up far more often than a highlight seen yesterday
// (score 2), and never-seen ones (366) most of all.
func TestSampleFollowsWeights(t *testing.T) {
	now := time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)
	var cands []store.DailyCandidate
	for i := range 30 {
		cands = append(cands, store.DailyCandidate{ID: int64(i), Weight: 1, LastSeen: now.AddDate(0, 0, -1)})
	}
	cands = append(cands,
		store.DailyCandidate{ID: 100, Weight: 1, Favorite: true, LastSeen: now.AddDate(0, 0, -30)},
		store.DailyCandidate{ID: 200, Weight: 1},
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
	var cands []store.DailyCandidate
	for i := range 50 {
		cands = append(cands, store.DailyCandidate{ID: int64(i), Weight: 1})
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

func TestVarySkipsNearRestatements(t *testing.T) {
	vecs := map[int64][]float32{
		1: {1, 0, 0},
		2: {0.99, 0.141, 0}, // nearly 1
		3: {0, 1, 0},
		4: {0, 0, 1},
	}
	if got := vary([]int64{1, 2, 3, 4}, 3, vecs); fmt.Sprint(got) != "[1 3 4]" {
		t.Errorf("vary = %v, want [1 3 4]", got)
	}
	// Too few distinct highlights: the skipped one fills the set.
	if got := vary([]int64{1, 2, 3}, 3, vecs); fmt.Sprint(got) != "[1 3 2]" {
		t.Errorf("vary = %v, want [1 3 2]", got)
	}
	// Highlights without vectors are never skipped.
	if got := vary([]int64{5, 6, 1}, 3, vecs); fmt.Sprint(got) != "[5 6 1]" {
		t.Errorf("vary = %v, want [5 6 1]", got)
	}
}

func TestTodayKeepsOneOfEachPoint(t *testing.T) {
	// 30 highlights: 15 making one point, 15 each on its own axis.
	s := seedStore(t, 30)
	const model = "test/model"
	var ids []int64
	vecs := make([][]float32, 30)
	rows, err := s.DB().Query(`SELECT id FROM highlights ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for i := range vecs {
		vecs[i] = make([]float32, 16)
		if i < 15 {
			vecs[i][0] = 1
		} else {
			vecs[i][i-14] = 1
		}
	}
	if err := s.SaveEmbeddings(model, ids, vecs, time.Now()); err != nil {
		t.Fatal(err)
	}
	for d := range 5 {
		day := time.Date(2026, 10, 24+d, 8, 0, 0, 0, time.Local)
		es, err := Today(s, day, DefaultSize, model)
		if err != nil {
			t.Fatal(err)
		}
		same := 0
		for _, e := range es {
			if e.ID <= ids[14] {
				same++
			}
		}
		if len(es) != DefaultSize || same > 1 {
			t.Errorf("day %d: %d highlights, %d making the same point", d, len(es), same)
		}
	}
}
