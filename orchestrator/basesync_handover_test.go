package orchestrator_test

// The closing line of an integration handed over to the pull request
// syncer, against the syncer's two transactions, the step's closeHandedOver
// and a publish recording.

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

// blockedBy counts the backends waiting on a lock pid holds.
const blockedBy = `SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY (pg_blocking_pids(pid))`

// interleaveHandOvers holds the syncer's hand-over transaction until the
// step's closeHandedOver transaction has written and not yet committed,
// then holds that commit until the syncer waits on a lock the step holds.
// finished is closed by the caller when Sync returns. Either side not
// reaching its point within 20 s fails t: the interleaving did not happen.
func (w *world) interleaveHandOvers(t *testing.T, finished <-chan struct{}) {
	gate := make(chan struct{})
	var opened sync.Once
	w.prs.HandingOver = func(context.Context) error {
		select {
		case <-gate:
		case <-time.After(20 * time.Second):
			t.Error("closeHandedOver never reached its hook: no interleaving")
		}
		return nil
	}
	store := &delivery.Store{DB: w.app}
	store.HandOverClosing = func(ctx context.Context, tx pgx.Tx) {
		first := false
		opened.Do(func() { first = true; close(gate) })
		if !first {
			return
		}
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Error(err)
			return
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-finished:
				t.Error("the syncer finished without waiting on closeHandedOver's lock")
				return
			default:
			}
			var n int
			if err := w.owner.QueryRow(ctx, blockedBy, pid).Scan(&n); err == nil && n > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("the syncer never waited on closeHandedOver's lock")
	}
	w.runtime.Register(delivery.Workflow(store, forge.Resolver{DB: w.app}))
}

// syncTakesNoDeliveryLock holds the delivery's row and fails t if a sync of
// prID waits on it.
func (w *world) syncTakesNoDeliveryLock(t *testing.T, wi, prID string) {
	t.Helper()
	ctx := context.Background()
	holder, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	var pid int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM workflow_runs WHERE task_id = $1 FOR UPDATE`, wi).
		Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.prs.Sync(ctx, w.org, prID) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
		if w.count(blockedBy, pid) > 0 {
			_ = holder.Rollback(ctx)
			<-done
			t.Fatal("the sync waited on the delivery's row lock")
		}
		if time.Now().After(deadline) {
			t.Fatal("the sync neither returned nor waited")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// handedOver is an integration whose merge published while GitHub had not
// worked out mergeability, past the step's reads: left to the pull request
// syncer. Under tell it is a person's Resolve conflicts.
func (w *world) handedOver(tell bool) string {
	return w.handedOverWith(tell, func() {})
}

// handedOverWith is handedOver, calling arm just before the step's last
// read of GitHub.
func (w *world) handedOverWith(tell bool, arm func()) string {
	w.t.Helper()
	w.toolsWithSync()
	wi := w.reviewing()
	if tell {
		w.tell()
	}
	w.conflictWithMovedBase()
	if tell {
		w.until("conflict escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
		if code, body := w.callAs(w.person("owner"), "/internal/pull-requests/"+w.prOf(wi)+"/resolve-conflicts", map[string]any{}); code != 200 {
			w.t.Fatalf("resolve: %d %v", code, body)
		}
	}
	w.integrated(wi)
	asked := w.count(escalations, wi)
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	w.agentsDone(wi, "validation reading GitHub", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='openPullRequest'
			AND (state->'integration'->>'polls')::int >= 1`, wi) == 1
	})
	arm()
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{integration,polls}', '8'), wake_at = now() WHERE task_id = $1`, wi)
	w.until("left to the syncer", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'
			AND state->'integration' IS NULL`, wi) == 1
	})
	if n := w.count(escalations, wi); n != asked {
		w.t.Fatalf("escalated while GitHub had not said: %d escalations, %d before", n, asked)
	}
	return wi
}

// handedOverWithout is handedOverWith(false, arm) with no pull request read
// once the step's last read of GitHub is armed: what follows is the
// workflow's alone.
func (w *world) handedOverWithout(arm func()) string {
	w.t.Helper()
	w.toolsWithSync()
	wi := w.reviewing()
	w.conflictWithMovedBase()
	w.integrated(wi)
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	w.agentsDone(wi, "validation reading GitHub", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='openPullRequest'
			AND (state->'integration'->>'polls')::int >= 1`, wi) == 1
	})
	arm()
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{integration,polls}', '8'), wake_at = now() WHERE task_id = $1`, wi)
	return wi
}

// integrated waits for task's integration Run, its merge on the real path,
// to finish: the review after it is left to agentsDone.
func (w *world) integrated(task string) {
	w.t.Helper()
	w.until("the integration Run", func() bool { w.sync(); return w.count(integrationRuns, task) == 1 })
	w.until("the integration Run finished", func() bool {
		w.sync()
		return w.count(integrationRuns+` AND status = 'completed'`, task) == 1
	})
}
