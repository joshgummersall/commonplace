package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/spf13/cobra"
)

// addResultJSON is the output of cmpl add.
type addResultJSON struct {
	Status    string        `json:"status"`
	Highlight highlightJSON `json:"highlight"`
}

func (a *App) newAddCommand() *cobra.Command {
	var (
		title, author, page, date, note string
		tags                            []string
	)
	cmd := &cobra.Command{
		Use:   "add [text]",
		Short: "Add one highlight or quote by hand",
		Long: `Add one highlight or quote that didn't come from an e-reader: a passage
from a paper book, an article, a talk, or a conversation.

The text is the argument, or stdin when the argument is "-" or missing,
which suits long or multi-line passages. Surrounding whitespace is
trimmed; line breaks inside are kept. --title names the book or other
work it comes from and is required; --author is optional, as on import.

Adding is safe to repeat. Text already stored for the same title and
author (ignoring case, spacing, and curly quotes) is not added again:
--note and --tag still apply to the stored highlight, and the same note
is never added twice. A longer or shorter version of a stored hand-added
highlight from the same title, author, and page replaces it, keeping its
notes, tags, and favorite.

--note adds a note to the highlight, as one written in the web app.
--tag adds a tag (repeat it, or separate names with commas), creating
the tag if new and keeping the highlight's other tags.

The new highlight is then embedded for search by meaning and suggested
tags, as after import; if Ollama isn't running, the highlight is still
stored and cmpl serve embeds it later. Those messages go to stderr.

Output: a JSON object on stdout,

  {"status": "added", "highlight": highlight}

where status is "added", "updated" (an older version was replaced), or
"exists" (already stored), and highlight is the stored highlight as cmpl
show prints it, with notes and tags.
` + jsonHelp,
		Example: `  cmpl add "We are what we repeatedly do." --title "The Story of Philosophy" --author "Will Durant"
  cmpl add "..." --title Meditations --author "Marcus Aurelius" --page 41 --tag stoicism --note "Reread before reviews"
  pbpaste | cmpl add --title "Working in Public" --author "Nadia Eghbal"
  cmpl add - --title "Essay title" <<'EOF'
  A passage with "quotes" and
  line breaks.
  EOF`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := a.addText(args)
			if err != nil {
				return err
			}
			title, author = strings.TrimSpace(title), strings.TrimSpace(author)
			if title == "" {
				return errors.New("--title is required: the book or work the text comes from")
			}
			at := a.now()
			if date != "" {
				if at, err = parseAddDate(date); err != nil {
					return err
				}
			}
			var names []string
			for _, t := range tags {
				for n := range strings.SplitSeq(t, ",") {
					if n = strings.TrimSpace(n); n != "" {
						if _, err := store.CleanTagName(n); err != nil {
							return fmt.Errorf("--tag %q: %w", n, err)
						}
						names = append(names, n)
					}
				}
			}

			c := clippings.Clipping{
				Kind:    clippings.Highlight,
				Dialect: clippings.Dialect(store.UserSource),
				Title:   title,
				Author:  author,
				Page:    strings.TrimSpace(page),
				AddedAt: at,
				Text:    text,
			}
			s, err := a.openStore()
			if err != nil {
				return err
			}
			id, r, err := importer.Add(s, c, a.now())
			if err != nil {
				return err
			}
			status := "exists"
			switch {
			case r.Added > 0:
				status = "added"
			case r.Replaced > 0:
				status = "updated"
			}

			if note = strings.TrimSpace(note); note != "" {
				if err := a.addNoteOnce(s, id, note); err != nil {
					return err
				}
			}
			if len(names) > 0 {
				if err := s.AddTags(id, names); err != nil {
					return err
				}
			}
			if status != "exists" {
				a.embedAfterImport(cmd.Context(), s, a.stderr)
			}

			entry, err := s.Entry(id)
			if err != nil {
				return err
			}
			hs, err := toJSON(s, []store.Entry{entry}, true)
			if err != nil {
				return err
			}
			return writeJSON(a.stdout, addResultJSON{Status: status, Highlight: hs[0]})
		},
	}
	cmd.Flags().StringVarP(&title, "title", "t", "", "book or work the text comes from (required)")
	cmd.Flags().StringVarP(&author, "author", "a", "", "its author")
	cmd.Flags().StringVarP(&page, "page", "p", "", "page number, as written (\"138\", \"xvi\")")
	cmd.Flags().StringVar(&date, "date", "", "when it was highlighted, as 2026-11-19 or RFC 3339 (default now)")
	cmd.Flags().StringVarP(&note, "note", "n", "", "add a note to the highlight")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "add a tag; repeat or comma-separate for more")
	return cmd
}

// addText reads the highlight's text from the argument, or from stdin when
// the argument is "-" or missing.
func (a *App) addText(args []string) (string, error) {
	var text string
	if len(args) == 1 && args[0] != "-" {
		text = args[0]
	} else {
		if a.stdin == nil || isTerminal(a.stdin) {
			return "", errors.New("no text: give it as an argument, or pipe it on stdin")
		}
		b, err := io.ReadAll(a.stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		text = string(b)
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return "", errors.New("the text is empty")
	}
	return text, nil
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// addNoteOnce adds a user note unless the highlight already has a live
// note with the same text, so repeating cmpl add doesn't duplicate it.
func (a *App) addNoteOnce(s *store.Store, id int64, body string) error {
	notes, err := s.Notes(id)
	if err != nil {
		return err
	}
	for _, n := range notes {
		if n.Body == body {
			return nil
		}
	}
	_, err = s.AddNote(id, body, a.now())
	return err
}

func parseAddDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--date %q: want a date like 2026-11-19 or 2026-11-19T21:30:00-08:00", s)
}
