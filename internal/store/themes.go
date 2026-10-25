package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Theme is a named group of highlights. An area holds themes; a theme
// belongs to one area.
type Theme struct {
	ID       int64 // 0 for a new theme
	ParentID int64 // 0 for an area
	Name     string
	Renamed  bool // named by you rather than the model
	// Members lists highlight ids, most central first.
	Members []int64
}

// Area is an area and the themes inside it, as ReplaceThemes saves them.
type Area struct {
	Theme
	Themes []Theme
}

// ThemeSummary is a theme as lists show it.
type ThemeSummary struct {
	ID       int64
	ParentID int64
	Name     string
	Renamed  bool
	Count    int
	Books    int
	Children int         // an area's themes; 0 for a theme
	TopBooks []BookCount // the books with the most highlights in it
	Themes   []ThemeSummary
}

// IsArea reports whether the summary is an area rather than a theme.
func (t ThemeSummary) IsArea() bool { return t.ParentID == 0 }

// ThemeRef names the theme a highlight belongs to.
type ThemeRef struct {
	ID   int64
	Name string
}

// ThemeRefresh records the last successful refresh.
type ThemeRefresh struct {
	At         time.Time
	Highlights int
}

// topBooks is how many books a theme summary names.
const topBooks = 3

// ReplaceThemes swaps every theme for areas, in order. Areas and themes
// with an ID keep it; the rest get new ids.
func (s *Store) ReplaceThemes(areas []Area, refresh ThemeRefresh) error {
	return s.InTx(func(t *Tx) error {
		if _, err := t.tx.Exec(`DELETE FROM themes`); err != nil {
			return err
		}
		insert := func(th Theme, parent int64, pos int) (int64, error) {
			var p any
			if parent != 0 {
				p = parent
			}
			var id any
			if th.ID != 0 {
				id = th.ID
			}
			res, err := t.tx.Exec(`INSERT INTO themes (id, parent_id, name, renamed, position) VALUES (?, ?, ?, ?, ?)`,
				id, p, th.Name, th.Renamed, pos)
			if err != nil {
				return 0, fmt.Errorf("save theme %q: %w", th.Name, err)
			}
			newID, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			// A highlight deleted since the refresh began is skipped.
			for rank, hid := range th.Members {
				if _, err := t.tx.Exec(`INSERT INTO theme_members (theme_id, highlight_id, rank)
					SELECT ?, id, ? FROM highlights WHERE id = ?`, newID, rank, hid); err != nil {
					return 0, fmt.Errorf("save theme %q: %w", th.Name, err)
				}
			}
			return newID, nil
		}
		for i, a := range areas {
			aid, err := insert(a.Theme, 0, i)
			if err != nil {
				return err
			}
			for j, th := range a.Themes {
				if _, err := insert(th, aid, j); err != nil {
					return err
				}
			}
		}
		_, err := t.tx.Exec(`INSERT OR REPLACE INTO theme_refresh (id, refreshed_at, highlights) VALUES (1, ?, ?)`,
			formatTime(refresh.At), refresh.Highlights)
		return err
	})
}

// AllThemes loads every theme with its members, areas first, for matching
// a refresh against the themes it replaces.
func (s *Store) AllThemes() ([]Theme, error) {
	rows, err := s.db.Query(`SELECT t.id, coalesce(t.parent_id, 0), t.name, t.renamed, m.highlight_id
		FROM themes t LEFT JOIN theme_members m ON m.theme_id = t.id
		ORDER BY t.parent_id IS NOT NULL, t.id, m.rank`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Theme
	for rows.Next() {
		var (
			th  Theme
			hid sql.NullInt64
		)
		if err := rows.Scan(&th.ID, &th.ParentID, &th.Name, &th.Renamed, &hid); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != th.ID {
			out = append(out, th)
		}
		if hid.Valid {
			last := &out[len(out)-1]
			last.Members = append(last.Members, hid.Int64)
		}
	}
	return out, rows.Err()
}

// summaries loads theme summaries matching where (on themes t), in order.
func (s *Store) summaries(where string, args ...any) ([]ThemeSummary, error) {
	rows, err := s.db.Query(`SELECT t.id, coalesce(t.parent_id, 0), t.name, t.renamed,
			count(m.highlight_id), count(DISTINCT h.book_id),
			(SELECT count(*) FROM themes c WHERE c.parent_id = t.id)
		FROM themes t
		LEFT JOIN theme_members m ON m.theme_id = t.id
		LEFT JOIN highlights h ON h.id = m.highlight_id
		WHERE `+where+`
		GROUP BY t.id
		ORDER BY t.parent_id IS NOT NULL, t.position`, args...)
	if err != nil {
		return nil, err
	}
	var out []ThemeSummary
	for rows.Next() {
		var t ThemeSummary
		if err := rows.Scan(&t.ID, &t.ParentID, &t.Name, &t.Renamed, &t.Count, &t.Books, &t.Children); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]int64, len(out))
	for i, t := range out {
		ids[i] = t.ID
	}
	books, err := s.themeBooks(ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].TopBooks = books[out[i].ID]
	}
	return out, nil
}

// themeBooks returns each theme's topBooks books with the most highlights
// in it, in one query.
func (s *Store) themeBooks(ids []int64) (map[int64][]BookCount, error) {
	out := make(map[int64][]BookCount)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT theme_id, id, title, author, n FROM (
			SELECT m.theme_id, b.id, b.title, b.author, count(*) AS n,
				row_number() OVER (PARTITION BY m.theme_id
					ORDER BY count(*) DESC, b.title COLLATE NOCASE) AS pos
			FROM theme_members m JOIN highlights h ON h.id = m.highlight_id JOIN books b ON b.id = h.book_id
			WHERE m.theme_id IN (`+placeholders(len(ids))+`)
			GROUP BY m.theme_id, b.id)
		WHERE pos <= ? ORDER BY theme_id, pos`, append(int64Args(ids), topBooks)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			tid int64
			b   BookCount
		)
		if err := rows.Scan(&tid, &b.ID, &b.Title, &b.Author, &b.Count); err != nil {
			return nil, err
		}
		out[tid] = append(out[tid], b)
	}
	return out, rows.Err()
}

// ThemeTree lists areas in order, each with its themes.
func (s *Store) ThemeTree() ([]ThemeSummary, error) {
	all, err := s.summaries("1")
	if err != nil {
		return nil, err
	}
	return nest(all), nil
}

// ThemeOutline is ThemeTree with only ids and names filled in, for menus.
func (s *Store) ThemeOutline() ([]ThemeSummary, error) {
	rows, err := s.db.Query(`SELECT id, coalesce(parent_id, 0), name FROM themes
		ORDER BY parent_id IS NOT NULL, position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []ThemeSummary
	for rows.Next() {
		var t ThemeSummary
		if err := rows.Scan(&t.ID, &t.ParentID, &t.Name); err != nil {
			return nil, err
		}
		all = append(all, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nest(all), nil
}

// nest puts themes under their areas. all must list areas first.
func nest(all []ThemeSummary) []ThemeSummary {
	var areas []ThemeSummary
	index := map[int64]int{}
	for _, t := range all {
		if t.IsArea() {
			index[t.ID] = len(areas)
			areas = append(areas, t)
		} else if i, ok := index[t.ParentID]; ok {
			areas[i].Themes = append(areas[i].Themes, t)
		}
	}
	return areas
}

// ThemeSummary loads one theme, or returns ErrNotFound.
func (s *Store) ThemeSummary(id int64) (ThemeSummary, error) {
	ts, err := s.summaries("t.id = ?", id)
	if err != nil {
		return ThemeSummary{}, err
	}
	if len(ts) == 0 {
		return ThemeSummary{}, ErrNotFound
	}
	return ts[0], nil
}

// ThemeChildren lists the themes inside an area, in order.
func (s *Store) ThemeChildren(id int64) ([]ThemeSummary, error) {
	return s.summaries("t.parent_id = ?", id)
}

// ThemesOf returns, for each highlight that has one, the most specific
// theme it belongs to: its theme if it has one, else its area.
func (s *Store) ThemesOf(ids []int64) (map[int64]ThemeRef, error) {
	out := make(map[int64]ThemeRef)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT m.highlight_id, t.id, t.name FROM theme_members m
		JOIN themes t ON t.id = m.theme_id
		WHERE m.highlight_id IN (`+placeholders(len(ids))+`)
		ORDER BY t.parent_id IS NULL`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hid int64
		var ref ThemeRef
		if err := rows.Scan(&hid, &ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		if _, ok := out[hid]; !ok {
			out[hid] = ref
		}
	}
	return out, rows.Err()
}

// ThemeMemberIDs returns the set of highlights in a theme.
func (s *Store) ThemeMemberIDs(id int64) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT highlight_id FROM theme_members WHERE theme_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]bool)
	for rows.Next() {
		var hid int64
		if err := rows.Scan(&hid); err != nil {
			return nil, err
		}
		out[hid] = true
	}
	return out, rows.Err()
}

// MaxThemeName caps a theme name's length in characters.
const MaxThemeName = 80

// RenameTheme gives a theme your own name, which later refreshes keep.
func (s *Store) RenameTheme(id int64, name string) error {
	name = strings.Join(strings.Fields(name), " ")
	switch {
	case name == "":
		return errors.New("a theme needs a name")
	case len([]rune(name)) > MaxThemeName:
		return fmt.Errorf("keep the name under %d characters", MaxThemeName)
	}
	return s.execOne(`UPDATE themes SET name = ?, renamed = 1 WHERE id = ?`, name, id)
}

// LastThemeRefresh returns the last successful refresh; ok is false if
// themes were never computed.
func (s *Store) LastThemeRefresh() (r ThemeRefresh, ok bool, err error) {
	var at string
	err = s.db.QueryRow(`SELECT refreshed_at, highlights FROM theme_refresh WHERE id = 1`).Scan(&at, &r.Highlights)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.At, err = parseTime(sql.NullString{String: at, Valid: true})
	return r, err == nil, err
}

// CountImportedSince counts highlights imported after t.
func (s *Store) CountImportedSince(t time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM highlights WHERE imported_at > ?`, formatTime(t)).Scan(&n)
	return n, err
}

// CountHighlights counts every highlight.
func (s *Store) CountHighlights() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM highlights`).Scan(&n)
	return n, err
}

// UnthemedCount counts highlights in no area.
func (s *Store) UnthemedCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM highlights
		WHERE id NOT IN (SELECT highlight_id FROM theme_members)`).Scan(&n)
	return n, err
}

// ThemeInput is an embedded highlight, as clustering needs it.
type ThemeInput struct {
	ID     int64
	BookID int64
	Text   string
	Vec    []float32
}

// ThemeInputs loads every highlight embedded by model, with its text.
func (s *Store) ThemeInputs(model string) ([]ThemeInput, error) {
	return s.embedded(model, true)
}
