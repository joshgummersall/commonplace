package themes

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

// library builds two areas of two themes each, 12 highlights per theme
// spread over four books, plus 8 highlights from a single book that sit
// together but are only that book's chapter.
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

func inputs(pts []point) []store.ThemeInput {
	books := map[string]int64{}
	out := make([]store.ThemeInput, len(pts))
	for i, p := range pts {
		if _, ok := books[p.book]; !ok {
			books[p.book] = int64(len(books) + 1)
		}
		out[i] = store.ThemeInput{ID: int64(i + 1), BookID: books[p.book], Text: p.text, Vec: p.vec}
	}
	return out
}

func TestBuildNestsThemesInAreasAndDropsSingleBookGroups(t *testing.T) {
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
	merges := linkage(d)
	// Within a theme points sit about 0.003 apart, sibling themes about
	// 0.3, and areas about 1.
	areas := buildAt(in, vecs, merges, 0.1, 0.6)
	if len(areas) != 2 {
		t.Fatalf("got %d areas, want 2", len(areas))
	}
	for _, a := range areas {
		if len(a.theme.Members) != 24 || len(a.themes) != 2 {
			t.Errorf("area has %d highlights and %d themes, want 24 and 2", len(a.theme.Members), len(a.themes))
		}
		for _, th := range a.themes {
			if len(th.theme.Members) != 12 {
				t.Errorf("theme has %d highlights, want 12", len(th.theme.Members))
			}
			for _, id := range th.theme.Members {
				if !slices.Contains(a.theme.Members, id) {
					t.Errorf("highlight %d is in a theme but not its area", id)
				}
			}
		}
	}
	for _, a := range areas {
		for _, id := range a.theme.Members {
			if strings.HasPrefix(in[id-1].Text, "solo") {
				t.Errorf("the single-book group became a theme")
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

func TestCutHeightsFollowNeighborDistances(t *testing.T) {
	// 30 points; point i's distances are all i/100, so each one's 20th
	// neighbor is at i/100 or (for larger j) further.
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
	theme, area := cutHeights(d)
	if !(theme < area) {
		t.Errorf("theme cut %v should be below area cut %v", theme, area)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"\"Cognitive bias\"":                  "Cognitive bias",
		"AGREEING WITH TRUTH":                 "Agreeing with truth",
		"uncertainty in measurement.":         "Uncertainty in measurement",
		"No clear theme.":                     "",
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

// fakeNamer names groups "Area N" or "Theme N" in call order, declines
// groups whose first passage contains "decline", and can fail.
type fakeNamer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeNamer) Model() string { return "fake" }

func (f *fakeNamer) Name(ctx context.Context, passages []string, broad bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.calls++
	if strings.Contains(passages[0], "decline") {
		return "", nil
	}
	if broad {
		return fmt.Sprintf("Area %d", f.calls), nil
	}
	return fmt.Sprintf("Theme %d", f.calls), nil
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

// addPoints stores highlights with their embeddings.
func addPoints(t *testing.T, s *store.Store, pts []point) {
	t.Helper()
	var existing int
	s.DB().QueryRow(`SELECT count(*) FROM highlights`).Scan(&existing)
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
			book, p.text, now.Format(time.RFC3339), fmt.Sprint(existing+i))
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

func TestRefreshKeepsNamesAndIDs(t *testing.T) {
	s := openStore(t, library())
	namer := &fakeNamer{}
	var stages []string
	res, err := Refresh(context.Background(), s, model, namer, now, func(p Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Areas == 0 || res.Highlights != 56 || res.Kept != 0 {
		t.Fatalf("first refresh = %+v", res)
	}
	if want := []string{StageReading, StageGrouping, StageNaming, StageSaving}; !slices.Equal(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
	before, err := s.ThemeTree()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameTheme(before[0].ID, "My own name"); err != nil {
		t.Fatal(err)
	}

	// The same highlights regroup the same way: no new names needed.
	calls := namer.calls
	res, err = Refresh(context.Background(), s, model, namer, now.Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if namer.calls != calls {
		t.Errorf("second refresh named %d groups, want 0", namer.calls-calls)
	}
	if res.Kept != res.Areas+res.Themes {
		t.Errorf("kept %d of %d groups", res.Kept, res.Areas+res.Themes)
	}
	after, err := s.ThemeTree()
	if err != nil {
		t.Fatal(err)
	}
	if after[0].ID != before[0].ID || after[0].Name != "My own name" || !after[0].Renamed {
		t.Errorf("renamed area became %+v", after[0])
	}
	last, ok, err := s.LastThemeRefresh()
	if err != nil || !ok || !last.At.Equal(now.Add(time.Hour)) || last.Highlights != 56 {
		t.Errorf("last refresh = %+v, %v, %v", last, ok, err)
	}
}

func TestRefreshFailureLeavesThemesAlone(t *testing.T) {
	s := openStore(t, library())
	if _, err := Refresh(context.Background(), s, model, &fakeNamer{}, now, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := s.ThemeTree()

	// A new group of highlights needs a new name.
	rng := rand.New(rand.NewPCG(3, 4))
	var fresh []point
	for i := range 12 {
		fresh = append(fresh, point{book: fmt.Sprintf("Book %d", i%4), text: fmt.Sprintf("new passage %d", i),
			vec: vec(rng, 16, map[int]float32{12: 1})})
	}
	addPoints(t, s, fresh)
	down := &fakeNamer{err: fmt.Errorf("%w: Ollama is not reachable", ErrUnavailable)}
	if _, err := Refresh(context.Background(), s, model, down, now, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	after, _ := s.ThemeTree()
	if len(after) != len(before) || after[0].ID != before[0].ID {
		t.Errorf("a failed refresh changed the themes")
	}
}

func TestRefreshNeedsEnoughHighlights(t *testing.T) {
	s := openStore(t, library()[:10])
	_, err := Refresh(context.Background(), s, model, &fakeNamer{}, now, nil)
	if err == nil || !strings.Contains(err.Error(), "at least 30") {
		t.Errorf("err = %v, want a too-few error", err)
	}
}

func TestRefreshDropsDeclinedThemes(t *testing.T) {
	pts := library()
	for i := range pts[:24] { // all of the first area
		pts[i].text = "decline " + pts[i].text
	}
	s := openStore(t, pts)
	res, err := Refresh(context.Background(), s, model, &fakeNamer{}, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Declined == 0 {
		t.Errorf("no group was declined: %+v", res)
	}
	tree, _ := s.ThemeTree()
	for _, a := range tree {
		for _, th := range a.Themes {
			if th.Name == "" {
				t.Errorf("a declined theme was saved")
			}
		}
	}
}

func TestRefresherReportsStatus(t *testing.T) {
	s := openStore(t, library())
	r := NewRefresher(s, model, &fakeNamer{}, func() time.Time { return now }, nil)
	if !r.Start() {
		t.Fatal("Start = false with nothing running")
	}
	r.Wait()
	st := r.Status()
	if st.Running || st.Err != "" || st.Finished.IsZero() || st.Result.Areas == 0 {
		t.Errorf("status after a refresh = %+v", st)
	}
}

func TestRefreshReportsLostNames(t *testing.T) {
	s := openStore(t, library())
	if _, err := Refresh(context.Background(), s, model, &fakeNamer{}, now, nil); err != nil {
		t.Fatal(err)
	}
	tree, _ := s.ThemeTree()
	if len(tree) < 2 {
		t.Fatalf("got %d areas, want 2", len(tree))
	}
	// Name every area, then remove one area's highlights entirely.
	var gone, kept string
	for i, a := range tree {
		name := fmt.Sprintf("Mine %d", i)
		if err := s.RenameTheme(a.ID, name); err != nil {
			t.Fatal(err)
		}
		members, _ := s.ThemeMemberIDs(a.ID)
		var id int64
		for id = range members {
			break
		}
		e, _ := s.Entry(id)
		if strings.HasPrefix(e.Text, "area 0 ") {
			gone = name
		} else if strings.HasPrefix(e.Text, "area 1 ") {
			kept = name
		}
	}
	if _, err := s.DB().Exec(`DELETE FROM highlights WHERE text LIKE 'area 0 %'`); err != nil {
		t.Fatal(err)
	}
	res, err := Refresh(context.Background(), s, model, &fakeNamer{}, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Lost, gone) || slices.Contains(res.Lost, kept) {
		t.Errorf("Lost = %v, want %q and not %q", res.Lost, gone, kept)
	}
}
