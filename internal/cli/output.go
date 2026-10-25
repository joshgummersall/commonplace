package cli

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/search"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/spf13/cobra"
)

// The read commands print JSON, in the shapes documented in jsonHelp.

// highlightJSON is a highlight in JSON output.
type highlightJSON struct {
	ID            int64      `json:"id"`
	Text          string     `json:"text"`
	BookID        int64      `json:"book_id"`
	Title         string     `json:"title"`
	Author        string     `json:"author"`
	Location      string     `json:"location"`
	HighlightedAt *time.Time `json:"highlighted_at"`
	Favorite      bool       `json:"favorite"`
	Buried        bool       `json:"buried"`
	NoteCount     int        `json:"note_count"`
	// Theme is the most specific theme the highlight is in, if any.
	Theme *themeRefJSON `json:"theme,omitempty"`
	// Notes is set by show, and by the other read commands with --notes.
	Notes []noteJSON `json:"notes,omitempty"`
	// Why is set by search: where the query matched.
	Why []string `json:"why,omitempty"`
	// NoteMatch is set by search when the query's words were found in a
	// note: the part of the note around them.
	NoteMatch string `json:"note_match,omitempty"`
	// Score is set by similar: cosine similarity, higher is closer.
	Score *float32 `json:"score,omitempty"`
}

type themeRefJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type noteJSON struct {
	ID        int64     `json:"id"`
	Source    string    `json:"source"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// pageJSON is a page of list or search results.
type pageJSON struct {
	Total int `json:"total"`
	// NextOffset is the --offset of the next page, or null on the last.
	NextOffset *int            `json:"next_offset"`
	Results    []highlightJSON `json:"results"`
	// Fallback says why search matched words only.
	Fallback string `json:"fallback,omitempty"`
}

// jsonHelp is appended to the help of each read command.
const jsonHelp = `
A highlight is an object:

  {"id": 412, "text": "...", "book_id": 7, "title": "...", "author": "...",
   "location": "page 138, loc 2452-2456", "highlighted_at": "2022-03-07T01:58:16Z",
   "favorite": false, "buried": false, "note_count": 1}

highlighted_at is null when the device didn't record a time. A highlight
in a theme adds "theme": {"id": 31, "name": "..."}, its most specific
theme (see cmpl themes).

With --notes (always, for show), each highlight that has notes adds them:

  "notes": [{"id": 3, "source": "user", "body": "...", "created_at": "2026-10-24T18:00:00Z"}]

source is "user" for notes written in the web app, or "kindle" or
"crosspoint" for notes made on the e-reader.`

func addNotesFlag(cmd *cobra.Command, v *bool) {
	cmd.Flags().BoolVar(v, "notes", false, "include each highlight's notes in full")
}

// attachNotes loads every highlight's notes.
func attachNotes(s *store.Store, hs []highlightJSON) error {
	for i := range hs {
		notes, err := s.Notes(hs[i].ID)
		if err != nil {
			return err
		}
		hs[i].Notes = make([]noteJSON, len(notes))
		for j, n := range notes {
			hs[i].Notes[j] = noteJSON{ID: n.ID, Source: n.Source, Body: n.Body, CreatedAt: n.CreatedAt.UTC()}
		}
	}
	return nil
}

// segmentText joins a snippet's segments into plain text.
func segmentText(segs []search.Segment) string {
	var b strings.Builder
	for _, sg := range segs {
		b.WriteString(sg.Text)
	}
	return b.String()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// toJSON converts entries, filling note counts.
func toJSON(s *store.Store, entries []store.Entry) ([]highlightJSON, error) {
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	counts, err := s.NoteCounts(ids)
	if err != nil {
		return nil, err
	}
	themes, err := s.ThemesOf(ids)
	if err != nil {
		return nil, err
	}
	out := make([]highlightJSON, len(entries))
	for i, e := range entries {
		h := highlightJSON{
			ID:        e.ID,
			Text:      e.Text,
			BookID:    e.BookID,
			Title:     e.BookTitle,
			Author:    e.BookAuthor,
			Location:  e.Location,
			Favorite:  e.Favorite,
			Buried:    e.Buried,
			NoteCount: counts[e.ID],
		}
		if t, ok := themes[e.ID]; ok {
			h.Theme = &themeRefJSON{t.ID, t.Name}
		}
		if !e.HighlightedAt.IsZero() {
			t := e.HighlightedAt.UTC()
			h.HighlightedAt = &t
		}
		out[i] = h
	}
	return out, nil
}

var whyText = map[search.Field]string{
	search.FieldText:   "words in the passage",
	search.FieldNotes:  "words in a note",
	search.FieldTitle:  "words in the title",
	search.FieldAuthor: "words in the author",
}

// why explains a search hit, as the web app does.
func why(r search.Result) []string {
	var out []string
	for _, f := range r.Matched {
		out = append(out, whyText[f])
	}
	if r.Similar {
		out = append(out, "similar in meaning")
	}
	return out
}
