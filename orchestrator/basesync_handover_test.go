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

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// blockedBy counts the backends waiting on a lock pid holds.
const blockedBy = `SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY (pg_blocking_pids(pid))`

// The step records the hand-over after the syncer's hand-over transaction
// found none, and its closeHandedOver reads the pull request before the
// syncer's reading commits clean; or clean is on record with no closing
// line however it got there. A later sync reading the pull request clean
// writes the line.
func TestAHandOverRecordedBetweenTheSyncersTransactionsIsCompleted(t *testing.T) {
	t.Run("a syncer between its transactions", func(t *testing.T) {
		w := newWorld(t)
		wi := w.handedOver(false)
		ctx := context.Background()
		var prID string
		var handedOver []byte
		if err := w.owner.QueryRow(ctx, `SELECT p.id, w.state->'handedOver' FROM pull_requests p JOIN workflow_runs w ON w.task_id = p.task_id
			WHERE p.task_id = $1`, wi).Scan(&prID, &handedOver); err != nil {
			t.Fatal(err)
		}
		// As before the step's transition committed: no hand-over on record.
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'handedOver' WHERE task_id = $1`, wi)

		conn, err := pgx.ConnectConfig(ctx, w.owner.Config())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		holder, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Rollback(ctx)
		var pid int
		if err := holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM pull_requests WHERE id = $1 FOR UPDATE`, prID).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
		done := make(chan error, 1)
		go func() { done <- w.prs.Sync(ctx, w.org, prID) }()
		deadline := time.Now().Add(20 * time.Second)
		for w.count(blockedBy, pid) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the syncer never waited on the pull request")
			}
			time.Sleep(5 * time.Millisecond)
		}
		// The step's transition commits HandedOver; its next pass runs
		// closeHandedOver, which reads the pull request still unknown.
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{handedOver}', $2), status = 'running' WHERE task_id = $1`,
			wi, handedOver)
		if _, err := w.runtime.Tick(ctx, 10); err != nil {
			t.Fatal(err)
		}
		if n := w.count(integrationCompleted, wi); n != 0 {
			t.Fatalf("completions %d before the syncer's reading, want 0", n)
		}
		if err := holder.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if n := w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND mergeable_state = 'clean'`, prID); n != 1 {
			t.Fatalf("the syncer did not record clean")
		}
		for range 5 {
			w.sync()
			w.pump()
		}
		if c := w.count(integrationCompleted, wi); c != 1 {
			t.Errorf("completions %d, want 1", c)
		}
	})
	t.Run("clean on record without the line", func(t *testing.T) {
		w := newWorld(t)
		wi := w.handedOver(false)
		w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
		mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state = 'clean', updated_at = now() WHERE task_id = $1`, wi)
		for range 6 {
			w.sync()
			w.pump()
		}
		if c := w.count(integrationCompleted, wi); c != 1 {
			t.Errorf("completions %d, want 1", c)
		}
	})
}

// The step's own hand-over pass, through the runtime: the syncer's
// hand-over transaction runs before the step commits HandedOver, and its
// reading commits clean only after the step's closeHandedOver read the pull
// request unknown and the step parked. A later sync writes the line.
func TestAHandOverPassWhileASyncerWaitsToRecordCleanIsCompleted(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var once sync.Once
	var prID, wi string
	syncErr := make(chan error, 1)
	lockErr := make(chan error, 1)
	parked := `SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'
		AND state->'integration' IS NULL`
	armed := w.gated(func(step, next string) {
		if step != "openPullRequest" || next != "awaitPullRequest" {
			return
		}
		once.Do(func() {
			w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
			// Its own connection: the holder is used from another goroutine.
			conn, err := pgx.ConnectConfig(ctx, w.owner.Config())
			if err != nil {
				t.Error(err)
				return
			}
			t.Cleanup(func() { conn.Close(context.Background()) })
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid(), id FROM pull_requests WHERE task_id = $1 FOR UPDATE`, wi).
				Scan(&pid, &prID); err != nil {
				t.Error(err)
				return
			}
			// Released from here, not the test goroutine: the test goroutine
			// is inside the step's transition, which a syncer holding a lock
			// it needs would keep from committing.
			go func() {
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					var n int
					if err := tx.QueryRow(ctx, parked, wi).Scan(&n); err == nil && n == 1 {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				lockErr <- tx.Commit(ctx)
			}()
			go func() { syncErr <- w.prs.Sync(ctx, w.org, prID) }()
			deadline := time.Now().Add(20 * time.Second)
			for w.count(blockedBy, pid) == 0 {
				if time.Now().After(deadline) {
					t.Error("the syncer never queued on the pull request's row")
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	})
	w.toolsWithSync()
	wi = w.reviewing()
	integrations := `SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`
	w.gh.AdvanceBase("main", "Base moved")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("the integration Run", func() bool { w.sync(); return w.count(integrations, wi) == 1 })
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	w.until("validation reading GitHub", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='openPullRequest'
			AND (state->'integration'->>'polls')::int >= 1`, wi) == 1
	})
	armed.Store(true)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{integration,polls}', '8'), wake_at = now() WHERE task_id = $1`, wi)
	// No sync here: only the held syncer reads GitHub.
	w.until("parked after the hand-over pass", func() bool { return w.count(parked, wi) == 1 })
	select {
	case err := <-lockErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the pull request's lock was never released")
	}
	select {
	case err := <-syncErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the held syncer hung")
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND mergeable_state='clean'`, wi); n != 1 {
		t.Fatalf("the held syncer did not record clean")
	}
	for range 6 {
		w.sync()
		w.pump()
	}
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Errorf("completions %d, want 1", n)
	}
}

// A clean reading with no handed-over integration left to close, none ever
// or one already closed, takes no lock on the delivery: a sync is not
// queued behind a step or a publish recording holding it.
func TestACleanSyncWithNoHandOverToCloseDoesNotLockTheDelivery(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "none", true: "closed"}[closed], func(t *testing.T) {
			w := newWorld(t)
			var wi string
			if closed {
				wi = w.handedOver(false)
				w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1], s.Conflicting[1] = false, false })
				w.until("the closing line", func() bool { w.sync(); return w.count(integrationCompleted, wi) == 1 })
			} else {
				w.toolsWithSync()
				wi = w.reviewing()
			}
			for range 2 {
				w.sync()
				w.pump()
			}
			ctx := context.Background()
			var prID string
			if err := w.owner.QueryRow(ctx, `SELECT id FROM pull_requests WHERE task_id = $1 AND mergeable_state = 'clean'`, wi).
				Scan(&prID); err != nil {
				t.Fatalf("no clean pull request: %v", err)
			}
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
		})
	}
}
