package orchestrator_test

import (
	"testing"
)

// limitsOf is the conductor's limits answer.
func (w *world) limitsOf(task string) map[string]any {
	w.t.Helper()
	return w.must(task, "limits", `{}`)
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// The two fix-round bounds a pull request fix needs, side by side: the
// delivery policy's per review round (5) and the organisation's per pull
// request (8), each with its source, value, what two landed fixes spent of
// it and what is left; the one that stops the next fix first is named, and
// follows the settings when they change. The head's rerun, the Run's
// reruns and the task's GitHub reads show what was spent of them.
func TestTheConductorSeesBothFixRoundLimitsAndWhatIsSpent(t *testing.T) {
	w := conducting(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"fixRoundsPerPr":8,"ciStuckMinutes":45,"whenBehind":"tell"}'
		WHERE organization_id = $1`, w.org)
	task := w.reviewing()
	setPolicy(w, task, `{"maxPrFixIterations":5}`)
	for i, ask := range []string{"Please rename the greeting.", "And the farewell."} {
		w.gh.Comment(1, "alice", ask)
		w.until("the fix landed", func() bool { w.sync(); return w.fixes(task) == i+1 && w.taskStatus(task) == "review" })
	}
	w.until("both fixes charged", func() bool {
		return w.count(`SELECT COALESCE((state->'prFixesByRepo'->>'target')::int, 0) FROM workflow_runs WHERE task_id = $1`, task) == 2
	})
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}

	out := w.limitsOf(task)
	round := obj(field(out, "fixRounds", "perReviewRound"))
	if round["source"] != "delivery policy" || num(round["limit"]) != 5 || num(round["used"]) != 2 || num(round["remaining"]) != 3 {
		t.Errorf("per review round: %v", round)
	}
	perPR := obj(field(out, "fixRounds", "perPullRequest"))
	if perPR["source"] != "organisation GitHub settings" || num(perPR["limit"]) != 8 {
		t.Errorf("per pull request: %v", perPR)
	}
	pr := obj(field(perPR, "pullRequests", 0))
	if pr["repo"] != "target" || num(pr["used"]) != 2 || num(pr["remaining"]) != 6 || pr["binding"] != "delivery policy" {
		t.Errorf("the pull request's fixes: %v", pr)
	}
	if v := field(out, "ciStuckMinutes", "value"); num(v) != 45 {
		t.Errorf("ciStuckMinutes %v", out["ciStuckMinutes"])
	}
	if v := field(out, "whenBehind", "value"); v != "tell" {
		t.Errorf("whenBehind %v", out["whenBehind"])
	}

	// Three rounds per pull request: one left there, three in the round.
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"fixRoundsPerPr":3}' WHERE organization_id = $1`, w.org)
	pr = obj(field(w.limitsOf(task), "fixRounds", "perPullRequest", "pullRequests", 0))
	if num(pr["remaining"]) != 1 || pr["binding"] != "organisation GitHub settings" {
		t.Errorf("with 3 per pull request: %v", pr)
	}

	if used := field(w.limitsOf(task), "failedCheckReruns", "perHead", 0, "used"); used != false {
		t.Errorf("the head's rerun before any: %v", used)
	}
	w.gh.SetChecks("run:failure")
	w.sync()
	w.must(task, "rerun_failed_checks", `{"pr":"1"}`)
	w.must(task, "github_pull_request", `{"repo":"acme/target","number":1}`)
	out = w.limitsOf(task)
	if used := field(out, "failedCheckReruns", "perHead", 0, "used"); used != true {
		t.Errorf("the head's rerun after one: %v", used)
	}
	if run := obj(field(out, "failedCheckReruns", "thisRun")); num(run["used"]) != 1 || num(run["limit"]) != 10 || num(run["remaining"]) != 9 {
		t.Errorf("this Run's reruns: %v", run)
	}
	if reads := obj(field(out, "githubReadsThisHour")); num(reads["used"]) != 1 || num(reads["limit"]) != 60 {
		t.Errorf("GitHub reads: %v", reads)
	}
}
