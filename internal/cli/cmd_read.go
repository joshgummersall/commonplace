package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/daily"
	"github.com/joshgummersall/commonplace/internal/search"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/spf13/cobra"
)

// defaultLimit is the page size for list and search. Smaller than the web
// app's, so a page fits comfortably in an agent's context.
const defaultLimit = 20

func (a *App) newDailyCommand() *cobra.Command {
	var withNotes bool
	cmd := &cobra.Command{
		Use:   "daily",
		Short: "Print today's daily highlights",
		Long: `Print today's daily shuffle: about ten highlights, the same set the web
app's Daily page shows.

The set is chosen on the first request of the day (from here or the web
app) and stays fixed for that day. Highlights not seen for longest come
up first, and favorites come up more often. Buried highlights are left
out, and no two in a set make nearly the same point. Printing the set
here does not count as reading it, so it does not change which
highlights come up on later days.
` + jsonHelp + `

The JSON output is an array of highlights.`,
		Example: `  cmpl daily
  cmpl daily --notes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			entries, err := daily.Today(s, a.now(), daily.DefaultSize, a.embedder().Model())
			if err != nil {
				return err
			}
			hs, err := toJSON(s, entries, withNotes)
			if err != nil {
				return err
			}
			return writeJSON(a.stdout, nonNil(hs))
		},
	}
	addNotesFlag(cmd, &withNotes)
	return cmd
}

// pageFlags are the filters and paging that list and search share.
type pageFlags struct {
	book      string
	theme     string
	favorites bool
	limit     int
	offset    int
	notes     bool
}

func (f *pageFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.book, "book", "", "only this book: its id, or part of its title (see cmpl books)")
	cmd.Flags().StringVar(&f.theme, "theme", "", "only this theme or area: its id, or part of its name (see cmpl themes)")
	cmd.Flags().BoolVar(&f.favorites, "favorites", false, "only favorites")
	cmd.Flags().IntVar(&f.limit, "limit", defaultLimit, "highlights per page")
	cmd.Flags().IntVar(&f.offset, "offset", 0, "highlights to skip, to fetch later pages")
	addNotesFlag(cmd, &f.notes)
}

const pageHelp = `
Results come a page at a time, as an object:

  {"total": 1546, "next_offset": 20, "results": [highlight, ...]}

next_offset is null on the last page.`

func (a *App) newListCommand() *cobra.Command {
	var f pageFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List highlights, newest first",
		Long: `List highlights, newest first, optionally only one book's, one theme's,
or only favorites. Buried highlights are included and marked. With
--theme, the theme's most typical highlights come first instead.

Use list to browse, for example all of one book's highlights; use search
to find highlights about something.
` + pageHelp + `
` + jsonHelp,
		Example: `  cmpl list
  cmpl list --favorites --notes --limit 50
  cmpl list --book meditations
  cmpl list --book 7 --offset 20
  cmpl list --theme 31`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPage(cmd, "", f)
		},
	}
	f.add(cmd)
	return cmd
}

func (a *App) newSearchCommand() *cobra.Command {
	var f pageFlags
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search highlights by words and meaning",
		Long: `Search highlights, notes, book titles, and authors. Matches the query's
words and its meaning together, best first, the same way as the web app.

Write the query as words or as an idea ("letting go of control"). Put an
exact phrase in double quotes inside the query. Each result says why it
matched: words in the passage, a note, the title, or the author, or
similar in meaning.

Matching by meaning needs Ollama running with the embedding model (see
cmpl serve). Without it, search matches words only and says why in
"fallback".
` + pageHelp + `
` + jsonHelp + `

Search results add "why": a list of where the query matched. When the
query's words are in a note, "note_match" holds that part of the note;
pass --notes for whole notes.`,
		Example: `  cmpl search stoicism
  cmpl search 'fear of death' --limit 5
  cmpl search '"the obstacle is the way"'
  cmpl search attention --book 7 --favorites`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPage(cmd, strings.Join(args, " "), f)
		},
	}
	f.add(cmd)
	return cmd
}

// runPage runs list (empty text) or search.
func (a *App) runPage(cmd *cobra.Command, text string, f pageFlags) error {
	if f.limit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	if f.offset < 0 {
		return fmt.Errorf("--offset must be 0 or more")
	}
	s, err := a.openStore()
	if err != nil {
		return err
	}
	q := search.Query{Text: text, Favorites: f.favorites, Limit: f.limit, Offset: f.offset}
	if f.book != "" {
		if q.BookID, err = resolveBook(s, f.book); err != nil {
			return err
		}
	}
	if f.theme != "" {
		if q.ThemeID, err = resolveTheme(s, f.theme); err != nil {
			return err
		}
	}
	p, err := search.New(s, a.embedder()).Run(cmd.Context(), q, search.ModeHybrid)
	if err != nil {
		return err
	}
	entries := make([]store.Entry, len(p.Results))
	for i, r := range p.Results {
		entries[i] = r.Entry
	}
	hs, err := toJSON(s, entries, f.notes)
	if err != nil {
		return err
	}
	if text != "" {
		for i, r := range p.Results {
			hs[i].Why = r.Why()
			hs[i].NoteMatch = segmentText(r.NoteSnippet)
		}
	}
	out := pageJSON{Total: p.Total, Results: nonNil(hs), Fallback: p.Fallback}
	if p.More {
		next := f.offset + len(hs)
		out.NextOffset = &next
	}
	return writeJSON(a.stdout, out)
}

// resolveTheme turns --theme into a theme or area id (see resolve).
func resolveTheme(s *store.Store, arg string) (int64, error) {
	areas, err := s.ThemeTree()
	if err != nil {
		return 0, err
	}
	var all []named
	for _, a := range areas {
		all = append(all, named{a.ID, a.Name})
		for _, t := range a.Themes {
			all = append(all, named{t.ID, t.Name})
		}
	}
	if len(all) == 0 {
		return 0, fmt.Errorf("there are no themes yet; refresh them on the web app's Themes page")
	}
	return resolve(arg, "theme", "name", all)
}

// resolveBook turns --book into a book id (see resolve).
func resolveBook(s *store.Store, arg string) (int64, error) {
	books, err := s.Books()
	if err != nil {
		return 0, err
	}
	all := make([]named, len(books))
	for i, b := range books {
		all[i] = named{b.ID, b.Title}
	}
	return resolve(arg, "book", "title", all)
}

// named is a book or theme as --book and --theme match it.
type named struct {
	id   int64
	name string
}

// resolve turns a --book or --theme value into an id: a number is an id;
// anything else must match exactly one name, ignoring case, either whole
// or as a part. noun is "book" or "theme", and field what its name is
// called in messages.
func resolve(arg, noun, field string, all []named) (int64, error) {
	list := "cmpl " + noun + "s"
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, n := range all {
			if n.id == id {
				return id, nil
			}
		}
		return 0, fmt.Errorf("no %s with id %d; run %s to list them", noun, id, list)
	}
	want := strings.ToLower(arg)
	var matches []named
	for _, n := range all {
		name := strings.ToLower(n.name)
		if name == want {
			return n.id, nil
		}
		if strings.Contains(name, want) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("no %s %s contains %q; run %s to list them", noun, field, arg, list)
	case 1:
		return matches[0].id, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d %ss; pass an id instead:", arg, len(matches), noun)
	const shown = 10
	for _, m := range matches[:min(len(matches), shown)] {
		fmt.Fprintf(&b, "\n  %d  %s", m.id, m.name)
	}
	if len(matches) > shown {
		fmt.Fprintf(&b, "\n  and %d more", len(matches)-shown)
	}
	return 0, fmt.Errorf("%s", b.String())
}

func (a *App) newShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>...",
		Short: "Print highlights in full, with their notes",
		Long: `Print one or more highlights by id, with all their notes. Ids come from
the other read commands ("id") and from the
web app's quote URLs (/h/412).
` + jsonHelp + `

The JSON output is an array of highlights, each with its notes.`,
		Example: `  cmpl show 412
  cmpl show 412 977`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			s, err := a.openStore()
			if err != nil {
				return err
			}
			entries, err := s.Entries(ids)
			if err != nil {
				return err
			}
			if len(entries) < len(ids) {
				return fmt.Errorf("no highlight with id %d", missingID(ids, entries))
			}
			hs, err := toJSON(s, entries, true)
			if err != nil {
				return err
			}
			return writeJSON(a.stdout, hs)
		},
	}
	return cmd
}

func (a *App) newSimilarCommand() *cobra.Command {
	var (
		limit     int
		withNotes bool
	)
	cmd := &cobra.Command{
		Use:   "similar <id>",
		Short: "Print highlights similar in meaning to one highlight",
		Long: `Print the highlights closest in meaning to one highlight: the web app's
"More like this". Results come in two groups, other books first, then
the same book, each best first, with a similarity score (higher is
closer, at most 1).

It uses the embeddings stored by import and serve, so Ollama need not be
running. A highlight imported while Ollama was down has no embedding
until cmpl serve or another import embeds it.
` + jsonHelp + `

The JSON output is an object:

  {"other_books": [highlight, ...], "same_book": [highlight, ...]}

Each highlight adds "score".`,
		Example: `  cmpl similar 412
  cmpl similar 412 --limit 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 {
				return fmt.Errorf("--limit must be at least 1")
			}
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			s, err := a.openStore()
			if err != nil {
				return err
			}
			if _, err := s.Entry(ids[0]); err != nil {
				return fmt.Errorf("no highlight with id %d", ids[0])
			}
			other, same, ok, err := search.New(s, a.embedder()).Index.SimilarEntries(ids[0], limit)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("highlight %d has no embedding yet; run cmpl serve with Ollama up to embed it", ids[0])
			}
			out := struct {
				OtherBooks []highlightJSON `json:"other_books"`
				SameBook   []highlightJSON `json:"same_book"`
			}{}
			if out.OtherBooks, err = scored(s, other, withNotes); err != nil {
				return err
			}
			if out.SameBook, err = scored(s, same, withNotes); err != nil {
				return err
			}
			return writeJSON(a.stdout, out)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 6, "highlights per group")
	addNotesFlag(cmd, &withNotes)
	return cmd
}

// scored converts similar highlights, adding their scores.
func scored(s *store.Store, hits []search.Scored, withNotes bool) ([]highlightJSON, error) {
	entries := make([]store.Entry, len(hits))
	for i, h := range hits {
		entries[i] = h.Entry
	}
	hs, err := toJSON(s, entries, withNotes)
	if err != nil {
		return nil, err
	}
	for i := range hs {
		hs[i].Score = &hits[i].Score
	}
	return nonNil(hs), nil
}

func (a *App) newBooksCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "books",
		Short: "List books with their ids and highlight counts",
		Long: `List every book that has highlights, by title, with its id and how many
highlights it has. Pass an id (or part of a title) to --book on list and
search.

The JSON output is an array:

  [{"id": 7, "title": "Meditations", "author": "Marcus Aurelius", "highlights": 41}]`,
		Example: `  cmpl books`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			books, err := s.Books()
			if err != nil {
				return err
			}
			type bookJSON struct {
				ID         int64  `json:"id"`
				Title      string `json:"title"`
				Author     string `json:"author"`
				Highlights int    `json:"highlights"`
			}
			out := make([]bookJSON, len(books))
			for i, b := range books {
				out[i] = bookJSON{b.ID, b.Title, b.Author, b.Count}
			}
			return writeJSON(a.stdout, out)
		},
	}
	return cmd
}

func (a *App) newThemesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "themes",
		Short: "List themes: groups of highlights that say related things",
		Long: `List themes, the web app's Themes page: groups of highlights that say
related things, usually across books. Broad areas each hold specific
themes. Not every highlight is in one.

Themes are found and named on the web app's Themes page (Refresh
themes), not here, so they cover highlights up to the last refresh.
Pass an id (or part of a name) to --theme on list and search.

The JSON output is an object:

  {"refreshed_at": "2026-10-25T09:00:00Z", "unthemed": 412,
   "areas": [{"id": 3, "name": "...", "highlights": 129, "books": 37,
              "themes": [{"id": 31, "name": "...", "highlights": 44, "books": 14}]}]}

Areas come largest first, and so do the themes in each. refreshed_at is
null and areas is [] before the first refresh. unthemed counts
highlights in no area.`,
		Example: `  cmpl themes
  cmpl list --theme 31`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			tree, err := s.ThemeTree()
			if err != nil {
				return err
			}
			type themeJSON struct {
				ID         int64       `json:"id"`
				Name       string      `json:"name"`
				Highlights int         `json:"highlights"`
				Books      int         `json:"books"`
				Themes     []themeJSON `json:"themes,omitempty"`
			}
			out := struct {
				RefreshedAt *time.Time  `json:"refreshed_at"`
				Unthemed    int         `json:"unthemed"`
				Areas       []themeJSON `json:"areas"`
			}{Areas: []themeJSON{}}
			last, ok, err := s.LastThemeRefresh()
			if err != nil {
				return err
			}
			if ok {
				at := last.At.UTC()
				out.RefreshedAt = &at
			}
			if out.Unthemed, err = s.UnthemedCount(); err != nil {
				return err
			}
			for _, a := range tree {
				area := themeJSON{ID: a.ID, Name: a.Name, Highlights: a.Count, Books: a.Books}
				for _, t := range a.Themes {
					area.Themes = append(area.Themes, themeJSON{ID: t.ID, Name: t.Name, Highlights: t.Count, Books: t.Books})
				}
				out.Areas = append(out.Areas, area)
			}
			return writeJSON(a.stdout, out)
		},
	}
	return cmd
}

func parseIDs(args []string) ([]int64, error) {
	ids := make([]int64, len(args))
	for i, arg := range args {
		id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
		if err != nil || id < 1 {
			return nil, fmt.Errorf("%q is not a highlight id; ids are positive numbers like 412", arg)
		}
		ids[i] = id
	}
	return ids, nil
}

func missingID(ids []int64, entries []store.Entry) int64 {
	found := make(map[int64]bool, len(entries))
	for _, e := range entries {
		found[e.ID] = true
	}
	for _, id := range ids {
		if !found[id] {
			return id
		}
	}
	return 0
}

// nonNil makes an empty list encode as [] rather than null.
func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}
