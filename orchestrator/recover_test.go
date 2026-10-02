package orchestrator_test

// Picking a stopped task back up (api/recover.go): resume the Runs that
// stopped, try the step again, or start over as a new attempt.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
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
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"implementer":{"model":"llm/impl"}}'::jsonb WHERE id = $1`, w.project)
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
	w.gh.Comment(1, "octo", "@dude please rename it")
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
