package ollama

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type reply struct {
	Answer string `json:"answer"`
}

// serve returns a Client for a server that answers every request with
// status and body.
func serve(t *testing.T, status int, body string) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/test" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, time.Minute)
}

func TestNew(t *testing.T) {
	for in, want := range map[string]string{
		"":                        DefaultURL,
		"localhost:9999":          "http://localhost:9999",
		"https://ollama.example/": "https://ollama.example",
	} {
		if got := New(in, time.Minute).BaseURL; got != want {
			t.Errorf("New(%q).BaseURL = %q, want %q", in, got, want)
		}
	}
}

func TestPost(t *testing.T) {
	ctx := context.Background()
	var r reply
	if err := serve(t, 200, `{"answer":"yes"}`).Post(ctx, "/api/test", "m", map[string]string{}, &r); err != nil || r.Answer != "yes" {
		t.Errorf("Post = %+v, %v", r, err)
	}

	err := serve(t, 404, `{"error":"model \"m\" not found, try pulling it first"}`).Post(ctx, "/api/test", "m", nil, &r)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "ollama pull m") {
		t.Errorf("not pulled: err = %v", err)
	}

	err = serve(t, 500, `{"error":"out of memory"}`).Post(ctx, "/api/test", "m", nil, &r)
	if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "ollama test: HTTP 500: out of memory") {
		t.Errorf("server error: err = %v", err)
	}

	err = serve(t, 502, `<html>bad gateway</html>`).Post(ctx, "/api/test", "m", nil, &r)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502: <html>") {
		t.Errorf("non-JSON error: err = %v", err)
	}
}

func TestPostUnreachable(t *testing.T) {
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now
	var r reply
	if err := New(addr, time.Minute).Post(ctx, "/api/test", "m", nil, &r); !errors.Is(err, ErrUnavailable) {
		t.Errorf("closed port: err = %v, want ErrUnavailable", err)
	}
	// A mistyped OLLAMA_HOST fails name resolution rather than dialing.
	if err := New("no-such-host.invalid:11434", time.Minute).Post(ctx, "/api/test", "m", nil, &r); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unknown host: err = %v, want ErrUnavailable", err)
	}
}
