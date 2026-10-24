package embedding

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

// DefaultOllamaURL is where a local Ollama listens by default.
const DefaultOllamaURL = "http://localhost:11434"

// DefaultModel is the embedding model used unless configured otherwise.
const DefaultModel = "qwen3-embedding:0.6b"

// qwen3QueryInstruction is the task description Qwen3-Embedding expects
// in front of queries (but not documents): the model was trained to embed
// "Instruct: <task>\nQuery:<text>" for the retrieval side.
const qwen3QueryInstruction = "Given a search query, retrieve passages from a reader's book highlights that are relevant to it"

// Ollama embeds text with a model served by Ollama's /api/embed.
type Ollama struct {
	BaseURL string
	Name    string // model name, e.g. "qwen3-embedding:0.6b"
	Client  *http.Client
}

// NewOllama returns an Ollama embedder with defaults filled in.
func NewOllama(baseURL, model string) *Ollama {
	if baseURL == "" {
		baseURL = DefaultOllamaURL
	}
	if model == "" {
		model = DefaultModel
	}
	if !strings.Contains(baseURL, "://") {
		// OLLAMA_HOST is often set as host:port.
		baseURL = "http://" + baseURL
	}
	return &Ollama{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Name:    model,
		Client:  &http.Client{Timeout: 2 * time.Minute},
	}
}

func (o *Ollama) Model() string {
	return "ollama/" + o.Name
}

func (o *Ollama) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return o.embed(ctx, texts)
}

func (o *Ollama) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vs, err := o.embed(ctx, []string{o.queryText(text)})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// queryText applies the model's query instruction. Only Qwen3 embedding
// models are known to want one; others get the bare query.
func (o *Ollama) queryText(text string) string {
	if strings.HasPrefix(o.Name, "qwen3-embedding") {
		return "Instruct: " + qwen3QueryInstruction + "\nQuery:" + text
	}
	return text
}

type embedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Truncate bool     `json:"truncate"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

func (o *Ollama) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: o.Name, Input: texts, Truncate: true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		if isConnErr(err) {
			return nil, fmt.Errorf("%w: Ollama is not reachable at %s", ErrUnavailable, o.BaseURL)
		}
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: read response: %w", err)
	}
	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ollama embed: HTTP %d: %.200s", resp.StatusCode, raw)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound && strings.Contains(out.Error, "not found") {
			return nil, fmt.Errorf("%w: model %s is not pulled; run `ollama pull %s`", ErrUnavailable, o.Name, o.Name)
		}
		return nil, fmt.Errorf("ollama embed: HTTP %d: %s", resp.StatusCode, out.Error)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: got %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	for _, v := range out.Embeddings {
		if len(v) == 0 {
			return nil, errors.New("ollama embed: empty vector")
		}
		Normalize(v)
	}
	return out.Embeddings, nil
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
