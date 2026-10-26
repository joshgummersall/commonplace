package search

import (
	"context"
	"errors"
	"slices"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/ollama"
	"github.com/joshgummersall/commonplace/internal/store"
)

// Mode picks how text queries are matched.
type Mode string

const (
	ModeKeyword  Mode = "keyword"  // FTS5 words, bm25-ranked
	ModeSemantic Mode = "semantic" // embedding similarity
	ModeHybrid   Mode = "hybrid"   // both, fused by reciprocal rank
)

// Modes lists every mode. The web app always uses ModeHybrid; cmpl eval
// runs each to compare them.
var Modes = []Mode{ModeHybrid, ModeKeyword, ModeSemantic}

const (
	// SemanticK caps semantic results. Similarity has no natural cutoff,
	// and past the first few dozen the list is mostly noise.
	SemanticK = 30
	// hybridKeywordK caps keyword results going into fusion. It is high
	// so paging can reach every word match.
	hybridKeywordK = 1000
	// rrfK is the reciprocal rank fusion constant from the original paper
	// (Cormack et al., 2009); it damps the weight of top ranks.
	rrfK = 60
)

// Page is one page of search results.
type Page struct {
	Results []Result
	Total   int  // all matches across pages
	More    bool // another page follows
	// WordMatches and MeaningOnly break Total down when meaning-based
	// matching ran: highlights containing the query's words, and those
	// found by meaning alone.
	WordMatches int
	MeaningOnly int
	Semantic    bool // meaning-based matching ran
	// Fallback is set when meaning-based matching couldn't run, and says
	// why; the results are then keyword results.
	Fallback string
}

// Searcher runs searches in every mode. Embedder and Index may be nil, in
// which case only keyword search works.
type Searcher struct {
	Store    *store.Store
	Embedder embedding.Embedder
	Index    *Index
}

// New returns a Searcher over s. With a nil e, only keyword search works.
func New(s *store.Store, e embedding.Embedder) *Searcher {
	sr := &Searcher{Store: s}
	if e != nil {
		sr.Embedder, sr.Index = e, NewIndex(s, e.Model())
	}
	return sr
}

// Run searches one page in q's mode. Listing (no text) ignores the mode.
func (sr *Searcher) Run(ctx context.Context, q Query, mode Mode) (Page, error) {
	q.Limit = q.limit()
	if q.Text == "" || mode == ModeKeyword {
		return sr.keyword(q)
	}
	qvec, err := sr.embedQuery(ctx, q.Text)
	if err != nil {
		if !errors.Is(err, ollama.ErrUnavailable) {
			return Page{}, err
		}
		p, kerr := sr.keyword(q)
		p.Fallback = err.Error()
		var unavailable *ollama.UnavailableError
		if errors.As(err, &unavailable) {
			p.Fallback = unavailable.Reason
		}
		return p, kerr
	}
	semantic, err := sr.nearest(q, qvec)
	if err != nil {
		return Page{}, err
	}
	similar := make(map[int64]bool, len(semantic))
	for _, id := range semantic {
		similar[id] = true
	}
	if mode == ModeSemantic {
		p, err := sr.page(q, semantic, nil, similar)
		p.Semantic, p.MeaningOnly = true, len(semantic)
		return p, err
	}

	kq := q
	kq.Limit, kq.Offset, kq.AnyWords = hybridKeywordK, 0, true
	kw, err := Keyword(sr.Store, kq)
	if err != nil {
		return Page{}, err
	}
	snippets := make(map[int64]Result, len(kw))
	kwIDs := make([]int64, len(kw))
	for i, r := range kw {
		kwIDs[i] = r.ID
		snippets[r.ID] = r
	}
	p, err := sr.page(q, Fuse(kwIDs, semantic), snippets, similar)
	p.Semantic, p.WordMatches = true, len(kw)
	for _, id := range semantic {
		if _, ok := snippets[id]; !ok {
			p.MeaningOnly++
		}
	}
	return p, err
}

func (sr *Searcher) embedQuery(ctx context.Context, text string) ([]float32, error) {
	if sr.Embedder == nil || sr.Index == nil {
		return nil, &ollama.UnavailableError{Reason: "no embedder configured"}
	}
	if n, err := sr.Index.Size(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, &ollama.UnavailableError{Reason: "no highlights are embedded yet"}
	}
	return sr.Embedder.EmbedQuery(ctx, text)
}

// nearest ranks every embedded highlight passing q's filters.
func (sr *Searcher) nearest(q Query, qvec []float32) ([]int64, error) {
	keep, err := sr.keep(q)
	if err != nil {
		return nil, err
	}
	hits, err := sr.Index.Nearest(qvec, SemanticK, keep)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids, nil
}

// Similar finds the k highlights nearest id in meaning, in other books and
// in its own, among those passing q's filters; q's text is ignored. ok is
// false when id has no embedding. It needs an Index.
func (sr *Searcher) Similar(id int64, k int, q Query) (other, same []Scored, ok bool, err error) {
	keep, err := sr.keep(q)
	if err != nil {
		return nil, nil, false, err
	}
	return sr.Index.SimilarItems(id, k, keep)
}

// keep turns q's filters into a check on vectors, or nil when q has none.
func (sr *Searcher) keep(q Query) (func(store.Vector) bool, error) {
	ids, err := filterIDs(sr.Store, q)
	if err != nil || ids == nil {
		return nil, err
	}
	return func(v store.Vector) bool { return ids[v.HighlightID] }, nil
}

func (sr *Searcher) keyword(q Query) (Page, error) {
	lim := q.Limit
	q.Limit = lim + 1 // one extra row says whether another page exists
	rs, err := Keyword(sr.Store, q)
	if err != nil {
		return Page{}, err
	}
	p := Page{Results: rs}
	if len(rs) > lim {
		p.Results, p.More = rs[:lim], true
	}
	if err := sr.items(p.Results); err != nil {
		return Page{}, err
	}
	p.Total, err = Count(sr.Store, q)
	return p, err
}

// page slices a ranked id list and loads its items. Keyword results
// (snippets and matched fields) are kept where the keyword search produced
// them; similar marks the ids the semantic search found.
func (sr *Searcher) page(q Query, ranked []int64, keyword map[int64]Result, similar map[int64]bool) (Page, error) {
	p := Page{Total: len(ranked)}
	start := min(max(q.Offset, 0), len(ranked))
	end := min(start+q.Limit, len(ranked))
	p.More = end < len(ranked)
	entries, err := sr.Store.Entries(ranked[start:end])
	if err != nil {
		return Page{}, err
	}
	for _, e := range entries {
		r, ok := keyword[e.ID]
		if !ok {
			r = Result{Snippet: []Segment{{Text: e.Text}}}
		}
		r.Entry = e
		r.Similar = similar[e.ID]
		p.Results = append(p.Results, r)
	}
	return p, sr.items(p.Results)
}

// items fills in the note counts and tags of a page of results. Only the
// page is filled, not the longer keyword list fused into it.
func (sr *Searcher) items(rs []Result) error {
	entries := make([]store.Entry, len(rs))
	for i, r := range rs {
		entries[i] = r.Entry
	}
	items, err := sr.Store.Items(entries, false)
	if err != nil {
		return err
	}
	for i := range rs {
		rs[i].Item = items[i]
	}
	return nil
}

// Fuse merges ranked id lists with reciprocal rank fusion: each list adds
// 1/(rrfK + rank) to an id's score. Ids found by both lists rise; ties
// keep the order of first appearance.
func Fuse(lists ...[]int64) []int64 {
	score := make(map[int64]float64)
	var order []int64
	for _, list := range lists {
		for rank, id := range list {
			if _, seen := score[id]; !seen {
				order = append(order, id)
			}
			score[id] += 1 / float64(rrfK+rank+1)
		}
	}
	slices.SortStableFunc(order, func(a, b int64) int {
		switch {
		case score[a] > score[b]:
			return -1
		case score[a] < score[b]:
			return 1
		}
		return 0
	})
	return order
}

// limit is Limit, or PageSize when unset.
func (q Query) limit() int {
	if q.Limit <= 0 {
		return PageSize
	}
	return q.Limit
}

// Reason says where a match in f was found, for "why" lists.
func (f Field) Reason() string {
	switch f {
	case FieldText:
		return "words in the passage"
	case FieldNotes:
		return "words in a note"
	case FieldTitle:
		return "words in the title"
	case FieldAuthor:
		return "words in the author"
	}
	return "words in the " + string(f)
}

// Why explains in plain terms why a search listed r.
func (r Result) Why() []string {
	var out []string
	for _, f := range r.Matched {
		out = append(out, f.Reason())
	}
	if r.Similar {
		out = append(out, "similar in meaning")
	}
	return out
}
