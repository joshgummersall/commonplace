// Package importer stores parsed clippings, keeping the newest version of
// each edited highlight and attaching notes.
package importer

import (
	"fmt"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/store"
)

// Result counts what an import did.
type Result struct {
	Added     int // new highlights
	Replaced  int // stored highlights updated to a newer version
	Unchanged int // newest version already stored
	Older     int // a newer version is already stored; skipped
	Merged    int // older versions in the file, folded into the newest
	Notes     int // notes attached to a highlight
	Orphans   int // notes with no highlight, stored as highlights
	Bookmarks int // skipped
	// Changed lists highlights whose text is new or replaced, for embedding.
	Changed []int64
}

type bookKey struct{ title, author string }

// Import stores clips in one transaction. now is the imported_at stamp.
//
// Each book's entries are grouped into versions of the same highlight
// first, and only the newest of each group is compared with the database,
// so the outcome doesn't depend on entry order. Notes go in last so a note
// can find its highlight wherever it sits in the file.
func Import(s *store.Store, clips []clippings.Clipping, now time.Time) (Result, error) {
	var (
		r      Result
		books  []bookKey
		byBook = make(map[bookKey][]version)
		notes  []clippings.Clipping
	)
	for _, c := range clips {
		switch c.Kind {
		case clippings.Bookmark:
			r.Bookmarks++
		case clippings.Note:
			notes = append(notes, c)
		case clippings.Highlight:
			k := bookKey{c.Title, c.Author}
			if _, ok := byBook[k]; !ok {
				books = append(books, k)
			}
			byBook[k] = append(byBook[k], version{clip: c, norm: normalize(c.Text)})
		default:
			return Result{}, fmt.Errorf("unknown clipping kind %q", c.Kind)
		}
	}

	err := s.InTx(func(tx *store.Tx) error {
		for _, k := range books {
			kept, dropped := latestVersions(byBook[k])
			r.Merged += dropped
			for _, v := range kept {
				if err := storeVersion(tx, v, now, &r); err != nil {
					return err
				}
			}
		}
		for _, n := range notes {
			if err := importNote(tx, n, now, &r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return r, nil
}

// storeVersion stores the newest version of a highlight. If the database
// holds older versions of it, the most recent is updated in place and any
// others are merged into it.
func storeVersion(tx *store.Tx, v version, now time.Time, r *Result) error {
	c := v.clip
	bookID, err := tx.BookID(c.Title, c.Author)
	if err != nil {
		return err
	}
	h := &store.Highlight{
		BookID:        bookID,
		Source:        string(c.Dialect),
		Text:          c.Text,
		Location:      c.Location(),
		Page:          c.Page,
		LocStart:      c.LocStart,
		LocEnd:        c.LocEnd,
		HighlightedAt: c.AddedAt,
		ImportedAt:    now,
		Hash:          hashText(v.norm),
	}

	existing, err := tx.HighlightByHash(bookID, h.Hash)
	if err != nil {
		return err
	}
	if existing != nil {
		r.Unchanged++
		return nil
	}

	near, err := tx.HighlightsNear(bookID, h.LocStart, h.LocEnd, h.Page)
	if err != nil {
		return err
	}
	var matches []store.Highlight
	for _, cand := range near {
		if sameHighlight(v.norm, normalize(cand.Text)) {
			if newer(&cand, h) {
				r.Older++
				return nil
			}
			matches = append(matches, cand)
		}
	}
	if len(matches) == 0 {
		if err := tx.InsertHighlight(h); err != nil {
			return err
		}
		r.Added++
		r.Changed = append(r.Changed, h.ID)
		return nil
	}

	keep := &matches[0]
	for i := range matches[1:] {
		if newer(&matches[i+1], keep) {
			keep = &matches[i+1]
		}
	}
	for i := range matches {
		other := &matches[i]
		if other.ID == keep.ID {
			continue
		}
		keep.Note = mergeNote(keep.Note, other.Note)
		if err := tx.MergeInto(keep.ID, other.ID, keep.Note); err != nil {
			return err
		}
	}
	if err := tx.ReplaceHighlightText(keep.ID, h); err != nil {
		return err
	}
	r.Replaced++
	r.Changed = append(r.Changed, keep.ID)
	return nil
}

// newer reports whether stored highlight a is a later version than b.
func newer(a, b *store.Highlight) bool {
	return newerAt(a.HighlightedAt, b.HighlightedAt, len(a.Text), len(b.Text))
}

// importNote attaches a note to the highlight it was written on. Kindle
// records a note at a single location, normally the highlight's last one.
func importNote(tx *store.Tx, c clippings.Clipping, now time.Time, r *Result) error {
	bookID, err := tx.BookID(c.Title, c.Author)
	if err != nil {
		return err
	}
	// An orphan note stored by an earlier import would otherwise be found
	// nearby and attached to itself.
	stored, err := tx.HighlightByHash(bookID, hashText(normalize(c.Text)))
	if err != nil {
		return err
	}
	if stored != nil {
		r.Unchanged++
		return nil
	}
	near, err := tx.HighlightsNear(bookID, c.LocStart, c.LocEnd, c.Page)
	if err != nil {
		return err
	}
	if target := noteTarget(near, c.LocStart); target != nil {
		note := mergeNote(target.Note, c.Text)
		if note != target.Note {
			if err := tx.SetNote(target.ID, note); err != nil {
				return err
			}
		}
		r.Notes++
		return nil
	}

	// No highlight to attach to: keep the note as a highlight of its own
	// so it isn't lost. Highlight dedupe makes this idempotent.
	c.Kind = clippings.Highlight
	before := r.Added
	if err := storeVersion(tx, version{clip: c, norm: normalize(c.Text)}, now, r); err != nil {
		return err
	}
	if r.Added > before {
		r.Added--
		r.Orphans++
	}
	return nil
}

// noteTarget prefers a highlight ending at the note's location, then the
// most recent of the rest.
func noteTarget(near []store.Highlight, loc int) *store.Highlight {
	var best *store.Highlight
	for i := range near {
		h := &near[i]
		if loc > 0 && h.LocEnd == loc {
			return h
		}
		if best == nil || newer(h, best) {
			best = h
		}
	}
	return best
}

// mergeNote appends add to existing unless it's already there, so
// re-importing doesn't duplicate notes.
func mergeNote(existing, add string) string {
	add = strings.TrimSpace(add)
	switch {
	case add == "" || strings.Contains(existing, add):
		return existing
	case existing == "":
		return add
	default:
		return existing + "\n\n" + add
	}
}
