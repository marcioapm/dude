package orchestrator_test

// Close and Archive against an open pull request's integration candidate
// (docs/design/tracking-work.md, Epic 1; docs/CONTRIBUTING.md, "A fix for an
// open pull request is checked before the pull request shows it").

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// An integration whose review failed waits for a person with its merge on
// the candidate. Close and Archive are refused and write nothing; the
// candidate is neither promoted nor dropped, and once the person accepts
// it is promoted with no further review, as without the refused calls.
func TestCloseAndArchiveRefuseATaskWhoseIntegrationCandidateIsOut(t *testing.T) {
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

	for _, route := range []string{"close", "archive"} {
		status, body := w.callAs(ana, "/internal/tasks/"+wi+"/"+route, map[string]any{"resolution": "wont_do"})
		e, _ := body["error"].(map[string]any)
		msg, _ := e["message"].(string)
		if status != 409 || e["code"] != "running" || !strings.HasPrefix(msg, "Stop its agents first") {
			t.Fatalf("%s with the candidate out: %d %v", route, status, body)
		}
	}
	if w.resolutionOf(wi) != "" || w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NOT NULL`, wi) != 0 {
		t.Fatal("a refused close or archive wrote the task")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1`, wi); n != events {
		t.Fatalf("a refused close or archive wrote %d events", n-events)
	}
	if got := w.gh.SHA(branch); got != before {
		t.Fatalf("the pull request moved to %s on a refused close, from %s", got, before)
	}
	if n := w.count(integrationCompleted, wi); n != 0 {
		t.Fatalf("integration-completed %d before the person decided", n)
	}

	crashing.Store(false)
	reviews := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review'`, wi)
	if code, body := w.decide(ana, wi, "accept", "Read the merge myself."); code != 200 {
		t.Fatal(code, body)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	w.until("promoted", func() bool { w.sync(); return w.gh.SHA(branch) != before && w.count(integrationCompleted, wi) == 1 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review'`, wi); n != reviews {
		t.Fatalf("%d reviews after the person accepted", n-reviews)
	}
	if w.resolutionOf(wi) != "" || w.taskStatus(wi) != "review" {
		t.Fatalf("after promotion: resolution %q, status %q", w.resolutionOf(wi), w.taskStatus(wi))
	}
}
