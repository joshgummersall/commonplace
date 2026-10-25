package store

import (
	"fmt"
	"testing"
	"time"
)

func TestThemeTreeCountsAndTopBooks(t *testing.T) {
	s := openTemp(t)
	// Books 1-4 hold 4, 3, 2, and 1 highlights; the area holds them all
	// and its theme holds the first two books' highlights.
	var all, firstTwo []int64
	err := s.InTx(func(tx *Tx) error {
		for b := 1; b <= 4; b++ {
			book, err := tx.BookID(fmt.Sprintf("Book %d", b), "")
			if err != nil {
				return err
			}
			for i := range 5 - b {
				h := &Highlight{BookID: book, Text: fmt.Sprintf("b%d h%d", b, i), Hash: fmt.Sprintf("%d-%d", b, i)}
				if err := tx.InsertHighlight(h); err != nil {
					return err
				}
				all = append(all, h.ID)
				if b <= 2 {
					firstTwo = append(firstTwo, h.ID)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	areas := []Area{{Theme: Theme{Name: "Area", Members: all}, Themes: []Theme{{Name: "Theme", Members: firstTwo}}}}
	if err := s.ReplaceThemes(areas, ThemeRefresh{At: time.Now(), Highlights: len(all)}); err != nil {
		t.Fatal(err)
	}

	tree, err := s.ThemeTree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || len(tree[0].Themes) != 1 {
		t.Fatalf("tree = %+v", tree)
	}
	area, theme := tree[0], tree[0].Themes[0]
	if area.Count != 10 || area.Books != 4 || area.Children != 1 {
		t.Errorf("area = %d highlights, %d books, %d children; want 10, 4, 1", area.Count, area.Books, area.Children)
	}
	if theme.Count != 7 || theme.Books != 2 || theme.Children != 0 {
		t.Errorf("theme = %d highlights, %d books, %d children; want 7, 2, 0", theme.Count, theme.Books, theme.Children)
	}
	if got := bookTitles(area.TopBooks); got != "[Book 1:4 Book 2:3 Book 3:2]" {
		t.Errorf("area top books = %s", got)
	}
	if got := bookTitles(theme.TopBooks); got != "[Book 1:4 Book 2:3]" {
		t.Errorf("theme top books = %s", got)
	}

	one, err := s.ThemeSummary(area.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.Children != 1 || bookTitles(one.TopBooks) != bookTitles(area.TopBooks) {
		t.Errorf("ThemeSummary = %+v, want it to match the tree's area", one)
	}
}

func bookTitles(bs []BookCount) string {
	var out []string
	for _, b := range bs {
		out = append(out, fmt.Sprintf("%s:%d", b.Title, b.Count))
	}
	return fmt.Sprint(out)
}
