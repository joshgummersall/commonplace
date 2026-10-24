package eval

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/search"
	"github.com/joshgummersall/commonplace/internal/store"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "eval.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	f, err := Load(writeFile(t, Example))
	if err != nil {
		t.Fatalf("the documented example doesn't load: %v", err)
	}
	if f.K != 10 || len(f.Queries) != 1 || f.Queries[0].Expect[1] != 977 {
		t.Errorf("loaded %+v", f)
	}
	// JSON is YAML too; k defaults.
	f, err = Load(writeFile(t, `{"queries": [{"query": "q", "expect": [1]}]}`))
	if err != nil || f.K != DefaultK {
		t.Errorf("JSON file: %+v, %v", f, err)
	}
	for _, bad := range []string{"queries: []", "queries:\n  - query: q", "queries:\n  - expect: [1]", "k: [oops"} {
		if _, err := Load(writeFile(t, bad)); err == nil {
			t.Errorf("Load accepted %q", bad)
		}
	}
}

func TestRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clips []clippings.Clipping
	for i, text := range []string{
		"The river carries everything downstream.",
		"A river never steps twice into the same man.",
		"Mountains stand still while rivers move.",
	} {
		clips = append(clips, clippings.Clipping{Kind: clippings.Highlight, Title: "B", LocStart: 10 * (i + 1),
			LocEnd: 10*(i+1) + 1, AddedAt: at, Text: text})
	}
	if _, err := importer.Import(s, clips, at); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	rows, _ := s.DB().Query(`SELECT id, text FROM highlights`)
	for rows.Next() {
		var id int64
		var text string
		rows.Scan(&id, &text)
		ids[text[:5]] = id
	}
	rows.Close()

	f := File{K: 2, Queries: []Case{
		// Found at rank 1 of 1 expected: recall 1, RR 1.
		{Query: "carries", Expect: []int64{ids["The r"]}},
		// One of two expected found, at rank 1 (the other id doesn't exist).
		{Query: "steps", Expect: []int64{ids["A riv"], 9999}},
		// Not found at all.
		{Query: "volcano", Expect: []int64{ids["Mount"]}},
	}}
	r, err := Run(context.Background(), &search.Searcher{Store: s}, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Missing) != 1 || r.Missing[0] != 9999 {
		t.Errorf("Missing = %v, want [9999]", r.Missing)
	}
	if len(r.Modes) != len(search.Modes) {
		t.Fatalf("%d modes reported", len(r.Modes))
	}
	kw := r.Modes[1]
	if kw.Mode != search.ModeKeyword {
		t.Fatalf("mode order = %v", kw.Mode)
	}
	if math.Abs(kw.Recall-(1+0.5+0)/3) > 1e-9 {
		t.Errorf("keyword recall = %v", kw.Recall)
	}
	if kw.Cases[1] != (CaseResult{Found: 1, FirstRank: 1}) {
		t.Errorf("partial find scored %+v", kw.Cases[1])
	}
	if math.Abs(kw.MRR-(1+1+0)/3.0) > 1e-9 {
		t.Errorf("keyword MRR = %v", kw.MRR)
	}
	if kw.Cases[2] != (CaseResult{}) {
		t.Errorf("miss scored %+v", kw.Cases[2])
	}
	// Without an embedder, semantic and hybrid report that they fell back.
	for _, m := range r.Modes {
		if m.Mode != search.ModeKeyword && !strings.Contains(m.Fallback, "no embedder") {
			t.Errorf("%s fallback = %q", m.Mode, m.Fallback)
		}
	}
}
