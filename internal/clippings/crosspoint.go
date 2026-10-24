package clippings

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// crossPointHeaderRe matches:
//
//	"- Your Highlight on Page 49 | <chapter> | Added on <date>"
var crossPointHeaderRe = regexp.MustCompile(
	`^- Your (Highlight|Note|Bookmark) on Page (\S+) \| (.+) \| Added on (.+)$`)

const crossPointDateLayout = "Monday, January 2, 2006, 3:04 PM"

func parseCrossPointHeader(header string, loc *time.Location, c *Clipping) error {
	m := crossPointHeaderRe.FindStringSubmatch(header)
	if m == nil {
		return errNotThisDialect
	}
	// The chapter is free text, so a field added by a firmware update
	// (say, "| Location 10-12 |") would otherwise be absorbed into it.
	// Chapters containing " | " are rare enough to treat as drift too.
	if strings.Contains(m[3], " | ") {
		return fmt.Errorf("unexpected extra field in crosspoint header %q", header)
	}
	c.Kind = parseKind(m[1])
	c.Page = m[2]
	c.Section = m[3]
	t, err := time.ParseInLocation(crossPointDateLayout, m[4], loc)
	if err != nil {
		return fmt.Errorf("unrecognized crosspoint date %q", m[4])
	}
	c.AddedAt = t
	return nil
}
