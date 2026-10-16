package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// The state a failed hand-over transaction leaves behind: the syncer's
// first transaction committed GitHub's clean reading, its second (the
// closing line) did not. Does anything write the closing line after?
func TestRev6ProbeHandOverLostBetweenTransactions(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
	mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state = 'clean', updated_at = now() WHERE task_id = $1`, wi)
	for range 6 {
		w.sync()
		w.pump()
	}
	var step, status string
	_ = w.owner.QueryRow(context.Background(), `SELECT step, status FROM workflow_runs WHERE task_id=$1`, wi).Scan(&step, &status)
	t.Logf("completions after 6 sync+pump: %d (workflow %s/%s)", w.count(integrationCompleted, wi), step, status)
}
