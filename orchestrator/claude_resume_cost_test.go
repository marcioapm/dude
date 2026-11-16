package orchestrator_test

import (
	"context"
	"math"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A reviewer on Claude Code taken over for its next rounds is resumed into
// its session, whose running total (total_cost_usd) carries on across the
// resumes: each round's row holds its own share, and the last row's base
// plus its share is the session's total, counted once.
func TestAResumedClaudeReviewerCountsItsSessionCostOnce(t *testing.T) {
	w := resuming(t)
	for _, role := range []string{"implementer", "reviewer", "simplifier"} {
		w.setHarness(role, "claude-code")
	}
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	reviews := w.reviewRows(wi)
	if len(reviews) < 2 {
		t.Fatalf("%d review Runs, want the first round and its resumed rounds", len(reviews))
	}
	luxRun := reviews[len(reviews)-1].LuxRun
	// The session's own total: its last result line's.
	total := -1.0
	for _, r := range w.lux.Records(luxRun) {
		if r["type"] == "claude.result" {
			total, _ = r["data"].(map[string]any)["total_cost_usd"].(float64)
		}
	}
	if total <= 0 {
		t.Fatalf("no Claude result on %s", luxRun)
	}
	rows, err := w.owner.Query(context.Background(), `SELECT agent_cost_usd::float8, agent_cost_base::float8 FROM runs
		WHERE task_id = $1 AND phase = 'review' ORDER BY created_at, id`, wi)
	if err != nil {
		t.Fatal(err)
	}
	costs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Share, Base float64 }])
	if err != nil {
		t.Fatal(err)
	}
	sum := 0.0
	for i, c := range costs {
		if math.Abs(c.Base-sum) > 1e-9 {
			t.Errorf("review %d's base = %v, want its predecessors' %v (all: %v)", i+1, c.Base, sum, costs)
		}
		if c.Share < 0.0099 {
			t.Errorf("review %d's share = %v, want its own turns' (all: %v)", i+1, c.Share, costs)
		}
		sum += c.Share
	}
	last := costs[len(costs)-1]
	if math.Abs(last.Base+last.Share-total) > 1e-9 || math.Abs(sum-total) > 1e-9 {
		t.Errorf("rows sum to %v (last base %v + share %v), want the session's total %v", sum, last.Base, last.Share, total)
	}
}
