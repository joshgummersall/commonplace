package embedding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/ollama"
)

// DefaultModel is the embedding model used unless configured otherwise.
const DefaultModel = "qwen3-embedding:0.6b"

// qwen3QueryInstruction is the task description Qwen3-Embedding expects
// in front of queries (but not documents): the model was trained to embed
// "Instruct: <task>\nQuery:<text>" for the retrieval side.
const qwen3QueryInstruction = "Given a search query, retrieve passages from a reader's book highlights that are relevant to it"

// Ollama embeds text with a model served by Ollama's /api/embed.
type Ollama struct {
	ollama.Client
	Name string // model name, e.g. "qwen3-embedding:0.6b"
}

// NewOllama returns an Ollama embedder with defaults filled in.
func NewOllama(baseURL, model string) *Ollama {
	if model == "" {
		model = DefaultModel
	}
	return &Ollama{Client: ollama.New(baseURL, 2*time.Minute), Name: model}
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
}

func (o *Ollama) embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out embedResponse
	req := embedRequest{Model: o.Name, Input: texts, Truncate: true}
	if err := o.Post(ctx, "/api/embed", o.Name, req, &out); err != nil {
		return nil, err
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
