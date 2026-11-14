package fzf

import (
	"slices"
	"testing"
)

func TestFilter(t *testing.T) {
	items := []string{
		"core/tests/aws/services/redshift/v2_provider.py",
		"core/services/sts/provider_basic.py",
		"core/services/sts/provider.py",
		"core/services/s3/models.py",
		"README.md",
	}

	tests := []struct {
		name  string
		query string
		want  []int
	}{
		{name: "word boundaries beat scattered characters", query: "stsprovider", want: []int{2, 1, 0}},
		{name: "terms must all match", query: "sts basic", want: []int{1}},
		{name: "lowercase query ignores case", query: "readme", want: []int{4}},
		{name: "uppercase query is case sensitive", query: "Readme", want: nil},
		{name: "no match", query: "zzz", want: nil},
		{name: "blank query keeps every item in order", query: " ", want: []int{0, 1, 2, 3, 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Filter(tt.query, items)

			if !slices.Equal(got, tt.want) {
				t.Errorf("Filter(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}
