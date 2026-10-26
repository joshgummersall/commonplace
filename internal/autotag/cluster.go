// Package autotag suggests tags. Seed groups the highlights of a library
// with no tags yet and names each group as a tag; Suggest then tags new
// highlights by what their nearest neighbors are tagged.
//
// Grouping is average-linkage agglomerative clustering on cosine distance,
// cut at one height. Unlike k-means it needs no cluster count and leaves
// highlights that fit no group out, and it gives the same answer every
// run. A local LLM names each group.
package autotag

import (
	"cmp"
	"math"
	"slices"

	"github.com/joshgummersall/commonplace/internal/embedding"
)

// The cut height comes from the data rather than a fixed cosine value,
// which shifts with the model and the library: each highlight's distance
// to its neighborK-th nearest neighbor, at cutQuantile. On the library it
// was tuned on (1,546 highlights, qwen3-embedding:0.6b) this is about
// 0.60, the level a blind review of sample groups chose.
const (
	neighborK   = 20
	cutQuantile = 0.88
)

// merge joins clusters a and b (each named by one of its points) at
// height, the average distance between their points.
type merge struct {
	a, b   int
	height float32
}

// tree clusters unit vectors and returns the merges and the cut height.
// It holds an n×n distance matrix, so it needs 4n² bytes of memory (see
// MaxHighlights).
func tree(vecs [][]float32) (merges []merge, height float32) {
	n := len(vecs)
	d := make([][]float32, n)
	for i := range d {
		d[i] = make([]float32, n)
	}
	for i := range n {
		for j := i + 1; j < n; j++ {
			x := 1 - embedding.Dot(vecs[i], vecs[j])
			d[i][j], d[j][i] = x, x
		}
	}
	height = cutHeight(d)
	return linkage(d), height
}

// cutHeight reads the cut height off the neighbor distances.
func cutHeight(d [][]float32) float32 {
	n := len(d)
	k := min(neighborK, n-1)
	kth := make([]float32, n)
	row := make([]float32, n)
	for i := range n {
		copy(row, d[i])
		slices.Sort(row)
		kth[i] = row[k] // row[0] is the point itself
	}
	slices.Sort(kth)
	return kth[int(cutQuantile*float64(n-1))]
}

// linkage runs average-linkage clustering with the nearest-neighbor chain
// algorithm: O(n²) time. It overwrites d.
func linkage(d [][]float32) []merge {
	n := len(d)
	size := make([]float32, n)
	active := make([]bool, n)
	for i := range n {
		size[i], active[i] = 1, true
	}
	merges := make([]merge, 0, n-1)
	var chain []int
	for len(merges) < n-1 {
		if len(chain) == 0 {
			chain = append(chain, slices.Index(active, true))
		}
		a := chain[len(chain)-1]
		prev, b := -1, -1
		best := float32(math.Inf(1))
		if len(chain) > 1 {
			prev = chain[len(chain)-2]
			b, best = prev, d[a][prev]
		}
		for j := range n {
			if active[j] && j != a && d[a][j] < best {
				b, best = j, d[a][j]
			}
		}
		if b != prev {
			chain = append(chain, b)
			continue
		}
		// a and b are each other's nearest: merge b into a.
		chain = chain[:len(chain)-2]
		merges = append(merges, merge{a, b, best})
		for j := range n {
			if active[j] && j != a && j != b {
				x := (size[a]*d[a][j] + size[b]*d[b][j]) / (size[a] + size[b])
				d[a][j], d[j][a] = x, x
			}
		}
		size[a] += size[b]
		active[b] = false
	}
	return merges
}

// cut returns the clusters formed by merges at or below height, as lists
// of point indexes.
func cut(n int, merges []merge, height float32) [][]int {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for _, m := range merges {
		if m.height <= height {
			parent[find(m.b)] = find(m.a)
		}
	}
	groups := map[int][]int{}
	for i := range n {
		r := find(i)
		groups[r] = append(groups[r], i)
	}
	out := make([][]int, 0, len(groups))
	for _, g := range groups {
		out = append(out, g)
	}
	// Map order is random; sort for stable output.
	slices.SortFunc(out, func(a, b []int) int { return cmp.Compare(a[0], b[0]) })
	return out
}

// central orders a group's points by mean similarity to the rest of the
// group, most typical first.
func central(vecs [][]float32, group []int) []int {
	score := make(map[int]float32, len(group))
	for _, i := range group {
		for _, j := range group {
			score[i] += embedding.Dot(vecs[i], vecs[j])
		}
	}
	out := slices.Clone(group)
	slices.SortStableFunc(out, func(a, b int) int { return cmp.Compare(score[b], score[a]) })
	return out
}
