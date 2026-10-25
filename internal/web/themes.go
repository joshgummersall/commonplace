package web

import (
	"net/http"
	"time"

	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/joshgummersall/commonplace/internal/themes"
)

// themeRow is an area or theme as a row of the Themes page shows it.
type themeRow struct {
	store.ThemeSummary
	// Children counts an area's themes.
	Children int
}

type areaView struct {
	Row    themeRow
	Themes []themeRow
}

type themesPage struct {
	page
	Areas    []areaView
	Unthemed int
	Status   refreshView
}

// refreshView is everything the refresh panel says.
type refreshView struct {
	// Enabled is false when the server has no embedder or namer.
	Enabled   bool
	NameModel string
	themes.Status
	// Last is the last saved refresh; HasLast is false if none.
	Last    store.ThemeRefresh
	HasLast bool
	// Newer counts highlights imported since the last refresh.
	Newer int
	Now   time.Time
}

func (s *Server) refreshView() (refreshView, error) {
	v := refreshView{Now: s.now()}
	if s.themes != nil {
		v.Enabled = true
		v.NameModel = s.themes.NameModel()
		v.Status = s.themes.Status()
	}
	var err error
	if v.Last, v.HasLast, err = s.store.LastThemeRefresh(); err != nil {
		return v, err
	}
	if v.HasLast {
		if v.Newer, err = s.store.CountImportedSince(v.Last.At); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (s *Server) themeList(w http.ResponseWriter, r *http.Request) error {
	tree, err := s.store.ThemeTree()
	if err != nil {
		return err
	}
	data := themesPage{page: page{Title: "Themes", Nav: "themes"}}
	for _, a := range tree {
		v := areaView{Row: themeRow{ThemeSummary: a, Children: len(a.Themes)}}
		for _, t := range a.Themes {
			v.Themes = append(v.Themes, themeRow{ThemeSummary: t})
		}
		data.Areas = append(data.Areas, v)
	}
	if data.Unthemed, err = s.store.UnthemedCount(); err != nil {
		return err
	}
	if data.Status, err = s.refreshView(); err != nil {
		return err
	}
	return s.render(w, "themes.html", data)
}

// refreshThemes starts a refresh, or reports the one already running.
func (s *Server) refreshThemes(w http.ResponseWriter, r *http.Request) error {
	if s.themes == nil {
		return badRequest("Refreshing themes needs Ollama; start it and restart cmpl serve.")
	}
	s.themes.Start()
	v, err := s.refreshView()
	if err != nil {
		return err
	}
	return s.render(w, "refresh-status", v)
}

// refreshStatus is polled while a refresh runs. Once it ends, the page
// reloads to show the new themes and how the refresh went.
func (s *Server) refreshStatus(w http.ResponseWriter, r *http.Request) error {
	v, err := s.refreshView()
	if err != nil {
		return err
	}
	if !v.Running {
		w.Header().Set("HX-Refresh", "true")
	}
	return s.render(w, "refresh-status", v)
}

func (s *Server) loadThemeRow(r *http.Request) (themeRow, error) {
	id, err := pathID(r)
	if err != nil {
		return themeRow{}, err
	}
	t, err := s.store.ThemeSummary(id)
	if err != nil {
		return themeRow{}, err
	}
	row := themeRow{ThemeSummary: t}
	if t.IsArea() {
		children, err := s.store.ThemeChildren(id)
		if err != nil {
			return themeRow{}, err
		}
		row.Children = len(children)
	}
	return row, nil
}

func (s *Server) themeRow(w http.ResponseWriter, r *http.Request) error {
	row, err := s.loadThemeRow(r)
	if err != nil {
		return err
	}
	return s.render(w, "theme-row", row)
}

type renameData struct {
	themeRow
	Draft string
	Error string
}

func (s *Server) renameForm(w http.ResponseWriter, r *http.Request) error {
	row, err := s.loadThemeRow(r)
	if err != nil {
		return err
	}
	return s.render(w, "theme-rename", renameData{themeRow: row, Draft: row.Name})
}

func (s *Server) renameTheme(w http.ResponseWriter, r *http.Request) error {
	row, err := s.loadThemeRow(r)
	if err != nil {
		return err
	}
	name := r.FormValue("name")
	if err := s.store.RenameTheme(row.ID, name); err != nil {
		// Show the problem in the form rather than losing the draft.
		return s.render(w, "theme-rename", renameData{themeRow: row, Draft: name, Error: err.Error()})
	}
	if row, err = s.loadThemeRow(r); err != nil {
		return err
	}
	return s.render(w, "theme-row", row)
}

// themeFilter is the header Search shows when filtered to a theme.
type themeFilter struct {
	store.ThemeSummary
	// Area is set when the filter is a theme inside an area.
	Area *store.ThemeSummary
	// Related lists the area's other themes, or an area's own themes.
	Related []store.ThemeSummary
}

func (s *Server) themeFilter(id int64) (*themeFilter, error) {
	t, err := s.store.ThemeSummary(id)
	if err != nil {
		return nil, err
	}
	f := &themeFilter{ThemeSummary: t}
	areaID := t.ID
	if !t.IsArea() {
		a, err := s.store.ThemeSummary(t.ParentID)
		if err != nil {
			return nil, err
		}
		f.Area, areaID = &a, a.ID
	}
	children, err := s.store.ThemeChildren(areaID)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		if c.ID != t.ID {
			f.Related = append(f.Related, c)
		}
	}
	return f, nil
}
