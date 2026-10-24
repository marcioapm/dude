package fakelux

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// stoppedWith submits spec to fake and stops its Run once it runs.
func stoppedWith(t *testing.T, fake *Server, spec lux.Spec) (*lux.HTTPClient, string) {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(fake.Close)
	c := lux.New(srv.URL, "k")
	run, err := c.Submit(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "never ran", func(r *Run) bool { return r.State == "running" })
	if err := c.Stop(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "never stopped", func(r *Run) bool { return r.State == "stopped" })
	return c, run.ID
}

func storedNames(t *testing.T, c *lux.HTTPClient, id string) []string {
	t.Helper()
	got, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got.Spec.Secrets {
		names = append(names, s.Name)
	}
	return names
}

// As lux's resumeRun and RunSpec.ResumeSecrets: a resume supplying a name
// the Run lacks declares it (stored with no value), and every later resume
// must give it too; only a missing or empty value is refused.
func TestAResumeDeclaresASecretTheRunLacked(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	c, id := stoppedWith(t, fake, lux.Spec{Workload: lux.Workload{Adapter: "opencode", Prompt: "go"},
		Secrets: []lux.Secret{{Name: "ACME_KEY", Value: "acme-1", As: "env"}}})
	ctx := context.Background()
	if _, err := c.Resume(ctx, id, lux.ResumeInput{Secrets: []lux.Secret{{Name: "ACME_KEY", Value: "acme-2", As: "env"},
		{Name: "ACME_LATER", Value: "", As: "env"}}}); !refused(err, 422, "secrets_required") {
		t.Errorf("a new name with an empty value: %v, want 422 secrets_required", err)
	}
	if names := storedNames(t, c, id); !slices.Equal(names, []string{"ACME_KEY"}) {
		t.Errorf("a refused resume changed the stored secrets: %v", names)
	}
	if _, err := c.Resume(ctx, id, lux.ResumeInput{Secrets: []lux.Secret{{Name: "ACME_KEY", Value: "acme-2", As: "env"},
		{Name: "ACME_LATER", Value: "later-1", As: "env"}}}); err != nil {
		t.Fatalf("a resume declaring ACME_LATER: %v", err)
	}
	if names := storedNames(t, c, id); !slices.Equal(names, []string{"ACME_KEY", "ACME_LATER"}) {
		t.Errorf("stored secrets after the resume: %v, want ACME_LATER declared", names)
	}
	fake.mu.Lock()
	held := fake.runs[id].secretValues["ACME_LATER"]
	fake.mu.Unlock()
	if held != "later-1" {
		t.Errorf("the placement holds ACME_LATER %q", held)
	}
	awaitRun(t, fake, id, "never ran again", func(r *Run) bool { return r.State == "running" })
	if err := c.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, id, "never stopped again", func(r *Run) bool { return r.State == "stopped" })
	if _, err := c.Resume(ctx, id, lux.ResumeInput{Secrets: []lux.Secret{{Name: "ACME_KEY", Value: "acme-3", As: "env"}}}); !refused(err, 422, "secrets_required") {
		t.Errorf("a later resume without ACME_LATER: %v, want 422 secrets_required", err)
	}
}

// A name lux dropped with a failed clone's repository (git.droppedCredentials)
// is never declared again: given with as none it is skipped, and as env or
// file it is refused.
func TestAResumeSkipsADroppedCredentialAndRefusesItAsEnv(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	c, id := stoppedWith(t, fake, lux.Spec{Workload: lux.Workload{Adapter: "opencode", Prompt: "go"},
		Secrets: []lux.Secret{{Name: "ACME_KEY", Value: "acme-1", As: "env"}}})
	fake.mu.Lock()
	var spec map[string]any
	_ = json.Unmarshal(fake.runs[id].Spec, &spec)
	spec["git"] = map[string]any{"repositories": []any{}, "droppedCredentials": []any{"GIT_TOKEN_ACME"}}
	fake.runs[id].Spec, _ = json.Marshal(spec)
	fake.mu.Unlock()
	ctx := context.Background()
	for _, as := range []string{"env", "file"} {
		sec := lux.Secret{Name: "GIT_TOKEN_ACME", Value: "ghs_x", As: as}
		if as == "file" {
			sec.Path = "/home/agent/token"
		}
		if _, err := c.Resume(ctx, id, lux.ResumeInput{Secrets: []lux.Secret{{Name: "ACME_KEY", Value: "acme-2", As: "env"}, sec}}); !refused(err, 422, "invalid_spec") {
			t.Errorf("a dropped credential as %s: %v, want 422 invalid_spec", as, err)
		}
	}
	if _, err := c.Resume(ctx, id, lux.ResumeInput{Secrets: []lux.Secret{{Name: "ACME_KEY", Value: "acme-2", As: "env"},
		{Name: "GIT_TOKEN_ACME", Value: "ghs_x"}}}); err != nil {
		t.Fatalf("a dropped credential with no as: %v", err)
	}
	if names := storedNames(t, c, id); !slices.Equal(names, []string{"ACME_KEY"}) {
		t.Errorf("stored secrets %v: the dropped credential was declared", names)
	}
}
