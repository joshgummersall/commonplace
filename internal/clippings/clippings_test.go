package clippings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parseFile(t *testing.T, path string) ([]Clipping, []Warning) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	clips, warnings, err := Parse(f, time.UTC)
	if err != nil {
		t.Fatalf("Parse(%s): %v", path, err)
	}
	return clips, warnings
}

func date(y int, m time.Month, d, h, min, s int) time.Time {
	return time.Date(y, m, d, h, min, s, 0, time.UTC)
}

// want lists the fields worth checking per entry; Text is matched by prefix
// so long real highlights don't have to be pasted in full.
type want struct {
	kind       Kind
	dialect    Dialect
	title      string
	author     string
	page       string
	section    string
	locStart   int
	locEnd     int
	addedAt    time.Time
	textPrefix string
	textLen    int // 0 means don't check
}

func checkClips(t *testing.T, got []Clipping, wants []want) {
	t.Helper()
	if len(got) != len(wants) {
		t.Fatalf("got %d clippings, want %d", len(got), len(wants))
	}
	for i, w := range wants {
		c := got[i]
		t.Run(w.title+"/"+c.Location(), func(t *testing.T) {
			if c.Kind != w.kind {
				t.Errorf("Kind = %q, want %q", c.Kind, w.kind)
			}
			if c.Dialect != w.dialect {
				t.Errorf("Dialect = %q, want %q", c.Dialect, w.dialect)
			}
			if c.Title != w.title {
				t.Errorf("Title = %q, want %q", c.Title, w.title)
			}
			if c.Author != w.author {
				t.Errorf("Author = %q, want %q", c.Author, w.author)
			}
			if c.Page != w.page {
				t.Errorf("Page = %q, want %q", c.Page, w.page)
			}
			if c.Section != w.section {
				t.Errorf("Section = %q, want %q", c.Section, w.section)
			}
			if c.LocStart != w.locStart || c.LocEnd != w.locEnd {
				t.Errorf("Loc = %d-%d, want %d-%d", c.LocStart, c.LocEnd, w.locStart, w.locEnd)
			}
			if !c.AddedAt.Equal(w.addedAt) {
				t.Errorf("AddedAt = %v, want %v", c.AddedAt, w.addedAt)
			}
			if !strings.HasPrefix(c.Text, w.textPrefix) {
				t.Errorf("Text = %.60q..., want prefix %q", c.Text, w.textPrefix)
			}
			if w.textLen != 0 && len(c.Text) != w.textLen {
				t.Errorf("len(Text) = %d, want %d", len(c.Text), w.textLen)
			}
			if strings.ContainsAny(c.Text, "\r\ufeff") {
				t.Errorf("Text contains CR or BOM: %q", c.Text)
			}
		})
	}
}

// testdata/kindle/clippings.txt is synthetic: it mirrors a real Kindle export
// entry by entry (headers, locations, dates, BOMs, text lengths) with
// invented text.
func TestParseKindle(t *testing.T) {
	clips, warnings := parseFile(t, filepath.Join("..", "..", "testdata", "kindle", "clippings.txt"))
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	const gtd = "Getting Things Done"
	const gtdAuthor = "Allen, David"
	checkClips(t, clips, []want{
		// The file opens with a BOM, and the second entry has one too.
		{kind: Highlight, dialect: Kindle, title: "1984", author: "Orwell, George", page: "138", locStart: 2452, locEnd: 2456,
			addedAt: date(2022, 3, 6, 17, 58, 16), textPrefix: "Nothing perceptible changes"},
		{kind: Highlight, dialect: Kindle, title: "1984", author: "Orwell, George", page: "152", locStart: 2699, locEnd: 2700,
			addedAt: date(2022, 3, 7, 7, 47, 43), textPrefix: "One way to learn the depth"},
		// An extended highlight: same location, second is a superset.
		{kind: Highlight, dialect: Kindle, title: gtd, author: gtdAuthor, page: "4", locStart: 399, locEnd: 403,
			addedAt: date(2022, 3, 15, 22, 50, 24), textPrefix: "Three habits keep a garden", textLen: 636},
		{kind: Highlight, dialect: Kindle, title: gtd, author: gtdAuthor, page: "4", locStart: 399, locEnd: 403,
			addedAt: date(2022, 3, 15, 22, 50, 32), textPrefix: "Three habits keep a garden", textLen: 688},
		// A chain of five extensions, including one exact repeat.
		{kind: Highlight, dialect: Kindle, title: gtd, author: gtdAuthor, page: "116", locStart: 2217, locEnd: 2239,
			addedAt: date(2022, 3, 19, 9, 7, 6), textLen: 724},
		{kind: Highlight, dialect: Kindle, title: gtd, author: gtdAuthor, page: "116", locStart: 2217, locEnd: 2299,
			addedAt: date(2022, 3, 19, 9, 7, 29), textLen: 2331},
		{kind: Highlight, dialect: Kindle, title: gtd, author: gtdAuthor, page: "116", locStart: 2217, locEnd: 2299,
			addedAt: date(2022, 3, 19, 9, 7, 35), textLen: 2331},
		{kind: Highlight, dialect: Kindle, title: gtd, author: gtdAuthor, page: "116", locStart: 2217, locEnd: 2395,
			addedAt: date(2022, 3, 19, 9, 8, 8), textLen: 4459},
		{kind: Highlight, dialect: Kindle, title: gtd, author: gtdAuthor, page: "116", locStart: 2217, locEnd: 2399,
			addedAt: date(2022, 3, 19, 9, 8, 18), textLen: 4571},
		// Location only, no page.
		{kind: Highlight, dialect: Kindle, title: "The Courage to Be Disliked: The Japanese Phenomenon That Shows You How to Change Your Life and Achieve Real Happiness",
			author: "Kishimi, Ichiro", locStart: 137, locEnd: 140, addedAt: date(2022, 4, 19, 8, 8, 2)},
		// Roman-numeral page.
		{kind: Highlight, dialect: Kindle, title: "High Output Management", author: "Grove, Andrew S.", page: "xvi", locStart: 183, locEnd: 184,
			addedAt: date(2022, 5, 26, 20, 9, 20)},
		// Bookmarks: single location, empty body.
		{kind: Bookmark, dialect: Kindle, title: "An Elegant Puzzle: Systems of Engineering Management", author: "Larson, Will", page: "116",
			locStart: 1522, locEnd: 1522, addedAt: date(2022, 6, 14, 19, 20, 5)},
		{kind: Bookmark, dialect: Kindle, title: "Notes on Bridges #5", author: "bridges.example.com",
			locStart: 106, locEnd: 106, addedAt: date(2022, 8, 30, 20, 17, 15)},
		// Trailing spaces on the title line; the parenthetical is not an
		// author, but the format can't tell (see splitTitleAuthor).
		{kind: Highlight, dialect: Kindle, title: "Walden", author: "Or, Life in the Woods", page: "1", locStart: 13, locEnd: 15,
			addedAt: date(2023, 11, 6, 12, 13, 14)},
		// No author at all.
		{kind: Highlight, dialect: Kindle, title: "A Field Guide to Clouds", page: "4", locStart: 49, locEnd: 50,
			addedAt: date(2023, 11, 16, 8, 2, 41)},
		{kind: Highlight, dialect: Kindle, title: "Game Theory", author: "Very Short Introductions", page: "6", locStart: 217, locEnd: 218,
			addedAt: date(2025, 7, 27, 8, 10, 48)},
		// Series in parens before the author.
		{kind: Highlight, dialect: Kindle, title: "Fooled by Randomness (Incerto)", author: "Taleb, Nassim Nicholas", page: "14", locStart: 277, locEnd: 278,
			addedAt: date(2025, 11, 2, 22, 0, 46)},
	})
}

// testdata/crosspoint/clippings.txt is synthetic, in the format of a real
// CrossPoint export.
func TestParseCrossPoint(t *testing.T) {
	clips, warnings := parseFile(t, filepath.Join("..", "..", "testdata", "crosspoint", "clippings.txt"))
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	checkClips(t, clips, []want{
		{kind: Highlight, dialect: CrossPoint, title: "On Walking", author: "Henry David Thoreau", page: "49",
			section: "On Walking", addedAt: date(2026, 10, 24, 10, 19, 0), textPrefix: "I have met with but one or two"},
		{kind: Highlight, dialect: CrossPoint, title: "Self-Reliance", author: "Ralph Waldo Emerson", page: "9",
			section: "Self-Reliance", addedAt: date(2026, 10, 24, 11, 15, 0), textPrefix: "Trust thyself: every heart"},
	})
}

// testdata/kindle/edge-cases.txt is hand-written: the real export has no
// notes and no malformed entries.
func TestParseEdgeCases(t *testing.T) {
	clips, warnings := parseFile(t, filepath.Join("..", "..", "testdata", "kindle", "edge-cases.txt"))
	const title, author = "Meditations", "Marcus Aurelius"
	checkClips(t, clips, []want{
		{kind: Highlight, dialect: Kindle, title: title, author: author, page: "12", locStart: 200, locEnd: 203,
			addedAt: date(2026, 1, 5, 9, 15, 0), textPrefix: "The impediment to action"},
		{kind: Note, dialect: Kindle, title: title, author: author, page: "12", locStart: 203, locEnd: 203,
			addedAt: date(2026, 1, 5, 9, 15, 30), textPrefix: "obstacle is the way", textLen: len("obstacle is the way")},
		{kind: Highlight, dialect: Kindle, title: title, author: author, page: "40", locStart: 610, locEnd: 640,
			addedAt: date(2026, 1, 5, 9, 30, 0), textPrefix: "First paragraph.\nSecond paragraph.", textLen: len("First paragraph.\nSecond paragraph.")},
		// Unparseable date: kept with a zero time.
		{kind: Highlight, dialect: Kindle, title: title, author: author, page: "60", locStart: 800, locEnd: 801,
			textPrefix: "You have power over your mind"},
		// Final entry with no trailing separator.
		{kind: Highlight, dialect: Kindle, title: title, author: author, page: "70", locStart: 900, locEnd: 905,
			addedAt: date(2026, 1, 6, 7, 0, 0), textPrefix: "Unterminated final entry."},
	})

	wantReasons := []string{"unrecognized header", "has no text"}
	if len(warnings) != len(wantReasons) {
		t.Fatalf("warnings = %v, want %d", warnings, len(wantReasons))
	}
	for i, r := range wantReasons {
		if !strings.Contains(warnings[i].Reason, r) {
			t.Errorf("warning %d = %q, want it to mention %q", i, warnings[i].Reason, r)
		}
	}
}

func TestSplitTitleAuthor(t *testing.T) {
	tests := []struct {
		in, title, author string
	}{
		{"1984 (Orwell, George)", "1984", "Orwell, George"},
		{"The Wind in the Willows  ", "The Wind in the Willows", ""},
		{"Leaves of Grass (Deathbed Edition) (Whitman, Walt)", "Leaves of Grass (Deathbed Edition)", "Whitman, Walt"},
		{"(1) Notes on Tides (tides.example.com)", "(1) Notes on Tides", "tides.example.com"},
		{"Nested (Author (Ed.))", "Nested", "Author (Ed.)"},
		{"(Only Parens)", "(Only Parens)", ""},
		{"Unbalanced)", "Unbalanced)", ""},
	}
	for _, tt := range tests {
		title, author := splitTitleAuthor(tt.in)
		if title != tt.title || author != tt.author {
			t.Errorf("splitTitleAuthor(%q) = %q, %q; want %q, %q", tt.in, title, author, tt.title, tt.author)
		}
	}
}

func TestLocation(t *testing.T) {
	tests := []struct {
		c    Clipping
		want string
	}{
		{Clipping{Page: "4", LocStart: 399, LocEnd: 403}, "page 4, loc 399-403"},
		{Clipping{LocStart: 106, LocEnd: 106}, "loc 106"},
		{Clipping{Page: "49"}, "page 49"},
		{Clipping{}, ""},
	}
	for _, tt := range tests {
		if got := tt.c.Location(); got != tt.want {
			t.Errorf("Location() = %q, want %q", got, tt.want)
		}
	}
}

// TestParseFullExports runs against the untracked full exports, if present,
// to catch formats the trimmed fixtures miss.
func TestParseFullExports(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "*", "*-full.txt"))
	if len(paths) == 0 {
		t.Skip("no full exports in testdata")
	}
	for _, p := range paths {
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			clips, warnings := parseFile(t, p)
			if len(warnings) != 0 {
				t.Errorf("%d warnings, first: %v", len(warnings), warnings[0])
			}
			for _, c := range clips {
				if c.AddedAt.IsZero() {
					t.Errorf("%q %s: date did not parse", c.Title, c.Location())
				}
			}
			t.Logf("%d clippings", len(clips))
		})
	}
}
