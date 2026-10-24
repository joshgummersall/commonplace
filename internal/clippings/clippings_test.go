package clippings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...)
}

func parseFile(t *testing.T, path string, dialect Dialect) ([]Clipping, []Warning) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	clips, warnings, err := Parse(f, dialect, time.UTC)
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
	textLen    int // bytes; 0 means don't check
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

// parseString parses an inline file with entries joined by separators.
func parseString(t *testing.T, dialect Dialect, entries ...string) ([]Clipping, []Warning, error) {
	t.Helper()
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e + "\n" + separator + "\n")
	}
	return Parse(strings.NewReader(b.String()), dialect, time.UTC)
}

const (
	kindleEntry     = "Meditations (Marcus Aurelius)\n- Your Highlight on page 12 | Location 200-203 | Added on Monday, January 5, 2026 9:15:00 AM\n\nThe impediment to action advances action."
	crossPointEntry = "Self-Reliance (Ralph Waldo Emerson)\n- Your Highlight on Page 9 | Self-Reliance | Added on Saturday, October 24, 2026, 11:15 AM\n\nTrust thyself."
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  Dialect
	}{
		{"kindle", kindleEntry, Kindle},
		{"crosspoint", crossPointEntry, CrossPoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clips, warnings, err := parseString(t, "", tt.entry)
			if err != nil || len(warnings) != 0 || len(clips) != 1 {
				t.Fatalf("got %d clips, warnings %v, err %v", len(clips), warnings, err)
			}
			if clips[0].Dialect != tt.want {
				t.Errorf("Dialect = %q, want %q", clips[0].Dialect, tt.want)
			}
		})
	}
}

func TestDetectFails(t *testing.T) {
	german := "Das Kapital (Marx, Karl)\n- Ihre Markierung bei Position 100-102 | Hinzugefügt am Montag, 5. Januar 2026 09:15:00\n\nAlles Ständische verdampft."
	_, _, err := parseString(t, "", german, kindleEntry)
	if err == nil || !strings.Contains(err.Error(), "cannot detect format") {
		t.Fatalf("err = %v, want detection failure", err)
	}
}

func TestMixedDialectsFail(t *testing.T) {
	tests := []struct {
		name    string
		dialect Dialect
		entries []string
		want    string
	}{
		{"detected kindle then crosspoint", "", []string{kindleEntry, crossPointEntry}, "crosspoint entry in a kindle file"},
		{"detected crosspoint then kindle", "", []string{crossPointEntry, kindleEntry}, "kindle entry in a crosspoint file"},
		{"wrong --source", CrossPoint, []string{kindleEntry}, "kindle entry in a crosspoint file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseString(t, tt.dialect, tt.entries...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestParseEmpty(t *testing.T) {
	clips, warnings, err := Parse(strings.NewReader("\ufeff\r\n"), "", time.UTC)
	if err != nil || len(clips) != 0 || len(warnings) != 0 {
		t.Fatalf("got %d clips, warnings %v, err %v", len(clips), warnings, err)
	}
}

func TestParseDialect(t *testing.T) {
	for _, s := range []string{"kindle", "crosspoint"} {
		if _, err := ParseDialect(s); err != nil {
			t.Errorf("ParseDialect(%q): %v", s, err)
		}
	}
	if _, err := ParseDialect("kobo"); err == nil {
		t.Error("ParseDialect(\"kobo\") succeeded, want error")
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
	paths, _ := filepath.Glob(fixture("*", "*-full.txt"))
	if len(paths) == 0 {
		t.Skip("no full exports in testdata")
	}
	for _, p := range paths {
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			clips, warnings := parseFile(t, p, "")
			if len(warnings) != 0 {
				t.Errorf("%d warnings, first: %v", len(warnings), warnings[0])
			}
			t.Logf("%d clippings", len(clips))
		})
	}
}
