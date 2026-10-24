package search

import (
	"fmt"
	"slices"
	"sync"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/store"
)

// Hit is a highlight and its similarity to a query or another highlight.
type Hit struct {
	ID     int64
	BookID int64
	Score  float32 // cosine similarity, -1 to 1
}

// Index holds one model's vectors in memory for brute-force nearest
// neighbor search. At tens of thousands of highlights a full scan takes a
// few milliseconds, so no approximate index is needed.
//
// It reloads lazily: each lookup checks a cheap fingerprint of the
// embeddings table and reloads when it changed (after an import or a
// background backfill).
type Index struct {
	store *store.Store
	model string

	mu    sync.Mutex
	stamp string
	vecs  []store.Vector
	byID  map[int64]int
}

// NewIndex returns an index over model's vectors in s.
func NewIndex(s *store.Store, model string) *Index {
	return &Index{store: s, model: model}
}

func (ix *Index) snapshot() ([]store.Vector, map[int64]int, error) {
	var stamp string
	err := ix.store.DB().QueryRow(`SELECT count(*) || ':' || coalesce(max(created_at), '') || ':' || coalesce(sum(highlight_id), 0)
		FROM embeddings WHERE model = ?`, ix.model).Scan(&stamp)
	if err != nil {
		return nil, nil, fmt.Errorf("check embeddings: %w", err)
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if stamp != ix.stamp {
		vecs, err := ix.store.Vectors(ix.model)
		if err != nil {
			return nil, nil, err
		}
		byID := make(map[int64]int, len(vecs))
		for i, v := range vecs {
			byID[v.HighlightID] = i
		}
		ix.vecs, ix.byID, ix.stamp = vecs, byID, stamp
	}
	return ix.vecs, ix.byID, nil
}

// Size reports how many highlights have vectors.
func (ix *Index) Size() (int, error) {
	vecs, _, err := ix.snapshot()
	return len(vecs), err
}

// Nearest returns up to k highlights most similar to q, best first,
// considering only those keep accepts (nil keeps all).
func (ix *Index) Nearest(q []float32, k int, keep func(store.Vector) bool) ([]Hit, error) {
	vecs, _, err := ix.snapshot()
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(vecs))
	for _, v := range vecs {
		if len(v.Vec) != len(q) || (keep != nil && !keep(v)) {
			continue
		}
		hits = append(hits, Hit{ID: v.HighlightID, BookID: v.BookID, Score: embedding.Dot(q, v.Vec)})
	}
	return topK(hits, k), nil
}

// Similar returns the highlights nearest to highlight id, split into those
// from other books and those from the same book, k of each at most. ok is
// false when id has no vector yet.
func (ix *Index) Similar(id int64, k int) (other, same []Hit, ok bool, err error) {
	vecs, byID, err := ix.snapshot()
	if err != nil {
		return nil, nil, false, err
	}
	i, ok := byID[id]
	if !ok {
		return nil, nil, false, nil
	}
	self := vecs[i]
	for _, v := range vecs {
		if v.HighlightID == id || len(v.Vec) != len(self.Vec) {
			continue
		}
		h := Hit{ID: v.HighlightID, BookID: v.BookID, Score: embedding.Dot(self.Vec, v.Vec)}
		if v.BookID == self.BookID {
			same = append(same, h)
		} else {
			other = append(other, h)
		}
	}
	return topK(other, k), topK(same, k), true, nil
}

// topK sorts hits best first, ties by id for stable paging, and keeps k.
func topK(hits []Hit, k int) []Hit {
	slices.SortFunc(hits, func(a, b Hit) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}
