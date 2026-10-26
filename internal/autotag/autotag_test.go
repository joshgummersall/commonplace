package autotag

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/ollama"
	"github.com/joshgummersall/commonplace/internal/store"
)

var now = time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

const model = "test/model"

// vec returns a unit vector pointing mostly along the given axes, with a
// little seeded noise.
func vec(rng *rand.Rand, dims int, axes map[int]float32) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = float32(rng.NormFloat64()) * 0.03
	}
	for i, w := range axes {
		v[i] += w
	}
	embedding.Normalize(v)
	return v
}

// point is a synthetic highlight: its book, text, and vector.
type point struct {
	book string
	text string
	vec  []float32
}

// library builds two groups of 24 highlights spread over four books (each
// with two tighter clusters of 12 inside), plus 8 highlights from a single
// book that sit together but are only that book's chapter.
func library() []point {
	rng := rand.New(rand.NewPCG(1, 2))
	const dims = 16
	var pts []point
	for area := range 2 {
		for theme := range 2 {
			axes := map[int]float32{area: 1, 2 + 2*area + theme: 0.8}
			for i := range 12 {
				pts = append(pts, point{
					book: fmt.Sprintf("Book %d", (area*2+theme+i)%4),
					text: fmt.Sprintf("area %d theme %d passage %d", area, theme, i),
					vec:  vec(rng, dims, axes),
				})
			}
		}
	}
	for i := range 8 {
		pts = append(pts, point{book: "Solo", text: fmt.Sprintf("solo passage %d", i), vec: vec(rng, dims, map[int]float32{10: 1})})
	}
	return pts
}

func inputs(pts []point) []store.EmbeddedText {
	books := map[string]int64{}
	out := make([]store.EmbeddedText, len(pts))
	for i, p := range pts {
		if _, ok := books[p.book]; !ok {
			books[p.book] = int64(len(books) + 1)
		}
		out[i] = store.EmbeddedText{ID: int64(i + 1), BookID: books[p.book], Text: p.text, Vec: p.vec}
	}
	return out
}

func TestBuildDropsSingleBookGroups(t *testing.T) {
	in := inputs(library())
	vecs := make([][]float32, len(in))
	for i := range in {
		vecs[i] = in[i].Vec
	}
	d := make([][]float32, len(vecs))
	for i := range d {
		d[i] = make([]float32, len(vecs))
		for j := range d[i] {
			d[i][j] = 1 - embedding.Dot(vecs[i], vecs[j])
		}
	}
	// Within a tight cluster points sit about 0.003 apart, sibling
	// clusters about 0.3, and the two groups about 1.
	groups := buildAt(in, vecs, linkage(d), 0.6)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	for _, g := range groups {
		if len(g) != 24 {
			t.Errorf("group has %d highlights, want 24", len(g))
		}
		for _, id := range g {
			if strings.HasPrefix(in[id-1].Text, "solo") {
				t.Errorf("the single-book group became a tag")
			}
		}
	}
}

func TestLinkageMergesNearestFirst(t *testing.T) {
	// Points on a line at 0, 1, 10, 11, 30.
	pos := []float32{0, 1, 10, 11, 30}
	d := make([][]float32, len(pos))
	for i := range d {
		d[i] = make([]float32, len(pos))
		for j := range d[i] {
			d[i][j] = max(pos[i]-pos[j], pos[j]-pos[i])
		}
	}
	merges := linkage(d)
	if len(merges) != 4 {
		t.Fatalf("got %d merges, want 4", len(merges))
	}
	got := cut(len(pos), merges, 2)
	want := [][]int{{0, 1}, {2, 3}, {4}}
	if fmt.Sprint(sorted(got)) != fmt.Sprint(want) {
		t.Errorf("cut at 2 = %v, want %v", got, want)
	}
	// Average linkage: {0,1} to {2,3} is the mean of 10, 11, 9, 10.
	if got := cut(len(pos), merges, 9.9); len(got) != 3 {
		t.Errorf("cut just under the average distance gave %d groups, want 3", len(got))
	}
	if got := cut(len(pos), merges, 10); len(got) != 2 {
		t.Errorf("cut at the average distance gave %d groups, want 2", len(got))
	}
}

func sorted(groups [][]int) [][]int {
	for _, g := range groups {
		slices.Sort(g)
	}
	slices.SortFunc(groups, func(a, b []int) int { return a[0] - b[0] })
	return groups
}

func TestCutHeightFollowsNeighborDistances(t *testing.T) {
	// 30 points; the distance between i and j is max(i, j)/100, so each
	// point's 20th neighbor is at 0.20 or further, and the cut lands among
	// the far ones.
	n := 30
	d := make([][]float32, n)
	for i := range d {
		d[i] = make([]float32, n)
		for j := range d[i] {
			if i != j {
				d[i][j] = float32(max(i, j)) / 100
			}
		}
	}
	if got := cutHeight(d); got < 0.2 || got > 0.29 {
		t.Errorf("cut height = %v, want within the 20th-neighbor distances", got)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"\"Cognitive bias\"":                  "Cognitive bias",
		"AGREEING WITH TRUTH":                 "Agreeing with truth",
		"uncertainty in measurement.":         "Uncertainty in measurement",
		"Risk, reward":                        "Risk reward",
		"Risk\nBecause the passages":          "Risk",
		"**Bayesian reasoning**":              "Bayesian reasoning",
		"Personal Growth和发展策略":                "Personal growth",
		"Investment Risks And Markets":        "Investment risks and markets",
		"Inferences from Specific to General": "Inferences from specific to general",
		"Black Swan unpredictability":         "Black Swan unpredictability",
		"Risk in AI models":                   "Risk in AI models",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeNamer names groups "Tag N" in call order, declines groups whose
// first passage contains "decline", and can fail.
type fakeNamer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeNamer) Model() string { return "fake" }

func (f *fakeNamer) Name(ctx context.Context, passages []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.calls++
	if strings.Contains(passages[0], "decline") {
		return "", nil
	}
	return fmt.Sprintf("Tag %d", f.calls), nil
}

func openStore(t *testing.T, pts []point) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	addPoints(t, s, pts)
	return s
}

// addPoints stores highlights with their embeddings. Their ids count up
// from 1 in order.
func addPoints(t *testing.T, s *store.Store, pts []point) {
	t.Helper()
	ids := make([]int64, len(pts))
	vecs := make([][]float32, len(pts))
	for i, p := range pts {
		var book int64
		if _, err := s.DB().Exec(`INSERT OR IGNORE INTO books (title, author) VALUES (?, '')`, p.book); err != nil {
			t.Fatal(err)
		}
		if err := s.DB().QueryRow(`SELECT id FROM books WHERE title = ?`, p.book).Scan(&book); err != nil {
			t.Fatal(err)
		}
		res, err := s.DB().Exec(`INSERT INTO highlights (book_id, text, imported_at, hash) VALUES (?, ?, ?, ?)`,
			book, p.text, now.Format(time.RFC3339), fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		ids[i], _ = res.LastInsertId()
		vecs[i] = p.vec
	}
	if err := s.SaveEmbeddings(model, ids, vecs, now); err != nil {
		t.Fatal(err)
	}
}

func TestSeedSuggestsNamedGroups(t *testing.T) {
	s := openStore(t, library())
	var stages []string
	res, err := Seed(context.Background(), s, model, &fakeNamer{}, func(p Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tags != 2 || res.Highlights != 56 {
		t.Fatalf("seed = %+v, want 2 tags from 56 highlights", res)
	}
	if want := []string{StageReading, StageGrouping, StageNaming, StageSaving}; !slices.Equal(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
	tags, err := s.Tags()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Count != 24 || tag.Suggested != 24 {
			t.Errorf("tag %q has %d highlights, %d suggested; want 24 and 24", tag.Name, tag.Count, tag.Suggested)
		}
	}
	if _, err := Seed(context.Background(), s, model, &fakeNamer{}, nil); !errors.Is(err, store.ErrTagsExist) {
		t.Errorf("seeding again: err = %v, want ErrTagsExist", err)
	}
}

func TestSeedFailureSavesNothing(t *testing.T) {
	s := openStore(t, library())
	down := &fakeNamer{err: fmt.Errorf("%w: Ollama is not reachable", ollama.ErrUnavailable)}
	if _, err := Seed(context.Background(), s, model, down, nil); !errors.Is(err, ollama.ErrUnavailable) {
		t.Fatalf("err = %v, want ollama.ErrUnavailable", err)
	}
	if n, _ := s.CountTags(); n != 0 {
		t.Errorf("a failed run saved %d tags", n)
	}
}

func TestSeedNeedsEnoughHighlights(t *testing.T) {
	s := openStore(t, library()[:10])
	_, err := Seed(context.Background(), s, model, &fakeNamer{}, nil)
	if err == nil || !strings.Contains(err.Error(), "at least 30") {
		t.Errorf("err = %v, want a too-few error", err)
	}
}

func TestSeedDropsDeclinedGroups(t *testing.T) {
	pts := library()
	for i := range pts[:24] { // all of the first group
		pts[i].text = "decline " + pts[i].text
	}
	s := openStore(t, pts)
	res, err := Seed(context.Background(), s, model, &fakeNamer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tags != 1 {
		t.Errorf("got %d tags, want 1", res.Tags)
	}
}

func TestSeederReportsStatus(t *testing.T) {
	s := openStore(t, library())
	r := NewSeeder(s, model, &fakeNamer{}, func() time.Time { return now }, nil)
	if !r.Start() {
		t.Fatal("Start = false with nothing running")
	}
	r.Wait()
	st := r.Status()
	if st.Running || st.Err != "" || st.Finished.IsZero() || st.Result.Tags == 0 {
		t.Errorf("status after a run = %+v", st)
	}
}

func TestSeedProgressStaysWithinTotal(t *testing.T) {
	s := openStore(t, library())
	var last Progress
	_, err := Seed(context.Background(), s, model, &fakeNamer{}, func(p Progress) {
		if p.Stage != StageNaming {
			return
		}
		if p.Total == 0 || p.Done >= p.Total || p.Done < last.Done {
			t.Errorf("progress %d of %d after %d of %d", p.Done, p.Total, last.Done, last.Total)
		}
		last = p
	})
	if err != nil {
		t.Fatal(err)
	}
	if last.Done != last.Total-1 {
		t.Errorf("naming ended at %d of %d", last.Done, last.Total)
	}
}

// ids returns the ids of library() points whose text starts with prefix.
func ids(prefix string) []int64 {
	var out []int64
	for i, p := range library() {
		if strings.HasPrefix(p.text, prefix) {
			out = append(out, int64(i+1))
		}
	}
	return out
}

func tagNames(t *testing.T, s *store.Store, id int64) []string {
	t.Helper()
	tags, err := s.TagsOf([]int64{id})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tag := range tags[id] {
		out = append(out, tag.Name)
	}
	return out
}

func TestSuggestFollowsNeighbors(t *testing.T) {
	s := openStore(t, library())
	first, second := ids("area 0 "), ids("area 1 ")
	// Leave three of the first group untagged, and tag the second group
	// yourself rather than by suggestion.
	if err := s.SeedTags([]store.SeedTag{{Name: "First", Members: first[3:]}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range second {
		if err := s.SetTags(id, []string{"Second"}); err != nil {
			t.Fatal(err)
		}
	}
	// You removed the first group's tag from one highlight, leaving it
	// with none.
	if err := s.SetTags(first[3], nil); err != nil {
		t.Fatal(err)
	}

	n, err := Suggest(s, model)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("suggested %d tags, want 3", n)
	}
	for _, id := range first[:3] {
		if got := tagNames(t, s, id); !slices.Equal(got, []string{"First"}) {
			t.Errorf("highlight %d has %v, want [First]", id, got)
		}
	}
	if got := tagNames(t, s, first[3]); len(got) != 0 {
		t.Errorf("a removed tag came back: %v", got)
	}
	for _, id := range ids("solo") {
		if got := tagNames(t, s, id); len(got) != 0 {
			t.Errorf("solo highlight %d got %v; its neighbors are untagged", id, got)
		}
	}
	if n, err := Suggest(s, model); err != nil || n != 0 {
		t.Errorf("suggesting again added %d (err %v), want 0", n, err)
	}
}

func TestNearestOrdersBySimilarity(t *testing.T) {
	vecs := []store.Vector{
		{HighlightID: 1, Vec: []float32{1, 0}},
		{HighlightID: 2, Vec: []float32{0.6, 0.8}},
		{HighlightID: 3, Vec: []float32{0.8, 0.6}},
		{HighlightID: 4, Vec: []float32{0, 1}},
	}
	if got := nearest(vecs, 0, 2); !slices.Equal(got, []int{2, 1}) {
		t.Errorf("nearest = %v, want [2 1]", got)
	}
}
