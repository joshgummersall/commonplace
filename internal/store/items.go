package store

// Item is a highlight with what a list shows beside it: how many notes it
// has and the most specific theme it is in. The CLI and the web app both
// build their output from it.
type Item struct {
	Entry
	NoteCount int
	Theme     *ThemeRef // nil when the highlight is in no theme
	// Notes is set only when Items is asked for them.
	Notes []Note
}

// Items adds note counts and themes to entries, in order. withNotes also
// loads each highlight's notes in full.
func (s *Store) Items(entries []Entry, withNotes bool) ([]Item, error) {
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	counts, err := s.NoteCounts(ids)
	if err != nil {
		return nil, err
	}
	themes, err := s.ThemesOf(ids)
	if err != nil {
		return nil, err
	}
	out := make([]Item, len(entries))
	for i, e := range entries {
		out[i] = Item{Entry: e, NoteCount: counts[e.ID]}
		if t, ok := themes[e.ID]; ok {
			out[i].Theme = &t
		}
		if withNotes {
			if out[i].Notes, err = s.Notes(e.ID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// Item loads one highlight with its note count and theme, or returns
// ErrNotFound.
func (s *Store) Item(id int64, withNotes bool) (Item, error) {
	e, err := s.Entry(id)
	if err != nil {
		return Item{}, err
	}
	items, err := s.Items([]Entry{e}, withNotes)
	if err != nil {
		return Item{}, err
	}
	return items[0], nil
}
