package orchestrator_test

// A pull request merged or closed on GitHub ends the delivery from whatever
// step it is at (delivery.endable): parked on the conductor's decision, on a
// person's escalation, or on a pull request fixer at work.

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// afterEnd syncs and pumps a few times: what a merge or a close is going to
// do has been done.
func (w *world) afterEnd() {
	for range 4 {
		w.sync()
		w.pump()
	}
}

// deliveryStatus is the task's newest delivery's status, step and last error.
func (w *world) deliveryStatus(task string) (status, step, lastErr string) {
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text, step, COALESCE(last_error, '') FROM workflow_runs
		WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, task).Scan(&status, &step, &lastErr)
	return
}

// assertEnded: the task is status, its delivery completed with no step
// failure, nothing pending in its state, and no Run of it live.
func (w *world) assertEnded(task, status string) {
	w.t.Helper()
	if s := w.taskStatus(task); s != status {
		w.t.Fatalf("task is %s, want %s\n%s", s, status, w.describeRuns())
	}
	if st, step, e := w.deliveryStatus(task); st != "completed" || e != "" {
		w.t.Errorf("delivery %s at %s, error %q, want completed", st, step, e)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
		AND (state ? 'decision' OR state ? 'escalation' OR state ? 'candidate')`, task); n != 0 {
		w.t.Errorf("the ended delivery still holds a decision, escalation or candidate: %s",
			w.str(`SELECT state::text FROM workflow_runs WHERE task_id = $1`, task))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IS NOT NULL
		AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, task); n != 0 {
		w.t.Errorf("%d phase Runs live after the delivery ended", n)
	}
}

// conductedFeedback is a conducted task's delivery parked on the
// conductor's decision about a person's comment on its pull request.
func (w *world) conductedFeedback() string {
	w.t.Helper()
	task := w.task()
	w.deliver(task)
	w.until("review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	if status, out := w.chat(task, "I'll take it from here"); status >= 300 {
		w.t.Fatalf("chat: %d %v", status, out)
	}
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("feedback to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	return task
}

// ABS-28: merged on GitHub while the delivery waited on its conductor's
// decision about pull request feedback. The delivery ends done, the
// decision withdrawn, and no fixer runs; handing decisions back to Deliver
// afterwards runs nothing either.
func TestAMergeWhileTheConductorDecidesOnFeedbackFinishesTheTask(t *testing.T) {
	w := conducting(t)
	task := w.conductedFeedback()
	w.gh.Merge(1)
	w.until("the task to finish", func() bool { w.sync(); return w.taskStatus(task) == "done" })
	w.afterEnd()
	w.assertEnded(task, "done")
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'decision' AND delivered_at IS NULL`, task); n != 0 {
		t.Errorf("%d decision wakes still to tell the conductor of", n)
	}
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers for feedback on a merged pull request", n)
	}
	w.handBack(task, map[string]any{"decider": "policy"})
	w.afterEnd()
	if n := w.fixes(task); n != 0 {
		t.Errorf("handed back, %d fixers ran", n)
	}
	if n := len(w.gh.Pulls()); n != 1 {
		t.Errorf("%d pull requests, want the merged one", n)
	}
	w.assertEnded(task, "done")
}

// Closed without merging while the conductor decides on its feedback: the
// task is aborted there.
func TestACloseWhileTheConductorDecidesOnFeedbackAbortsTheTask(t *testing.T) {
	w := conducting(t)
	task := w.conductedFeedback()
	w.gh.Close(1)
	w.until("the task to end", func() bool { w.sync(); return w.taskStatus(task) == "aborted" })
	w.afterEnd()
	w.assertEnded(task, "aborted")
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers for feedback on a closed pull request", n)
	}
}

// Merged while an escalation waits on a person (pr_loop_exhausted): the task
// is done, not left awaiting input, and the escalation is recorded as
// decided by dude, which takes it off the banner.
func TestAMergeWhileAnEscalationWaitsOnAPersonFinishesTheTask(t *testing.T) {
	w := newWorld(t)
	task := w.reviewing()
	setPolicy(w, task, `{"maxPrFixIterations":1}`)
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fix", func() bool { w.sync(); return w.fixes(task) == 1 && w.taskStatus(task) == "review" })
	w.gh.Comment(1, "alice", "And the farewell.")
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(task) == "awaiting_input" })
	if r := w.escalationReason(task); r != "pr_loop_exhausted" {
		t.Fatalf("escalated %q", r)
	}
	w.gh.Merge(1)
	w.until("the task to finish", func() bool { w.sync(); return w.taskStatus(task) == "done" })
	w.afterEnd()
	w.assertEnded(task, "done")
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'
		AND payload->>'reason' = 'pr_loop_exhausted' AND payload->>'action' = 'done' AND actor_type = 'system'`, task); n != 1 {
		t.Errorf("%d withdrawals of the escalation recorded, want 1", n)
	}
	var last string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'status' FROM events WHERE task_id = $1
		AND event_type = 'task.status_changed' ORDER BY cursor DESC LIMIT 1`, task).Scan(&last)
	if last != "done" {
		t.Errorf("the task's last status change is %q, want done", last)
	}
	if n := w.fixes(task); n != 1 {
		t.Errorf("%d fixers, want the one before the escalation", n)
	}
}

// Merged while a pull request fixer is at work: the fixer is aborted, its
// candidate abandoned, and the task done with no pull request of its own
// for the fix and no step failing.
func TestAMergeWhileAPullRequestFixerWorksStopsIt(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
			b.Hang = true
		}
		return b
	}
	task := w.reviewing()
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fixer at work", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`, task) == 1
	})
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'candidate'`, task); n != 1 {
		t.Fatalf("the fix is not on a candidate")
	}
	w.gh.Merge(1)
	w.until("the task to finish", func() bool { w.sync(); return w.taskStatus(task) == "done" })
	w.afterEnd()
	w.assertEnded(task, "done")
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'aborted'
		AND control_reason = 'the pull request was merged'`, task); n != 1 {
		t.Errorf("the fixer was not aborted for the merge\n%s", w.describeRuns())
	}
	if n := len(w.gh.Pulls()); n != 1 {
		t.Errorf("%d pull requests, want the merged one", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND attempt > 0`, task); n != 0 {
		t.Errorf("a step failed")
	}
}

// The ABS-28 shape: the pull request is merged as dude records it, with no
// pr.ended ever sent (a merge from before v0.1.23), and then a pull request
// fixer finishes with commits. Its fix reaches nothing: no pull request is
// opened for it, no GitHub call fails, and the task is done.
func TestAFixFinishingAfterAnUnsignalledMergeOpensNothing(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
			b.Hang = true
		}
		return b
	}
	task := w.reviewing()
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fixer at work", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`, task) == 1
	})
	var luxID string
	if err := w.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND status = 'running'`, task).Scan(&luxID); err != nil {
		t.Fatal(err)
	}
	// Merged on GitHub and in dude's record, the workflow never told.
	w.gh.Merge(1)
	mustExec(t, w.owner, `UPDATE pull_requests SET state = 'merged', merged_at = now() WHERE task_id = $1`, task)
	w.lux.EndTurn(luxID)
	w.until("the task to finish", func() bool { w.pump(); return w.taskStatus(task) == "done" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'
		AND pr_feedback <> '[]'::jsonb AND heads <> '{}'::jsonb`, task); n != 1 {
		t.Fatalf("the pull request fixer did not finish with commits\n%s", w.describeRuns())
	}
	w.afterEnd()
	w.assertEnded(task, "done")
	if n := len(w.gh.Pulls()); n != 1 {
		t.Errorf("%d pull requests, want the merged one", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND attempt > 0`, task); n != 0 {
		t.Errorf("a step failed: %s", w.str(`SELECT COALESCE(last_error, '') FROM workflow_runs WHERE task_id = $1`, task))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND created_at >
		(SELECT max(created_at) FROM runs WHERE task_id = $1 AND phase = 'fix')`, task); n != 0 {
		t.Errorf("%d reviews of a fix no pull request can show", n)
	}
}

// The same unsignalled merge, recorded while the fix's candidate is tested:
// the checked candidate is not promoted (promoteCandidate's guard). The pull
// request's branch never moves and the task is done.
func TestACandidateCheckedAcrossAnUnsignalledMergeIsNotPromoted(t *testing.T) {
	w := newPRFixWorld(t)
	w.onTest = func() {
		w.gh.Merge(1)
		mustExec(t, w.owner, `UPDATE pull_requests SET state = 'merged', merged_at = now() WHERE task_id = $1`, w.wi)
	}
	w.comment("Please rename the greeting.")
	// Done, or the candidate promoted onto the merged branch.
	w.agentsDone(w.wi, "the task to finish", func() bool { return w.taskStatus(w.wi) == "done" || w.gh.SHA(w.branch) != w.before })
	if n := w.candidateTests.Load(); n != 1 {
		t.Fatalf("%d candidate tests, want the one the merge landed in\n%s", n, w.describeRuns())
	}
	if at := w.gh.SHA(w.branch); at != w.before {
		t.Errorf("the merged pull request's branch moved from %.8s to %.8s", w.before, at)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 0 {
		t.Errorf("%d promotions recorded onto a merged pull request", n)
	}
	w.afterEnd()
	w.assertEnded(w.wi, "done")
	if n := len(w.gh.Pulls()); n != 1 {
		t.Errorf("%d pull requests, want the merged one", n)
	}
}

// Two repositories: one pull request merges while a fixer works for the
// other. The fixer goes on and the task is not done; once the second
// merges, it is.
func TestOneOfTwoPullRequestsMergingLeavesTheOthersFixerAtWork(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
			b.Hang = true
		}
		return b
	}
	task := w.prOpen(w.task(), prAt{web: true})
	w.web.Comment(1, "alice", "Please fix the page title.")
	fixer := `SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`
	w.until("web's fixer at work", func() bool { w.sync(); return w.count(fixer, task) == 1 })
	w.gh.Merge(1)
	w.afterEnd()
	if n := w.count(fixer, task); n != 1 {
		t.Fatalf("target's merge stopped web's fixer\n%s", w.describeRuns())
	}
	if s := w.taskStatus(task); s == "done" {
		t.Fatalf("done with web's pull request still open")
	}
	if st, step, _ := w.deliveryStatus(task); st != "waiting" || step != "awaitPRFix" {
		t.Fatalf("delivery %s at %s, want waiting on the fix", st, step)
	}
	w.web.Merge(1)
	w.until("the task to finish", func() bool { w.sync(); return w.taskStatus(task) == "done" })
	w.afterEnd()
	w.assertEnded(task, "done")
}

// Two repositories: target's pull request closes while web's fixer works.
// One closed beside one open is not an end the fixer's step acts on: the
// fixer goes on and its fix lands on web's pull request, and the person is
// asked once (pull_request_closed), as on main. Web closing while they are
// asked ends the task (aborted) and withdraws the question. (A person who
// chooses wait is asked again by the close's terminal pr.feedback: main
// does the same, and this branch leaves it unchanged.)
func TestAMixedEndWhileASiblingsFixerWorksLetsTheFixLand(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	var fixers atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		switch labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] {
		case "fix":
			b.Hang = fixers.Add(1) == 1
			b.Commit = map[string]string{"web:TITLE.md": "the page title, fixed\n"}
		case "simplify":
			// The candidate changes web alone: nothing for target's closed one.
			b.Commit = nil
		}
		return b
	}
	task := w.prOpen(w.task(), prAt{web: true})
	webHead := func() string { return w.web.SHA(w.web.Pull(1).Head) }
	webBefore := webHead()
	w.web.Comment(1, "alice", "Please fix the page title.")
	fixer := `SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`
	w.until("web's fixer at work", func() bool { w.sync(); return w.count(fixer, task) == 1 })
	var luxID string
	if err := w.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND status = 'running'`, task).Scan(&luxID); err != nil {
		t.Fatal(err)
	}
	w.gh.Close(1)
	w.afterEnd()
	if n := w.count(fixer, task); n != 1 {
		t.Fatalf("target's close stopped web's fixer\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'aborted'`, task); n != 0 {
		t.Fatalf("%d Runs aborted by a close beside an open pull request\n%s", n, w.describeRuns())
	}
	w.lux.EndTurn(luxID)
	w.until("the person asked", func() bool { w.sync(); w.pump(); return w.taskStatus(task) == "awaiting_input" })
	if r := w.escalationReason(task); r != "pull_request_closed" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	if webHead() == webBefore {
		t.Fatalf("web's fix never reached its pull request\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, task); n != 1 {
		t.Errorf("%d promotions of web's fix, want 1", n)
	}
	asked := `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'reason' = 'pull_request_closed'`
	if n := w.count(asked, task); n != 1 {
		t.Fatalf("asked %d times about the close, want once; task %s", n, w.taskStatus(task))
	}
	if n := len(w.gh.Pulls()); n != 1 {
		t.Errorf("%d target pull requests, want the closed one", n)
	}
	// Web closing too, while the person is still asked, ends it: the question
	// is withdrawn, not asked again.
	w.web.Close(1)
	w.until("the task to end", func() bool { w.sync(); return w.taskStatus(task) == "aborted" })
	w.afterEnd()
	w.assertEnded(task, "aborted")
	if n := w.count(asked, task); n != 1 {
		t.Errorf("asked %d times about the closes, want once", n)
	}
}
