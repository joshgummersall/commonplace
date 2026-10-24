// Package clippings parses "My Clippings.txt" files from Kindle and
// CrossPoint (the Xteink X4 firmware).
//
// Both devices share the file structure: entries end with "==========", and
// each entry is a title line, a header line, a blank line, and the body.
// They differ in the header:
//
//	Kindle:     - Your Highlight on page 4 | Location 399-403 | Added on Tuesday, March 15, 2022 10:50:24 PM
//	CrossPoint: - Your Highlight on Page 49 | <chapter> | Added on Saturday, October 24, 2026, 10:19 AM
//
// This file holds the shared structure. Each device's header parser lives
// in its own file and is strict, so a firmware change surfaces as warnings
// rather than misread entries.
package clippings

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type Kind string

const (
	Highlight Kind = "highlight"
	Note      Kind = "note"
	Bookmark  Kind = "bookmark"
)

type Dialect string

const (
	Kindle     Dialect = "kindle"
	CrossPoint Dialect = "crosspoint"
)

// Clipping is one entry between "==========" separators.
type Clipping struct {
	Kind    Kind
	Dialect Dialect
	Title   string
	Author  string
	Page    string // as written: "138", "xvi"; empty if absent
	Section string // CrossPoint chapter name; empty for Kindle
	// LocStart and LocEnd are the Kindle location range; 0 when absent.
	// A single location ("Location 106") sets both.
	LocStart int
	LocEnd   int
	AddedAt  time.Time
	Text     string // body with paragraph breaks kept as "\n"
}

// Location renders the position for display, e.g. "page 4, loc 399-403".
func (c Clipping) Location() string {
	var parts []string
	if c.Page != "" {
		parts = append(parts, "page "+c.Page)
	}
	switch {
	case c.LocStart == 0:
	case c.LocStart == c.LocEnd:
		parts = append(parts, fmt.Sprintf("loc %d", c.LocStart))
	default:
		parts = append(parts, fmt.Sprintf("loc %d-%d", c.LocStart, c.LocEnd))
	}
	return strings.Join(parts, ", ")
}

// Warning describes an entry the parser skipped.
type Warning struct {
	Line   int // 1-based line of the entry's title
	Reason string
}

func (w Warning) String() string {
	return fmt.Sprintf("line %d: %s", w.Line, w.Reason)
}

// headerParser parses one dialect's header line into c. It returns
// errNotThisDialect when the header's shape does not match, and any other
// error when the shape matches but a field (the date) does not parse.
type headerParser func(header string, loc *time.Location, c *Clipping) error

var errNotThisDialect = errors.New("header does not match dialect")

var parsers = map[Dialect]headerParser{
	Kindle:     parseKindleHeader,
	CrossPoint: parseCrossPointHeader,
}

// ParseDialect validates a --source value.
func ParseDialect(s string) (Dialect, error) {
	d := Dialect(s)
	if _, ok := parsers[d]; !ok {
		return "", fmt.Errorf("unknown source %q (want %s or %s)", s, Kindle, CrossPoint)
	}
	return d, nil
}

const separator = "=========="

type rawEntry struct {
	line  int // 1-based line of the title
	lines []string
}

// Parse reads every entry from r. If dialect is empty it is detected from
// the first entry. Times are interpreted in loc, since the devices write
// local time without a zone.
//
// Entries that fail to parse are skipped and reported as warnings. Parse
// fails outright on read errors, when the dialect can't be detected, and
// when an entry has the other dialect's header, since a mixed file means
// something other than a device export.
func Parse(r io.Reader, dialect Dialect, loc *time.Location) ([]Clipping, []Warning, error) {
	entries, err := splitEntries(r)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		return nil, nil, nil
	}
	if dialect == "" {
		if dialect, err = detect(entries[0], loc); err != nil {
			return nil, nil, err
		}
	}
	parse, ok := parsers[dialect]
	if !ok {
		return nil, nil, fmt.Errorf("unknown dialect %q", dialect)
	}

	var (
		out      []Clipping
		warnings []Warning
	)
	for _, e := range entries {
		c, err := parseEntry(e, parse, loc)
		if errors.Is(err, errNotThisDialect) {
			if other := matchingDialect(e, loc); other != "" {
				return nil, nil, fmt.Errorf("line %d: %s entry in a %s file", e.line, other, dialect)
			}
			err = fmt.Errorf("unrecognized %s header %q", dialect, e.lines[1])
		}
		if err != nil {
			warnings = append(warnings, Warning{Line: e.line, Reason: err.Error()})
			continue
		}
		c.Dialect = dialect
		out = append(out, *c)
	}
	return out, warnings, nil
}

func detect(e rawEntry, loc *time.Location) (Dialect, error) {
	if d := matchingDialect(e, loc); d != "" {
		return d, nil
	}
	header := ""
	if len(e.lines) > 1 {
		header = e.lines[1]
	}
	return "", fmt.Errorf("line %d: cannot detect format from header %q; pass --source", e.line, header)
}

// matchingDialect returns the dialect whose parser fully accepts e's
// header, if any. A shape match alone is not enough: the header regexes
// overlap (a Kindle header with "Page" capitalized fits CrossPoint's shape,
// reading the location as the chapter), but the date layouts don't.
func matchingDialect(e rawEntry, loc *time.Location) Dialect {
	if len(e.lines) < 2 {
		return ""
	}
	for d, parse := range parsers {
		if err := parse(e.lines[1], loc, &Clipping{}); err == nil {
			return d
		}
	}
	return ""
}

func splitEntries(r io.Reader) ([]rawEntry, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var (
		out    []rawEntry
		cur    rawEntry
		lineNo int
	)
	for sc.Scan() {
		lineNo++
		// Kindle writes a BOM at the start of the file, and sometimes again
		// mid-file after the device rewrites it.
		line := strings.TrimRight(strings.ReplaceAll(sc.Text(), "\ufeff", ""), "\r")
		if line == separator {
			if len(cur.lines) > 0 {
				out = append(out, cur)
			}
			cur = rawEntry{}
			continue
		}
		if len(cur.lines) == 0 {
			if strings.TrimSpace(line) == "" {
				continue
			}
			cur.line = lineNo
		}
		cur.lines = append(cur.lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read clippings: %w", err)
	}
	// A final entry without a trailing separator is still complete enough
	// to parse; the device may have been unplugged mid-write.
	if len(cur.lines) > 0 {
		out = append(out, cur)
	}
	return out, nil
}

// parseEntry parses one entry. Bookmarks are returned with empty Text;
// callers decide whether to keep them.
func parseEntry(e rawEntry, parse headerParser, loc *time.Location) (*Clipping, error) {
	if len(e.lines) < 2 {
		return nil, fmt.Errorf("entry has no header line")
	}
	c := &Clipping{}
	c.Title, c.Author = splitTitleAuthor(e.lines[0])
	if c.Title == "" {
		return nil, fmt.Errorf("entry has an empty title")
	}
	if err := parse(e.lines[1], loc, c); err != nil {
		return nil, err
	}

	body := e.lines[2:]
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	for i := range body {
		body[i] = strings.TrimRightFunc(body[i], isSpace)
	}
	c.Text = strings.Join(body, "\n")

	if c.Kind != Bookmark && c.Text == "" {
		return nil, fmt.Errorf("%s in %q has no text", c.Kind, c.Title)
	}
	return c, nil
}

// parseKind maps the header's "Highlight", "Note", or "Bookmark".
func parseKind(s string) Kind {
	return Kind(strings.ToLower(s))
}

// splitTitleAuthor treats a trailing parenthesized group as the author:
// "Fooled by Randomness (Incerto) (Taleb, Nassim Nicholas)" yields the
// title "Fooled by Randomness (Incerto)". This misreads titles that end in
// a parenthetical but have no author, e.g. "Game Theory (Very Short
// Introductions)"; the format gives no way to tell them apart.
func splitTitleAuthor(line string) (title, author string) {
	line = strings.TrimSpace(line)
	if !strings.HasSuffix(line, ")") {
		return line, ""
	}
	depth := 0
	for i := len(line) - 1; i >= 0; i-- {
		switch line[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				title = strings.TrimSpace(line[:i])
				author = strings.TrimSpace(line[i+1 : len(line)-1])
				if title == "" {
					return line, ""
				}
				return title, author
			}
		}
	}
	return line, ""
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == ' '
}
