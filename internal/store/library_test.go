package store

import (
	"errors"
	"testing"
)

func TestToggleFavorite(t *testing.T) {
	s := openTemp(t)
	id := addHighlights(t, s, 1, "A")[0]
	for _, want := range []bool{true, false} {
		got, err := s.ToggleFavorite(id)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("ToggleFavorite = %v, want %v", got, want)
		}
		it, err := s.Item(id, false)
		if err != nil {
			t.Fatal(err)
		}
		if it.Favorite != want {
			t.Errorf("stored favorite = %v, want %v", it.Favorite, want)
		}
	}
	if _, err := s.ToggleFavorite(id + 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing highlight: err = %v, want ErrNotFound", err)
	}
}
