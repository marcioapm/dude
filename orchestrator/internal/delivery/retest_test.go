package delivery

import "testing"

// A non-product glob names a path in a repository, never the repository:
// the default docs/** covers a docs/ directory, but a repository named docs
// is product like any other.
func TestNonProductMatchesThePathInItsRepositoryOnly(t *testing.T) {
	p := DefaultPolicy()
	for _, c := range []struct {
		paths []string
		want  bool
	}{
		{[]string{"docs/src/components/Search.tsx"}, false},
		{[]string{".github/src/deploy.ts"}, false},
		{[]string{"api/docs/guide.txt", "api/.github/workflows/ci.yml", "web/README.md"}, true},
		{[]string{"docs/README.md"}, true},
		{nil, false},
	} {
		if got := NonProduct(p, c.paths); got != c.want {
			t.Errorf("NonProduct(%q) = %v, want %v", c.paths, got, c.want)
		}
	}
}
