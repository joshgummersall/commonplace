package themes

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
	"unicode"
	"unicode/utf8"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/store"
)

// Namer names a group of highlights from its most typical passages. Broad
// asks for an area's subject rather than a theme's idea. An empty name
// means the passages share no idea, and the group is dropped.
type Namer interface {
	Model() string
	Name(ctx context.Context, passages []string, broad bool) (string, error)
}

// DefaultNameModel is the Ollama model that names themes unless
// configured otherwise.
const DefaultNameModel = "qwen2.5:14b"

// ErrUnavailable means the naming model could not be reached.
var ErrUnavailable = errors.New("naming model unavailable")

// noTheme is what the model answers when a group shares no idea.
const noTheme = "no clear theme"

// OllamaNamer names groups with a model served by Ollama's /api/generate.
type OllamaNamer struct {
	BaseURL   string
	ModelName string // model name, e.g. "qwen2.5:14b"
	Client    *http.Client
}

// NewOllamaNamer returns an OllamaNamer with defaults filled in.
func NewOllamaNamer(baseURL, model string) *OllamaNamer {
	if baseURL == "" {
		baseURL = embedding.DefaultOllamaURL
	}
	if model == "" {
		model = DefaultNameModel
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	return &OllamaNamer{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		ModelName: model,
		Client:    &http.Client{Timeout: 5 * time.Minute},
	}
}

func (o *OllamaNamer) Model() string { return o.ModelName }

// Book titles are withheld from the prompt so the model names the idea
// rather than the book.
const (
	themePrompt = "These passages were highlighted by one reader. Name the idea they share in 2 to 5 English words, in sentence case. " +
		"Name the idea, not a book or author. If they share no idea, answer exactly: " + noTheme + ". " +
		"Answer with the name only.\n\n"
	areaPrompt = "These passages were highlighted by one reader. Name the broad subject they share in 1 to 4 English words, in sentence case. " +
		"Name the subject, not a book or author. Answer with the name only.\n\n"
	// passageChars caps each passage in the prompt.
	passageChars = 300
)

func (o *OllamaNamer) Name(ctx context.Context, passages []string, broad bool) (string, error) {
	var b strings.Builder
	if broad {
		b.WriteString(areaPrompt)
	} else {
		b.WriteString(themePrompt)
	}
	for _, p := range passages {
		fmt.Fprintf(&b, "- %s\n", truncate(strings.Join(strings.Fields(p), " "), passageChars))
	}
	body, err := json.Marshal(map[string]any{
		"model":   o.ModelName,
		"prompt":  b.String(),
		"stream":  false,
		"options": map[string]any{"temperature": 0},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return "", fmt.Errorf("%w: Ollama is not reachable at %s", ErrUnavailable, o.BaseURL)
		}
		return "", fmt.Errorf("ollama generate: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("ollama generate: read response: %w", err)
	}
	var out struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("ollama generate: HTTP %d: %.200s", resp.StatusCode, raw)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound && strings.Contains(out.Error, "not found") {
			return "", fmt.Errorf("%w: model %s is not pulled; run `ollama pull %s`", ErrUnavailable, o.ModelName, o.ModelName)
		}
		return "", fmt.Errorf("ollama generate: HTTP %d: %s", resp.StatusCode, out.Error)
	}
	return cleanName(out.Response), nil
}

// cleanName tidies a model's answer into a display name: one line, no
// quotes or final period, sentence case, at most store.MaxThemeName
// characters. The decline answer becomes "".
func cleanName(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	// Qwen models sometimes drift into Chinese mid-answer.
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Han, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), `"'“”‘’*.`)
	s = strings.Join(strings.Fields(s), " ")
	if strings.EqualFold(s, noTheme) || s == "" {
		return ""
	}
	if s == strings.ToUpper(s) {
		s = strings.ToLower(s)
	}
	s = sentenceCase(s)
	r, size := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[size:]
	return truncate(s, store.MaxThemeName)
}

// sentenceCase lowercases the later words of a title-cased name ("Risks
// And Markets"). A name is title-cased when every word but small function
// words is capitalized; otherwise its capitals are taken as proper nouns
// and kept. All-caps words (acronyms) are kept either way.
func sentenceCase(s string) string {
	words := strings.Fields(s)
	content := 0
	for _, w := range words {
		if minor[strings.ToLower(w)] {
			continue
		}
		content++
		if r, _ := utf8.DecodeRuneInString(w); !unicode.IsUpper(r) {
			return s
		}
	}
	if content < 2 {
		return s
	}
	for i, w := range words[1:] {
		if w != strings.ToUpper(w) || minor[strings.ToLower(w)] {
			words[i+1] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}

// minor lists words title case leaves lowercase.
var minor = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "but": true, "by": true,
	"for": true, "from": true, "in": true, "into": true, "nor": true, "of": true, "on": true,
	"or": true, "the": true, "to": true, "vs": true, "with": true,
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
