package orchestrator_test

// Close and Archive while agents work (delivery/lifecycle_write.go): every
// live Run stops, the conductor and its children included, nothing kept;
// what a Stop kept is let go at once; the pull request is closed on GitHub
// only when asked.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// released says the Run is ended and nothing keeps it for a resume.
func (w *world) released(runID string) bool {
	return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('aborted', 'completed', 'failed')
		AND NOT keep AND NOT run_kept(runs)`, runID) == 1
}

// The prod case: a conductor task, the conductor between turns after it
// said there is nothing to build. Close ends the conductor and the
// delivery and closes the task, naming what it stopped.
func TestClosingAConductorTaskWithNothingToBuild(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do", "note": "nothing to build"})
	if status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	if s, r := w.taskStatus(task), w.resolutionOf(task); s != "aborted" || r != "wont_do" {
		t.Fatalf("closed: %s %s", s, r)
	}
	if !w.released(conductor) {
		t.Errorf("the conductor was not stopped, or is kept")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, task); n != 1 {
		t.Errorf("the delivery did not end")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.closed'
		AND payload->'stopped' = jsonb_build_array($2::text) AND NOT payload ? 'pullRequestCloseRequested' AND actor_id = $3`, task, conductor, ana); n != 1 {
		t.Errorf("task.closed does not name the conductor it stopped: %s",
			w.str(`SELECT payload::text FROM events WHERE task_id = $1 AND event_type = 'task.closed'`, task))
	}
	w.until("the conductor's lux Run to be let go", func() bool { return w.luxRunFor(conductor).Cancelled })
}

// The conductor and a reviewer it started, both live: Close (by an admin
// who is not the owner) stops both, keeps neither, and says who closed.
func TestClosingWhileTheConductorAndAReviewerWorkStopsBoth(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.ownedBy(task, "Ana")
	cy := w.person("Cy")
	mustExec(t, w.owner, `UPDATE people SET role = 'admin' WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, cy)
	conductor := w.talk(task)
	w.implemented(task)
	w.must(task, "start_phase", `{"phase":"review"}`)
	w.advanced()
	reviewers := w.pendingPhaseRuns(task)
	if len(reviewers) == 0 {
		t.Fatalf("no reviewer\n%s", w.describeRuns())
	}
	child := w.taskChild(task, conductor)
	if status, body := w.callAs(cy, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("an admin's close: %d %v", status, body)
	}
	for _, id := range append([]string{conductor}, reviewers...) {
		if !w.released(id) {
			t.Errorf("%s was not stopped, or is kept", id)
		}
	}
	// A child is asked to stop, and the syncer ends it.
	w.until("the conductor's child to end", func() bool { return w.released(child) })
	stopped := w.str(`SELECT payload->>'stopped' FROM events WHERE task_id = $1 AND event_type = 'task.closed' AND actor_id = $2`, task, cy)
	for _, id := range append(append([]string{conductor}, reviewers...), child) {
		if !strings.Contains(stopped, id) {
			t.Errorf("task.closed does not name %s: %s", id, stopped)
		}
	}
	if got, _ := w.options(task); len(got) != 0 {
		t.Errorf("a closed task offers %v", got)
	}
}

// A task a Stop left kept for a resume: Close lets the kept Runs go at
// once, not when their time is up.
func TestClosingAStoppedTaskLetsWhatWasKeptGo(t *testing.T) {
	w := conducting(t)
	task, ana, conductor := w.stoppedAtDecision()
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	if !w.released(conductor) {
		t.Errorf("the kept conductor is still kept after the close")
	}
	w.until("the kept lux Run to be cancelled", func() bool { return w.luxRunFor(conductor).Cancelled })
}

// A reason to wake the conductor that came after a Stop is settled when
// the task closes, and the halt cleared: nothing is held for a pick-up a
// closed task will not get, and no conductor is woken.
func TestClosingAStoppedTaskSettlesItsHeldWakes(t *testing.T) {
	w := conducting(t)
	task, ana, _ := w.stoppedAtDecision()
	w.heldWake(task)
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	w.assertWakesEnded(task)
}

// Start over ends the stopped attempt's wakes the same way: attempt 2,
// handed to Deliver, gets no conductor for attempt 1's reasons.
func TestStartingOverSettlesTheStoppedAttemptsWakes(t *testing.T) {
	w := conducting(t)
	task, ana, _ := w.stoppedAtDecision()
	w.heldWake(task)
	w.pickUp(ana, task, map[string]any{"action": "restart", "decider": "policy"})
	w.assertWakesEnded(task)
}

func (w *world) heldWake(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_held_'||$2, $1, $2, 'decision', 'decision:held', 'A decision waits.', now() - interval '1 hour')`, w.org, task)
}

func (w *world) assertWakesEnded(task string) {
	w.t.Helper()
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, task); n != 0 {
		w.t.Errorf("%d reasons still pending", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'halt'`, task); n != 0 {
		w.t.Errorf("a delivery still holds its halt")
	}
	if got := w.conductorRuns(task); len(got) != 1 {
		w.t.Errorf("conductors: %v", got)
	}
}

// A pull request dude opened: closed on GitHub with a comment saying why
// when asked; the task in review closes either way (Q8).
func TestClosingInReviewClosesDudesPullRequestWhenAsked(t *testing.T) {
	w := newWorld(t)
	kept := w.reviewing()
	ana := w.ownedBy(kept, "Ana")
	if status, body := w.callAs(ana, "/internal/tasks/"+kept+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close, leaving the pull request: %d %v", status, body)
	}
	if p := w.gh.Pull(1); p.State != "open" {
		t.Fatalf("a pull request nobody asked to close is %s", p.State)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.closed'
		AND payload->>'pullRequestCloseRequested' = 'false'`, kept); n != 1 {
		t.Errorf("task.closed does not say the pull request was left open")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'close'`, kept); n != 0 {
		t.Errorf("a close nobody asked for was recorded")
	}
	closed := w.reviewing()
	w.assignOwner(closed, ana)
	status, body := w.callAs(ana, "/internal/tasks/"+closed+"/close", map[string]any{"resolution": "wont_do", "note": "Superseded by the API task.",
		"closePullRequest": true})
	if status != 200 {
		t.Fatalf("close with its pull request: %d %v", status, body)
	}
	p := w.gh.Pull(2)
	if p.State != "closed" || p.MergedAt != nil {
		t.Fatalf("dude's pull request is %s", p.State)
	}
	want := "Closed with " + w.keyOf(closed) + " as Won't do: Superseded by the API task."
	if len(p.Comments) == 0 || p.Comments[len(p.Comments)-1].Body != want {
		t.Errorf("the closing comment: %+v, want %q", p.Comments, want)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.closed'
		AND payload->>'pullRequestCloseRequested' = 'true'`, closed); n != 1 {
		t.Errorf("task.closed does not say closing the pull request was asked for")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'close' AND (payload->>'number')::int = 2 AND payload->>'closed' = 'true'
		AND NOT payload ? 'error' AND actor_id = $2`, closed, ana); n != 1 {
		t.Errorf("GitHub's close is not recorded as done, by Ana")
	}
}

// GitHub refuses the close: the task is closed all the same, the pull
// request stays open, and the ledger says the close was asked and failed —
// never that it was closed.
func TestClosingWhenGitHubRefusesToCloseThePullRequestSaysSo(t *testing.T) {
	w := newWorld(t)
	task := w.reviewing()
	ana := w.ownedBy(task, "Ana")
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/pulls/") {
				return http.StatusForbidden
			}
			return 0
		}
	})
	status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do", "closePullRequest": true})
	if status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	if s, r := w.taskStatus(task), w.resolutionOf(task); s != "aborted" || r != "wont_do" {
		t.Fatalf("the task is %s %s", s, r)
	}
	if p := w.gh.Pull(1); p.State != "open" {
		t.Fatalf("the pull request GitHub refused to close is %s", p.State)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.closed'
		AND payload->>'pullRequestCloseRequested' = 'true' AND NOT payload ? 'pullRequestClosed'`, task); n != 1 {
		t.Errorf("task.closed: %s", w.str(`SELECT payload::text FROM events WHERE task_id = $1 AND event_type = 'task.closed'`, task))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'close' AND (payload->>'number')::int = 1 AND payload ? 'error' AND NOT payload ? 'closed'`, task); n != 1 {
		t.Errorf("the failed close is not recorded: %s", w.str(`SELECT COALESCE(string_agg(payload::text, ' '), '') FROM events
			WHERE task_id = $1 AND event_type = 'pull_request.action'`, task))
	}
	w.sync()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.closed'`, task); n != 0 {
		t.Errorf("the ledger says the pull request was closed")
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND state = 'open'`, task); n != 1 {
		t.Errorf("the pull request is not open in dude's record")
	}
}

// A pull request the task was started from: left open by a Close unless
// asked; Archive of the live task stops its reviewer, closes, archives.
func TestArchivingALiveAdoptedTaskLeavesItsPullRequestUnlessAsked(t *testing.T) {
	for _, ask := range []bool{false, true} {
		w := newWorld(t)
		w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
		n := w.gh.OpenAsPerson("alice", "feature/half", "main", "Half done")
		task := w.adopted("pr", n, "feature/half", "main", delivery.ModeReview)
		ana := w.ownedBy(task, "Ana")
		w.startDelivery(task)
		w.until("a reviewer at work", func() bool {
			return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'`, task) > 0
		})
		reviewer := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' LIMIT 1`, task)
		status, body := w.callAs(ana, "/internal/tasks/"+task+"/archive", map[string]any{"resolution": "wont_do", "closePullRequest": ask})
		if status != 200 {
			t.Fatalf("archive while live (ask %v): %d %v", ask, status, body)
		}
		if w.resolutionOf(task) != "wont_do" || w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NOT NULL`, task) != 1 {
			t.Fatalf("not closed and archived")
		}
		if !w.released(reviewer) {
			t.Errorf("the reviewer was not stopped, or is kept")
		}
		want := "open"
		if ask {
			want = "closed"
		}
		if p := w.gh.Pull(n); p.State != want {
			t.Errorf("asked %v: the adopted pull request is %s, want %s", ask, p.State, want)
		}
	}
}
