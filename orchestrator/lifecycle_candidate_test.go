package orchestrator_test

// Close and Archive against an open pull request's integration candidate
// (docs/design/tracking-work.md, Epic 1; docs/CONTRIBUTING.md, "A fix for an
// open pull request is checked before the pull request shows it").

import (
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// An integration whose review failed waits for a person with its merge on
// the candidate. Archive with no resolution is refused and writes nothing;
// Close ends the delivery there, and the candidate is never promoted onto
// the pull request.
func TestClosingATaskWhoseIntegrationCandidateIsOutNeverPromotesIt(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	ana := w.ownedBy(wi, "Ana")
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	scripted := w.lux.Decide
	var crashing atomic.Bool
	crashing.Store(true)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "review" && crashing.Load() {
			return fakelux.Behaviour{Crash: true}
		}
		return scripted(spec)
	}
	w.conflictWithMovedBase()
	w.until("review_failed", func() bool { w.sync(); return w.escalationReason(wi) != "" })
	if r := w.escalationReason(wi); r != "review_failed" {
		t.Fatalf("escalated %q", r)
	}
	if n := w.count(integrationRuns+` AND branch LIKE '%-candidate'`, wi); n != 1 {
		t.Fatalf("integration Runs on the candidate %d, want 1", n)
	}
	events := w.count(`SELECT count(*) FROM events WHERE task_id = $1`, wi)

	// Archive with no resolution is refused, and writes nothing.
	status, body := w.callAs(ana, "/internal/tasks/"+wi+"/archive", map[string]any{})
	if s, code := w.refusal(status, body); s != 409 || code != "not_ended" {
		t.Fatalf("archive with no resolution, the candidate out: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1`, wi); n != events {
		t.Fatalf("a refused archive wrote %d events", n-events)
	}
	// Close ends the delivery with the candidate out: it is never promoted.
	if status, body := w.callAs(ana, "/internal/tasks/"+wi+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close with the candidate out: %d %v", status, body)
	}
	crashing.Store(false)
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	for range 5 {
		w.sync()
		w.pump()
	}
	if got := w.gh.SHA(branch); got != before {
		t.Fatalf("the pull request moved to %s after the close, from %s", got, before)
	}
	if n := w.count(integrationCompleted, wi); n != 0 {
		t.Fatalf("integration-completed %d after the close", n)
	}
	if w.resolutionOf(wi) != "wont_do" {
		t.Fatalf("not closed: resolution %q", w.resolutionOf(wi))
	}
}
