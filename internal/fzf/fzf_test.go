package fzf

import (
	"slices"
	"testing"
)

func TestFilter(t *testing.T) {
	paths := []string{
		"core/tests/aws/services/redshift/v2_provider.py",
		"core/services/sts/provider_basic.py",
		"core/services/sts/provider.py",
		"core/services/s3/models.py",
		"README.md",
	}

	tests := []struct {
		name  string
		query string
		items []string
		want  []int
	}{
		{name: "word boundaries beat scattered characters", query: "stsprovider", items: paths, want: []int{2, 1, 0}},
		{name: "terms must all match", query: "sts basic", items: paths, want: []int{1}},
		{name: "lowercase query ignores case", query: "readme", items: paths, want: []int{4}},
		{name: "uppercase query is case sensitive", query: "Readme", items: paths, want: nil},
		{name: "no match", query: "zzz", items: paths, want: nil},
		{name: "blank query keeps every item in order", query: " ", items: paths, want: []int{0, 1, 2, 3, 4}},

		{name: "title case query matches itself", query: "ǅ", items: []string{"ǅ"}, want: []int{0}},
		{name: "circled letter query matches itself", query: "Ⓐ", items: []string{"Ⓐ"}, want: []int{0}},
		{name: "roman numeral query matches itself", query: "Ⅻ", items: []string{"Ⅻ"}, want: []int{0}},
		{name: "circled capital makes the query case sensitive", query: "Ⓐb", items: []string{"ⒶB"}, want: nil},

		{name: "non-word character after a separator starts a word", query: "_t", items: []string{"a_test.go", "_test/a.go"}, want: []int{1, 0}},

		{name: "slash is a stronger boundary than space", query: "bar", items: []string{"x bar", "x/bar"}, want: []int{1, 0}},
		{name: "only slash is a delimiter", query: "bar", items: []string{"a_bar", "a:bar"}, want: []int{0, 1}},
		{name: "ties go to matches in the file name", query: "foo", items: []string{"foo/abc", "abc/foo"}, want: []int{1, 0}},
		{name: "repeated match counts from the file name", query: "foo", items: []string{"foo/foo", "bar/foo"}, want: []int{0, 1}},
		{name: "repeated single character counts from the file name", query: "f", items: []string{"f/f", "x/f"}, want: []int{0, 1}},
		{name: "file name tie-break comes before length", query: "foo", items: []string{"foo/ab", "abc/foo"}, want: []int{1, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Filter(tt.query, tt.items)

			if !slices.Equal(got, tt.want) {
				t.Errorf("Filter(%q, %q) = %v, want %v", tt.query, tt.items, got, tt.want)
			}
		})
	}
}
