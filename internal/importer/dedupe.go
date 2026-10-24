package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/joshgummersall/commonplace/internal/clippings"
)

// Dedupe is string-based on purpose: embeddings would merge distinct
// passages that make the same point.
//
// Two highlights in the same book at overlapping locations are versions of
// one highlight when their normalized text is identical, one contains the
// other (extended or trimmed), or they share at least sameThreshold of the
// shorter one's word 3-shingles (both ends moved). The latest version wins,
// even when it trimmed text away: the final edit is the one that matters.
//
// Overlapping locations alone are not enough: in a real 1,700-highlight
// Kindle export, 124 of 318 overlapping pairs were distinct adjacent
// passages sharing a boundary location. In that export, pairs with both
// ends moved scored 0.67 and 0.71, and distinct pairs scored at most 0.45;
// the threshold sits in that gap.
const sameThreshold = 0.5

const shingleSize = 3

var normalizeReplacer = strings.NewReplacer(
	"‘", "'", "’", "'", // curly single quotes
	"“", `"`, "”", `"`, // curly double quotes
	"—", "-", "–", "-", // em and en dashes
	"…", "...", // ellipsis
	" ", " ", // non-breaking space
)

// normalize lowercases, straightens typographic punctuation, and collapses
// whitespace, so formatting differences don't defeat dedupe.
func normalize(s string) string {
	s = normalizeReplacer.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// hashText is the dedupe key stored in highlights.hash.
func hashText(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:16])
}

// sameHighlight reports whether two normalized texts at overlapping
// locations are versions of one highlight.
func sameHighlight(a, b string) bool {
	if a == b || strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	return overlap(shingles(a), shingles(b)) >= sameThreshold
}

func shingles(s string) map[string]struct{} {
	words := strings.Fields(s)
	out := make(map[string]struct{})
	for i := 0; i+shingleSize <= len(words); i++ {
		out[strings.Join(words[i:i+shingleSize], " ")] = struct{}{}
	}
	return out
}

// overlap is the overlap coefficient |A∩B| / min(|A|, |B|). Unlike Jaccard,
// it stays high when a short highlight is edited into a much longer one.
func overlap(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	n := 0
	for k := range a {
		if _, ok := b[k]; ok {
			n++
		}
	}
	return float64(n) / float64(len(a))
}

// version is one highlight entry from a file, prepared for grouping.
type version struct {
	clip clippings.Clipping
	norm string
}

// overlaps reports whether two entries are close enough in the book to be
// versions of one highlight: overlapping location ranges, or the same page
// when there are no locations (CrossPoint).
func overlaps(a, b clippings.Clipping) bool {
	if a.LocStart > 0 && b.LocStart > 0 {
		return a.LocStart <= b.LocEnd && b.LocStart <= a.LocEnd
	}
	return a.LocStart == 0 && b.LocStart == 0 && a.Page == b.Page
}

// latestVersions replays one book's entries in time order, as the device
// applied them: each entry replaces any current highlight it is a version
// of. What remains is the device's final set, so a chain of edits keeps
// only its last version, while a highlight split into two pieces keeps
// both. dropped counts the entries replaced along the way.
//
// Replaying the whole file before touching the database makes the result
// independent of what's already stored, which keeps re-imports idempotent.
func latestVersions(vs []version) (kept []version, dropped int) {
	ordered := slices.Clone(vs)
	slices.SortStableFunc(ordered, func(a, b version) int {
		return a.clip.AddedAt.Compare(b.clip.AddedAt)
	})
	for _, v := range ordered {
		kept = slices.DeleteFunc(kept, func(cur version) bool {
			return overlaps(cur.clip, v.clip) && sameHighlight(cur.norm, v.norm)
		})
		kept = append(kept, v)
	}
	return kept, len(vs) - len(kept)
}

// newerAt reports whether version a is a later edit than b. Ties
// (CrossPoint has minute resolution) go to the longer text; a missing date
// loses.
func newerAt(aAt, bAt time.Time, aLen, bLen int) bool {
	switch {
	case aAt.IsZero() != bAt.IsZero():
		return bAt.IsZero()
	case !aAt.Equal(bAt):
		return aAt.After(bAt)
	default:
		return aLen > bLen
	}
}
