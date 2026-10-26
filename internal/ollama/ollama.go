// Package ollama talks to a local Ollama server, which serves both the
// embedding model and the model that names suggested tags.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultURL is where a local Ollama listens by default.
const DefaultURL = "http://localhost:11434"

// ErrUnavailable means a model could not be reached: Ollama is not
// running, the host is wrong, or the model is not pulled. Callers degrade
// (keyword search, no tag suggestions) rather than failing.
var ErrUnavailable = errors.New("model unavailable")

// maxResponse caps how much of a reply is read. A batch of embeddings is
// the largest reply.
const maxResponse = 256 << 20

// Client posts JSON to an Ollama server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a Client for baseURL, or DefaultURL when it is empty.
func New(baseURL string, timeout time.Duration) Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	if !strings.Contains(baseURL, "://") {
		// OLLAMA_HOST is often set as host:port.
		baseURL = "http://" + baseURL
	}
	return Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: timeout}}
}

// Post sends req to path (such as "/api/embed") and decodes the reply into
// resp. model names the model req asks for, for the not-pulled error.
func (c Client) Post(ctx context.Context, path, model string, req, resp any) error {
	op := "ollama " + strings.TrimPrefix(path, "/api/")
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hresp, err := c.HTTP.Do(hreq)
	if err != nil {
		if isConnErr(err) {
			return fmt.Errorf("%w: Ollama is not reachable at %s", ErrUnavailable, c.BaseURL)
		}
		return fmt.Errorf("%s: %w", op, err)
	}
	defer hresp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(hresp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", op, err)
	}
	if hresp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("%s: HTTP %d: %.200s", op, hresp.StatusCode, raw)
		}
		if hresp.StatusCode == http.StatusNotFound && strings.Contains(e.Error, "not found") {
			return fmt.Errorf("%w: model %s is not pulled; run `ollama pull %s`", ErrUnavailable, model, model)
		}
		return fmt.Errorf("%s: HTTP %d: %s", op, hresp.StatusCode, e.Error)
	}
	if err := json.Unmarshal(raw, resp); err != nil {
		return fmt.Errorf("%s: HTTP %d: %.200s", op, hresp.StatusCode, raw)
	}
	return nil
}

// isConnErr reports whether err means nothing answered: Ollama is not
// running, or the host is wrong.
func isConnErr(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
