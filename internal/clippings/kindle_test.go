package clippings

import (
	"strings"
	"testing"
)

// testdata/kindle/clippings.txt is synthetic: it mirrors a real Kindle export
// entry by entry (headers, locations, dates, BOMs, text lengths) with
// invented text.
func TestParseKindle(t *testing.T) {
	clips, warnings := parseFile(t, fixture("kindle", "clippings.txt"), Kindle)
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
		// Trailing spaces on the title line mean no author, so the
		// parenthetical stays in the title (see splitTitleAuthor).
		{kind: Highlight, dialect: Kindle, title: "Walden (Or, Life in the Woods)", page: "1", locStart: 13, locEnd: 15,
			addedAt: date(2023, 11, 6, 12, 13, 14)},
		// No author at all.
		{kind: Highlight, dialect: Kindle, title: "A Field Guide to Clouds", page: "4", locStart: 49, locEnd: 50,
			addedAt: date(2023, 11, 16, 8, 2, 41)},
		{kind: Highlight, dialect: Kindle, title: "Game Theory (Very Short Introductions)", page: "6", locStart: 217, locEnd: 218,
			addedAt: date(2025, 7, 27, 8, 10, 48)},
		// Series in parens before the author.
		{kind: Highlight, dialect: Kindle, title: "Fooled by Randomness (Incerto)", author: "Taleb, Nassim Nicholas", page: "14", locStart: 277, locEnd: 278,
			addedAt: date(2025, 11, 2, 22, 0, 46)},
	})
}

// testdata/kindle/edge-cases.txt is hand-written: the real export has no
// notes and no malformed entries.
func TestParseKindleEdgeCases(t *testing.T) {
	clips, warnings := parseFile(t, fixture("kindle", "edge-cases.txt"), "")
	const title, author = "Meditations", "Marcus Aurelius"
	checkClips(t, clips, []want{
		{kind: Highlight, dialect: Kindle, title: title, author: author, page: "12", locStart: 200, locEnd: 203,
			addedAt: date(2026, 1, 5, 9, 15, 0), textPrefix: "The impediment to action"},
		{kind: Note, dialect: Kindle, title: title, author: author, page: "12", locStart: 203, locEnd: 203,
			addedAt: date(2026, 1, 5, 9, 15, 30), textPrefix: "obstacle is the way", textLen: len("obstacle is the way")},
		{kind: Highlight, dialect: Kindle, title: title, author: author, page: "40", locStart: 610, locEnd: 640,
			addedAt: date(2026, 1, 5, 9, 30, 0), textPrefix: "First paragraph.\nSecond paragraph.", textLen: len("First paragraph.\nSecond paragraph.")},
		// Final entry with no trailing separator.
		{kind: Highlight, dialect: Kindle, title: title, author: author, page: "70", locStart: 900, locEnd: 905,
			addedAt: date(2026, 1, 6, 7, 0, 0), textPrefix: "Unterminated final entry."},
	})

	wantReasons := []string{"unrecognized kindle header", "has no text", "unrecognized kindle date"}
	if len(warnings) != len(wantReasons) {
		t.Fatalf("warnings = %v, want %d", warnings, len(wantReasons))
	}
	for i, r := range wantReasons {
		if !strings.Contains(warnings[i].Reason, r) {
			t.Errorf("warning %d = %q, want it to mention %q", i, warnings[i].Reason, r)
		}
	}
}

// Header changes a firmware update could plausibly make must produce
// warnings, not quietly misread entries.
func TestParseKindleDrift(t *testing.T) {
	const title = "Meditations (Marcus Aurelius)\n"
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"location dropped", "- Your Highlight on page 12 | Added on Monday, January 5, 2026 9:15:00 AM", "unrecognized kindle header"},
		{"field added", "- Your Highlight on page 12 | Location 200-203 | Chapter 3 | Added on Monday, January 5, 2026 9:15:00 AM", "unrecognized kindle header"},
		{"page capitalized", "- Your Highlight on Page 12 | Location 200-203 | Added on Monday, January 5, 2026 9:15:00 AM", "unrecognized kindle header"},
		{"seconds dropped", "- Your Highlight on page 12 | Location 200-203 | Added on Monday, January 5, 2026 9:15 AM", "unrecognized kindle date"},
		{"24-hour clock", "- Your Highlight on page 12 | Location 200-203 | Added on Monday, January 5, 2026 21:15:00", "unrecognized kindle date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clips, warnings, err := parseString(t, Kindle, kindleEntry, title+tt.header+"\n\nSome text.")
			if err != nil {
				t.Fatal(err)
			}
			if len(clips) != 1 || len(warnings) != 1 {
				t.Fatalf("got %d clips, warnings %v; want 1 clip, 1 warning", len(clips), warnings)
			}
			if !strings.Contains(warnings[0].Reason, tt.want) {
				t.Errorf("warning = %q, want it to mention %q", warnings[0].Reason, tt.want)
			}
		})
	}
}
