package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// addHighlights stores n highlights in each of the named books and returns
// their ids in order.
func addHighlights(t *testing.T, s *Store, n int, books ...string) []int64 {
	t.Helper()
	var ids []int64
	err := s.InTx(func(tx *Tx) error {
		for _, title := range books {
			book, err := tx.BookID(title, "")
			if err != nil {
				return err
			}
			for i := range n {
				h := &Highlight{BookID: book, Text: fmt.Sprintf("%s %d", title, i), Hash: fmt.Sprintf("%s-%d", title, i)}
				if err := tx.InsertHighlight(h); err != nil {
					return err
				}
				ids = append(ids, h.ID)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// states returns a highlight's tags by name, with their states.
func states(t *testing.T, s *Store, hid int64) map[string]string {
	t.Helper()
	rows, err := s.db.Query(`SELECT g.name, ht.state FROM highlight_tags ht JOIN tags g ON g.id = ht.tag_id
		WHERE ht.highlight_id = ?`, hid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			t.Fatal(err)
		}
		out[name] = state
	}
	return out
}

func tagByName(t *testing.T, s *Store, name string) Tag {
	t.Helper()
	tags, err := s.Tags()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == name {
			return tag
		}
	}
	t.Fatalf("no tag %q in %+v", name, tags)
	return Tag{}
}

func TestSetTagsConfirmsAddsAndRemoves(t *testing.T) {
	s := openTemp(t)
	h := addHighlights(t, s, 1, "Book")[0]
	if err := s.SeedTags([]SeedTag{{Name: "Risk", Members: []int64{h}}, {Name: "Luck", Members: []int64{h}}}); err != nil {
		t.Fatal(err)
	}
	// Typed in another case, "risk" is the same tag.
	if err := s.SetTags(h, []string{"risk", " Chance  of  ruin "}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Risk": TagYes, "Luck": TagNo, "Chance of ruin": TagYes}
	if got := states(t, s, h); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("states = %v, want %v", got, want)
	}
	refs, err := s.TagsOf([]int64{h})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range refs[h] {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"Chance of ruin", "Risk"}) {
		t.Errorf("TagsOf = %v, want the two kept, by name", names)
	}
	if err := s.SetTags(h, []string{"a, b"}); err == nil {
		t.Error("a name with a comma was accepted")
	}
	if err := s.SetTags(9999, []string{"x"}); err != ErrNotFound {
		t.Errorf("unknown highlight: err = %v, want ErrNotFound", err)
	}
}

func TestConfirmTags(t *testing.T) {
	s := openTemp(t)
	h := addHighlights(t, s, 1, "Book")[0]
	if err := s.SeedTags([]SeedTag{{Name: "Risk", Members: []int64{h}}}); err != nil {
		t.Fatal(err)
	}
	if tag := tagByName(t, s, "Risk"); tag.Suggested != 1 {
		t.Errorf("suggested = %d, want 1", tag.Suggested)
	}
	if err := s.ConfirmTags(h); err != nil {
		t.Fatal(err)
	}
	if tag := tagByName(t, s, "Risk"); tag.Count != 1 || tag.Suggested != 0 {
		t.Errorf("after confirming: %+v", tag)
	}
}

func TestRenameTagMergesIntoExisting(t *testing.T) {
	s := openTemp(t)
	ids := addHighlights(t, s, 4, "Book")
	if err := s.SeedTags([]SeedTag{
		{Name: "Growth", Members: ids},
		{Name: "Personal growth", Members: ids},
	}); err != nil {
		t.Fatal(err)
	}
	set := func(hid int64, name, state string) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE highlight_tags SET state = ?
			WHERE highlight_id = ? AND tag_id = (SELECT id FROM tags WHERE name = ?)`, state, hid, name); err != nil {
			t.Fatal(err)
		}
	}
	// ids[0]: set on one side wins. ids[1]: removed on one side beats a
	// suggestion. ids[2]: removed beats nothing. ids[3]: suggested on both
	// stays suggested.
	set(ids[0], "Personal growth", TagYes)
	set(ids[1], "Personal growth", TagNo)
	set(ids[2], "Growth", TagNo)
	if _, err := s.db.Exec(`DELETE FROM highlight_tags WHERE highlight_id = ?
		AND tag_id = (SELECT id FROM tags WHERE name = 'Personal growth')`, ids[2]); err != nil {
		t.Fatal(err)
	}
	from, into := tagByName(t, s, "Personal growth"), tagByName(t, s, "Growth")
	got, err := s.RenameTag(from.ID, "growth")
	if err != nil {
		t.Fatal(err)
	}
	if got != into.ID {
		t.Errorf("merged into %d, want %d", got, into.ID)
	}
	for i, want := range []string{TagYes, TagNo, TagNo, TagAuto} {
		if st := states(t, s, ids[i]); len(st) != 1 || st["Growth"] != want {
			t.Errorf("highlight %d: %v, want Growth %s", i, st, want)
		}
	}
	if n, _ := s.CountTags(); n != 1 {
		t.Errorf("%d tags after merging, want 1", n)
	}
}

func TestMergeTag(t *testing.T) {
	s := openTemp(t)
	ids := addHighlights(t, s, 3, "Book")
	if err := s.SeedTags([]SeedTag{{Name: "Odds", Members: ids[:2]}, {Name: "Probability", Members: ids[1:]}}); err != nil {
		t.Fatal(err)
	}
	from, into := tagByName(t, s, "Odds"), tagByName(t, s, "Probability")
	if err := s.MergeTag(from.ID, from.ID); err == nil {
		t.Error("a tag merged into itself")
	}
	if err := s.MergeTag(from.ID, 9999); err != ErrNotFound {
		t.Errorf("merging into a missing tag: err = %v, want ErrNotFound", err)
	}
	if err := s.MergeTag(from.ID, into.ID); err != nil {
		t.Fatal(err)
	}
	if got := tagByName(t, s, "Probability"); got.Count != 3 {
		t.Errorf("merged tag has %d highlights, want 3", got.Count)
	}
	if n, _ := s.CountTags(); n != 1 {
		t.Errorf("%d tags after merging, want 1", n)
	}
}

func TestRenameTagChangesCase(t *testing.T) {
	s := openTemp(t)
	h := addHighlights(t, s, 1, "Book")[0]
	if err := s.SetTags(h, []string{"bayes"}); err != nil {
		t.Fatal(err)
	}
	tag := tagByName(t, s, "bayes")
	if id, err := s.RenameTag(tag.ID, "Bayes"); err != nil || id != tag.ID {
		t.Fatalf("RenameTag = %d, %v", id, err)
	}
	tagByName(t, s, "Bayes")
	if _, err := s.RenameTag(9999, "x"); err != ErrNotFound {
		t.Errorf("unknown tag: err = %v, want ErrNotFound", err)
	}
}

func TestTagSummariesAndDelete(t *testing.T) {
	s := openTemp(t)
	ids := addHighlights(t, s, 2, "A", "B", "C")
	if err := s.SeedTags([]SeedTag{{Name: "Risk", Members: ids[:5]}}); err != nil {
		t.Fatal(err)
	}
	// Removing the tag from a highlight takes it out of every count.
	if err := s.SetTags(ids[4], nil); err != nil {
		t.Fatal(err)
	}
	tag := tagByName(t, s, "Risk")
	if tag.Count != 4 || tag.Suggested != 4 || tag.Books != 2 {
		t.Errorf("tag = %+v, want 4 highlights, 4 suggested, 2 books", tag)
	}
	if len(tag.TopBooks) != 2 || tag.TopBooks[0].Title != "A" || tag.TopBooks[0].Count != 2 {
		t.Errorf("top books = %+v", tag.TopBooks)
	}
	members, err := s.TagMemberIDs(tag.ID)
	if err != nil || len(members) != 4 || members[ids[4]] {
		t.Errorf("members = %v, %v", members, err)
	}
	if n, _ := s.UntaggedCount(); n != 2 {
		t.Errorf("untagged = %d, want 2", n)
	}
	if err := s.DeleteTag(tag.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UntaggedCount(); n != 6 {
		t.Errorf("untagged after delete = %d, want 6", n)
	}
	if err := s.DeleteTag(tag.ID); err != ErrNotFound {
		t.Errorf("deleting again: err = %v, want ErrNotFound", err)
	}
}

func TestSeedTagsOnlyIntoAnEmptyLibrary(t *testing.T) {
	s := openTemp(t)
	ids := addHighlights(t, s, 2, "Book")
	// Names that match ignoring case merge.
	if err := s.SeedTags([]SeedTag{{Name: "Risk", Members: ids[:1]}, {Name: "risk", Members: ids[1:]}}); err != nil {
		t.Fatal(err)
	}
	if tag := tagByName(t, s, "Risk"); tag.Count != 2 {
		t.Errorf("merged seed has %d highlights, want 2", tag.Count)
	}
	if err := s.SeedTags([]SeedTag{{Name: "Luck", Members: ids}}); err != ErrTagsExist {
		t.Errorf("err = %v, want ErrTagsExist", err)
	}
}

func TestSuggestTagsKeepsExisting(t *testing.T) {
	s := openTemp(t)
	ids := addHighlights(t, s, 2, "Book")
	if err := s.SetTags(ids[0], []string{"Risk"}); err != nil {
		t.Fatal(err)
	}
	risk := tagByName(t, s, "Risk")
	n, err := s.SuggestTags(map[int64][]int64{ids[0]: {risk.ID}, ids[1]: {risk.ID, 9999}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("added %d, want 1 (the unknown tag is skipped, the set one kept)", n)
	}
	if st := states(t, s, ids[0]); st["Risk"] != TagYes {
		t.Errorf("a suggestion overwrote a set tag: %v", st)
	}
}

func TestMigrateThemesToTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{db: db}
	if err := old.migrateTo(7); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO books (id, title, author) VALUES (1, 'Meditations', '')`,
		`INSERT INTO highlights (id, book_id, source, text, imported_at, hash) VALUES
			(1, 1, 'kindle', 'one', '2026-01-01T00:00:00Z', 'h1'),
			(2, 1, 'kindle', 'two', '2026-01-01T00:00:00Z', 'h2'),
			(3, 1, 'kindle', 'three', '2026-01-01T00:00:00Z', 'h3')`,
		// Two areas share a name; a theme nests in the first.
		`INSERT INTO themes (id, parent_id, name, renamed, position) VALUES
			(1, NULL, 'Personal growth', 0, 0), (2, NULL, 'Personal growth', 0, 1),
			(3, 1, 'Habits', 0, 0), (4, NULL, 'Mine', 1, 2)`,
		`INSERT INTO theme_members (theme_id, highlight_id, rank) VALUES
			(1, 1, 0), (2, 2, 0), (3, 1, 0), (4, 3, 0)`,
		`INSERT INTO theme_refresh (id, refreshed_at, highlights) VALUES (1, '2026-01-01T00:00:00Z', 3)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tags, err := s.Tags()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tag := range tags {
		got = append(got, fmt.Sprintf("%s:%d/%d", tag.Name, tag.Count, tag.Suggested))
	}
	if want := []string{"Mine:1/1", "Personal growth:2/2"}; !slices.Equal(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
}

func TestCountsLeaveOutBuried(t *testing.T) {
	s := openTemp(t)
	ids := addHighlights(t, s, 2, "A", "B")
	if err := s.SeedTags([]SeedTag{{Name: "Risk", Members: ids[:3]}}); err != nil {
		t.Fatal(err)
	}
	// Bury both of book A's highlights, which are tagged.
	for _, id := range ids[:2] {
		if err := s.SetBuried(id, true); err != nil {
			t.Fatal(err)
		}
	}
	if tag := tagByName(t, s, "Risk"); tag.Count != 1 || tag.Suggested != 1 || tag.Books != 1 || len(tag.TopBooks) != 1 {
		t.Errorf("tag = %+v, want only book B's one highlight", tag)
	}
	if n, _ := s.UntaggedCount(); n != 1 {
		t.Errorf("untagged = %d, want 1", n)
	}
	if n, _ := s.UnconfirmedCount(); n != 1 {
		t.Errorf("unconfirmed = %d, want 1", n)
	}
	books, _ := s.Books(false)
	all, _ := s.Books(true)
	if len(books) != 1 || books[0].Title != "B" || len(all) != 2 {
		t.Errorf("books = %+v without buried, %+v with", books, all)
	}
}

// Errors about bad input match ErrInvalid but read as just the problem.
func TestValidationErrorsAreInvalid(t *testing.T) {
	s := openTemp(t)
	ids := addHighlights(t, s, 2, "Book")
	if err := s.SeedTags([]SeedTag{{Name: "Odds", Members: ids[:1]}, {Name: "Probability", Members: ids[1:]}}); err != nil {
		t.Fatal(err)
	}
	odds := tagByName(t, s, "Odds")
	_, renameErr := s.RenameTag(odds.ID, " ")
	_, addErr := s.AddNote(ids[0], " ", time.Now())
	for _, c := range []struct {
		err  error
		want string
	}{
		{renameErr, "a tag needs a name"},
		{s.SetTags(ids[0], []string{"a,b"}), "a tag name can't hold a comma"},
		{s.SetTags(ids[0], []string{strings.Repeat("x", MaxTagName+1)}), fmt.Sprintf("keep tag names under %d characters", MaxTagName)},
		{s.MergeTag(odds.ID, odds.ID), "a tag can't merge into itself"},
		{addErr, "note is empty"},
		{s.UpdateNote(1, " ", time.Now()), "note is empty"},
	} {
		if !errors.Is(c.err, ErrInvalid) {
			t.Errorf("%v does not match ErrInvalid", c.err)
		}
		if c.err == nil || c.err.Error() != c.want {
			t.Errorf("err = %v, want %q", c.err, c.want)
		}
	}
	if err := s.MergeTag(odds.ID, 9999); errors.Is(err, ErrInvalid) {
		t.Errorf("a missing tag matches ErrInvalid: %v", err)
	}
}
