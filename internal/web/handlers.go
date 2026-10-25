package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/joshgummersall/commonplace/internal/daily"
	"github.com/joshgummersall/commonplace/internal/search"
	"github.com/joshgummersall/commonplace/internal/store"
)

// card is a highlight as the card template shows it.
type card struct {
	store.Entry
	Notes int
	// Snippet, NoteSnippet, and Why are set on search results.
	Snippet     []search.Segment
	NoteSnippet []search.Segment
	// Why says why a search listed this highlight.
	Why []string
	// Detail is set on the quote page, where the notes are already shown
	// below the card.
	Detail bool
}

func (s *Server) cards(entries []store.Entry) ([]card, error) {
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	notes, err := s.store.NoteCounts(ids)
	if err != nil {
		return nil, err
	}
	out := make([]card, len(entries))
	for i, e := range entries {
		out[i] = card{Entry: e, Notes: notes[e.ID]}
	}
	return out, nil
}

func (s *Server) card(id int64) (card, error) {
	e, err := s.store.Entry(id)
	if err != nil {
		return card{}, err
	}
	cs, err := s.cards([]store.Entry{e})
	if err != nil {
		return card{}, err
	}
	return cs[0], nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpError{http.StatusNotFound, "Not found"}
	}
	return id, nil
}

type page struct {
	Title string
	Nav   string // which nav link is current
}

type dailyPage struct {
	page
	Date  string
	Cards []card
}

func (s *Server) daily(w http.ResponseWriter, r *http.Request) error {
	now := s.now()
	entries, err := daily.Today(s.store, now, daily.DefaultSize)
	if err != nil {
		return err
	}
	cards, err := s.cards(entries)
	if err != nil {
		return err
	}
	if err := s.render(w, "daily.html", dailyPage{
		page:  page{Title: "Daily", Nav: "daily"},
		Date:  now.Format("Monday, January 2"),
		Cards: cards,
	}); err != nil {
		return err
	}
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	return daily.MarkSeen(s.store, ids, now)
}

type searchPage struct {
	page
	Query   search.Query
	Books   []store.BookCount
	Results []card
	// Total counts every match. When meaning-based matching ran, Words
	// and Meaning split it into word matches and meaning-only matches.
	Total    int
	Semantic bool
	Words    int
	Meaning  int
	// Next is the URL of the next page of results, or "" on the last.
	Next string
	// Fallback explains why meaning-based matching didn't run, so the
	// results are keyword matches only.
	Fallback string
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) error {
	q := search.Query{Text: r.FormValue("q"), Favorites: r.FormValue("fav") == "1"}
	if b := r.FormValue("book"); b != "" {
		id, err := strconv.ParseInt(b, 10, 64)
		if err != nil {
			return badRequest("bad book id %q", b)
		}
		q.BookID = id
	}
	// Later pages are only fetched by the Load more button; a full page
	// load always starts from the top.
	if o := r.FormValue("offset"); o != "" && isHTMX(r) {
		n, err := strconv.Atoi(o)
		if err != nil || n < 0 {
			return badRequest("bad offset %q", o)
		}
		q.Offset = n
	}
	data := searchPage{
		page:  page{Title: "Search", Nav: "search"},
		Query: q,
	}
	// Favorites is Search with the filter on, but gets its own nav link.
	if q.Favorites {
		data.page = page{Title: "Favorites", Nav: "favorites"}
	}
	// With no words or filters, every highlight is listed, newest first.
	if err := s.searchResults(r, &data); err != nil {
		return err
	}
	switch {
	case isHTMX(r) && q.Offset > 0:
		return s.render(w, "results-page", data)
	case isHTMX(r):
		return s.render(w, "results", data)
	}
	var err error
	if data.Books, err = s.store.Books(); err != nil {
		return err
	}
	return s.render(w, "search.html", data)
}

// searchResults fills in one page of results and the link to the next.
// The app always searches in hybrid mode; the other modes exist for
// cmpl eval to compare against.
func (s *Server) searchResults(r *http.Request, data *searchPage) error {
	q := data.Query
	p, err := s.searcher.Run(r.Context(), q, search.ModeHybrid)
	if err != nil {
		return err
	}
	data.Total, data.Fallback = p.Total, p.Fallback
	data.Semantic, data.Words, data.Meaning = p.Semantic, p.WordMatches, p.MeaningOnly
	if p.More {
		next := url.Values{"offset": {strconv.Itoa(q.Offset + search.PageSize)}}
		if q.Text != "" {
			next.Set("q", q.Text)
		}
		if q.BookID != 0 {
			next.Set("book", strconv.FormatInt(q.BookID, 10))
		}
		if q.Favorites {
			next.Set("fav", "1")
		}
		data.Next = "/search?" + next.Encode()
	}
	entries := make([]store.Entry, len(p.Results))
	for i, res := range p.Results {
		entries[i] = res.Entry
	}
	cards, err := s.cards(entries)
	if err != nil {
		return err
	}
	for i, res := range p.Results {
		cards[i].Snippet = res.Snippet
		cards[i].NoteSnippet = res.NoteSnippet
		cards[i].Why = why(res)
	}
	data.Results = cards
	return nil
}

var fieldReasons = map[search.Field]string{
	search.FieldText:   "Words in the passage",
	search.FieldNotes:  "Words in your note",
	search.FieldTitle:  "Words in the title",
	search.FieldAuthor: "Words in the author",
}

// why explains a search hit in plain terms.
func why(r search.Result) []string {
	var out []string
	for _, f := range r.Matched {
		out = append(out, fieldReasons[f])
	}
	if r.Similar {
		out = append(out, "Similar in meaning")
	}
	return out
}

type highlightPage struct {
	page
	Card  card
	Notes notesData
}

func (s *Server) highlight(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	c, err := s.card(id)
	if err != nil {
		return err
	}
	notes, err := s.store.Notes(id)
	if err != nil {
		return err
	}
	c.Detail = true
	return s.render(w, "highlight.html", highlightPage{
		page:  page{Title: c.BookTitle},
		Card:  c,
		Notes: notesData{HighlightID: id, Notes: notes},
	})
}

// similarK is how many neighbors the Similar panel shows per group.
const similarK = 6

type similarData struct {
	// Ready is false when the highlight has no embedding yet.
	Ready bool
	// Reason says why similar highlights are unavailable.
	Reason     string
	OtherBooks []similarItem
	SameBook   []similarItem
}

type similarItem struct {
	store.Entry
	Score float32
}

// similar renders the Similar panel's contents for a quote page. It is
// fetched after the page loads so a slow or missing embedder never delays
// the quote itself.
func (s *Server) similar(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if _, err := s.store.Entry(id); err != nil {
		return err
	}
	var data similarData
	if s.searcher.Index == nil {
		data.Reason = "Similar highlights need an embedding model; start Ollama and restart cmpl serve."
		return s.render(w, "similar", data)
	}
	other, same, ok, err := s.searcher.Index.Similar(id, similarK)
	if err != nil {
		return err
	}
	if !ok {
		data.Reason = "This highlight isn't embedded yet. Similar highlights appear once Ollama has processed it."
		return s.render(w, "similar", data)
	}
	data.Ready = true
	if data.OtherBooks, err = s.similarItems(other); err != nil {
		return err
	}
	if data.SameBook, err = s.similarItems(same); err != nil {
		return err
	}
	return s.render(w, "similar", data)
}

func (s *Server) similarItems(hits []search.Hit) ([]similarItem, error) {
	ids := make([]int64, len(hits))
	scores := make(map[int64]float32, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
		scores[h.ID] = h.Score
	}
	// Entries skips ids deleted since the index loaded, so match scores
	// by id rather than position.
	entries, err := s.store.Entries(ids)
	if err != nil {
		return nil, err
	}
	out := make([]similarItem, len(entries))
	for i, e := range entries {
		out[i] = similarItem{Entry: e, Score: scores[e.ID]}
	}
	return out, nil
}

func (s *Server) favorite(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	c, err := s.card(id)
	if err != nil {
		return err
	}
	if err := s.store.SetFavorite(id, !c.Favorite); err != nil {
		return err
	}
	c.Favorite = !c.Favorite
	return s.render(w, "favorite", c)
}

func (s *Server) bury(w http.ResponseWriter, r *http.Request) error {
	return s.setBuried(w, r, true)
}

func (s *Server) unbury(w http.ResponseWriter, r *http.Request) error {
	return s.setBuried(w, r, false)
}

func (s *Server) setBuried(w http.ResponseWriter, r *http.Request, buried bool) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.store.SetBuried(id, buried); err != nil {
		return err
	}
	c, err := s.card(id)
	if err != nil {
		return err
	}
	return s.render(w, "card", c)
}

type notesData struct {
	HighlightID int64
	Notes       []store.Note
	Error       string
	Draft       string
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if _, err := s.store.Entry(id); err != nil {
		return err
	}
	data := notesData{HighlightID: id}
	if _, err := s.store.AddNote(id, r.FormValue("body"), s.now()); err != nil {
		// Re-render the form with the problem rather than losing the draft.
		data.Error = err.Error()
		data.Draft = r.FormValue("body")
	}
	if data.Notes, err = s.store.Notes(id); err != nil {
		return err
	}
	return s.render(w, "notes", data)
}

func (s *Server) noteByID(r *http.Request) (store.Note, error) {
	id, err := pathID(r)
	if err != nil {
		return store.Note{}, err
	}
	return s.store.Note(id)
}

func (s *Server) note(w http.ResponseWriter, r *http.Request) error {
	n, err := s.noteByID(r)
	if err != nil {
		return err
	}
	return s.render(w, "note", n)
}

func (s *Server) editNote(w http.ResponseWriter, r *http.Request) error {
	n, err := s.noteByID(r)
	if err != nil {
		return err
	}
	return s.render(w, "note-edit", n)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) error {
	n, err := s.noteByID(r)
	if err != nil {
		return err
	}
	if err := s.store.UpdateNote(n.ID, r.FormValue("body"), s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return err
		}
		return badRequest("%s", err)
	}
	if n, err = s.store.Note(n.ID); err != nil {
		return err
	}
	return s.render(w, "note", n)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) error {
	n, err := s.noteByID(r)
	if err != nil {
		return err
	}
	if err := s.store.DeleteNote(n.ID, s.now()); err != nil {
		return err
	}
	return s.render(w, "note-deleted", n)
}

// restoreNote undoes a delete and re-renders the notes section, since the
// deleted note's element is gone.
func (s *Server) restoreNote(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	hid, err := s.store.RestoreNote(id)
	if err != nil {
		return err
	}
	notes, err := s.store.Notes(hid)
	if err != nil {
		return err
	}
	return s.render(w, "notes", notesData{HighlightID: hid, Notes: notes})
}
