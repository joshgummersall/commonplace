// Package clippings parses "My Clippings.txt" files.
//
// Kindle and CrossPoint (the Xteink X4 firmware) write the same format,
// differing only in header details:
//
//	Kindle:     - Your Highlight on page 4 | Location 399-403 | Added on Tuesday, March 15, 2022 10:50:24 PM
//	CrossPoint: - Your Highlight on Page 49 | <chapter> | Added on Saturday, October 24, 2026, 10:19 AM
//
// so one tolerant parser handles both and reports which dialect each entry
// matched.
package clippings

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
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
	AddedAt  time.Time // zero if the date did not parse
	Text     string    // body with paragraph breaks kept as "\n"
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

const separator = "=========="

// dateLayouts are tried in order. The first entry is Kindle's; the second
// is CrossPoint's (comma after the year, no seconds).
var dateLayouts = []struct {
	layout  string
	dialect Dialect
}{
	{"Monday, January 2, 2006 3:04:05 PM", Kindle},
	{"Monday, January 2, 2006, 3:04 PM", CrossPoint},
}

var (
	kindRe     = regexp.MustCompile(`^- Your (Highlight|Note|Bookmark) on (.+)$`)
	pageRe     = regexp.MustCompile(`^(?i:page) (\S+)$`)
	locationRe = regexp.MustCompile(`^Location (\d+)(?:-(\d+))?$`)
)

// Parse reads every entry from r. Times are interpreted in loc, since the
// devices write local time without a zone. Malformed entries are skipped
// and reported as warnings; only read errors fail the parse.
func Parse(r io.Reader, loc *time.Location) ([]Clipping, []Warning, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var (
		out      []Clipping
		warnings []Warning
		entry    []string
		start    int
		lineNo   int
	)
	flush := func() {
		if len(entry) == 0 {
			return
		}
		c, err := parseEntry(entry, loc)
		switch {
		case err != nil:
			warnings = append(warnings, Warning{Line: start, Reason: err.Error()})
		case c != nil:
			out = append(out, *c)
		}
		entry = nil
	}
	for sc.Scan() {
		lineNo++
		// Kindle writes a BOM at the start of the file, and sometimes again
		// mid-file after the device rewrites it.
		line := strings.TrimRight(strings.ReplaceAll(sc.Text(), "\ufeff", ""), "\r")
		if line == separator {
			flush()
			continue
		}
		if len(entry) == 0 {
			if strings.TrimSpace(line) == "" {
				continue
			}
			start = lineNo
		}
		entry = append(entry, line)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("read clippings: %w", err)
	}
	// A final entry without a trailing separator is still complete enough
	// to parse; the device may have been unplugged mid-write.
	flush()
	return out, warnings, nil
}

// parseEntry parses one entry. Bookmarks are returned with empty Text;
// callers decide whether to keep them.
func parseEntry(lines []string, loc *time.Location) (*Clipping, error) {
	if len(lines) < 2 {
		return nil, fmt.Errorf("entry has no header line")
	}
	c := &Clipping{}
	c.Title, c.Author = splitTitleAuthor(lines[0])
	if c.Title == "" {
		return nil, fmt.Errorf("entry has an empty title")
	}
	if err := parseHeader(lines[1], loc, c); err != nil {
		return nil, err
	}

	body := lines[2:]
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

func parseHeader(header string, loc *time.Location, c *Clipping) error {
	m := kindRe.FindStringSubmatch(header)
	if m == nil {
		return fmt.Errorf("unrecognized header %q", header)
	}
	c.Kind = Kind(strings.ToLower(m[1]))

	fields := strings.Split(m[2], " | ")
	last := fields[len(fields)-1]
	date, ok := strings.CutPrefix(last, "Added on ")
	if !ok {
		return fmt.Errorf("header %q has no \"Added on\" date", header)
	}
	fields = fields[:len(fields)-1]

	c.Dialect = Kindle
	for _, l := range dateLayouts {
		if t, err := time.ParseInLocation(l.layout, date, loc); err == nil {
			c.AddedAt = t
			c.Dialect = l.dialect
			break
		}
	}

	var section []string
	for i, f := range fields {
		if pm := pageRe.FindStringSubmatch(f); pm != nil && i == 0 {
			c.Page = pm[1]
			continue
		}
		if lm := locationRe.FindStringSubmatch(f); lm != nil {
			c.LocStart, _ = strconv.Atoi(lm[1])
			c.LocEnd = c.LocStart
			if lm[2] != "" {
				c.LocEnd, _ = strconv.Atoi(lm[2])
			}
			continue
		}
		// Anything else is free text (CrossPoint's chapter), which may
		// itself contain " | ", so rejoin it.
		section = append(section, f)
	}
	c.Section = strings.Join(section, " | ")
	return nil
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
