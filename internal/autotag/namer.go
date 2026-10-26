package autotag

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/joshgummersall/commonplace/internal/ollama"
	"github.com/joshgummersall/commonplace/internal/store"
)

// Namer names a group of highlights from its most typical passages. An
// empty name drops the group.
type Namer interface {
	Model() string
	Name(ctx context.Context, passages []string) (string, error)
}

// DefaultNameModel is the Ollama model that names suggested tags unless
// configured otherwise.
const DefaultNameModel = "qwen2.5:14b"

// ErrUnavailable means the naming model could not be reached.
var ErrUnavailable = ollama.ErrUnavailable

// OllamaNamer names groups with a model served by Ollama's /api/generate.
type OllamaNamer struct {
	ollama.Client
	ModelName string // model name, e.g. "qwen2.5:14b"
}

// NewOllamaNamer returns an OllamaNamer with defaults filled in.
func NewOllamaNamer(baseURL, model string) *OllamaNamer {
	if model == "" {
		model = DefaultNameModel
	}
	return &OllamaNamer{Client: ollama.New(baseURL, 5*time.Minute), ModelName: model}
}

func (o *OllamaNamer) Model() string { return o.ModelName }

// Book titles are withheld from the prompt so the model names the
// subject rather than the book.
const (
	prompt = "These passages were highlighted by one reader. Name the broad subject they share in 1 to 4 English words, in sentence case. " +
		"Name the subject, not a book or author. Answer with the name only.\n\n"
	// passageChars caps each passage in the prompt.
	passageChars = 300
)

func (o *OllamaNamer) Name(ctx context.Context, passages []string) (string, error) {
	var b strings.Builder
	b.WriteString(prompt)
	for _, p := range passages {
		fmt.Fprintf(&b, "- %s\n", truncate(strings.Join(strings.Fields(p), " "), passageChars))
	}
	req := map[string]any{
		"model":   o.ModelName,
		"prompt":  b.String(),
		"stream":  false,
		"options": map[string]any{"temperature": 0},
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := o.Post(ctx, "/api/generate", o.ModelName, req, &out); err != nil {
		return "", err
	}
	return cleanName(out.Response), nil
}

// cleanName tidies a model's answer into a display name: one line, no
// quotes, commas, or final period, sentence case, at most
// store.MaxTagName characters.
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
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, ",", " ")), " ")
	if s == "" {
		return ""
	}
	if s == strings.ToUpper(s) {
		s = strings.ToLower(s)
	}
	s = sentenceCase(s)
	r, size := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[size:]
	return truncate(s, store.MaxTagName)
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
