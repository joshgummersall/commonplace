// Package daily picks the day's highlights to resurface.
package daily

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/joshgummersall/commonplace/internal/embedding"
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

// score is a candidate's weight in the draw.
func score(c store.DailyCandidate, now time.Time) float64 {
	days := float64(maxAgeDays)
	if !c.LastSeen.IsZero() {
		days = math.Min(now.Sub(c.LastSeen).Hours()/24, maxAgeDays)
	}
	w := (1 + math.Max(days, 0)) * c.Weight
	if c.Favorite {
		w *= favoriteBoost
	}
	return w
}

// maxSimilar is the cosine similarity above which two highlights make
// the same point, so a day's set keeps only one of them. On the library
// it was tuned on, pairs from different books above 0.60 read as near
// restatements, and about one random day in fourteen had such a pair.
const maxSimilar = 0.60

// Today returns the day's shuffle for the local date of now, choosing and
// storing it on the first call of the day. Later calls return the same
// highlights, minus any deleted or buried since. model names the
// embeddings used to keep the set varied; "" (or no embeddings yet)
// skips that.
func Today(s *store.Store, now time.Time, size int, model string) ([]store.Entry, error) {
	day := now.Format(time.DateOnly)
	ids, err := s.DailyPicks(day)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		if ids, err = choose(s, day, now, size, model); err != nil {
			return nil, err
		}
	}
	entries, err := s.Entries(ids)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(entries, func(e store.Entry) bool { return e.Buried }), nil
}

// choose samples the day's highlights and stores them. If a concurrent
// first request stored its set first, that set is returned instead.
func choose(s *store.Store, day string, now time.Time, size int, model string) ([]int64, error) {
	cands, err := s.DailyCandidates()
	if err != nil {
		return nil, err
	}
	vecs := map[int64][]float32{}
	if model != "" {
		vs, err := s.Vectors(model)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			vecs[v.HighlightID] = v.Vec
		}
	}
	chosen := vary(sample(cands, len(cands), now, seed(day)), size, vecs)
	return s.SaveDailyPicks(day, chosen)
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
func sample(cands []store.DailyCandidate, size int, now time.Time, seed uint64) []int64 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	type keyed struct {
		id  int64
		key float64
	}
	keys := make([]keyed, 0, len(cands))
	for _, c := range cands {
		w := score(c, now)
		if w <= 0 {
			continue
		}
		u := rng.Float64()
		for u == 0 {
			u = rng.Float64()
		}
		keys = append(keys, keyed{c.ID, math.Log(u) / w})
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

// vary keeps up to size ids from order, skipping any more similar than
// maxSimilar to one already kept. Highlights without a vector are never
// skipped. If too few remain, skipped ids fill the set in order, so a
// small or uniform library still gets a full day.
func vary(order []int64, size int, vecs map[int64][]float32) []int64 {
	var kept, skipped []int64
	for _, id := range order {
		if len(kept) == size {
			break
		}
		if similarToAny(vecs, id, kept) {
			skipped = append(skipped, id)
			continue
		}
		kept = append(kept, id)
	}
	for _, id := range skipped {
		if len(kept) == size {
			break
		}
		kept = append(kept, id)
	}
	return kept
}

func similarToAny(vecs map[int64][]float32, id int64, kept []int64) bool {
	v, ok := vecs[id]
	if !ok {
		return false
	}
	for _, k := range kept {
		if w, ok := vecs[k]; ok && len(w) == len(v) && embedding.Dot(v, w) > maxSimilar {
			return true
		}
	}
	return false
}
