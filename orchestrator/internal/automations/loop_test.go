package automations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gate is a file a test's commands wait on: a command made by hold runs
// until release creates it.
type gate string

func newGate(t *testing.T) gate { return gate(filepath.Join(t.TempDir(), "go")) }

func (g gate) hold(then string) string {
	return fmt.Sprintf("while [ ! -e %s ]; do sleep 0.05; done; %s", string(g), then)
}

func (g gate) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(string(g), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A webhook endpoint that does not answer and live agents whose output is
// still open cost the sweep nothing: a pass returns while the endpoint is
// still silent, having matched an unrelated delivery and started its run.
func TestAutomationSlowWorkStaysOffTheSweep(t *testing.T) {
	h := newHarness(t)
	answer := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(answer) }) }
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-answer
		w.WriteHeader(200)
	}))
	t.Cleanup(hook.Close)
	// Runs before hook.Close, which waits for the held request.
	t.Cleanup(release)
	h.automation("aut_live", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"concurrency": "parallel", "agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Work"}}})
	exec(t, h.owner, `UPDATE automations SET parallel_max = 4 WHERE id = 'aut_live'`)
	h.automation("aut_slowhook", "webhook", map[string]any{"kind": "github", "event": "release"},
		map[string]any{"webhook": map[string]any{"url": hook.URL}})
	for i := range 4 {
		h.deliver(fmt.Sprintf("p-%d", i), "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	}
	h.deliver("rel-1", "release", map[string]any{"action": "published", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("four live agents and the webhook POSTing", func() bool {
		live, hk := 0, h.runs("aut_slowhook")
		for _, r := range h.runs("aut_live") {
			if r.Status == "running" {
				live++
			}
		}
		return live == 4 && len(hk) == 1 && hk[0].Status == "running"
	})

	h.automation("aut_other", "command", map[string]any{"kind": "github", "event": "issues"}, map[string]any{"command": "echo other"})
	h.deliver("i-1", "issues", map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"}})
	done := make(chan error, 1)
	go func() { _, err := h.runner.Sweep(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		release()
		<-done
		t.Fatal("a pass waited on the silent webhook endpoint")
	}
	if r := h.runs("aut_other"); len(r) != 1 || r[0].Status != "running" {
		t.Fatalf("after one pass the unrelated run is %+v", r)
	}
	release()
	h.until("the webhook and the unrelated run succeed", func() bool {
		hk, o := h.runs("aut_slowhook"), h.runs("aut_other")
		return hk[0].Status == "succeeded" && o[0].Status == "succeeded"
	})
}

// A delivery an automation cannot be matched to (a paths filter with no
// GitHub to read the pull request's files from) holds up nothing: a later
// push still fires its own automation, a Run now on a schedule still starts,
// and after MatchAttempts tries the stuck automation gets a skipped run
// saying why and the delivery is marked read.
func TestAutomationLoopGoesOnPastADeliveryItCannotMatch(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_paths", "command", map[string]any{"kind": "github", "event": "pull_request",
		"filters": map[string]any{"paths": []string{"src/"}}}, map[string]any{"command": "echo paths"})
	h.automation("aut_push", "command", map[string]any{"kind": "github", "event": "push"}, map[string]any{"command": "echo pushed"})
	h.automation("aut_nightly", "command", map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "UTC"},
		map[string]any{"command": "echo nightly"})
	h.deliver("d-pr", "pull_request", prBody("opened", "acme/web", "ana"))
	h.deliver("d-push", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	if _, _, err := h.runner.RunNow(context.Background(), h.org, "aut_nightly", h.person, "", nil); err != nil {
		t.Fatal(err)
	}
	h.until("the push and the Run now succeed", func() bool {
		p, s := h.runs("aut_push"), h.runs("aut_nightly")
		return len(p) == 1 && p[0].Status == "succeeded" && len(s) == 1 && s[0].Status == "succeeded"
	})
	if r := h.runs("aut_paths"); len(r) != 0 {
		t.Fatalf("paths automation fired on its first try: %+v", r)
	}
	var marked bool
	var attempts int
	read := func() {
		t.Helper()
		if err := h.owner.QueryRow(context.Background(), `SELECT automations_at IS NOT NULL, automations_attempts FROM webhook_deliveries WHERE id = 'd-pr'`).
			Scan(&marked, &attempts); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if marked || attempts != 1 {
		t.Fatalf("after one try: marked %v attempts %d", marked, attempts)
	}
	for i := 0; i < MatchAttempts; i++ {
		h.advance(11 * time.Minute)
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	read()
	r := h.runs("aut_paths")
	if !marked || len(r) != 1 || r[0].Status != "skipped" ||
		r[0].Reason != "it could not be matched: no GitHub to read the pull request's files from" {
		t.Fatalf("after %d tries: marked %v runs %+v", attempts, marked, r)
	}
	// The push automation was not fired again by the retries.
	if p := h.runs("aut_push"); len(p) != 1 {
		t.Errorf("push runs %+v", p)
	}
	if !strings.Contains(h.runs("aut_push")[0].Log, "pushed") {
		t.Errorf("push log %q", h.runs("aut_push")[0].Log)
	}
}
