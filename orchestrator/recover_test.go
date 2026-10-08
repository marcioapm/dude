package orchestrator_test

// Picking a stopped task back up (api/recover.go): resume the Runs that
// stopped, try the step again, or start over as a new attempt.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// aborted delivers a task whose implementer hangs until it is aborted; the
// next implementer (after a retry or a restart) commits and finishes.
// Returns the task and the aborted Run.
func (w *world) aborted() (string, string) {
	w.t.Helper()
	var implements atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			if implements.Add(1) == 1 {
				return fakelux.Behaviour{Hang: true, Reply: "Done after resume.", Commit: map[string]string{"A.md": "a\n"}}
			}
			return fakelux.Behaviour{Commit: map[string]string{"B.md": "b\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	if status, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{"reason": "wrong approach"}); status != 200 {
		w.t.Fatalf("abort: %d %v", status, body)
	}
	w.until("the run to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, runID) == 1
	})
	return wi, runID
}

func (w *world) options(wi string) (actions []string, attempt int) {
	w.t.Helper()
	code, body := w.get("/internal/tasks/"+wi+"/recover", w.org)
	if code != 200 {
		w.t.Fatalf("recovery options: %d %s", code, body)
	}
	var out struct {
		Actions []string
		Attempt int
	}
	_ = json.Unmarshal([]byte(body), &out)
	return out.Actions, out.Attempt
}

func TestAnAbortedTaskOffersEveryWayBack(t *testing.T) {
	w := newWorld(t)
	wi, _ := w.aborted()
	if got, attempt := w.options(wi); strings.Join(got, " ") != "resume retry restart" || attempt != 1 {
		t.Fatalf("an aborted task offers %v (attempt %d)", got, attempt)
	}
	// Once it is no longer kept, only a fresh agent can pick it up.
	mustExec(t, w.owner, `UPDATE runs SET kept_until = now() - interval '1 second' WHERE task_id = $1`, wi)
	w.until("the lux run to be cancelled", func() bool { return w.lux.Runs()[0].Cancelled })
	if got, _ := w.options(wi); strings.Join(got, " ") != "retry restart" {
		t.Fatalf("a task no longer kept offers %v", got)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 409 || !strings.Contains(body["error"].(map[string]any)["code"].(string), "not_kept") {
		t.Fatalf("resuming what is no longer kept: %d %v", status, body)
	}
}

// Resume: the same lux Run, the same conversation, told the note; the
// delivery goes on from where it stopped.
func TestResumingAnAbortedTaskContinuesTheSameAgent(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.aborted()
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume", "note": "Keep the API."}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	if s := w.taskStatus(wi); s != "running" {
		t.Errorf("a resumed task is %s", s)
	}
	w.until("the resumed run to finish, and review to start", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	r := w.lux.Runs()[0]
	if r.Resumed != 1 || r.Cancelled {
		t.Errorf("lux run resumed %d times, cancelled %v", r.Resumed, r.Cancelled)
	}
	if len(r.Inputs) == 0 || r.Inputs[0] != "Keep the API." {
		t.Errorf("the note was not the agent's next message: %v", r.Inputs)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi); n != 1 {
		t.Errorf("%d implementers: a resume made a new one", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.recovered' AND payload->>'action' = 'resume'`, wi); n != 1 {
		t.Errorf("%d task.recovered events", n)
	}
}

// Retry: a new implementer on the same branch, told the note; the stopped
// one stays as it was.
func TestRetryingAnAbortedTaskStartsANewAgentOnTheSameBranch(t *testing.T) {
	w := newWorld(t)
	// A real model, so the prompt is the one a real one reads.
	w.onModel("implementer", "llm-impl")
	wi, runID := w.aborted()
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "retry", "note": "Use the new API."}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("a second implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi) == 2
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, runID); n != 1 {
		t.Errorf("the aborted run did not stay aborted")
	}
	if n := w.count(`SELECT count(DISTINCT attempt) FROM runs WHERE task_id = $1`, wi); n != 1 {
		t.Errorf("a retry is a new attempt")
	}
	w.until("the second implementer's lux run", func() bool { return len(w.lux.Runs()) == 2 })
	if prompt := w.lux.Runs()[1].Prompt(); !strings.Contains(prompt, "Use the new API.") {
		t.Errorf("the new implementer was not told the note:\n%s", prompt)
	}
}

// Restart: attempt 2, on its own branch from main; attempt 1's pull
// request is closed and its findings stay with it.
func TestStartingOverIsANewAttemptOnItsOwnBranch(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Commit: map[string]string{"A.md": "a\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	// A finding of attempt 1, left open, and a person closing its pull request.
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title)
		SELECT 'f_old', organization_id, task_id, id, 'correctness', 'high', 'old' FROM runs WHERE task_id = $1 AND phase = 'review' LIMIT 1`, wi)
	w.gh.Close(1)
	w.until("the task to be aborted by the close", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "aborted"
	})
	if got, _ := w.options(wi); strings.Join(got, " ") != "restart" {
		t.Fatalf("a task whose pull request was closed offers %v", got)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"}); status != 200 {
		t.Fatalf("restart: %d %v", status, body)
	}
	w.until("attempt 2's pull request", func() bool { return len(w.gh.Pulls()) == 2 })
	pulls := w.gh.Pulls()
	if want := delivery.BranchFor(wi, 2); pulls[1].Head != want {
		t.Errorf("attempt 2's pull request is from %s, want %s", pulls[1].Head, want)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2 AND phase = 'implement'`, wi); n != 1 {
		t.Errorf("%d implementers in attempt 2", n)
	}
	// Attempt 1's open finding did not hold attempt 2 back, nor was it
	// sent to attempt 2's fixer: it stays with its attempt.
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2 AND phase = 'fix'`, wi); n != 0 {
		t.Errorf("attempt 1's finding sent attempt 2 to a fixer")
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE id = 'f_old' AND status = 'open'`); n != 1 {
		t.Errorf("attempt 1's finding changed")
	}
}

// Only its owner picks it up; anyone else is told who can.
func TestOnlyATasksOwnerPicksItBackUp(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	wi, _ := w.aborted()
	w.assignOwner(wi, ana)
	if status, body := w.callAs(bo, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "retry"}); status != 403 {
		t.Fatalf("a non-owner picking it up: %d %v", status, body)
	}
	if w.taskStatus(wi) != "aborted" {
		t.Fatalf("the refused pick-up moved the task")
	}
	if status, body := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("the owner picking it up: %d %v", status, body)
	}
	// Once picked up, it is not stopped: a second is refused.
	if status, body := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "retry"}); status != 409 {
		t.Fatalf("a second pick-up: %d %v", status, body)
	}
}

// Aborting one reviewer stops the review: the others do not run on.
func TestAbortingAReviewerStopsTheReview(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Commit: map[string]string{"auth/session.go": "package auth\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Hang: true}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("two reviewers at work", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'`, wi) >= 2
	})
	var one string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' LIMIT 1`, wi).Scan(&one)
	if status, body := w.call("/internal/runs/"+one+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status <> 'aborted'`, wi); n != 0 {
		t.Errorf("%d reviewers ran on after one was aborted", n)
	}
	w.until("every aborted reviewer to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND lux_stop_reason IS DISTINCT FROM 'kept'`, wi) == 0
	})
	if got, _ := w.options(wi); strings.Join(got, " ") != "resume retry restart" {
		t.Fatalf("an aborted review offers %v", got)
	}
}

// An implementer whose agent died is kept: the escalation offers to resume
// it, and resuming continues the same lux Run.
func TestAFailedAgentIsResumedFromItsEscalation(t *testing.T) {
	w := newWorld(t)
	// It hangs until its container dies (Crash); resumed, it finishes.
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true, Reply: "Back.", Commit: map[string]string{"A.md": "a\n"}}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	w.lux.Crash(w.lux.Runs()[0].ID)
	w.until("the task to need a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	w.until("the failed run to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed' AND lux_stop_reason = 'kept'`, wi) == 1
	})
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("review, after the same implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi); n != 1 {
		t.Errorf("%d implementers: resuming made a new one", n)
	}
	if r := w.lux.Runs()[0]; r.Resumed != 1 {
		t.Errorf("lux run resumed %d times", r.Resumed)
	}
}

// Starting over while attempt 1's pull request is still open (a fix the
// person aborted) closes it: attempt 2 opens its own.
func TestStartingOverClosesTheLastAttemptsOpenPullRequest(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Commit: map[string]string{"A.md": "a\n"}, Message: "work"}
		case "fix":
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.gh.Comment(1, "octo", "Please rename it")
	w.until("a fixer for the feedback", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`, wi) == 1
	})
	var fix string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi).Scan(&fix)
	if status, body := w.call("/internal/runs/"+fix+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"}); status != 200 {
		t.Fatalf("restart: %d %v", status, body)
	}
	if p := w.gh.Pull(1); p.State != "closed" || p.MergedAt != nil {
		t.Errorf("attempt 1's open pull request is %s", p.State)
	}
	w.until("attempt 2's pull request", func() bool { return len(w.gh.Pulls()) == 2 })
}

// A kept Run taken back up and failing again tells its workflow again: the
// delivery stops for a person, not waiting on it for ever.
func TestAResumeThatFailsStopsTheDeliveryAgain(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.aborted()
	// lux lost it meanwhile: the resume is refused.
	w.lux.Forget()
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the delivery to stop for a person", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1 && w.taskStatus(wi) == "awaiting_input"
	})
}

// A Run that died with a push asked for is asked for its push again when
// its resumed turn ends, not left waiting on the first.
func TestAResumedRunPushesAgain(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.aborted()
	mustExec(t, w.owner, `UPDATE runs SET push_request_id = 'push-stale', push_result = '{"results":[]}'::jsonb WHERE id = $1`, runID)
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the resumed run to push and finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed' AND heads <> '{}'::jsonb`, runID) == 1
	})
}

// After a start over, a comment on attempt 1's pull request does not wake
// attempt 2's delivery.
func TestAnEarlierAttemptsPullRequestDoesNotWakeTheNext(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Commit: map[string]string{"A.md": "a\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	var impl string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&impl)
	// Stopped by a person, as an abort would leave it, then started over.
	mustExec(t, w.owner, `UPDATE tasks SET status = 'aborted' WHERE id = $1`, wi)
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'completed' WHERE task_id = $1`, wi)
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"}); status != 200 {
		t.Fatalf("restart: %d %v", status, body)
	}
	w.until("attempt 2's pull request", func() bool { return len(w.gh.Pulls()) == 2 })
	// Reopened on GitHub and commented on.
	w.gh.Reopen(1)
	w.gh.Comment(1, "octo", "Please rename it")
	for range 5 {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi); n != 0 {
		t.Errorf("attempt 1's pull request woke %d fixers", n)
	}
}

// Resume is offered, and taken, only once lux has stopped and kept the
// Run: one taken up before would wait on a stop nobody asks for.
func TestResumeWaitsForTheRunToBeKept(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	// Aborted, the sweep not yet round to keep it.
	if status, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	if got, _ := w.options(wi); strings.Join(got, " ") != "retry restart" {
		t.Fatalf("a run not yet kept offers %v", got)
	}
	if status, _ := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 409 {
		t.Fatalf("resuming a run not yet kept: %d", status)
	}
	w.until("the run to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, runID) == 1
	})
	if got, _ := w.options(wi); got[0] != "resume" {
		t.Fatalf("a kept run offers %v", got)
	}
}

// A reviewer that finished just before the others were aborted does not
// keep the rest from being resumed.
func TestAReviewerThatFinishedDoesNotBlockResumingTheRest(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Commit: map[string]string{"auth/session.go": "package auth\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Hang: true}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("two reviewers at work", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'`, wi) >= 2
	})
	var done, other string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at LIMIT 1`, wi).Scan(&done)
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND id <> $2 LIMIT 1`, wi, done).Scan(&other)
	// One reviewer finished; its signal is not yet taken.
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = $1`, done)
	if status, body := w.call("/internal/runs/"+other+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	w.until("the aborted reviewers to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'aborted' AND lux_stop_reason <> 'kept'`, wi) == 0
	})
	if got, _ := w.options(wi); len(got) == 0 || got[0] != "resume" {
		t.Fatalf("an aborted review with one reviewer done offers %v", got)
	}
}

// A task changed while it was stopped: the resumed agent is told what it
// asks for now. A resume carries no images, so the note names them.
func TestAResumedAgentIsToldTheTaskChanged(t *testing.T) {
	w := newWorld(t)
	wi, _ := w.aborted()
	mustExec(t, w.owner, `UPDATE tasks SET goal = 'Say goodbye instead, like ![x.png](attachment:att_x)',
		acceptance_criteria = '["It waves, as in ![y.png](attachment:att_y)"]'::jsonb WHERE id = $1`, wi)
	mustExec(t, w.owner, `INSERT INTO events (id, organization_id, project_id, task_id, event_type, actor_type, actor_id, source, payload)
		VALUES ('evt_edit', $1, $2, $3, 'task.updated', 'human', 'someone', 'control-plane', '{"goal":"Say goodbye instead"}'::jsonb)`,
		w.org, w.project, wi)
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the agent to be told", func() bool { r := w.lux.Runs()[0]; return len(r.Inputs) > 0 })
	in := w.lux.Runs()[0].Inputs[0]
	if !strings.Contains(in, "the task was changed") || !strings.Contains(in, "Say goodbye instead, like [Image: x.png]") ||
		!strings.Contains(in, "It waves, as in [Image: y.png]") || strings.Contains(in, "attachment:") {
		t.Errorf("the resumed agent was told %q", in)
	}
}

// A kept Run an operator ended in lux is not offered to resume, and one
// resumed all the same fails rather than waiting for ever: whether lux
// calls the end terminated, or cancelled as before the rename.
func TestARunCancelledInLuxIsNotResumed(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminated", true: "cancelled"}[old], func(t *testing.T) {
			runCancelledInLux(t, old)
		})
	}
}

func runCancelledInLux(t *testing.T, cancelledState bool) {
	w := newWorld(t)
	w.lux.CancelledState = cancelledState
	wi, runID := w.aborted()
	w.lux.CancelInLux(w.lux.Runs()[0].ID)
	// Resumed before dude heard of the cancel: lux refuses, and it fails.
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the resume to fail, and stop the delivery for a person", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed' AND error LIKE 'cannot resume%'`, runID) == 1 &&
			w.taskStatus(wi) == "awaiting_input"
	})
	w.until("its lux run to be let go, not kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel'`, runID) == 1
	})
}

// A kept Run whose lux Run then succeeded (its workload exited 0) is still
// offered to resume. A lux that resumes a succeeded Run resumes it; a lux
// from before, which refuses, fails it as it does an ended one, and it is
// let go.
func TestAKeptRunThatSucceededInLuxIsResumedWhereLuxCan(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "resumable", true: "final"}[old], func(t *testing.T) {
			runSucceededInLux(t, old)
		})
	}
}

func runSucceededInLux(t *testing.T, cancelledState bool) {
	w := newWorld(t)
	w.lux.CancelledState = cancelledState
	wi, runID := w.aborted()
	// Kept already (aborted waits for it); its workload then exits 0. dude
	// follows no kept Run's feed, so only the resume finds it succeeded.
	w.lux.Succeed(w.lux.Runs()[0].ID)
	if s := w.lux.Runs()[0].State; s != "succeeded" {
		t.Fatalf("the fake's Run is %s, want succeeded", s)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, runID); n != 1 {
		t.Fatalf("the aborted Run is not kept")
	}
	if got, _ := w.options(wi); len(got) == 0 || got[0] != "resume" {
		t.Fatalf("a kept Run lux ended succeeded offers %v; want resume first", got)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	if !cancelledState {
		w.until("the same lux Run resumed", func() bool {
			return w.lux.Runs()[0].Resumed == 1 &&
				w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('running', 'completed')`, runID) == 1
		})
		if n := len(w.lux.Runs()); n != 1 {
			t.Errorf("%d lux runs; want the succeeded one resumed, not replaced", n)
		}
		return
	}
	w.until("the resume to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'
			AND error = 'cannot resume: lux says the run is succeeded'`, runID) == 1
	})
	w.until("its lux run to be let go, not kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel'`, runID) == 1
	})
	if r := w.lux.Runs()[0]; r.Resumed != 0 || !r.Cancelled {
		t.Errorf("the old lux's succeeded Run: resumed %d, cancelled %v; want let go in lux", r.Resumed, r.Cancelled)
	}
}

// A reviewer still waiting for room when the review was aborted does not
// keep the others from being resumed: it is queued again with them.
func TestAReviewerNeverStartedIsQueuedAgainOnResume(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Commit: map[string]string{"auth/session.go": "package auth\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Hang: true}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("two reviewers at work", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'`, wi) >= 2
	})
	var started, pending string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at LIMIT 1`, wi).Scan(&started)
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND id <> $2 LIMIT 1`, wi, started).Scan(&pending)
	// The second never reached lux: it began waiting for an image long ago.
	mustExec(t, w.owner, `UPDATE runs SET status = 'pending', lux_run_id = NULL, lux_state = NULL,
		image_waiting_since = now() - interval '3 hours' WHERE id = $1`, pending)
	if status, body := w.call("/internal/runs/"+started+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	w.until("the started reviewer to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, started) == 1
	})
	if got, _ := w.options(wi); len(got) == 0 || got[0] != "resume" {
		t.Fatalf("an aborted review with a reviewer never started offers %v", got)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	// As if new: the image it waited on before is asked for afresh.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND image_waiting_since IS NULL`, pending); n != 1 {
		t.Errorf("a never-started reviewer resumed still waits on its old image job")
	}
	w.until("the never-started reviewer to be submitted", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id IS NOT NULL`, pending) == 1
	})
}

// After a start over, attempt 1's Runs are not signalled to attempt 2's
// delivery.
func TestAnEarlierAttemptsRunsAreNotSignalledToTheNext(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.aborted()
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"}); status != 200 {
		t.Fatalf("restart: %d %v", status, body)
	}
	w.until("attempt 2's implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2`, wi) == 1
	})
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM workflow_signals WHERE payload->>'runId' = $1`, runID); n != 0 {
		t.Errorf("attempt 1's run was signalled %d times", n)
	}
}

// A resume whose first answer was lost is asked again, and lux answers
// 409 because the Run is already up: that is not a refusal, and the Run
// goes on.
func TestAResumeLuxAlreadyTookIsNotFailed(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.aborted()
	// The first resume reaches lux, but its answer is lost; every one after
	// is answered as lux answers a Run that is already resuming or running.
	var resumes atomic.Int32
	handler := w.lux.Handler()
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/resume") && req.Method == http.MethodPost {
			if resumes.Add(1) == 1 {
				handler.ServeHTTP(httptest.NewRecorder(), req)
				http.Error(rw, "gateway timeout", http.StatusGatewayTimeout)
				return
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusConflict)
			_, _ = rw.Write([]byte(`{"error":{"code":"not_resumable","message":"run is running: stop it first"}}`))
			return
		}
		handler.ServeHTTP(rw, req)
	}))
	t.Cleanup(proxy.Close)
	w.syncer.Lux = lux.New(proxy.URL, "lux-key")
	w.syncer.RetryAhead = time.Minute
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the resumed run to finish its turn", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if resumes.Load() < 2 {
		t.Fatalf("the resume was asked %d times: the lost answer was never retried", resumes.Load())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND error LIKE 'cannot resume%'`, runID); n != 0 {
		t.Errorf("a resume lux had already taken failed the run")
	}
}
