// Package search finds highlights by keyword (and, later, by meaning).
package search

import (
	"fmt"
	"strings"

	"github.com/joshgummersall/commonplace/internal/store"
)

// Query is a search request. An empty Text with a filter lists the
// matching highlights, newest first.
type Query struct {
	Text      string
	BookID    int64 // 0 means any book
	Favorites bool  // only favorites
	Limit     int   // 0 means PageSize
	Offset    int   // rows to skip, for loading the next page
}

// PageSize is how many results a page of search holds.
const PageSize = 50

// Segment is a run of snippet text; Match marks a matched term. Templates
// escape Text and wrap matches, so snippets are safe to render as HTML.
type Segment struct {
	Text  string
	Match bool
}

// Result is one search hit.
type Result struct {
	store.Entry
	// Snippet is the highlight text around the matches, or the whole text
	// when it is short or nothing in it matched.
	Snippet []Segment
	// NoteSnippet is set when the match was in the highlight's notes.
	NoteSnippet []Segment
	// Rank is bm25: lower is better. Zero when listing without a query.
	Rank float64
}

// Snippet markers. Control characters can't appear in highlight text that
// came through the parser, so they can't collide with real text.
const (
	markOpen  = "\x01"
	markClose = "\x02"
)

// bm25 column weights, in highlights_fts column order: text, notes, title,
// author. A word in the passage itself counts most; a word in the book
// title should surface the book's highlights without outranking passages
// that say it.
const bm25Weights = "1.0, 0.8, 0.4, 0.4"

// Keyword runs a full-text search with bm25 ranking, one page at a time.
// Ordering breaks ties on id, so pages don't overlap or skip rows.
func Keyword(s *store.Store, q Query) ([]Result, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = PageSize
	}
	from, where, args, ranked := clauses(q)

	var sql string
	if ranked {
		sql = fmt.Sprintf(`SELECT %s,
				bm25(highlights_fts, %s),
				snippet(highlights_fts, 0, '%s', '%s', '…', 48),
				snippet(highlights_fts, 1, '%s', '%s', '…', 24)
			%s WHERE %s
			ORDER BY bm25(highlights_fts, %s), h.id
			LIMIT ? OFFSET ?`,
			store.EntryColumns, bm25Weights, markOpen, markClose, markOpen, markClose,
			from, where, bm25Weights)
	} else {
		sql = fmt.Sprintf(`SELECT %s, 0, '', ''
			%s WHERE %s
			ORDER BY h.highlighted_at IS NULL, h.highlighted_at DESC, h.id DESC
			LIMIT ? OFFSET ?`,
			store.EntryColumns, from, where)
	}
	args = append(args, limit, max(q.Offset, 0))

	rows, err := s.DB().Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var (
			r                Result
			textSnip, noteSn string
		)
		e, err := store.ScanEntry(rows, &r.Rank, &textSnip, &noteSn)
		if err != nil {
			return nil, err
		}
		r.Entry = e
		r.Snippet = segments(textSnip)
		if !hasMatch(r.Snippet) {
			r.Snippet = []Segment{{Text: e.Text}}
		}
		if ns := segments(noteSn); hasMatch(ns) {
			r.NoteSnippet = ns
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ftsQuery turns what a person types into a safe FTS5 query. Every word is
// quoted, so FTS syntax (AND, NEAR, -, :, *) is matched literally rather
// than interpreted; double-quoted runs become phrases. Words are ANDed,
// and the last one matches as a prefix so results update while typing.
// Count returns how many highlights match q, ignoring Limit and Offset.
func Count(s *store.Store, q Query) (int, error) {
	from, where, args, _ := clauses(q)
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) `+from+` WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count search results: %w", err)
	}
	return n, nil
}

// clauses builds the FROM and WHERE shared by Keyword and Count. ranked is
// true when there is text to match, so results order by bm25.
func clauses(q Query) (from, where string, args []any, ranked bool) {
	var conds []string
	from = "FROM highlights h JOIN books b ON b.id = h.book_id"
	if match := ftsQuery(q.Text); match != "" {
		ranked = true
		from = `FROM highlights_fts
			JOIN highlights h ON h.id = highlights_fts.rowid
			JOIN books b ON b.id = h.book_id`
		conds = append(conds, "highlights_fts MATCH ?")
		args = append(args, match)
	}
	if q.BookID != 0 {
		conds = append(conds, "h.book_id = ?")
		args = append(args, q.BookID)
	}
	if q.Favorites {
		conds = append(conds, "h.favorite = 1")
	}
	if len(conds) == 0 {
		conds = append(conds, "1")
	}
	return from, strings.Join(conds, " AND "), args, ranked
}

func ftsQuery(text string) string {
	var (
		terms   []string
		inQuote bool
		cur     strings.Builder
		phrase  []string
	)
	flushWord := func() {
		if cur.Len() == 0 {
			return
		}
		if inQuote {
			phrase = append(phrase, cur.String())
		} else {
			terms = append(terms, `"`+cur.String()+`"`)
		}
		cur.Reset()
	}
	for _, r := range text {
		switch {
		case r == '"':
			flushWord()
			if inQuote && len(phrase) > 0 {
				terms = append(terms, `"`+strings.Join(phrase, " ")+`"`)
			}
			phrase = nil
			inQuote = !inQuote
		case r == ' ' || r == '\t' || r == '\n':
			flushWord()
		case r < ' ':
			// Control characters would collide with the snippet markers.
		default:
			cur.WriteRune(r)
		}
	}
	trailingWord := cur.Len() > 0 && !inQuote
	flushWord()
	if inQuote && len(phrase) > 0 {
		// An unclosed quote still searches as a phrase.
		terms = append(terms, `"`+strings.Join(phrase, " ")+`"`)
		trailingWord = false
	}
	if len(terms) == 0 {
		return ""
	}
	if trailingWord {
		terms[len(terms)-1] += "*"
	}
	return strings.Join(terms, " ")
}

func segments(snippet string) []Segment {
	var out []Segment
	for snippet != "" {
		open := strings.Index(snippet, markOpen)
		if open < 0 {
			out = append(out, Segment{Text: snippet})
			break
		}
		if open > 0 {
			out = append(out, Segment{Text: snippet[:open]})
		}
		rest := snippet[open+len(markOpen):]
		end := strings.Index(rest, markClose)
		if end < 0 {
			out = append(out, Segment{Text: rest, Match: true})
			break
		}
		out = append(out, Segment{Text: rest[:end], Match: true})
		snippet = rest[end+len(markClose):]
	}
	return out
}

func hasMatch(segs []Segment) bool {
	for _, s := range segs {
		if s.Match {
			return true
		}
	}
	return false
}
