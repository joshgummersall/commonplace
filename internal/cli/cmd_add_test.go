package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/embedding"
)

// runAdd runs cmpl add against db with stdin, and decodes its output.
func runAdd(t *testing.T, db, stdin string, args ...string) addResultJSON {
	t.Helper()
	var out, errOut bytes.Buffer
	a := NewApp(strings.NewReader(stdin), &out, &errOut)
	a.getenv = func(string) string { return "" }
	a.newEmbedder = func() embedding.Embedder { return unitEmbedder{} }
	a.now = func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC) }
	if err := a.run(append([]string{"--db", db, "add"}, args...)); err != nil {
		t.Fatalf("cmpl add %v: %v\nstderr: %s", args, err, errOut.String())
	}
	var r addResultJSON
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return r
}

func tagNames(h highlightJSON) []string {
	var names []string
	for _, t := range h.Tags {
		names = append(names, t.Name)
	}
	return names
}

func TestAddCommand(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")

	r := runAdd(t, db, "", "We are what we repeatedly do.",
		"--title", "The Story of Philosophy", "--author", "Will Durant",
		"--page", "87", "--tag", "habit,virtue", "--note", "Aristotle, via Durant")
	h := r.Highlight
	if r.Status != "added" || h.ID == 0 {
		t.Fatalf("status %q, id %d; want added", r.Status, h.ID)
	}
	if h.Text != "We are what we repeatedly do." || h.Title != "The Story of Philosophy" ||
		h.Author != "Will Durant" || h.Location != "page 87" {
		t.Errorf("stored %+v", h)
	}
	if h.HighlightedAt == nil || !h.HighlightedAt.Equal(time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("highlighted_at %v, want now", h.HighlightedAt)
	}
	if got := strings.Join(tagNames(h), ","); got != "habit,virtue" {
		t.Errorf("tags %q", got)
	}
	if len(h.Notes) != 1 || h.Notes[0].Body != "Aristotle, via Durant" || h.Notes[0].Source != "user" {
		t.Errorf("notes %+v", h.Notes)
	}

	// The same text from stdin, differently spaced, is the same highlight: the note isn't repeated, and
	// tags are added to the ones it has.
	r = runAdd(t, db, "  We are what we\nrepeatedly   do.\n", "-",
		"--title", "The Story of Philosophy", "--author", "Will Durant",
		"--page", "87", "--tag", "excellence", "--note", "Aristotle, via Durant")
	if r.Status != "exists" || r.Highlight.ID != h.ID {
		t.Fatalf("status %q, id %d; want exists, %d", r.Status, r.Highlight.ID, h.ID)
	}
	if got := strings.Join(tagNames(r.Highlight), ","); got != "excellence,habit,virtue" {
		t.Errorf("tags %q", got)
	}
	if len(r.Highlight.Notes) != 1 {
		t.Errorf("notes %+v, want the one note", r.Highlight.Notes)
	}

	// A longer version replaces it, keeping the id.
	r = runAdd(t, db, "We are what we repeatedly do. Excellence, then, is not an act, but a habit.",
		"--title", "The Story of Philosophy", "--author", "Will Durant", "--page", "87")
	if r.Status != "updated" || r.Highlight.ID != h.ID || !strings.HasSuffix(r.Highlight.Text, "a habit.") {
		t.Errorf("status %q, highlight %+v; want updated #%d", r.Status, r.Highlight, h.ID)
	}

	// Text read from stdin when there is no argument; --date sets the time.
	r = runAdd(t, db, "Waste no more time arguing what a good man should be. Be one.\n",
		"-t", "Meditations", "-a", "Marcus Aurelius", "--date", "2025-03-01")
	if r.Status != "added" || r.Highlight.ID == h.ID || r.Highlight.Text != "Waste no more time arguing what a good man should be. Be one." {
		t.Errorf("stdin add: %+v", r)
	}
	if d := r.Highlight.HighlightedAt; d == nil || d.In(time.Local).Format("2006-01-02") != "2025-03-01" {
		t.Errorf("highlighted_at %v, want 2025-03-01", d)
	}

	// The added highlights are searchable and embedded.
	out, _, err := runCLIWith(t, unitEmbedder{}, "--db", db, "similar", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Marcus Aurelius") {
		t.Errorf("similar 1 doesn't list the other added highlight:\n%s", out)
	}
}

func TestAddCommandErrors(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"add", "text"}, "--title is required"},
		{[]string{"add", "   ", "--title", "T"}, "text is empty"},
		{[]string{"add", "--title", "T"}, "no text"},
		{[]string{"add", "text", "--title", "T", "--date", "yesterday"}, "--date"},
		{[]string{"add", "a", "b", "--title", "T"}, "accepts at most 1 arg"},
	} {
		_, _, err := runCLI(t, append([]string{"--db", db}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error %v, want %q", tc.args, err, tc.want)
		}
	}
}
