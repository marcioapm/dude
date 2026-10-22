package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// waitingOnPR says the task's delivery is parked on its pull requests.
func (w *world) waitingOnPR(task string) bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest' AND status = 'waiting'`, task) == 1
}

// While the delivery waits on its pull request, the conductor may start a
// fix, a review or a test: the Run goes to work on the pull request's
// branch, and the delivery waits on it again after — no second pull
// request, no simplify, no Open or Draft. Implement and simplify are not
// for then, nor is a fix with nothing to do.
func TestTheConductorStartsAPhaseWhileThePullRequestWaits(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	phases := len(w.phasesRun(task))

	w.refused(task, "start_phase", `{"phase":"implement"}`, "cannot start while the delivery waits on its pull requests")
	w.refused(task, "start_phase", `{"phase":"simplify"}`, "cannot start while the delivery waits on its pull requests")
	w.refused(task, "start_phase", `{"phase":"fix"}`, "say what the fix is to do")

	w.must(task, "start_phase", `{"phase":"fix","note":"Rename greet to welcome, as Ana asked in Chat."}`)
	w.until("the fix on the branch, and waiting again", func() bool {
		return w.gh.SHA(branch) != before && w.waitingOnPR(task)
	})
	// Checked on its candidate before the pull request shows it, whoever
	// started it: a review of its own range, the simplifier and a review of
	// its diff (this policy has no tester).
	if got := w.phasesRun(task)[phases:]; !slices.Equal(got, []string{"fix", "review", "simplify", "review"}) {
		t.Fatalf("phases after start_phase fix: %v", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
		AND review_range->'base'->>'target' = $3`, task, delivery.RangePullRequestFix, before); n == 0 {
		t.Fatal("the fix reached the pull request without a review of its range")
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}

	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	// Its findings are the conductor's to triage, as after any review.
	w.until("the review's decision", func() bool { return w.decisionAt(task) == "after_review" })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting again", func() bool { return w.waitingOnPR(task) })
	if got := w.phasesRun(task)[phases+4:]; len(got) == 0 || got[0] != "review" || slices.Contains(got, "simplify") ||
		slices.Contains(got, "test") {
		t.Fatalf("phases after start_phase review: %v", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}
}

// A person pushed while the pull request waited: the conductor's review and
// a completed test start from GitHub's head, not the delivery's stale one.
func TestAPhaseStartedAtThePullRequestWaitStartsFromAPersonsPush(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", "fake/scripted")
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	branch := w.gh.Pull(1).Head

	pushed := w.gh.CommitOnTop(branch, "A person's change")
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the review's decision", func() bool { return w.decisionAt(task) == "after_review" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, task, pushed); n != 1 {
		t.Fatalf("%d reviews started from the person's push %s", n, pushed)
	}
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting again", func() bool { return w.waitingOnPR(task) })

	pushed = w.gh.CommitOnTop(branch, "Another change")
	w.must(task, "start_phase", `{"phase":"test"}`)
	w.until("the tester completed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'test' AND base_refs->>'target' = $2 AND status = 'completed'`,
			task, pushed) == 1
	})
}

// A test the conductor starts while the pull request waits, once it
// passes, waits on that pull request again: the verification gate's pass
// asks no Open or Draft of a pull request already open.
func TestATestStartedAtThePullRequestWaitWaitsOnItAgainOncePassed(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", "fake/scripted")
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })

	w.must(task, "start_phase", `{"phase":"test"}`)
	w.until("the test's verdict, then a wait or a decision", func() bool {
		return w.count(`SELECT count(*) FROM acceptance_verdicts WHERE task_id = $1`, task) > 0 &&
			(w.waitingOnPR(task) || w.decisionAt(task) != "")
	})
	if point := w.decisionAt(task); point != "" {
		t.Fatalf("parked on the conductor's %q decision, want waiting on the open pull request again", point)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}
}

// fixFailedTestAtPRWait carries a failed test's fix round through as a
// conductor deciding next at each point does: the fix, its review, then the
// test that judges the fix. Fails if the delivery waits on the pull request
// again with the failure untested.
func (w *world) fixFailedTestAtPRWait(task string) {
	w.t.Helper()
	w.until("the failed test's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	tests := w.phaseRuns(task, "test")
	for _, at := range []string{delivery.PointReviewed, delivery.PointFixed, delivery.PointReviewed} {
		w.until("the "+at+" decision", func() bool { return w.decisionAt(task) == at })
		w.must(task, "decide", `{"action":"next"}`)
	}
	w.until("a re-test, or the wait", func() bool { return w.phaseRuns(task, "test") > tests || w.waitingOnPR(task) })
	if w.phaseRuns(task, "test") == tests {
		w.t.Fatalf("waiting on the pull request again after a failed test's fix, with no re-test; verdicts %v", w.verdicts(task))
	}
}

// runsSoFar are the ids of the task's Runs now: what writersOnCandidate
// leaves out.
func (w *world) runsSoFar(task string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return ids
}

// writersOnCandidate fails the test unless the task has a Run of each of
// phases besides those in before, and every such Run published to a
// candidate branch.
func (w *world) writersOnCandidate(task string, before []string, phases ...string) {
	w.t.Helper()
	if before == nil {
		before = []string{}
	}
	for _, phase := range phases {
		all := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = $2::run_phase AND id <> ALL($3)`, task, phase, before)
		off := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = $2::run_phase AND id <> ALL($3)
			AND (branch IS NULL OR branch NOT LIKE '%-candidate')`, task, phase, before)
		if all == 0 || off != 0 {
			w.t.Errorf("%d %s Runs, %d of them off the candidate\n%s", all, phase, off, w.describeRuns())
		}
	}
}

// A test the conductor starts while the pull request waits, failing, sends
// its fix round through review and then tests again, as any failed test
// does; the re-test passing, the delivery waits on that pull request again.
func TestAFailedTestAtThePullRequestWaitIsTestedAgainAfterItsFix(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", fakeagent.TestFailOnceModel)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	phases := len(w.phasesRun(task))
	w.gh.Review(1, "ana", "APPROVED")
	branch := w.gh.Pull(1).Head
	moves, earlier := len(w.gh.BranchLog(branch)), w.runsSoFar(task)
	check := w.prShowsOnlyChecked(t, task, "target", branch, true)

	w.must(task, "start_phase", `{"phase":"test"}`)
	w.fixFailedTestAtPRWait(task)
	w.until("the re-test, then waiting again", func() bool { return w.waitingOnPR(task) })
	if got := w.phasesRun(task)[phases:]; !slices.Equal(got, []string{"test", "fix", "review", "test"}) {
		t.Errorf("phases after start_phase test: %v", got)
	}
	// The fix reached the pull request once, by promotion, reviewed and
	// tested at the head it moved to.
	check()
	if got := w.gh.BranchLog(branch)[moves:]; len(got) != 1 || got[0] != w.gh.SHA(branch) {
		t.Errorf("the pull request's branch moved to %v, want one promotion", got)
	}
	w.writersOnCandidate(task, earlier, "fix")
	if got := w.verdicts(task); !slices.Equal(got, []string{"pass"}) {
		t.Errorf("the re-test's verdicts = %v, want passed", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Errorf("%d pull requests", len(w.gh.Pulls()))
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state ? 'actingOnPr' OR state ? 'retestDue')`, task); n != 0 {
		t.Errorf("waiting again still acting on the pull request, or a re-test still pending")
	}
}

// A test at the pull request wait that keeps failing stops for a person
// after its fix rounds, as anywhere else.
func TestATestAtThePullRequestWaitThatKeepsFailingEscalates(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", fakeagent.TestFailModel)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	tests := w.phaseRuns(task, "test")

	w.must(task, "start_phase", `{"phase":"test"}`)
	for range delivery.MaxTestFixRounds {
		w.fixFailedTestAtPRWait(task)
	}
	w.until("the gate to stop", func() bool { return w.taskStatus(task) == "awaiting_input" })
	if r := w.escalationReason(task); r != delivery.ReasonTestFailedCriteria {
		t.Fatalf("escalated %q, want %s\n%s", r, delivery.ReasonTestFailedCriteria, w.describeRuns())
	}
	if n := w.phaseRuns(task, "test") - tests; n != delivery.MaxTestFixRounds+1 {
		t.Errorf("%d tests, want the first and one after each of %d fix rounds", n, delivery.MaxTestFixRounds)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Errorf("%d pull requests", len(w.gh.Pulls()))
	}
}

// firstReviewAfter makes the first review Run after arm is called raise a
// finding; every other phase is the scripted agent's.
func (w *world) firstReviewAfter() (arm func()) {
	var mu sync.Mutex
	armed, first := false, ""
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		run := fmt.Sprint(labels["dude.run"])
		mu.Lock()
		if armed && first == "" && labels["dude.phase"] == "review" {
			first = run
		}
		raise := run == first
		mu.Unlock()
		if raise {
			return fakelux.Behaviour{Reply: "One problem:\n\n```yaml\n" + fakeagent.Finding + "```\n"}
		}
		return scripted(spec)
	}
	return func() { mu.Lock(); armed = true; mu.Unlock() }
}

// verdictsOfTestAfter is the verdict status of each criterion judged on the
// task's first test Run past the first n.
func (w *world) verdictsOfTestAfter(task string, n int) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT status::text FROM acceptance_verdicts
		WHERE run_id = (SELECT id FROM runs WHERE task_id = $1 AND phase = 'test' ORDER BY created_at, id OFFSET $2 LIMIT 1)
		ORDER BY criterion`, task, n)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// The incident (ABS-28): the conductor reviewed the open pull request at
// its wait, sent the finding to a fix at after_review, and steered the
// fix's review on. The simplifier and the tester ran as for any review
// loop, and the delivery went back to the pull request already open for
// its only repository: no Open or Draft asked of it, no second pull
// request, and the reviewer of the older head asked again.
func TestWorkPastTheReviewLoopReturnsToTheOpenPullRequestWithoutAskingOpenOrDraft(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", "fake/scripted")
	arm := w.firstReviewAfter()
	task := w.prOpen(w.task(), prAt{test: true})
	setPolicy(w, task, `{"maxReviewIterations":9}`)
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	w.gh.Review(1, "jerv", "COMMENTED")
	// A standing approval: whatever head the branch shows can be merged.
	w.gh.Review(1, "ana", "APPROVED")
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	moves := len(w.gh.BranchLog(branch))
	check := w.prShowsOnlyChecked(t, task, "target", branch, true)
	phases, tests, earlier := len(w.phasesRun(task)), w.phaseRuns(task, "test"), w.runsSoFar(task)
	arm()

	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the review's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "start_phase", `{"phase":"fix","note":"Fix the finding the review raised."}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	// next at each review round the loop asks about (the fix's, the
	// simplifier's diff's), until the delivery waits or asks before the PR.
	w.until("waiting on the pull request, or the decision before it", func() bool {
		switch w.decisionAt(task) {
		case delivery.PointReviewed, delivery.PointFixed:
			w.must(task, "decide", `{"action":"next"}`)
		case delivery.PointBeforePR:
			return true
		}
		return w.waitingOnPR(task) && w.phaseRuns(task, "test") > tests
	})
	if point := w.decisionAt(task); point != "" {
		t.Fatalf("parked on the conductor's %q decision, want waiting on the open pull request again\n%s", point, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = $3`,
		task, delivery.EvDecisionAwaited, delivery.PointBeforePR); n != 0 {
		t.Errorf("%d decisions awaited before the pull request", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'`, task); n != 0 {
		t.Errorf("%d questions asked", n)
	}
	got := w.phasesRun(task)[phases:]
	if !slices.Contains(got, "fix") || !slices.Contains(got, "simplify") || !slices.Contains(got, "test") {
		t.Errorf("phases after the wait: %v, want the fix, the simplifier and the test", got)
	}
	if got := w.verdictsOfTestAfter(task, tests); len(got) == 0 || slices.ContainsFunc(got, func(v string) bool { return v != "pass" }) {
		t.Errorf("the new test's verdicts %v, want every criterion passed", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}
	head := w.gh.SHA(branch)
	if head == before {
		t.Fatal("the pull request's head did not move")
	}
	check()
	if got := w.gh.BranchLog(branch)[moves:]; len(got) != 1 || got[0] != head {
		t.Errorf("the pull request's branch moved to %v, want one promotion to %s", got, head)
	}
	w.writersOnCandidate(task, earlier, "fix", "simplify")
	// ana's approval was of the older head too: both are asked again.
	if e := w.rerequests(task); len(e) != 1 || e[0]["head"] != head || !slices.Equal(strs(e[0]["reviewersRequested"]), []string{"jerv", "ana"}) {
		t.Errorf("re-requests %v, want jerv and ana asked for head %s", e, head)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'actingOnPr'`, task); n != 0 {
		t.Error("waiting again still acting on the pull request")
	}
}

// The same steering, with a fix that changes web for the first time: web's
// work has no pull request, so the delivery still stops for the Open or
// Draft before opening one there.
func TestWorkPastTheReviewLoopAsksOpenOrDraftForARepositoryWithoutAnOpenPullRequest(t *testing.T) {
	w := conducting(t)
	arm := w.firstReviewAfter()
	inLoop := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := inLoop(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
			b.Commit = map[string]string{"target:FIX.md": "fix\n", "web:FIX.md": "fix\n"}
		}
		return b
	}
	task := w.prOpen(w.task(), prAt{})
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main') ON CONFLICT DO NOTHING`, "repo_web_"+w.org, w.org, w.project)
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		VALUES ($1, $2, $3, 'write')`, w.org, task, "repo_web_"+w.org)
	setPolicy(w, task, `{"maxReviewIterations":9}`)
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	arm()

	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the review's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "start_phase", `{"phase":"fix","note":"Fix the finding the review raised."}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the decision before the pull request, or waiting", func() bool {
		switch w.decisionAt(task) {
		case delivery.PointReviewed, delivery.PointFixed:
			w.must(task, "decide", `{"action":"next"}`)
		case delivery.PointBeforePR:
			return true
		}
		return w.waitingOnPR(task)
	})
	if point := w.decisionAt(task); point != delivery.PointBeforePR {
		t.Fatalf("at %q, want the decision before the pull request\n%s", point, w.describeRuns())
	}
	if n := len(w.web.Pulls()); n != 0 {
		t.Errorf("%d pull requests on web before the Open or Draft", n)
	}
}

// The same steering on two repositories, web's pull request merged at the
// head the work left there and target's still open: the fix changes only
// target, nothing would open, so the delivery waits on target's again.
func TestWorkPastTheReviewLoopReturnsToTheOpenPullRequestBesideOneMergedAtItsHead(t *testing.T) {
	w := conducting(t)
	arm := w.firstReviewAfter()
	task := w.prOpen(w.task(), prAt{web: true})
	setPolicy(w, task, `{"maxReviewIterations":9}`)
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	w.web.Merge(1)
	w.until("web's pull request merged, and waiting on target's", func() bool {
		w.syncs(1)
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND state = 'merged'`, task) == 1 && w.waitingOnPR(task)
	})
	branch := w.gh.Pull(1).Head
	before, webHead := w.gh.SHA(branch), w.web.SHA(branch)
	arm()

	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the review's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "start_phase", `{"phase":"fix","note":"Fix the finding the review raised."}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("waiting on the pull request, or the decision before it", func() bool {
		switch w.decisionAt(task) {
		case delivery.PointReviewed, delivery.PointFixed:
			w.must(task, "decide", `{"action":"next"}`)
		case delivery.PointBeforePR:
			return true
		}
		return w.waitingOnPR(task) && w.gh.SHA(branch) != before
	})
	if point := w.decisionAt(task); point != "" {
		t.Fatalf("parked on the conductor's %q decision, want waiting on target's pull request again\n%s", point, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = $3`,
		task, delivery.EvDecisionAwaited, delivery.PointBeforePR); n != 0 {
		t.Errorf("%d decisions awaited before the pull request", n)
	}
	if got := w.web.SHA(branch); got != webHead {
		t.Errorf("web's branch moved to %s, want %s: the fix was to change target only", got, webHead)
	}
	if a, b := len(w.gh.Pulls()), len(w.web.Pulls()); a != 1 || b != 1 {
		t.Errorf("%d pull requests on target, %d on web, want one each", a, b)
	}
}

// Refused while a phase that publishes is at work (one writer), and past
// the pull request's fix bound.
func TestStartingAPhaseWhileThePullRequestWaitsHasTheSameBounds(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })

	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_writer', $1, $2, $3, 1, 'running', 'simplify', 'simplifier')`, w.org, w.project, task)
	w.refused(task, "start_phase", `{"phase":"review"}`, "one writer at a time")
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = 'run_writer'`)

	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{prIteration}', to_jsonb((state->'policy'->>'maxPrFixIterations')::int))
		WHERE task_id = $1`, task)
	w.refused(task, "start_phase", `{"phase":"fix","note":"Rename it."}`, "at their bound")
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{iteration}', to_jsonb((state->'policy'->>'maxReviewIterations')::int))
		WHERE task_id = $1`, task)
	w.refused(task, "start_phase", `{"phase":"review"}`, "at its bound")
	if !w.waitingOnPR(task) {
		t.Error("a refused start moved the delivery")
	}
}

// Under Deliver, the conductor starts nothing while the pull request waits.
func TestADeliveryDeliverDecidesTakesNoStartWhileThePullRequestWaits(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "What is this pull request about?"); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{decider}', '"policy"') WHERE task_id = $1`, task)
	w.refused(task, "start_phase", `{"phase":"review"}`, "Deliver takes this task's decisions")
}

func TestAnEndedConductorCannotStartAtThePullRequestWait(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.conductorSpecOf(task)
	run, _, _ := w.conductor(task)
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = $1`, run)
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.ConductStartPhase(context.Background(), tx,
			delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: run}, delivery.StartPhase{Phase: "review"})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "no longer this task's conductor") {
		t.Fatalf("ended conductor start: %v", err)
	}
	if !w.waitingOnPR(task) {
		t.Fatal("a refused start moved the delivery")
	}
}
