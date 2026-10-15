package delivery

import (
	"fmt"
	"slices"
	"testing"
)

// The reviewers of continued work are picked by the implementer's paths,
// then the person's it did not touch, each once and in that order.
func TestWithPathsKeepsTheImplementersFirstThenThePersonsOnce(t *testing.T) {
	for _, c := range []struct {
		name               string
		implementer, other []string
		want               []string
	}{
		{"disjoint", []string{"t/b.go", "t/a.go"}, []string{"t/z.sql", "t/c.ts"}, []string{"t/b.go", "t/a.go", "t/z.sql", "t/c.ts"}},
		{"overlap", []string{"t/a.go", "t/b.go"}, []string{"t/b.go", "t/c.ts", "t/a.go"}, []string{"t/a.go", "t/b.go", "t/c.ts"}},
		{"the person's repeated", []string{"t/a.go"}, []string{"t/c.ts", "t/c.ts", "t/d.ts"}, []string{"t/a.go", "t/c.ts", "t/d.ts"}},
		{"nothing of the person's", []string{"t/a.go"}, nil, []string{"t/a.go"}},
	} {
		if got := withPaths(slices.Clone(c.implementer), c.other); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Many paths, half shared: order is the implementer's, then the person's as given.
	var implementer, other, want []string
	for i := range 2000 {
		implementer = append(implementer, fmt.Sprintf("t/i%04d", i))
		other = append(other, fmt.Sprintf("t/i%04d", 1999-i), fmt.Sprintf("t/p%04d", i))
	}
	want = slices.Clone(implementer)
	for i := range 2000 {
		want = append(want, fmt.Sprintf("t/p%04d", i))
	}
	if got := withPaths(implementer, other); !slices.Equal(got, want) {
		t.Errorf("2000 paths: %d paths, order not kept", len(got))
	}
}
