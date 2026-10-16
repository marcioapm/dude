package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// interleaveHandOvers holds the syncer's hand-over transaction, when hold
// says so, until the step's closeHandedOver transaction has written and not
// yet committed, then holds that commit until the syncer has finished or
// waits on a lock the step holds. finished is closed by the caller when
// Sync returns.
func (w *world) interleaveHandOvers(finished <-chan struct{}, hold func() bool) {
	gate := make(chan struct{})
	opened := false
	w.prs.HandingOver = func(context.Context) error {
		if !hold() {
			return nil
		}
		select {
		case <-gate:
		case <-time.After(20 * time.Second):
		}
		return nil
	}
	store := &delivery.Store{DB: w.app}
	store.HandOverClosing = func(ctx context.Context, tx pgx.Tx) {
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return
		}
		if !opened {
			opened = true
			close(gate)
		}
		waiting := `SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY (pg_blocking_pids(pid))`
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-finished:
				return
			default:
			}
			var n int
			if err := w.owner.QueryRow(ctx, waiting, pid).Scan(&n); err == nil && n > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	w.runtime.Register(delivery.Workflow(store, forge.Resolver{DB: w.app}))
}

// Probe: the workflow's closeHandedOver runs between the syncer's two
// transactions (mergeable_state committed clean, hand-over not yet written).
func TestRev6ProbeDuplicateClosingLine(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	ctx := context.Background()
	var prID string
	if err := w.owner.QueryRow(ctx, `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	// Held only in the window the probe is after: clean committed before
	// the hand-over.
	w.interleaveHandOvers(finished, func() bool {
		var clean bool
		_ = w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT mergeable_state = 'clean' FROM pull_requests WHERE id = $1`, prID).Scan(&clean)
		})
		return clean
	})
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
	done := make(chan error, 1)
	go func() { err := w.prs.Sync(ctx, w.org, prID); close(finished); done <- err }()
	deadline := time.Now().Add(20 * time.Second)
	for w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND mergeable_state = 'clean'`, prID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never clean")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'running' WHERE task_id = $1`, wi)
	if _, err := w.runtime.Tick(ctx, 10); err != nil {
		t.Fatal(err)
	}
	t.Logf("PROBE after step: completions %d", w.count(integrationCompleted, wi))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Logf("PROBE after syncer: completions %d", w.count(integrationCompleted, wi))
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Errorf("completions %d, want 1", n)
	}
}

// A syncer read the pull request before another recorded it clean without
// a closing line (that one read before the hand-over was recorded). The
// first syncer's hand-over and the workflow's closeHandedOver then run at
// once: the delivery's row lock serialises them, so the line is written
// once, and the step's own claim of that row does not block its lock.
func TestAHandOverClosedByTheWorkflowAndASyncerAtOnceIsWrittenOnce(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	ctx := context.Background()
	var prID string
	if err := w.owner.QueryRow(ctx, `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	w.interleaveHandOvers(finished, func() bool { return true })
	read := make(chan struct{})
	var once sync.Once
	handingOver := w.prs.HandingOver
	w.prs.HandingOver = func(ctx context.Context) error {
		once.Do(func() { close(read) })
		return handingOver(ctx)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
	done := make(chan error, 1)
	go func() { err := w.prs.Sync(ctx, w.org, prID); close(finished); done <- err }()
	select {
	case <-read:
	case <-time.After(20 * time.Second):
		t.Fatal("the syncer never reached its hand-over")
	}
	// The other syncer's clean reading: the held one's own write then loses
	// the race and reads again.
	mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state = 'clean', updated_at = now() WHERE id = $1`, prID)
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'running' WHERE task_id = $1`, wi)
	if n, err := w.runtime.Tick(ctx, 10); err != nil || n != 1 {
		t.Fatalf("tick advanced %d (err %v), want the delivery", n, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the syncer hung")
	}
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Fatalf("completions %d, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest'
		AND status = 'waiting' AND locked_by IS NULL AND last_error IS NULL`, wi); n != 1 {
		t.Fatalf("the step did not commit its transition:\n%s", w.describeRuns())
	}
}
