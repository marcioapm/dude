package api

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// An answer is kept a minute: the same words ask GitHub once.
func TestCandidateCacheAsksGitHubOncePerWord(t *testing.T) {
	var c candidateCache
	asks := 0
	ask := func(context.Context) ([]forge.Candidate, error) {
		asks++
		return []forge.Candidate{{Login: "ana"}}, nil
	}
	for _, words := range []string{"an", "an", "ana"} {
		if _, err := c.get(context.Background(), candidateQuery{org: "o", slug: "a/b", words: words}, ask); err != nil {
			t.Fatal(err)
		}
	}
	if asks != 2 {
		t.Fatalf("asked GitHub %d times for two words", asks)
	}
}
