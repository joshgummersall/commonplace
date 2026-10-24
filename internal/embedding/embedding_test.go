package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/store"
)

func TestNormalizeAndDot(t *testing.T) {
	v := []float32{3, 4}
	Normalize(v)
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Errorf("Normalize = %v", v)
	}
	if d := Dot(v, v); math.Abs(float64(d)-1) > 1e-6 {
		t.Errorf("Dot(v, v) = %v, want 1", d)
	}
	zero := []float32{0, 0}
	Normalize(zero)
	if zero[0] != 0 || zero[1] != 0 {
		t.Errorf("zero vector changed: %v", zero)
	}
}

// fakeOllama records requests and answers like /api/embed.
type fakeOllama struct {
	inputs [][]string
	status int
	body   string
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req embedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f.inputs = append(f.inputs, req.Input)
	if f.status != 0 {
		w.WriteHeader(f.status)
		fmt.Fprint(w, f.body)
		return
	}
	var out embedResponse
	for range req.Input {
		out.Embeddings = append(out.Embeddings, []float32{3, 4, 0})
	}
	json.NewEncoder(w).Encode(out)
}

func TestOllama(t *testing.T) {
	f := &fakeOllama{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	o := NewOllama(srv.URL, "")
	ctx := context.Background()

	if o.Model() != "ollama/qwen3-embedding:0.6b" {
		t.Errorf("Model() = %q", o.Model())
	}
	docs, err := o.EmbedDocuments(ctx, []string{"a passage", "another"})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || math.Abs(float64(docs[0][0])-0.6) > 1e-6 {
		t.Errorf("documents = %v, want two unit vectors", docs)
	}
	if _, err := o.EmbedQuery(ctx, "stoic courage"); err != nil {
		t.Fatal(err)
	}

	// Documents go in plain; queries carry Qwen3's instruction.
	if got := f.inputs[0]; got[0] != "a passage" || got[1] != "another" {
		t.Errorf("document inputs = %q", got)
	}
	q := f.inputs[1][0]
	if !strings.HasPrefix(q, "Instruct: ") || !strings.HasSuffix(q, "\nQuery:stoic courage") {
		t.Errorf("query input = %q", q)
	}

	// Other models get bare queries.
	other := NewOllama(srv.URL, "nomic-embed-text")
	if _, err := other.EmbedQuery(ctx, "stoic courage"); err != nil {
		t.Fatal(err)
	}
	if got := f.inputs[2][0]; got != "stoic courage" {
		t.Errorf("non-Qwen query input = %q", got)
	}
}

func TestOllamaErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("model not pulled", func(t *testing.T) {
		srv := httptest.NewServer(&fakeOllama{status: 404, body: `{"error":"model \"qwen3-embedding:0.6b\" not found, try pulling it first"}`})
		defer srv.Close()
		_, err := NewOllama(srv.URL, "").EmbedDocuments(ctx, []string{"x"})
		if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "ollama pull qwen3-embedding:0.6b") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("not running", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close() // nothing listens here now
		_, err = NewOllama(addr, "").EmbedDocuments(ctx, []string{"x"})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(&fakeOllama{status: 500, body: `{"error":"out of memory"}`})
		defer srv.Close()
		_, err := NewOllama(srv.URL, "").EmbedDocuments(ctx, []string{"x"})
		if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "out of memory") {
			t.Errorf("err = %v, want a plain error naming the cause", err)
		}
	})

	t.Run("wrong vector count", func(t *testing.T) {
		srv := httptest.NewServer(&fakeOllama{status: 200, body: `{"embeddings":[[1,0]]}`})
		defer srv.Close()
		if _, err := NewOllama(srv.URL, "").EmbedDocuments(ctx, []string{"x", "y"}); err == nil {
			t.Error("accepted 1 vector for 2 texts")
		}
	})
}

// hashEmbedder makes deterministic vectors without a model.
type hashEmbedder struct {
	model string
	calls int
	fail  error // returned once calls reaches failAt
	at    int
}

func (h *hashEmbedder) Model() string { return h.model }

func (h *hashEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	h.calls++
	if h.fail != nil && h.calls >= h.at {
		return nil, h.fail
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		f := fnv.New32a()
		f.Write([]byte(t))
		v := []float32{float32(f.Sum32()%97) + 1, 1, 2}
		Normalize(v)
		out[i] = v
	}
	return out, nil
}

func (h *hashEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vs, err := h.EmbedDocuments(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

func seedHighlights(t *testing.T, n int) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	err = s.InTx(func(tx *store.Tx) error {
		bookID, err := tx.BookID("Book", "Author")
		if err != nil {
			return err
		}
		for i := range n {
			h := &store.Highlight{BookID: bookID, Text: fmt.Sprintf("highlight %d", i), ImportedAt: time.Now(), Hash: fmt.Sprint(i)}
			if err := tx.InsertHighlight(h); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func stale(t *testing.T, s *store.Store, model string) int {
	t.Helper()
	n, err := s.CountStaleEmbeddings(model)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBackfill(t *testing.T) {
	s := seedHighlights(t, 70) // three batches: 32, 32, 6
	ctx := context.Background()
	e := &hashEmbedder{model: "m1"}

	var reports []string
	n, err := Backfill(ctx, s, e, func(done, total int) { reports = append(reports, fmt.Sprintf("%d/%d", done, total)) })
	if err != nil || n != 70 {
		t.Fatalf("Backfill = %d, %v; want 70", n, err)
	}
	if got := strings.Join(reports, " "); got != "32/70 64/70 70/70" {
		t.Errorf("progress = %s", got)
	}
	vecs, err := s.Vectors("m1")
	if err != nil || len(vecs) != 70 || len(vecs[0].Vec) != 3 {
		t.Fatalf("stored %d vectors, err %v", len(vecs), err)
	}

	// Nothing left to do.
	if n, _ := Backfill(ctx, s, e, nil); n != 0 {
		t.Errorf("second Backfill embedded %d", n)
	}

	// A changed highlight is re-embedded alone (the stale-embedding trigger).
	if _, err := s.DB().Exec(`UPDATE highlights SET text = 'edited' WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	if n, _ := Backfill(ctx, s, e, nil); n != 1 {
		t.Errorf("after one edit, Backfill embedded %d, want 1", n)
	}

	// A new model makes every vector stale; old vectors aren't served for it.
	e2 := &hashEmbedder{model: "m2"}
	if stale(t, s, "m2") != 70 {
		t.Error("model change did not mark all stale")
	}
	if vs, _ := s.Vectors("m2"); len(vs) != 0 {
		t.Errorf("%d vectors returned for a model that made none", len(vs))
	}
	if n, _ := Backfill(ctx, s, e2, nil); n != 70 {
		t.Errorf("re-embed after model change = %d, want 70", n)
	}
}

func TestBackfillKeepsProgressOnFailure(t *testing.T) {
	s := seedHighlights(t, 70)
	e := &hashEmbedder{model: "m1", fail: fmt.Errorf("%w: down", ErrUnavailable), at: 2}
	n, err := Backfill(context.Background(), s, e, nil)
	if !errors.Is(err, ErrUnavailable) || n != 32 {
		t.Fatalf("Backfill = %d, %v; want the first batch saved, then ErrUnavailable", n, err)
	}
	if left := stale(t, s, "m1"); left != 38 {
		t.Errorf("stale after failure = %d, want 38", left)
	}
	e.fail = nil
	if n, err := Backfill(context.Background(), s, e, nil); err != nil || n != 38 {
		t.Errorf("resume = %d, %v; want 38", n, err)
	}
}

// flaky is down until up is closed.
type flaky struct {
	hashEmbedder
	up chan struct{}
}

func (f *flaky) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-f.up:
		return f.hashEmbedder.EmbedDocuments(ctx, texts)
	default:
		return nil, fmt.Errorf("%w: down", ErrUnavailable)
	}
}

func TestKeepFreshCatchesUpWhenEmbedderReturns(t *testing.T) {
	s := seedHighlights(t, 5)
	f := &flaky{hashEmbedder: hashEmbedder{model: "m1"}, up: make(chan struct{})}
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs}, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		KeepFresh(ctx, s, f, 10*time.Millisecond, logger)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond) // several failed rounds
	if stale(t, s, "m1") != 5 {
		t.Fatal("embedded while down")
	}
	close(f.up)
	deadline := time.Now().Add(2 * time.Second)
	for stale(t, s, "m1") != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n := stale(t, s, "m1"); n != 0 {
		t.Fatalf("%d still stale after the embedder came back", n)
	}
	out := logs.String()
	if c := strings.Count(out, "embeddings paused"); c != 1 {
		t.Errorf("logged the outage %d times, want once:\n%s", c, out)
	}
	if !strings.Contains(out, "embedder reachable again") || !strings.Contains(out, "count=5") {
		t.Errorf("missing recovery logs:\n%s", out)
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
