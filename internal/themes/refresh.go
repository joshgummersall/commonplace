package themes

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

	// A group is shown when it has minGroup highlights from minBooks
	// books, none holding more than maxBookShare of it. Smaller groups
	// are noise; single-book groups are just that book's chapter.
	minGroup     = 5
	minBooks     = 3
	maxBookShare = 0.6
	// A theme holding maxAreaShare of its area or more says what the area
	// says, and gets a near-copy of its name, so it isn't shown. Themes
	// that duplicated their area held 60-100% of it on the library this
	// was tuned on; real sub-themes held 45% or less.
	maxAreaShare = 0.6

	// namePassages is how many of a group's most typical highlights the
	// model reads to name it.
	namePassages = 12
	// keepOverlap is the Jaccard overlap at which a new group inherits an
	// old one's id and name.
	keepOverlap = 0.5
)

// Stages of a refresh, as the app reports them.
const (
	StageReading  = "Reading highlights"
	StageGrouping = "Grouping highlights"
	StageNaming   = "Naming themes"
	StageSaving   = "Saving"
)

// Progress reports how far a refresh has got. Done and Total count names
// during StageNaming and are zero otherwise.
type Progress struct {
	Stage       string
	Done, Total int
}

// Result summarizes a finished refresh.
type Result struct {
	Areas, Themes int
	// Kept counts groups that kept a name (the model's or yours) from the
	// last refresh, so needed no new one.
	Kept int
	// Declined counts groups the model found no shared idea in.
	Declined int
	// Highlights counts the embedded highlights grouped; Unembedded
	// those left out because they have no embedding yet.
	Highlights, Unembedded int
	// Lost lists names you set whose groups no longer match any new
	// group, so are gone.
	Lost []string
}

// group is a candidate area or theme during a refresh.
type group struct {
	theme  store.Theme // Members set; ID and Name once matched or named
	themes []*group    // an area's themes
	broad  bool
	spread bool // passes the size and spread rules itself
}

// Refresh regroups every highlight embedded by model, names new groups
// with namer, and replaces the stored themes. Groups that match an old
// theme keep its id and name. progress, if set, is called as it goes.
// Nothing is saved unless the whole refresh succeeds.
func Refresh(ctx context.Context, s *store.Store, model string, namer Namer, now time.Time, progress func(Progress)) (Result, error) {
	report := func(p Progress) {
		if progress != nil {
			progress(p)
		}
	}
	report(Progress{Stage: StageReading})
	inputs, err := s.ThemeInputs(model)
	if err != nil {
		return Result{}, err
	}
	var total int
	if err := s.DB().QueryRow(`SELECT count(*) FROM highlights`).Scan(&total); err != nil {
		return Result{}, err
	}
	res := Result{Highlights: len(inputs), Unembedded: total - len(inputs)}
	switch {
	case len(inputs) < MinHighlights:
		return res, fmt.Errorf("themes need at least %d embedded highlights, and %d are embedded so far", MinHighlights, len(inputs))
	case len(inputs) > MaxHighlights:
		return res, fmt.Errorf("themes handle up to %d highlights for now, and you have %d", MaxHighlights, len(inputs))
	}

	report(Progress{Stage: StageGrouping})
	areas := build(inputs)
	if err := ctx.Err(); err != nil {
		return res, err
	}
	old, err := s.AllThemes()
	if err != nil {
		return res, err
	}
	var areaList, themeList []*group
	for _, a := range areas {
		areaList = append(areaList, a)
		themeList = append(themeList, a.themes...)
	}
	res.Kept = match(areaList, old, true) + match(themeList, old, false)

	// Name themes before areas: an area the model can't name borrows the
	// name of its largest theme.
	var unnamed []*group
	for _, g := range append(themeList, areaList...) {
		if g.theme.Name == "" {
			unnamed = append(unnamed, g)
		}
	}
	texts := make(map[int64]string, len(inputs))
	for _, in := range inputs {
		texts[in.ID] = in.Text
	}
	for i, g := range unnamed {
		report(Progress{Stage: StageNaming, Done: i, Total: len(unnamed)})
		if g.broad {
			g.themes = slices.DeleteFunc(g.themes, func(t *group) bool { return t.theme.Name == "" })
			if !g.spread && len(g.themes) == 0 {
				continue // kept only for themes the model declined
			}
		}
		var passages []string
		for _, id := range g.theme.Members[:min(namePassages, len(g.theme.Members))] {
			passages = append(passages, texts[id])
		}
		name, err := namer.Name(ctx, passages, g.broad)
		if err != nil {
			return res, err
		}
		if name == "" && g.broad && len(g.themes) > 0 {
			name = g.themes[0].theme.Name
		}
		if name == "" {
			res.Declined++
		}
		g.theme.Name = name
	}

	var out []store.Area
	for _, a := range areas {
		a.themes = slices.DeleteFunc(a.themes, func(t *group) bool { return t.theme.Name == "" })
		if a.theme.Name == "" || (!a.spread && len(a.themes) == 0) {
			continue
		}
		area := store.Area{Theme: a.theme}
		for _, t := range a.themes {
			area.Themes = append(area.Themes, t.theme)
		}
		res.Areas++
		res.Themes += len(area.Themes)
		out = append(out, area)
	}
	saved := map[int64]bool{}
	for _, a := range out {
		saved[a.ID] = true
		for _, t := range a.Themes {
			saved[t.ID] = true
		}
	}
	for _, o := range old {
		if o.Renamed && !saved[o.ID] {
			res.Lost = append(res.Lost, o.Name)
		}
	}
	report(Progress{Stage: StageSaving})
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, s.ReplaceThemes(out, store.ThemeRefresh{At: now, Highlights: len(inputs)})
}

// build clusters inputs into areas holding themes, largest first, keeping
// groups that pass the size and spread rules. An area is kept when it
// passes them itself or holds a theme that does.
func build(inputs []store.ThemeInput) []*group {
	vecs := make([][]float32, len(inputs))
	for i, in := range inputs {
		vecs[i] = in.Vec
	}
	merges, themeCut, areaCut := tree(vecs)
	return buildAt(inputs, vecs, merges, themeCut, areaCut)
}

// buildAt is build with the tree and cut heights given.
func buildAt(inputs []store.ThemeInput, vecs [][]float32, merges []merge, themeCut, areaCut float32) []*group {
	n := len(inputs)
	areaOf := make([]int, n)
	areaGroups := cut(n, merges, areaCut)
	for ai, g := range areaGroups {
		for _, i := range g {
			areaOf[i] = ai
		}
	}
	themesIn := make([][]*group, len(areaGroups))
	for _, g := range cut(n, merges, themeCut) {
		if spread(inputs, g) {
			ai := areaOf[g[0]]
			t := newGroup(inputs, vecs, g, false)
			t.spread = true
			themesIn[ai] = append(themesIn[ai], t)
		}
	}
	var out []*group
	for ai, g := range areaGroups {
		ok := spread(inputs, g)
		if !ok && len(themesIn[ai]) == 0 {
			continue
		}
		a := newGroup(inputs, vecs, g, true)
		a.spread = ok
		a.themes = slices.DeleteFunc(themesIn[ai], func(t *group) bool {
			return float64(len(t.theme.Members)) >= maxAreaShare*float64(len(g))
		})
		if !ok && len(a.themes) == 0 {
			continue
		}
		slices.SortStableFunc(a.themes, bySize)
		out = append(out, a)
	}
	slices.SortStableFunc(out, bySize)
	return out
}

func bySize(a, b *group) int { return cmp.Compare(len(b.theme.Members), len(a.theme.Members)) }

func newGroup(inputs []store.ThemeInput, vecs [][]float32, points []int, broad bool) *group {
	g := &group{broad: broad}
	for _, i := range central(vecs, points) {
		g.theme.Members = append(g.theme.Members, inputs[i].ID)
	}
	return g
}

// spread reports whether a group is big enough and spans enough books.
func spread(inputs []store.ThemeInput, points []int) bool {
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

// match gives each group the id and name of the old theme at the same
// level it overlaps most, best overlaps first, when they share at least
// keepOverlap of their highlights. It returns how many matched.
func match(groups []*group, old []store.Theme, areas bool) int {
	type pair struct {
		g       *group
		o       *store.Theme
		jaccard float64
	}
	var pairs []pair
	for i := range old {
		o := &old[i]
		if (o.ParentID == 0) != areas {
			continue
		}
		in := make(map[int64]bool, len(o.Members))
		for _, id := range o.Members {
			in[id] = true
		}
		for _, g := range groups {
			shared := 0
			for _, id := range g.theme.Members {
				if in[id] {
					shared++
				}
			}
			j := float64(shared) / float64(len(o.Members)+len(g.theme.Members)-shared)
			if j >= keepOverlap {
				pairs = append(pairs, pair{g, o, j})
			}
		}
	}
	slices.SortStableFunc(pairs, func(a, b pair) int { return cmp.Compare(b.jaccard, a.jaccard) })
	taken := map[int64]bool{}
	n := 0
	for _, p := range pairs {
		if p.g.theme.ID != 0 || taken[p.o.ID] {
			continue
		}
		taken[p.o.ID] = true
		p.g.theme.ID, p.g.theme.Name, p.g.theme.Renamed = p.o.ID, p.o.Name, p.o.Renamed
		n++
	}
	return n
}

// Status is what the app shows about refreshing.
type Status struct {
	Running  bool
	Progress Progress
	// Finished, Result, and Err describe the last run since the server
	// started; Finished is zero if none has finished.
	Finished time.Time
	Result   Result
	Err      string
}

// Refresher runs one refresh at a time in the background and reports its
// status.
type Refresher struct {
	store  *store.Store
	model  string
	namer  Namer
	now    func() time.Time
	logger *slog.Logger

	mu     sync.Mutex
	status Status
	done   chan struct{}
}

// NewRefresher returns a Refresher grouping highlights embedded by model.
func NewRefresher(s *store.Store, model string, namer Namer, now func() time.Time, logger *slog.Logger) *Refresher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Refresher{store: s, model: model, namer: namer, now: now, logger: logger}
}

// NameModel names the model that names themes.
func (r *Refresher) NameModel() string { return r.namer.Model() }

// Start begins a refresh unless one is running, and reports whether it
// began. The refresh runs to completion even if the caller goes away;
// stopping the server abandons it with nothing saved.
func (r *Refresher) Start() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.Running {
		return false
	}
	r.status.Running = true
	r.status.Progress = Progress{Stage: StageReading}
	r.done = make(chan struct{})
	go r.run(r.done)
	return true
}

func (r *Refresher) run(done chan struct{}) {
	defer close(done)
	res, err := Refresh(context.Background(), r.store, r.model, r.namer, r.now(), func(p Progress) {
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
			r.logger.Error("theme refresh failed", "err", err)
		}
		return
	}
	r.logger.Info("themes refreshed", "areas", res.Areas, "themes", res.Themes, "kept", res.Kept)
}

// Status reports the current or last refresh.
func (r *Refresher) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Wait blocks until the running refresh, if any, finishes.
func (r *Refresher) Wait() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}
