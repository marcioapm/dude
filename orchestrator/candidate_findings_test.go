package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// reviewFinding is a blocking correctness finding on FACTORY.md, in the
// scripted reviewer's format, titled title.
func reviewFinding(title string) string {
	return "```yaml\n---\nseverity: blocking\ncategory: correctness\nfile: FACTORY.md\nline: 1\n" +
		"title: " + title + "\ndescription: \"Traced: FACTORY.md:1.\"\nsuggested_fix: Fix it.\n```\n"
}

// A pull request's fix is checked on its candidate with resumed reviewers.
// The candidate's first review raises a finding; its fix changes more than
// MaxResumeDeltaLines as GitHub measures it, so the next round's reviewer
// is fresh (large_delta) and reviews the whole branch, with no review
// range. That reviewer judges the first finding fixed and raises another,
// blocking. The loop goes on because that finding blocks: every fix it
// goes on to is sent it.
func TestAPullRequestFixRoundIsSentTheBlockingFindingThatKeepsTheLoopGoing(t *testing.T) {
	w := newPRFixWorld(t)
	setPolicy(w.world, w.wi, `{"resumeRounds":true}`)
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
	var rangeOf string
	_ = w.owner.QueryRow(ctx, `SELECT COALESCE(review_range->>'of', 'none') FROM runs WHERE id = $1`, secondRun).Scan(&rangeOf)
	// The loop's end: a person asked, or the candidate promoted.
	w.until("the candidate's loop to end", func() bool {
		w.sync()
		return w.taskStatus(w.wi) == "awaiting_input" || w.gh.SHA(w.branch) != w.before
	})
	var status string
	_ = w.owner.QueryRow(ctx, `SELECT status::text FROM review_findings WHERE id = $1`, secondID).Scan(&status)
	afterSecond := `FROM runs WHERE task_id = $1 AND phase = 'fix' AND branch = $2
		AND created_at > (SELECT created_at FROM review_findings WHERE id = $3)`
	candidate := delivery.CandidateBranch(w.wi, 1)
	fixes := w.ids(`SELECT id `+afterSecond+` ORDER BY created_at, id`, w.wi, candidate, secondID)
	sent := w.ids(`SELECT id `+afterSecond+` AND $3 = ANY(finding_ids) ORDER BY created_at, id`, w.wi, candidate, secondID)
	empty := w.ids(`SELECT id `+afterSecond+` AND cardinality(finding_ids) = 0 AND cardinality(feedback_item_ids) = 0
		ORDER BY created_at, id`, w.wi, candidate, secondID)
	if len(fixes) == 0 || len(sent) != len(fixes) {
		t.Fatalf("%d fixes on the candidate after review %s (range %s) raised blocking %s: %d sent it, %d sent no finding and no feedback %v; "+
			"the finding ended %s, the task %s (escalation %q)",
			len(fixes), secondRun, rangeOf, secondID, len(sent), len(empty), empty, status, w.taskStatus(w.wi), w.escalationReason(w.wi))
	}
}
