package fakelux_test

// The fake's secrets against lux v0.1.11's contract, through dude's real
// lux client: the stored spec it answers with, and what reaches a
// server's process and its log.

import (
	"context"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lux answers a submit, and a repeat of its idempotency key, with the
// spec it stored from the first: each secret's name and normalized as, no
// value, a credential runnerOnly. A repeat with another spec changes
// nothing of it.
func TestASubmitAnswersWithTheFirstSubmitsStoredSecrets(t *testing.T) {
	fake := fakelux.New(t.TempDir(), "k", nil)
	_, c, _ := startedWith(t, fake, preview)
	ctx := context.Background()
	first := preview
	first.Secrets = []lux.Secret{{Name: "GIT_TOKEN", Value: "token"}, {Name: "ORIGINAL_KEY", Value: "original", As: "env"}}
	first.Git = &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "https://github.com/acme/app.git", Credential: "GIT_TOKEN"}}}
	want := []lux.StoredSecret{{Name: "GIT_TOKEN", As: "none", RunnerOnly: true}, {Name: "ORIGINAL_KEY", As: "env"}}
	r, err := c.Submit(ctx, first, "preview-key")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Spec.Secrets, want) {
		t.Errorf("submit answered secrets %+v; want %+v", r.Spec.Secrets, want)
	}
	retry := preview
	retry.Secrets = []lux.Secret{{Name: "ADDED_KEY", Value: "added", As: "env"}}
	again, err := c.Submit(ctx, retry, "preview-key")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != r.ID || !slices.Equal(again.Spec.Secrets, want) {
		t.Errorf("repeat answered %s with %+v; want %s with %+v", again.ID, again.Spec.Secrets, r.ID, want)
	}
	got, err := c.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Spec.Secrets, want) {
		t.Errorf("GET answered secrets %+v; want %+v", got.Spec.Secrets, want)
	}
}
