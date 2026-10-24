package clippings

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// kindleHeaderRe matches the two header shapes seen in real exports:
//
//	"- Your Highlight on page 4 | Location 399-403 | Added on <date>"
//	"- Your Bookmark on Location 106 | Added on <date>"
//
// The page is optional (some books have none) and may be a Roman numeral.
var kindleHeaderRe = regexp.MustCompile(
	`^- Your (Highlight|Note|Bookmark) on (?:page (\S+) \| )?Location (\d+)(?:-(\d+))? \| Added on (.+)$`)

const kindleDateLayout = "Monday, January 2, 2006 3:04:05 PM"

func parseKindleHeader(header string, loc *time.Location, c *Clipping) error {
	m := kindleHeaderRe.FindStringSubmatch(header)
	if m == nil {
		return errNotThisDialect
	}
	c.Kind = parseKind(m[1])
	c.Page = m[2]
	c.LocStart, _ = strconv.Atoi(m[3])
	c.LocEnd = c.LocStart
	if m[4] != "" {
		c.LocEnd, _ = strconv.Atoi(m[4])
	}
	t, err := time.ParseInLocation(kindleDateLayout, m[5], loc)
	if err != nil {
		return fmt.Errorf("unrecognized kindle date %q", m[5])
	}
	c.AddedAt = t
	return nil
}
