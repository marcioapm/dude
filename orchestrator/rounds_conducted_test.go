package orchestrator_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// roundReview is one review Run of a task, in creation order: its round,
// its range and its first message.
type roundReview struct {
	ID, Of, Base, Prompt string
	Round                int
}

func (w *world) roundReviews(task string) []roundReview {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(review_range->>'of', ''),
			COALESCE(review_range->'base'->>'target', ''), COALESCE(prompt, ''), COALESCE(review_round, 0)
		FROM runs WHERE task_id = $1 AND phase = 'review' AND replaced_by IS NULL ORDER BY created_at, id`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[roundReview])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// roundsWorld is a conducted world whose first review raises one evidenced
// medium finding, whose later reviews judge it fixed, and whose fixer and
// simplifier each commit.
func roundsWorld(t *testing.T) *world {
	w := conducting(t)
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: medium", "FACTORY.md says the wrong thing") + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		case "fix", "simplify":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	return w
}

// The review round counts per loop: round 1 is the implementation's first
// review, round 2 the review after its fix, and the simplifier's diff is a
// loop of its own whose first review is round 1 again. Each review Run
// stores its round, and each finding the round of the review that raised
// it.
func TestTheReviewRoundCountsPerLoopAndStartsAgainForTheSimplifier(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 2", func() bool { return w.decisionAt(task) == delivery.PointReviewed && len(w.roundReviews(task)) > 0 })
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.until("the simplifier's review", func() bool {
		rs := w.roundReviews(task)
		return w.decisionAt(task) == delivery.PointReviewed && rs[len(rs)-1].Of == "simplify"
	})
	var rounds []int
	byRun := map[int]int{}
	for _, r := range w.roundReviews(task) {
		if len(rounds) == 0 || rounds[len(rounds)-1] != r.Round {
			rounds = append(rounds, r.Round)
		}
		byRun[r.Round]++
	}
	if fmt.Sprint(rounds) != "[1 2 1]" {
		t.Errorf("review rounds in order: %v, want [1 2 1]\n%s", rounds, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM review_findings f JOIN runs r ON r.id = f.run_id
		WHERE f.task_id = $1 AND f.review_round IS NOT DISTINCT FROM r.review_round AND f.review_round = 1`, task); n != 1 {
		t.Errorf("findings stored with their review's round 1: %d, want 1", n)
	}
}

// A pull request fix's narrow loop is a loop of its own: whatever round
// the loop before it reached, its first review is round 1, over the fix's
// push (pull_request_fix), not a round of the earlier loop.
func TestTheRoundStartsAgainForAPullRequestFixsNarrowLoop(t *testing.T) {
	w := newPRFixWorldDirect(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"round":4,"roundHeads":{"target":"0000000"}}'::jsonb WHERE task_id = $1`, w.wi)
	w.comment("Please rename the greeting.")
	w.until("the fix's review", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2`,
			w.wi, delivery.RangePullRequestFix) > 0
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
		AND review_round IS DISTINCT FROM 1`, w.wi, delivery.RangePullRequestFix); n != 0 {
		t.Errorf("%d reviews of the pull request's fix not round 1\n%s", n, w.describeRuns())
	}
}

// A reviewer restarted keeps the round of the Run it replaces.
func TestARestartedReviewerKeepsItsRound(t *testing.T) {
	w, task, runs := restartable(t)
	stuck := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET review_round = 3 WHERE id = $1`, stuck)
	out := w.must(task, "restart_run", `{"run":"`+stuck+`","note":"again"}`)
	fresh, _ := out["run"].(string)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND review_round = 3`, fresh); n != 1 {
		t.Errorf("the replacement %s is not round 3", fresh)
	}
}
