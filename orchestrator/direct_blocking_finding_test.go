package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A pull request's fix, pushed directly, is reviewed with resumed
// reviewers. The first review raises a finding; its fix changes more than
// MaxResumeDeltaLines as GitHub measures it, so the next round's reviewer
// is fresh (large_delta) and reviews the whole branch. That reviewer
// judges the first finding fixed and raises another, blocking. The loop
// goes on because that finding blocks: every fix it goes on to is sent it.
func TestAPullRequestFixRoundIsSentTheBlockingFindingThatKeepsTheLoopGoing(t *testing.T) {
	w := newPRFixWorld(t)
	setPolicy(w.world, w.wi, `{"resumeRounds":true,"maxPrFixReviewIterations":4}`)
	var large atomic.Bool
	w.gh.CompareAnswer = func(base, head string, answer map[string]any) {
		if large.Load() {
			answer["files"] = []any{map[string]any{"filename": "big.go", "status": "modified",
				"additions": delivery.MaxResumeDeltaLines, "deletions": 1}}
		}
	}
	const first, second = "The fix leaves the greeting untranslated", "The cancel test passes with the fix reverted"
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		switch {
		case phase == "review" && n == 1:
			b.Reply = "One problem:\n\n" + reviewFinding(first)
		case phase == "fix" && n == 2:
			// The round after this fix measures a change over the bound.
			large.Store(true)
		case phase == "review" && n == 2:
			large.Store(false)
			b.Reply = fakeagent.Verdict + "\nOne more problem:\n\n" + reviewFinding(second)
		}
	}
	w.comment("Please rename the greeting.")

	ctx := context.Background()
	var secondID, secondRun string
	w.until("the second round's finding", func() bool {
		w.sync()
		_ = w.owner.QueryRow(ctx, `SELECT id, run_id FROM review_findings WHERE task_id = $1 AND title = $2`,
			w.wi, second).Scan(&secondID, &secondRun)
		return secondID != ""
	})
	var fresh string
	_ = w.owner.QueryRow(ctx, `SELECT COALESCE(payload->>'fresh', '') FROM events WHERE run_id = $1 AND event_type = 'run.created'`,
		secondRun).Scan(&fresh)
	if fresh != "large_delta" {
		t.Fatalf("review %s was not made fresh for a large delta (fresh %q): the repro's premise does not hold", secondRun, fresh)
	}
	// The loop's end: the pull request in review again, or a person asked.
	w.agentsDone(w.wi, "the fix loop to end", func() bool {
		w.sync()
		s := w.taskStatus(w.wi)
		return s == "awaiting_input" || s == "review"
	})
	var status string
	_ = w.owner.QueryRow(ctx, `SELECT status::text FROM review_findings WHERE id = $1`, secondID).Scan(&status)
	// Every fix after the finding: published to the pull request's own
	// branch (direct), each sent the finding.
	afterSecond := `FROM runs WHERE task_id = $1 AND phase = 'fix' AND COALESCE(branch, $2) = $2
		AND created_at > (SELECT created_at FROM review_findings WHERE id = $3)`
	fixes := w.ids(`SELECT id `+afterSecond+` ORDER BY created_at, id`, w.wi, w.branch, secondID)
	sent := w.ids(`SELECT id `+afterSecond+` AND $3 = ANY(finding_ids) ORDER BY created_at, id`, w.wi, w.branch, secondID)
	if len(fixes) == 0 || len(sent) != len(fixes) {
		t.Fatalf("%d fixes after review %s raised blocking %s, %d sent it; the finding ended %s, the task %s (escalation %q)\n%s",
			len(fixes), secondRun, secondID, len(sent), status, w.taskStatus(w.wi), w.escalationReason(w.wi), w.describeRuns())
	}
	if w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) != "" {
		t.Errorf("a candidate branch was written")
	}
	if reason := w.escalationReason(w.wi); reason == "exhausted" {
		t.Errorf("the loop ended exhausted (finding %s %s, task %s)", secondID, status, w.taskStatus(w.wi))
	}
}
