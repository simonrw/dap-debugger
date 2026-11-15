package ui

import (
	"slices"
	"testing"
)

func TestPickerListsEveryItemForEmptyQuery(t *testing.T) {
	items := []string{"b.go", "a.go", "dir/c.go"}

	for _, fuzzy := range []bool{true, false} {
		md := newPicker("pick", items, 0, fuzzy, nil)

		if !slices.Equal(md.filtered, []int{0, 1, 2}) {
			t.Errorf("fuzzy=%v: empty query filtered %v, want every item in order", fuzzy, md.filtered)
		}
	}
}
