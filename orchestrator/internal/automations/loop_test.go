package automations

import (
	"context"
	"strings"
	"testing"
	"time"
)

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
