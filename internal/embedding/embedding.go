// Package embedding turns text into vectors for semantic search, and keeps
// the stored vectors in step with the highlights.
package embedding

import (
	"context"
	"math"
)

// Embedder turns text into vectors. Implementations own any asymmetry
// between queries and documents (instruction prefixes and the like), so
// callers only say which kind of text they have.
type Embedder interface {
	// Model names the model and its settings. It is stored with each
	// vector; a change marks every stored vector stale.
	Model() string
	// EmbedDocuments embeds stored highlights, one vector per text, in
	// order. Vectors are unit length.
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	// EmbedQuery embeds a search query. The vector is unit length.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Normalize scales v to unit length in place, so cosine similarity becomes
// a dot product. A zero vector is left as is.
func Normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

// Dot is the dot product of two equal-length vectors; for unit vectors it
// is their cosine similarity.
func Dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
