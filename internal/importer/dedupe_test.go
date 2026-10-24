package importer

import (
	"testing"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
)

func TestNormalize(t *testing.T) {
	got := normalize("  “Don’t”  stop—\nnow… OK ")
	want := `"don't" stop- now... ok`
	if got != want {
		t.Errorf("normalize() = %q, want %q", got, want)
	}
}

// The pairs mirror ones from a real Kindle export, with the same overlap
// scores; see the sameThreshold comment.
func TestSameHighlight(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "the impediment to action", "the impediment to action", true},
		{"extended", "the impediment to action", "the impediment to action advances action", true},
		{"trimmed", "the impediment to action advances action", "advances action", true},
		{"both ends moved (0.67)",
			"all of the light. if you do not walk before dawn, then the path stays hidden from you. this kind of patience is rare - most",
			"never set out on a trail without the hope of catching all of the light. if you do not walk before dawn, then the path stays hidden from you.",
			true},
		{"overlapping context, distinct (0.45)",
			"along with careful pruning and steady watering in the spring, the first few seasons of tending a new garden should be patient",
			"the first few seasons of tending a new garden should be patient. these early seasons are vital for building roots and for showing the soil what kind of gardener you are.",
			false},
		{"adjacent passages", "effective executives know where their time goes.", "effective executives focus on outward contribution.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameHighlight(tt.a, tt.b); got != tt.want {
				t.Errorf("sameHighlight() = %v, want %v (overlap %.2f)", got, tt.want, overlap(shingles(tt.a), shingles(tt.b)))
			}
		})
	}
}

func clip(locStart, locEnd int, minute int, text string) clippings.Clipping {
	return clippings.Clipping{
		Kind: clippings.Highlight, Dialect: clippings.Kindle,
		Title: "The Black Swan", Author: "Taleb, Nassim Nicholas",
		LocStart: locStart, LocEnd: locEnd,
		AddedAt: time.Date(2025, 3, 27, 15, minute, 0, 0, time.UTC),
		Text:    text,
	}
}

func versions(cs ...clippings.Clipping) []version {
	var out []version
	for _, c := range cs {
		out = append(out, version{clip: c, norm: normalize(c.Text)})
	}
	return out
}

func texts(vs []version) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.clip.Text)
	}
	return out
}

func TestLatestVersions(t *testing.T) {
	const (
		first  = "the stones remember every flood that moved them."
		second = "we forget the quiet stones that never moved at all."
		both   = first + " " + second
	)
	tests := []struct {
		name        string
		in          []clippings.Clipping
		want        []string
		wantDropped int
	}{
		{"extend chain keeps the last",
			[]clippings.Clipping{clip(10, 10, 1, first), clip(10, 11, 2, both)},
			[]string{both}, 1},
		{"trim keeps the trimmed",
			[]clippings.Clipping{clip(10, 11, 1, both), clip(10, 10, 2, first)},
			[]string{first}, 1},
		// Extended, then split into two pieces: the device ends with both.
		{"split keeps both pieces",
			[]clippings.Clipping{clip(10, 10, 1, first), clip(10, 11, 2, both), clip(11, 11, 3, second), clip(10, 10, 4, first)},
			[]string{second, first}, 2},
		{"adjacent distinct highlights stay apart",
			[]clippings.Clipping{clip(10, 11, 1, first), clip(11, 12, 2, "an unrelated sentence about something else entirely.")},
			[]string{first, "an unrelated sentence about something else entirely."}, 0},
		{"same text far apart stays apart",
			[]clippings.Clipping{clip(10, 10, 1, first), clip(500, 500, 2, first)},
			[]string{first, first}, 0},
		// Replay follows time, not file order.
		{"out of order file",
			[]clippings.Clipping{clip(10, 11, 2, both), clip(10, 10, 1, first)},
			[]string{both}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, dropped := latestVersions(versions(tt.in...))
			got := texts(kept)
			if len(got) != len(tt.want) {
				t.Fatalf("kept %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("kept %q, want %q", got, tt.want)
					break
				}
			}
			if dropped != tt.wantDropped {
				t.Errorf("dropped = %d, want %d", dropped, tt.wantDropped)
			}
		})
	}
}

func TestOverlapsCrossPointUsesPage(t *testing.T) {
	a := clippings.Clipping{Page: "49"}
	if !overlaps(a, clippings.Clipping{Page: "49"}) {
		t.Error("same page without locations should overlap")
	}
	if overlaps(a, clippings.Clipping{Page: "50"}) {
		t.Error("different pages should not overlap")
	}
	if overlaps(a, clippings.Clipping{Page: "49", LocStart: 10, LocEnd: 12}) {
		t.Error("an entry with locations should not match one without")
	}
}
