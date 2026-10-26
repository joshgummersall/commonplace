package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/autotag"
	"github.com/joshgummersall/commonplace/internal/store"
)

type tagsPage struct {
	page
	Tags []store.Tag
	// Unconfirmed counts highlights with a suggestion awaiting
	// confirmation.
	Unconfirmed int
	Untagged    int
	Seed        seedView
}

// seedView is everything the panel that suggests a first set of tags
// says.
type seedView struct {
	// Enabled is false when the server has no embedder or namer.
	Enabled   bool
	NameModel string
	autotag.Status
	Now time.Time
}

func (s *Server) seedView() seedView {
	v := seedView{Now: s.now()}
	if s.seeder != nil {
		v.Enabled = true
		v.NameModel = s.seeder.NameModel()
		v.Status = s.seeder.Status()
	}
	return v
}

func (s *Server) tagList(w http.ResponseWriter, r *http.Request) error {
	data := tagsPage{page: page{Title: "Tags", Nav: "tags"}, Seed: s.seedView()}
	var err error
	if data.Tags, err = s.store.Tags(); err != nil {
		return err
	}
	if data.Unconfirmed, err = s.store.UnconfirmedCount(); err != nil {
		return err
	}
	if data.Untagged, err = s.store.UntaggedCount(); err != nil {
		return err
	}
	return s.render(w, "tags.html", data)
}

// startSeed starts suggesting a first set of tags, or reports the run
// already going.
func (s *Server) startSeed(w http.ResponseWriter, r *http.Request) error {
	if s.seeder == nil {
		return badRequest("Suggesting tags needs Ollama; start it and restart cmpl serve.")
	}
	s.seeder.Start()
	return s.render(w, "seed-status", s.seedView())
}

// seedStatus is polled while seeding runs. Once it ends, the page reloads
// to show the new tags and how the run went.
func (s *Server) seedStatus(w http.ResponseWriter, r *http.Request) error {
	v := s.seedView()
	if !v.Running {
		w.Header().Set("HX-Refresh", "true")
	}
	return s.render(w, "seed-status", v)
}

func (s *Server) loadTag(r *http.Request) (store.Tag, error) {
	id, err := pathID(r)
	if err != nil {
		return store.Tag{}, err
	}
	return s.store.Tag(id)
}

func (s *Server) tagRow(w http.ResponseWriter, r *http.Request) error {
	t, err := s.loadTag(r)
	if err != nil {
		return err
	}
	return s.render(w, "tag-row", t)
}

type renameData struct {
	store.Tag
	Draft string
	Error string
}

func (s *Server) renameForm(w http.ResponseWriter, r *http.Request) error {
	t, err := s.loadTag(r)
	if err != nil {
		return err
	}
	return s.render(w, "tag-rename", renameData{Tag: t, Draft: t.Name})
}

// renameTag renames a tag, or merges it into the tag it was renamed to,
// in which case the page reloads since the row is gone.
func (s *Server) renameTag(w http.ResponseWriter, r *http.Request) error {
	t, err := s.loadTag(r)
	if err != nil {
		return err
	}
	name := r.FormValue("name")
	id, err := s.store.RenameTag(t.ID, name)
	if errors.Is(err, store.ErrInvalid) {
		// Show the problem in the form rather than losing the draft.
		return s.render(w, "tag-rename", renameData{Tag: t, Draft: name, Error: err.Error()})
	}
	if err != nil {
		return err
	}
	if id != t.ID {
		w.Header().Set("HX-Refresh", "true")
	}
	if t, err = s.store.Tag(id); err != nil {
		return err
	}
	return s.render(w, "tag-row", t)
}

type mergeData struct {
	store.Tag
	// Others lists the tags it can merge into.
	Others []store.Tag
	Error  string
}

func (s *Server) mergeForm(w http.ResponseWriter, r *http.Request) error {
	t, err := s.loadTag(r)
	if err != nil {
		return err
	}
	return s.renderMerge(w, t, "")
}

func (s *Server) renderMerge(w http.ResponseWriter, t store.Tag, problem string) error {
	all, err := s.store.Tags()
	if err != nil {
		return err
	}
	data := mergeData{Tag: t, Error: problem}
	for _, o := range all {
		if o.ID != t.ID {
			data.Others = append(data.Others, o)
		}
	}
	return s.render(w, "tag-merge", data)
}

// mergeTag merges a tag into another, then reloads the page, since two
// rows change.
func (s *Server) mergeTag(w http.ResponseWriter, r *http.Request) error {
	t, err := s.loadTag(r)
	if err != nil {
		return err
	}
	into, err := strconv.ParseInt(r.FormValue("into"), 10, 64)
	if err != nil {
		return s.renderMerge(w, t, "Pick a tag to merge into.")
	}
	if err := s.store.MergeTag(t.ID, into); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			return s.renderMerge(w, t, err.Error())
		}
		return err
	}
	w.Header().Set("HX-Refresh", "true")
	return nil
}

func (s *Server) deleteForm(w http.ResponseWriter, r *http.Request) error {
	t, err := s.loadTag(r)
	if err != nil {
		return err
	}
	return s.render(w, "tag-delete", t)
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) error {
	t, err := s.loadTag(r)
	if err != nil {
		return err
	}
	if err := s.store.DeleteTag(t.ID); err != nil {
		return err
	}
	return s.render(w, "tag-deleted", t)
}

// cardTags is a quote's tags, as the card shows them.
type cardTags struct {
	ID   int64
	Tags []store.TagRef
}

// tagsEdit is the form for typing a quote's tags.
type tagsEdit struct {
	ID    int64
	Draft string
	Error string
	// Names lists every tag, as JSON, for completion.
	Names string
}

func (s *Server) loadCardTags(r *http.Request) (cardTags, error) {
	id, err := pathID(r)
	if err != nil {
		return cardTags{}, err
	}
	it, err := s.store.Item(id, false)
	if err != nil {
		return cardTags{}, err
	}
	return cardTags{ID: id, Tags: it.Tags}, nil
}

func (s *Server) highlightTags(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCardTags(r)
	if err != nil {
		return err
	}
	return s.render(w, "card-tags", c)
}

func (s *Server) editTagsForm(id int64, draft, problem string) (tagsEdit, error) {
	tags, err := s.store.Tags()
	if err != nil {
		return tagsEdit{}, err
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	b, err := json.Marshal(names)
	return tagsEdit{ID: id, Draft: draft, Error: problem, Names: string(b)}, err
}

func (s *Server) editTags(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCardTags(r)
	if err != nil {
		return err
	}
	names := make([]string, len(c.Tags))
	for i, t := range c.Tags {
		names[i] = t.Name
	}
	draft := strings.Join(names, ", ")
	if draft != "" {
		draft += ", "
	}
	data, err := s.editTagsForm(c.ID, draft, "")
	if err != nil {
		return err
	}
	return s.render(w, "tags-edit", data)
}

// splitTags splits typed tags on commas, dropping empty ones.
func splitTags(typed string) []string {
	var out []string
	for _, name := range strings.Split(typed, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// saveTags makes the typed tags the quote's tags; see store.SetTags.
func (s *Server) saveTags(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	typed := r.FormValue("tags")
	if err := s.store.SetTags(id, splitTags(typed)); err != nil {
		if !errors.Is(err, store.ErrInvalid) {
			return err
		}
		data, ferr := s.editTagsForm(id, typed, err.Error())
		if ferr != nil {
			return ferr
		}
		return s.render(w, "tags-edit", data)
	}
	return s.highlightTags(w, r)
}

func (s *Server) confirmTags(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.store.ConfirmTags(id); err != nil {
		return err
	}
	return s.highlightTags(w, r)
}
