package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/search"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/spf13/cobra"
)

// The read commands print text for people by default and JSON with --json.
// Both carry the same fields; the JSON shapes are documented in jsonHelp.

// highlightJSON is a highlight in --json output.
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
With --json, a highlight is an object:

  {"id": 412, "text": "...", "book_id": 7, "title": "...", "author": "...",
   "location": "page 138, loc 2452-2456", "highlighted_at": "2022-03-07T01:58:16Z",
   "favorite": false, "buried": false, "note_count": 1}

highlighted_at is null when the device didn't record a time.

With --notes (always, for show), each highlight that has notes adds them:

  "notes": [{"id": 3, "source": "user", "body": "...", "created_at": "2026-10-24T18:00:00Z"}]

source is "user" for notes written in the web app, or "kindle" or
"crosspoint" for notes made on the e-reader.`

func addNotesFlag(cmd *cobra.Command, v *bool) {
	cmd.Flags().BoolVar(v, "notes", false, "include each highlight's notes in full")
}

// attachNotes loads every highlight's notes. Highlights without notes get
// an empty list, so text output knows not to print a count.
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

func addJSONFlag(cmd *cobra.Command, v *bool) {
	cmd.Flags().BoolVar(v, "json", false, "print JSON instead of text")
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

// printHighlight prints one highlight as text: a header line, the full
// passage, then any notes and search reasons.
func printHighlight(w io.Writer, h highlightJSON) {
	head := []string{"#" + strconv.FormatInt(h.ID, 10), h.Title}
	if h.Author != "" {
		head = append(head, h.Author)
	}
	if h.Location != "" {
		head = append(head, h.Location)
	}
	if h.HighlightedAt != nil {
		head = append(head, h.HighlightedAt.Local().Format(time.DateOnly))
	}
	if h.Favorite {
		head = append(head, "favorite")
	}
	if h.Buried {
		head = append(head, "buried")
	}
	if h.NoteCount > 0 && h.Notes == nil {
		head = append(head, fmt.Sprintf("%d %s", h.NoteCount, plural(h.NoteCount, "note", "notes")))
	}
	if h.Score != nil {
		head = append(head, fmt.Sprintf("similarity %.2f", *h.Score))
	}
	fmt.Fprintln(w, strings.Join(head, " · "))
	fmt.Fprintln(w, h.Text)
	for _, n := range h.Notes {
		fmt.Fprintf(w, "Note #%d (%s, %s): %s\n", n.ID, n.Source, n.CreatedAt.Local().Format(time.DateOnly), n.Body)
	}
	if h.NoteMatch != "" && h.Notes == nil {
		fmt.Fprintf(w, "Note match: %s\n", h.NoteMatch)
	}
	if len(h.Why) > 0 {
		fmt.Fprintf(w, "Matched: %s\n", strings.Join(h.Why, ", "))
	}
}

func printHighlights(w io.Writer, hs []highlightJSON) {
	for i, h := range hs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		printHighlight(w, h)
	}
}

// printPage prints a page of results and how to get the next one.
func printPage(w io.Writer, p pageJSON, offset int) {
	if p.Fallback != "" {
		fmt.Fprintf(w, "Matched words only: %s.\n\n", p.Fallback)
	}
	if len(p.Results) == 0 {
		fmt.Fprintf(w, "No highlights (%d total).\n", p.Total)
		return
	}
	printHighlights(w, p.Results)
	fmt.Fprintf(w, "\nShowing %d-%d of %d.", offset+1, offset+len(p.Results), p.Total)
	if p.NextOffset != nil {
		fmt.Fprintf(w, " Next page: --offset %d", *p.NextOffset)
	}
	fmt.Fprintln(w)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
