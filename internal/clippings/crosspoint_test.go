package clippings

import (
	"strings"
	"testing"
)

// testdata/crosspoint/clippings.txt is synthetic, in the format of a real
// CrossPoint export.
func TestParseCrossPoint(t *testing.T) {
	clips, warnings := parseFile(t, fixture("crosspoint", "clippings.txt"), CrossPoint)
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

// CrossPoint is young firmware; header changes in a later release must
// produce warnings, not quietly misread entries.
func TestParseCrossPointDrift(t *testing.T) {
	const title = "Self-Reliance (Ralph Waldo Emerson)\n"
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"location added", "- Your Highlight on Page 9 | Self-Reliance | Location 10-12 | Added on Saturday, October 24, 2026, 11:15 AM", "unexpected extra field"},
		{"chapter dropped", "- Your Highlight on Page 9 | Added on Saturday, October 24, 2026, 11:15 AM", "unrecognized crosspoint header"},
		{"seconds added", "- Your Highlight on Page 9 | Self-Reliance | Added on Saturday, October 24, 2026, 11:15:30 AM", "unrecognized crosspoint date"},
		{"comma dropped", "- Your Highlight on Page 9 | Self-Reliance | Added on Saturday, October 24, 2026 11:15 AM", "unrecognized crosspoint date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clips, warnings, err := parseString(t, CrossPoint, crossPointEntry, title+tt.header+"\n\nSome text.")
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
