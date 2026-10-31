package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// reviewingDirect is a delivery waiting on its open pull request, frozen
// to push its pull request fixes directly, with the tester on.
func (w *world) reviewingDirect() string {
	return w.prOpen(w.task(), prAt{direct: true, test: true})
}

// A conflict on a conducted delivery's open pull request is a message to
// its conductor: a decision on the pull request's feedback naming the
// conflict, and no integration Run until the conductor decides. Deciding
// next has the integration fixer merge the base onto the pull request.
func TestDirectAConflictWakesTheConductorWithAMessageNotAnIntegration(t *testing.T) {
	w := conducting(t)
	w.toolsWithSync()
	task := w.prOpen(w.task(), prAt{direct: true})
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	w.conflictWithMovedBase()
	w.until("the conflict to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	w.wokenWith(task, "conflicts with main")
	if n := w.count(integrationRuns, task); n != 0 {
		t.Fatalf("%d integration Runs before the conductor decided", n)
	}
	branch := w.gh.Pull(1).Head
	w.must(task, "decide", `{"action":"next"}`)
	w.until("the integration on the pull request", func() bool {
		w.sync()
		return w.count(integrationRuns, task) == 1 && w.count(integrationRuns+` AND status = 'completed'`, task) == 1
	})
	if n := w.count(integrationRuns+` AND branch = $2`, task, branch); n != 1 {
		t.Errorf("the integration did not publish to the pull request's branch\n%s", w.describeRuns())
	}
}

// Under the policy, a conflict on the open pull request is merged by an
// integration fixer straight onto the pull request's branch, reviewed over
// the merge (remerge-diff), not tested, and the delivery waits again; no
// candidate branch, no hand-over.
func TestDirectAConflictUnderThePolicyIsMergedByAFixerOntoThePullRequestAndReviewed(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewingDirect()
	branch := w.gh.Pull(1).Head
	w.conflictWithMovedBase()
	w.until("integration fixer", func() bool { w.sync(); return w.count(integrationRuns, wi) == 1 })
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	w.until("reviewed integration back waiting", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND pr_feedback::text LIKE '%remerge-diff%'`, wi) == 1 &&
			w.waitingOnPR(wi)
	})
	if r := w.escalationReason(wi); r != "" {
		t.Fatalf("escalated %s", r)
	}
	if n := w.count(integrationRuns+` AND branch = $2`, wi, branch); n != 1 {
		t.Errorf("the integration did not publish to the pull request's branch\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase IN ('test', 'simplify')
		AND created_at > (SELECT min(created_at) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%')`, wi); n != 0 {
		t.Errorf("%d tester or simplifier Runs after the integration", n)
	}
	if out := gitOut(t, w.gh.Repo, "rev-list", "--count", branch+"..main"); out != "0" {
		t.Errorf("base not integrated: %s behind", out)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND state ? 'handedOver'`, wi); n != 0 {
		t.Errorf("the integration was handed over")
	}
	if w.gh.SHA(delivery.CandidateBranch(wi, 1)) != "" || len(w.gh.BranchLog(delivery.CandidateBranch(wi, 1))) != 0 {
		t.Errorf("a candidate branch was written")
	}
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Errorf("%d integration-completed lines", n)
	}
}

// A writer told to resolve the conflict, in a direct delivery, is told its
// merge goes to the pull request: nothing about a candidate or promotion.
func TestDirectResolveConflictsReachesTheLiveWriterWithoutCandidateWords(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewingDirect()
	w.tell()
	w.conflictWithMovedBase()
	w.sync()
	// A writer at work on the task (a fix the conductor started).
	mustExec(t, w.owner, `UPDATE workflow_runs SET step='awaitFix', awaiting_signals='[]', state=state-'escalation' WHERE task_id=$1`, wi)
	mustExec(t, w.owner, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind,phase)
		VALUES('run_queue_writer',$1,$2,$3,1,'running','implementer','agent','fix')`, w.org, w.project, wi)
	if code, body := w.resolve(w.person("owner"), w.prOf(wi)); code != 200 {
		t.Fatalf("resolve: %d %v", code, body)
	}
	texts := w.ids(`SELECT text FROM directives WHERE run_id = 'run_queue_writer'`)
	if len(texts) != 1 {
		t.Fatalf("%d directives to the writer", len(texts))
	}
	text := texts[0]
	if !strings.Contains(text, "merge lux/main") || !strings.Contains(text, "pull request when you publish") {
		t.Errorf("the directive: %s", text)
	}
	for _, word := range []string{"candidate", "promotion"} {
		if strings.Contains(strings.ToLower(text), word) {
			t.Errorf("the directive says %q: %s", word, text)
		}
	}
}

// The integration published while GitHub had not said whether the pull
// request still conflicts: no polling, back to the wait; the syncer's next
// conflicting reading at the pair attempted escalates once, and no second
// integration starts.
func TestDirectAConflictStillThereAfterTheIntegrationEscalatesOnce(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewingDirect()
	w.conflictWithMovedBase()
	w.until("the integration Run", func() bool { w.sync(); return w.count(integrationRuns, wi) == 1 })
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	w.until("back waiting, GitHub undecided", func() bool {
		w.pump()
		return w.waitingOnPR(wi) && w.count(integrationRuns+` AND status = 'completed'`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND state ? 'handedOver'`, wi); n != 0 {
		t.Errorf("the integration was handed over")
	}
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = false })
	w.until("the integration escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	raw, _ := w.escalationOf(wi)
	if !strings.Contains(string(raw), `"cause": "pair_attempted"`) && !strings.Contains(string(raw), `"cause": "still_conflicting"`) {
		t.Errorf("escalation %s", raw)
	}
	w.syncRounds(5)
	if n := w.count(integrationRuns, wi); n != 1 {
		t.Errorf("integration Runs %d", n)
	}
}

// Before the pull request nothing changes in direct mode: a clean base
// merge GitHub makes that touched none of the task's files skips the
// tester, as in candidate mode (mergedHeadOpensUntested).
func TestDirectACleanBaseMergeBeforeThePullRequestTouchingNoTaskFileSkipsTheTester(t *testing.T) {
	directAtTheGate = true
	t.Cleanup(func() { directAtTheGate = false })
	mergedHeadOpensUntested(t, "Open")
}
