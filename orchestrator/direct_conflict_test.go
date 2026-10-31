package orchestrator_test

import (
	"encoding/json"
	"fmt"
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
	w, task := conductedDirectWorld(t)
	w.conflictWithMovedBase()
	w.until("the conflict to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	w.wokenWith(task, "conflicts with main")
	branch := w.gh.Pull(1).Head
	// The fact, whole: which pull request, the base and its head, the
	// head it shows, and the three ways on.
	want := fmt.Sprintf("Decision waiting: pull request feedback. The pull request target#1 conflicts with main (at %s); it shows %s. "+
		"Decide to have an integration fixer merge main into it, start a fix with your own note, or wait.",
		w.gh.SHA("main")[:7], w.gh.SHA(branch)[:7])
	if got := w.ids(`SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'decision' ORDER BY created_at DESC LIMIT 1`, task); len(got) != 1 || got[0] != want {
		t.Errorf("the conductor's fact:\n got %q\nwant %q", got, want)
	}
	if n := w.count(integrationRuns, task); n != 0 {
		t.Fatalf("%d integration Runs before the conductor decided", n)
	}
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
	var detail struct {
		Detail struct {
			Cause string `json:"cause"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil || detail.Detail.Cause != "pair_attempted" {
		t.Errorf("escalation %s, want cause pair_attempted", raw)
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

// The conductor told of a conflict decides wait: no integration is kept
// in state and none starts; the pull request stays conflicting, and the
// next sync tells the conductor again.
func TestDirectAConflictTheConductorWaitsOnIsToldAgainAtTheNextSync(t *testing.T) {
	w, task := conductedDirectWorld(t)
	w.conflictWithMovedBase()
	w.until("the conflict to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	w.wokenWith(task, "conflicts with main")
	told := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND line LIKE '%conflicts with main%'`, task)
	w.must(task, "decide", `{"action":"wait"}`)
	w.until("waiting on the pull request again", func() bool { return w.waitingOnPR(task) })
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'integration'`, task); n != 0 {
		t.Errorf("an integration is kept in state after wait")
	}
	if n := w.count(integrationRuns, task); n != 0 {
		t.Errorf("%d integration Runs after wait", n)
	}
	// The pull request stays conflicting: the next sync tells the
	// conductor again (once per wait), deciding on the conflict.
	w.until("the conflict told again", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND line LIKE '%conflicts with main%'`, task) > told
	})
	if w.decisionAt(task) != delivery.PointPRFeedback {
		t.Errorf("not deciding on the conflict again: at %q", w.decisionAt(task))
	}
	again := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND line LIKE '%conflicts with main%'`, task)
	w.syncRounds(3)
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND line LIKE '%conflicts with main%'`, task); n != again {
		t.Errorf("told %d more times with no wait between", n-again)
	}
	if n := w.count(integrationRuns, task); n != 0 {
		t.Errorf("%d integration Runs before the conductor decided", n)
	}
}
