package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/ollama"
)

// unavailable is an embedder whose service is down.
type unavailable struct{}

func (unavailable) Model() string { return "test/unavailable" }
func (unavailable) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return nil, &ollama.UnavailableError{Reason: "test"}
}
func (unavailable) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, &ollama.UnavailableError{Reason: "test"}
}

// unitEmbedder returns the same unit vector for every text.
type unitEmbedder struct{}

func (unitEmbedder) Model() string { return "test/unit" }
func (unitEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}
func (u unitEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vs, err := u.EmbedDocuments(ctx, []string{text})
	return vs[0], err
}

// runCLI runs cmpl with an embedder that is down, so tests never reach a
// real Ollama.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runCLIWith(t, unavailable{}, args...)
}

func runCLIWith(t *testing.T, e embedding.Embedder, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := NewApp(nil, &out, &errOut)
	a.getenv = func(string) string { return "" }
	a.newEmbedder = func() embedding.Embedder { return e }
	err = a.run(args)
	return out.String(), errOut.String(), err
}

func TestImportCommand(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	fixture := filepath.Join("..", "..", "testdata", "kindle", "edge-cases.txt")

	out, errOut, err := runCLI(t, "--db", db, "import", fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 added", "1 notes attached", "3 entries skipped with warnings"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "unrecognized kindle header") {
		t.Errorf("stderr missing parse warning:\n%s", errOut)
	}

	out, _, err = runCLI(t, "--db", db, "import", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 added, 0 updated to a newer version, 4 unchanged") {
		t.Errorf("re-import stdout:\n%s", out)
	}
}

func TestImportCommandErrors(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	kindle := filepath.Join("..", "..", "testdata", "kindle", "clippings.txt")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown source", []string{"import", kindle, "--source", "kobo"}, `unknown source "kobo"`},
		{"wrong source", []string{"import", kindle, "--source", "crosspoint"}, "kindle entry in a crosspoint file"},
		{"missing file", []string{"import", "nope.txt"}, "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCLI(t, append([]string{"--db", db}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestImportEmbeds(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	fixture := filepath.Join("..", "..", "testdata", "kindle", "clippings.txt")

	// Embedder down: the import succeeds and says what was left undone.
	out, errOut, err := runCLI(t, "--db", db, "import", fixture)
	if err != nil {
		t.Fatalf("import failed with the embedder down: %v", err)
	}
	if !strings.Contains(out, "10 added") || !strings.Contains(errOut, "10 highlights not embedded") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", out, errOut)
	}

	// Embedder up: a re-import embeds what the first one could not.
	out, _, err = runCLIWith(t, unitEmbedder{}, "--db", db, "import", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "10 highlights embedded with test/unit") {
		t.Errorf("stdout:\n%s", out)
	}
	out, errOut, _ = runCLIWith(t, unitEmbedder{}, "--db", db, "import", fixture)
	if strings.Contains(out, "embedded") || errOut != "" {
		t.Errorf("third import should have nothing to embed:\n%s%s", out, errOut)
	}
}

func TestEvalCommand(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "c.db")
	if _, _, err := runCLI(t, "--db", db, "import", filepath.Join("..", "..", "testdata", "kindle", "clippings.txt")); err != nil {
		t.Fatal(err)
	}

	// Default location is next to the database; a missing file shows the format.
	_, _, err := runCLI(t, "--db", db, "eval")
	if err == nil || !strings.Contains(err.Error(), "eval.yaml does not exist") || !strings.Contains(err.Error(), "expect:") {
		t.Fatalf("err = %v", err)
	}

	evalFile := filepath.Join(dir, "eval.yaml")
	body := "k: 5\nqueries:\n  - query: perceptible change\n    expect: [1]\n  - query: nothing here\n    expect: [424242]\n"
	if err := os.WriteFile(evalFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runCLI(t, "--db", db, "eval", "-v")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 queries, k=5", "RECALL@5", "keyword", "hybrid", "keyword fallback", "perceptible change"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "[424242]") {
		t.Errorf("stderr should warn about the unknown id:\n%s", errOut)
	}

	// Hidden from help.
	help, _, _ := runCLI(t, "--help")
	if strings.Contains(help, "eval") {
		t.Error("eval shows in --help")
	}
}
