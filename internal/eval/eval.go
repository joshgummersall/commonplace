// Package eval scores search against queries with known answers, so
// models and settings can be compared by numbers rather than by feel.
package eval

import (
	"context"
	"fmt"
	"os"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/joshgummersall/commonplace/internal/search"
)

// DefaultK is how deep results are scored unless the file says otherwise.
const DefaultK = 10

// Case is one query and the highlights it should find.
type Case struct {
	Query  string  `yaml:"query"`
	Expect []int64 `yaml:"expect"`
}

// File is an eval file. YAML is a superset of JSON, so either works.
type File struct {
	K       int    `yaml:"k"`
	Queries []Case `yaml:"queries"`
}

// Example shows the file format, for error messages and docs.
const Example = `k: 10                    # optional, default 10
queries:
  - query: luck versus skill
    expect: [412, 977]   # highlight ids; a quote page's URL (/h/412) shows its id
`

// Load reads and validates an eval file.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	if f.K <= 0 {
		f.K = DefaultK
	}
	if len(f.Queries) == 0 {
		return File{}, fmt.Errorf("%s: no queries", path)
	}
	for i, c := range f.Queries {
		if c.Query == "" || len(c.Expect) == 0 {
			return File{}, fmt.Errorf("%s: query %d needs both query and expect", path, i+1)
		}
	}
	return f, nil
}

// CaseResult is how one query fared in one mode.
type CaseResult struct {
	Found     int // expected highlights in the top k
	FirstRank int // 1-based rank of the first expected highlight; 0 if none in the top k
}

// ModeReport scores one mode across all queries.
type ModeReport struct {
	Mode   search.Mode
	Recall float64 // mean recall@k
	MRR    float64 // mean reciprocal rank of the first expected hit, within k
	Cases  []CaseResult
	// Fallback is set when the mode couldn't run as itself (no
	// embedder); its scores are then keyword scores.
	Fallback string
}

// Report is a full eval run.
type Report struct {
	K     int
	Cases []Case
	Modes []ModeReport
	// Missing lists expected ids that don't exist in the database, which
	// usually means the file was written against another database.
	Missing []int64
}

// Run scores every mode on every query.
func Run(ctx context.Context, sr *search.Searcher, f File) (Report, error) {
	r := Report{K: f.K, Cases: f.Queries}
	missing, err := missingIDs(sr, f)
	if err != nil {
		return Report{}, err
	}
	r.Missing = missing

	for _, mode := range search.Modes {
		mr := ModeReport{Mode: mode}
		for _, c := range f.Queries {
			p, err := sr.Run(ctx, search.Query{Text: c.Query, Limit: f.K}, mode)
			if err != nil {
				return Report{}, fmt.Errorf("%s %q: %w", mode, c.Query, err)
			}
			if p.Fallback != "" {
				mr.Fallback = p.Fallback
			}
			cr := score(p.Results, c.Expect)
			mr.Cases = append(mr.Cases, cr)
			mr.Recall += float64(cr.Found) / float64(len(c.Expect))
			if cr.FirstRank > 0 {
				mr.MRR += 1 / float64(cr.FirstRank)
			}
		}
		n := float64(len(f.Queries))
		mr.Recall /= n
		mr.MRR /= n
		r.Modes = append(r.Modes, mr)
	}
	return r, nil
}

func score(results []search.Result, expect []int64) CaseResult {
	var cr CaseResult
	for i, res := range results {
		if slices.Contains(expect, res.ID) {
			cr.Found++
			if cr.FirstRank == 0 {
				cr.FirstRank = i + 1
			}
		}
	}
	return cr
}

func missingIDs(sr *search.Searcher, f File) ([]int64, error) {
	var ids []int64
	for _, c := range f.Queries {
		ids = append(ids, c.Expect...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	entries, err := sr.Store.Entries(ids)
	if err != nil {
		return nil, err
	}
	have := make(map[int64]bool, len(entries))
	for _, e := range entries {
		have[e.ID] = true
	}
	var missing []int64
	for _, id := range ids {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}
