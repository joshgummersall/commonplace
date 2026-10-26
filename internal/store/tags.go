package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// A highlight's tag is in one of three states.
const (
	// TagAuto marks a tag the app suggested and you haven't confirmed.
	TagAuto = "auto"
	// TagYes marks a tag you set or confirmed.
	TagYes = "yes"
	// TagNo marks a tag you removed; the app never suggests it again for
	// that highlight.
	TagNo = "no"
)

// Tag is a tag as lists show it. Counts leave out highlights you removed
// it from and buried highlights.
type Tag struct {
	ID    int64
	Name  string
	Count int
	// Suggested counts the highlights the app tagged that you haven't
	// confirmed.
	Suggested int
	Books     int
	TopBooks  []BookCount // the books with the most highlights in it
}

// TagRef names a tag on a highlight.
type TagRef struct {
	ID        int64
	Name      string
	Suggested bool // the app's suggestion, not yet confirmed
}

// SeedTag is a suggested tag and the highlights to suggest it for.
type SeedTag struct {
	Name    string
	Members []int64
}

// MaxTagName caps a tag name's length in characters.
const MaxTagName = 80

// ErrTagsExist means tags can only be seeded into a library with none.
var ErrTagsExist = errors.New("there are tags already")

// CleanTagName collapses a name's spaces and checks it. Commas separate
// tags where you type them, so a name can't hold one.
func CleanTagName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	switch {
	case name == "":
		return "", errors.New("a tag needs a name")
	case strings.Contains(name, ","):
		return "", errors.New("a tag name can't hold a comma")
	case len([]rune(name)) > MaxTagName:
		return "", fmt.Errorf("keep tag names under %d characters", MaxTagName)
	}
	return name, nil
}

// tags loads tag summaries matching where (on tags g), by name.
func (s *Store) tags(where string, args ...any) ([]Tag, error) {
	rows, err := s.db.Query(`SELECT g.id, g.name,
			coalesce(sum(ht.state != 'no' AND h.buried = 0), 0), coalesce(sum(ht.state = 'auto' AND h.buried = 0), 0),
			count(DISTINCT CASE WHEN ht.state != 'no' AND h.buried = 0 THEN h.book_id END)
		FROM tags g
		LEFT JOIN highlight_tags ht ON ht.tag_id = g.id
		LEFT JOIN highlights h ON h.id = ht.highlight_id
		WHERE `+where+`
		GROUP BY g.id
		ORDER BY g.name`, args...)
	if err != nil {
		return nil, err
	}
	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Count, &t.Suggested, &t.Books); err != nil {
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
	books, err := s.tagBooks(ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].TopBooks = books[out[i].ID]
	}
	return out, nil
}

// topBooks is how many books a tag summary names.
const topBooks = 3

// tagBooks returns each tag's topBooks books with the most highlights in
// it, in one query.
func (s *Store) tagBooks(ids []int64) (map[int64][]BookCount, error) {
	out := make(map[int64][]BookCount)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT tag_id, id, title, author, n FROM (
			SELECT ht.tag_id, b.id, b.title, b.author, count(*) AS n,
				row_number() OVER (PARTITION BY ht.tag_id
					ORDER BY count(*) DESC, b.title COLLATE NOCASE) AS pos
			FROM highlight_tags ht JOIN highlights h ON h.id = ht.highlight_id JOIN books b ON b.id = h.book_id
			WHERE ht.tag_id IN (`+placeholders(len(ids))+`) AND ht.state != 'no' AND h.buried = 0
			GROUP BY ht.tag_id, b.id)
		WHERE pos <= ? ORDER BY tag_id, pos`, append(int64Args(ids), topBooks)...)
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

// Tags lists every tag by name.
func (s *Store) Tags() ([]Tag, error) {
	return s.tags("1")
}

// Tag loads one tag, or returns ErrNotFound.
func (s *Store) Tag(id int64) (Tag, error) {
	ts, err := s.tags("g.id = ?", id)
	if err != nil {
		return Tag{}, err
	}
	if len(ts) == 0 {
		return Tag{}, ErrNotFound
	}
	return ts[0], nil
}

// CountTags counts every tag.
func (s *Store) CountTags() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM tags`).Scan(&n)
	return n, err
}

// TagsOf returns each highlight's tags by name, leaving out tags you
// removed.
func (s *Store) TagsOf(ids []int64) (map[int64][]TagRef, error) {
	out := make(map[int64][]TagRef)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT ht.highlight_id, g.id, g.name, ht.state = 'auto' FROM highlight_tags ht
		JOIN tags g ON g.id = ht.tag_id
		WHERE ht.highlight_id IN (`+placeholders(len(ids))+`) AND ht.state != 'no'
		ORDER BY g.name`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hid int64
		var ref TagRef
		if err := rows.Scan(&hid, &ref.ID, &ref.Name, &ref.Suggested); err != nil {
			return nil, err
		}
		out[hid] = append(out[hid], ref)
	}
	return out, rows.Err()
}

// UntaggedCount counts highlights with no tag, leaving out buried ones.
func (s *Store) UntaggedCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM highlights
		WHERE buried = 0 AND id NOT IN (SELECT highlight_id FROM highlight_tags WHERE state != 'no')`).Scan(&n)
	return n, err
}

// UnconfirmedCount counts highlights with a suggested tag not yet
// confirmed, leaving out buried ones.
func (s *Store) UnconfirmedCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(DISTINCT ht.highlight_id) FROM highlight_tags ht
		JOIN highlights h ON h.id = ht.highlight_id WHERE ht.state = 'auto' AND h.buried = 0`).Scan(&n)
	return n, err
}

// SetTags makes names a highlight's tags, as you typed them: each is set
// (and created if new), confirming any the app suggested, and every other
// tag is removed. Names match existing tags ignoring case.
func (s *Store) SetTags(hid int64, names []string) error {
	return s.InTx(func(t *Tx) error {
		var ok bool
		if err := t.tx.QueryRow(`SELECT 1 FROM highlights WHERE id = ?`, hid).Scan(&ok); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		keep := []any{hid}
		for _, raw := range names {
			name, err := CleanTagName(raw)
			if err != nil {
				return err
			}
			var id int64
			if err := t.tx.QueryRow(`INSERT INTO tags (name) VALUES (?)
				ON CONFLICT (name) DO UPDATE SET name = name RETURNING id`, name).Scan(&id); err != nil {
				return err
			}
			if _, err := t.tx.Exec(`INSERT INTO highlight_tags (highlight_id, tag_id, state) VALUES (?, ?, 'yes')
				ON CONFLICT DO UPDATE SET state = 'yes'`, hid, id); err != nil {
				return err
			}
			keep = append(keep, id)
		}
		_, err := t.tx.Exec(`UPDATE highlight_tags SET state = 'no'
			WHERE highlight_id = ? AND state != 'no' AND tag_id NOT IN (`+placeholders(len(keep)-1)+`)`, keep...)
		return err
	})
}

// ConfirmTags confirms every tag the app suggested for a highlight.
func (s *Store) ConfirmTags(hid int64) error {
	_, err := s.db.Exec(`UPDATE highlight_tags SET state = 'yes' WHERE highlight_id = ? AND state = 'auto'`, hid)
	return err
}

// RenameTag renames a tag and returns its id. Renaming a tag to another
// tag's name merges it into that tag (see MergeTag) and returns the other
// tag's id.
func (s *Store) RenameTag(id int64, name string) (int64, error) {
	name, err := CleanTagName(name)
	if err != nil {
		return 0, err
	}
	err = s.InTx(func(t *Tx) error {
		var into int64
		err := t.tx.QueryRow(`SELECT id FROM tags WHERE name = ? AND id != ?`, name, id).Scan(&into)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := t.tx.Exec(`UPDATE tags SET name = ? WHERE id = ?`, name, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrNotFound
			}
			return nil
		}
		if err != nil {
			return err
		}
		if err := t.mergeTag(id, into); err != nil {
			return err
		}
		id = into
		return nil
	})
	return id, err
}

// MergeTag moves every highlight from one tag to another and deletes the
// first, as RenameTag does when given the other tag's name.
func (s *Store) MergeTag(from, into int64) error {
	if from == into {
		return errors.New("a tag can't merge into itself")
	}
	return s.InTx(func(t *Tx) error {
		var n int
		if err := t.tx.QueryRow(`SELECT count(*) FROM tags WHERE id IN (?, ?)`, from, into).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return ErrNotFound
		}
		return t.mergeTag(from, into)
	})
}

// mergeTag merges one tag into another. On a highlight that had both, a
// tag you set wins over one you removed, which wins over a suggestion.
func (t *Tx) mergeTag(from, into int64) error {
	if _, err := t.tx.Exec(`INSERT INTO highlight_tags (highlight_id, tag_id, state)
		SELECT highlight_id, ?, state FROM highlight_tags WHERE tag_id = ?
		ON CONFLICT DO UPDATE SET state = CASE
			WHEN 'yes' IN (state, excluded.state) THEN 'yes'
			WHEN 'no' IN (state, excluded.state) THEN 'no'
			ELSE 'auto' END`, into, from); err != nil {
		return err
	}
	_, err := t.tx.Exec(`DELETE FROM tags WHERE id = ?`, from)
	return err
}

// DeleteTag deletes a tag from every highlight.
func (s *Store) DeleteTag(id int64) error {
	return s.execOne(`DELETE FROM tags WHERE id = ?`, id)
}

// SeedTags creates tags and suggests each for its members, in a library
// with no tags yet; otherwise it returns ErrTagsExist. Names that match
// ignoring case merge.
func (s *Store) SeedTags(seeds []SeedTag) error {
	return s.InTx(func(t *Tx) error {
		var n int
		if err := t.tx.QueryRow(`SELECT count(*) FROM tags`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrTagsExist
		}
		for _, sd := range seeds {
			name, err := CleanTagName(sd.Name)
			if err != nil {
				return fmt.Errorf("tag %q: %w", sd.Name, err)
			}
			var id int64
			if err := t.tx.QueryRow(`INSERT INTO tags (name) VALUES (?)
				ON CONFLICT (name) DO UPDATE SET name = name RETURNING id`, name).Scan(&id); err != nil {
				return err
			}
			// A highlight deleted since seeding began is skipped.
			for _, hid := range sd.Members {
				if _, err := t.tx.Exec(`INSERT OR IGNORE INTO highlight_tags (highlight_id, tag_id, state)
					SELECT id, ?, 'auto' FROM highlights WHERE id = ?`, id, hid); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// HighlightTags returns every highlight's tags, removed ones included, as
// tag id to state, for suggesting tags.
func (s *Store) HighlightTags() (map[int64]map[int64]string, error) {
	rows, err := s.db.Query(`SELECT highlight_id, tag_id, state FROM highlight_tags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]map[int64]string)
	for rows.Next() {
		var hid, tid int64
		var state string
		if err := rows.Scan(&hid, &tid, &state); err != nil {
			return nil, err
		}
		if out[hid] == nil {
			out[hid] = make(map[int64]string)
		}
		out[hid][tid] = state
	}
	return out, rows.Err()
}

// SuggestTags suggests tags (highlight id to tag ids), leaving any tag a
// highlight already has, or had removed, as it is. It returns how many
// suggestions it added.
func (s *Store) SuggestTags(suggest map[int64][]int64) (int, error) {
	added := 0
	err := s.InTx(func(t *Tx) error {
		for hid, tids := range suggest {
			for _, tid := range tids {
				res, err := t.tx.Exec(`INSERT OR IGNORE INTO highlight_tags (highlight_id, tag_id, state)
					SELECT ?, id, 'auto' FROM tags WHERE id = ?`, hid, tid)
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				added += int(n)
			}
		}
		return nil
	})
	return added, err
}

// CountHighlights counts every highlight.
func (s *Store) CountHighlights() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM highlights`).Scan(&n)
	return n, err
}

// EmbeddedText is an embedded highlight, as grouping needs it.
type EmbeddedText struct {
	ID     int64
	BookID int64
	Text   string
	Vec    []float32
}

// EmbeddedTexts loads every highlight embedded by model, with its text.
func (s *Store) EmbeddedTexts(model string) ([]EmbeddedText, error) {
	return s.embedded(model, true)
}
