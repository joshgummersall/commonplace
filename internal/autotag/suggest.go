package autotag

import (
	"slices"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/store"
)

// A highlight's voteK nearest neighbors vote, and it is suggested each tag
// at least voteMin of them carry. Checked against the seeded tags on the
// library the cut was tuned on, this agreed with the grouping 94% of the
// time and tagged half the highlights; a looser vote tags more but agrees
// less, and wrong tags are the problem tags were made to fix.
const (
	voteK   = 10
	voteMin = 6
)

// Suggest suggests tags for every highlight embedded by model that has no
// tag yet, set, suggested, or removed, from what its nearest neighbors
// carry. Tags you set and confirm count as much as suggestions, so your
// corrections steer later suggestions. It returns how many it added.
func Suggest(s *store.Store, model string) (int, error) {
	vecs, err := s.Vectors(model)
	if err != nil {
		return 0, err
	}
	tags, err := s.HighlightTags()
	if err != nil {
		return 0, err
	}
	out := map[int64][]int64{}
	for i, v := range vecs {
		if tags[v.HighlightID] != nil {
			continue
		}
		votes := map[int64]int{}
		for _, j := range nearest(vecs, i, voteK) {
			for tid, state := range tags[vecs[j].HighlightID] {
				if state != store.TagNo {
					votes[tid]++
				}
			}
		}
		for tid, n := range votes {
			if n >= voteMin {
				out[v.HighlightID] = append(out[v.HighlightID], tid)
			}
		}
	}
	if len(out) == 0 {
		return 0, nil
	}
	return s.SuggestTags(out)
}

// nearest returns the indexes of the k vectors most similar to vecs[i],
// leaving i out.
func nearest(vecs []store.Vector, i, k int) []int {
	type hit struct {
		j   int
		sim float32
	}
	top := make([]hit, 0, k+1)
	for j, v := range vecs {
		if j == i {
			continue
		}
		sim := embedding.Dot(vecs[i].Vec, v.Vec)
		if len(top) == k && sim <= top[k-1].sim {
			continue
		}
		at, _ := slices.BinarySearchFunc(top, sim, func(h hit, s float32) int {
			if h.sim > s {
				return -1
			}
			return 1
		})
		top = slices.Insert(top, at, hit{j, sim})
		if len(top) > k {
			top = top[:k]
		}
	}
	out := make([]int, len(top))
	for n, h := range top {
		out[n] = h.j
	}
	return out
}
