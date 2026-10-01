package api

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// An answer is kept a minute; one GitHub gave while someone was being asked
// is not, so the next look shows them asked.
func TestCandidateCacheForgetsAcrossALookup(t *testing.T) {
	c := candidateCache{seen: map[string]cachedCandidates{}, forgot: map[string]uint64{}}
	asks := 0
	ask := func(context.Context) ([]forge.Candidate, error) {
		asks++
		return []forge.Candidate{{Login: "ana"}}, nil
	}
	for range 2 {
		if _, err := c.get(context.Background(), "o\x00a/b", "o\x00a/b\x00\x007", ask); err != nil {
			t.Fatal(err)
		}
	}
	if asks != 1 {
		t.Fatalf("asked GitHub %d times for one word", asks)
	}
	// Asked while a lookup is in flight: its answer is not kept.
	inFlight := func(ctx context.Context) ([]forge.Candidate, error) {
		c.forget("o", "a/b")
		return ask(ctx)
	}
	if _, err := c.get(context.Background(), "o\x00a/b", "o\x00a/b\x00x\x007", inFlight); err != nil {
		t.Fatal(err)
	}
	if _, kept := c.seen["o\x00a/b\x00x\x007"]; kept {
		t.Error("kept an answer from before someone was asked")
	}
	if _, kept := c.seen["o\x00a/b\x00\x007"]; kept {
		t.Error("forget left an answer")
	}
}
