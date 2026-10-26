package autotag

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/joshgummersall/commonplace/internal/store"
)

const (
	// MinHighlights is the fewest embedded highlights worth grouping.
	MinHighlights = 30
	// MaxHighlights keeps the distance matrix (4n² bytes) under 256 MB.
	MaxHighlights = 8000

	// A group becomes a tag when it has minGroup highlights from minBooks
	// books, none holding more than maxBookShare of it. Smaller groups
	// are noise; single-book groups are just that book's chapter.
	minGroup     = 5
	minBooks     = 3
	maxBookShare = 0.6

	// namePassages is how many of a group's most typical highlights the
	// model reads to name it.
	namePassages = 12
)

// Stages of seeding, as the app reports them.
const (
	StageReading  = "Reading highlights"
	StageGrouping = "Grouping highlights"
	StageNaming   = "Naming tags"
	StageSaving   = "Saving"
)

// Progress reports how far seeding has got. Done and Total count names
// during StageNaming and are zero otherwise.
type Progress struct {
	Stage       string
	Done, Total int
}

// Result summarizes seeding.
type Result struct {
	Tags int
	// Highlights counts the embedded highlights grouped; Unembedded
	// those left out because they have no embedding yet.
	Highlights, Unembedded int
}

// Seed groups every highlight embedded by model, names each group with
// namer, and saves the groups as suggested tags. It works only on a
// library with no tags. progress, if set, is called as it goes. Nothing
// is saved unless the whole run succeeds.
func Seed(ctx context.Context, s *store.Store, model string, namer Namer, progress func(Progress)) (Result, error) {
	report := func(p Progress) {
		if progress != nil {
			progress(p)
		}
	}
	report(Progress{Stage: StageReading})
	inputs, res, err := load(s, model)
	if err != nil {
		return res, err
	}

	report(Progress{Stage: StageGrouping})
	groups := build(inputs)
	if err := ctx.Err(); err != nil {
		return res, err
	}

	texts := make(map[int64]string, len(inputs))
	for _, in := range inputs {
		texts[in.ID] = in.Text
	}
	var seeds []store.SeedTag
	for i, g := range groups {
		report(Progress{Stage: StageNaming, Done: i, Total: len(groups)})
		var passages []string
		for _, id := range g[:min(namePassages, len(g))] {
			passages = append(passages, texts[id])
		}
		name, err := namer.Name(ctx, passages)
		if err != nil {
			return res, err
		}
		if name != "" {
			seeds = append(seeds, store.SeedTag{Name: name, Members: g})
		}
	}
	report(Progress{Stage: StageSaving})
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if err := s.SeedTags(seeds); err != nil {
		return res, err
	}
	res.Tags, err = s.CountTags()
	return res, err
}

// load reads the embedded highlights and checks there are enough, and not
// too many, to group.
func load(s *store.Store, model string) ([]store.EmbeddedText, Result, error) {
	inputs, err := s.EmbeddedTexts(model)
	if err != nil {
		return nil, Result{}, err
	}
	total, err := s.CountHighlights()
	if err != nil {
		return nil, Result{}, err
	}
	res := Result{Highlights: len(inputs), Unembedded: total - len(inputs)}
	switch {
	case len(inputs) < MinHighlights:
		return nil, res, fmt.Errorf("suggesting tags needs at least %d embedded highlights, and %d are embedded so far", MinHighlights, len(inputs))
	case len(inputs) > MaxHighlights:
		return nil, res, fmt.Errorf("suggesting tags handles up to %d highlights for now, and you have %d", MaxHighlights, len(inputs))
	}
	return inputs, res, nil
}

// build clusters inputs and returns the groups that pass the size and
// spread rules, largest first, each as highlight ids, most central first.
func build(inputs []store.EmbeddedText) [][]int64 {
	vecs := make([][]float32, len(inputs))
	for i, in := range inputs {
		vecs[i] = in.Vec
	}
	merges, height := tree(vecs)
	return buildAt(inputs, vecs, merges, height)
}

// buildAt is build with the tree and cut height given.
func buildAt(inputs []store.EmbeddedText, vecs [][]float32, merges []merge, height float32) [][]int64 {
	var out [][]int64
	for _, g := range cut(len(inputs), merges, height) {
		if !spread(inputs, g) {
			continue
		}
		var ids []int64
		for _, i := range central(vecs, g) {
			ids = append(ids, inputs[i].ID)
		}
		out = append(out, ids)
	}
	slices.SortStableFunc(out, func(a, b []int64) int { return cmp.Compare(len(b), len(a)) })
	return out
}

// spread reports whether a group is big enough and spans enough books.
func spread(inputs []store.EmbeddedText, points []int) bool {
	if len(points) < minGroup {
		return false
	}
	books := map[int64]int{}
	most := 0
	for _, i := range points {
		books[inputs[i].BookID]++
		most = max(most, books[inputs[i].BookID])
	}
	return len(books) >= minBooks && float64(most) <= maxBookShare*float64(len(points))
}

// Status is what the app shows about seeding.
type Status struct {
	Running  bool
	Progress Progress
	// Finished, Result, and Err describe the last run since the server
	// started; Finished is zero if none has finished.
	Finished time.Time
	Result   Result
	Err      string
}

// Seeder runs Seed in the background, one run at a time, and reports its
// status.
type Seeder struct {
	store  *store.Store
	model  string
	namer  Namer
	now    func() time.Time
	logger *slog.Logger

	mu     sync.Mutex
	status Status
	done   chan struct{}
}

// NewSeeder returns a Seeder grouping highlights embedded by model.
func NewSeeder(s *store.Store, model string, namer Namer, now func() time.Time, logger *slog.Logger) *Seeder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Seeder{store: s, model: model, namer: namer, now: now, logger: logger}
}

// NameModel names the model that names suggested tags.
func (r *Seeder) NameModel() string { return r.namer.Model() }

// Start begins seeding unless a run is going. The run goes to completion
// even if the caller goes away; stopping the server abandons it with
// nothing saved.
func (r *Seeder) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.Running {
		return
	}
	r.status.Running = true
	r.status.Progress = Progress{Stage: StageReading}
	r.done = make(chan struct{})
	go r.run(r.done)
}

func (r *Seeder) run(done chan struct{}) {
	defer close(done)
	res, err := Seed(context.Background(), r.store, r.model, r.namer, func(p Progress) {
		r.mu.Lock()
		r.status.Progress = p
		r.mu.Unlock()
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Running = false
	r.status.Finished = r.now()
	r.status.Result = res
	r.status.Err = ""
	if err != nil {
		r.status.Err = err.Error()
		if !errors.Is(err, ErrUnavailable) {
			r.logger.Error("suggesting tags failed", "err", err)
		}
		return
	}
	r.logger.Info("tags suggested", "tags", res.Tags)
}

// Status reports the current or last run.
func (r *Seeder) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Wait blocks until the current run, if any, finishes.
func (r *Seeder) Wait() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}
