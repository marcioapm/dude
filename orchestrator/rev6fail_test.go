package orchestrator_test

import (
	"context"
	"errors"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// Probe: the syncer's hand-over transaction fails once after the pull
// request's transaction committed clean.
func TestRev6ProbeHandOverTxFailsOnce(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	ctx := context.Background()
	var prID string
	if err := w.owner.QueryRow(ctx, `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID); err != nil {
		t.Fatal(err)
	}
	// The injected failure: the first hand-over transaction rolls back.
	failed := false
	w.prs.HandingOver = func(context.Context) error {
		if failed {
			return nil
		}
		failed = true
		return errors.New("rev6 injected")
	}
	signals := `SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id WHERE r.task_id = $1`
	before := w.count(signals, wi)
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
	t.Logf("PROBE first sync err: %v", w.prs.Sync(ctx, w.org, prID))
	for range 5 {
		w.sync()
		w.pump()
	}
	t.Logf("PROBE completions %d, signals %d (before %d), mergeable %d", w.count(integrationCompleted, wi),
		w.count(signals, wi), before, w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND mergeable_state = 'clean'`, prID))
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Errorf("completions %d, want 1", n)
	}
}
