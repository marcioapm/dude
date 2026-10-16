package orchestrator_test

// The closing line of an integration handed over to the pull request
// syncer, against the syncer's two transactions, the step's closeHandedOver
// and a publish recording.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// A syncer read the pull request before another recorded it clean without
// a closing line (that one read before the hand-over was recorded). The
// first syncer's hand-over and the workflow's closeHandedOver then run at
// once: the delivery's row lock serialises them, so the line is written
// once, and the step's own claim of that row does not block its lock.
func TestAHandOverClosedByTheWorkflowAndASyncerAtOnceIsWrittenOnce(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	ctx := context.Background()
	prID := w.prOf(wi)
	finished := make(chan struct{})
	w.interleaveHandOvers(t, finished)
	read := make(chan struct{})
	var once sync.Once
	handingOver := w.prs.HandingOver
	w.prs.HandingOver = func(ctx context.Context) error {
		once.Do(func() { close(read) })
		return handingOver(ctx)
	}
	w.mergesCleanly()
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

// The syncer's hand-over transaction fails once: nothing of that sync is
// recorded, and the next sync writes the closing line.
func TestAHandOverWhoseTransactionFailsIsCompletedByTheNextSync(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	ctx := context.Background()
	prID := w.prOf(wi)
	injected := errors.New("hand-over transaction failed")
	failed := false
	w.prs.HandingOver = func(context.Context) error {
		if failed {
			return nil
		}
		failed = true
		return injected
	}
	w.mergesCleanly()
	if err := w.prs.Sync(ctx, w.org, prID); !errors.Is(err, injected) {
		t.Fatalf("first sync: %v, want the injected failure", err)
	}
	w.syncRounds(5)
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Errorf("completions %d, want 1", n)
	}
}

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
		w.mergesCleanly()
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
		w.syncRounds(5)
		if c := w.count(integrationCompleted, wi); c != 1 {
			t.Errorf("completions %d, want 1", c)
		}
	})
	t.Run("clean on record without the line", func(t *testing.T) {
		w := newWorld(t)
		wi := w.handedOver(false)
		w.mergesCleanly()
		mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state = 'clean', updated_at = now() WHERE task_id = $1`, wi)
		w.syncRounds(6)
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
			w.mergesCleanly()
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
	w.conflictWithMovedBase()
	w.until("the integration Run", func() bool { w.sync(); return w.count(integrationRuns, wi) == 1 })
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
	w.syncRounds(6)
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Errorf("completions %d, want 1", n)
	}
}

// An integration refused as still conflicting is closed: a later clean
// reading at the same head, as when the base moves on, writes no completed
// line for it and takes no lock on the delivery, nor does the locked closer
// write one, and its escalation still waits for a person.
func TestARefusedHandOverIsNotCompletedByALaterCleanReading(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	ctx := context.Background()
	var prID, repo, head string
	if err := w.owner.QueryRow(ctx, `SELECT p.id, split_part(k, ':', 1), split_part(k, ':', 2) FROM pull_requests p
		JOIN workflow_runs r ON r.task_id = p.task_id, jsonb_object_keys(r.state->'handedOver') k WHERE p.task_id = $1`, wi).
		Scan(&prID, &repo, &head); err != nil {
		t.Fatal(err)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = false })
	before := w.count(escalations, wi)
	w.until("the escalation", func() bool { w.sync(); return w.count(escalations, wi) == before+1 })
	refused := `SELECT count(*) FROM events WHERE task_id=$1 AND event_type='pull_request.action' AND payload->>'action'='integration-refused'`
	if n := w.count(refused, wi); n != 1 {
		t.Fatalf("refusals %d, want 1", n)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	w.syncRounds(4)
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND mergeable_state='clean'`, wi); n != 1 {
		t.Fatalf("the syncer did not record clean")
	}
	w.syncTakesNoDeliveryLock(t, wi, prID)
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		return delivery.HandOverMergedTx(ctx, tx, w.org, wi, repo, head)
	}); err != nil {
		t.Fatal(err)
	}
	if n := w.count(integrationCompleted, wi); n != 0 {
		t.Errorf("completions %d, want 0", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='decide' AND status='waiting'`, wi); n != 1 {
		t.Errorf("the run is not waiting at decide:\n%s", w.describeRuns())
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
				w.mergesCleanly()
				w.until("the closing line", func() bool { w.sync(); return w.count(integrationCompleted, wi) == 1 })
			} else {
				w.toolsWithSync()
				wi = w.reviewing()
			}
			w.syncRounds(2)

			ctx := context.Background()
			var prID string
			if err := w.owner.QueryRow(ctx, `SELECT id FROM pull_requests WHERE task_id = $1 AND mergeable_state = 'clean'`, wi).
				Scan(&prID); err != nil {
				t.Fatalf("no clean pull request: %v", err)
			}
			w.syncTakesNoDeliveryLock(t, wi, prID)
		})
	}
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
	w.until("the integration Run", func() bool { w.sync(); return w.count(integrationRuns, wi) == 1 })
	asked := w.count(escalations, wi)
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	w.until("validation reading GitHub", func() bool {
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

// A conflict the syncer reads after the hand-over is dude's attempt still
// conflicting, under either setting: the escalation names it, with retry.
func TestAHandedOverIntegrationStillConflictingNamesDudesAttempt(t *testing.T) {
	for _, tell := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "tell"}[tell], func(t *testing.T) {
			w := newWorld(t)
			wi := w.handedOver(tell)
			w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = false })
			before := w.count(escalations, wi)
			w.until("the escalation", func() bool { w.sync(); return w.count(escalations, wi) == before+1 })
			raw, e := w.escalationOf(wi)
			var d struct {
				Cause       string `json:"cause"`
				RunID       string `json:"runId"`
				Integration bool   `json:"integration"`
			}
			var detail struct {
				Detail json.RawMessage `json:"detail"`
			}
			_ = json.Unmarshal(raw, &detail)
			_ = json.Unmarshal(detail.Detail, &d)
			run := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND id=$2 AND pr_feedback::text LIKE '%Base integration%'`, wi, d.RunID)
			if strings.Join(e.Actions(), " ") != "retry wait stop" || d.Cause != "still_conflicting" || !d.Integration || run != 1 {
				t.Fatalf("escalation %s, actions %v", raw, e.Actions())
			}
		})
	}
}

// GitHub's first reading after the hand-over says the pull request merges:
// the integration's activity gets its closing line.
func TestAHandedOverIntegrationThatMergesIsCompleted(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	if n := w.count(integrationCompleted, wi); n != 0 {
		t.Fatalf("%d completions before GitHub said", n)
	}
	w.mergesCleanly()
	w.until("the closing line", func() bool { w.sync(); return w.count(integrationCompleted, wi) == 1 })
	w.syncRounds(3)
	if n := w.count(integrationCompleted+` AND payload->>'runId' LIKE 'run_%'`, wi); n != 1 {
		t.Fatalf("completions %d", n)
	}
	if r := w.escalationReason(wi); r != "" {
		t.Fatalf("escalated: %s", r)
	}
}

// The syncer reads the pull request merging in the window between the
// step forgetting GitHub's reading and its hand-over being recorded: it
// finds nothing handed over then, and every reading after is clean on
// clean. The workflow's next pass writes the closing line.
func TestAHandOverTheSyncerReadsBeforeItIsRecordedIsCompleted(t *testing.T) {
	w := newWorld(t)
	var once sync.Once
	armed := w.gated(func(step, next string) {
		if step == "openPullRequest" && next == "awaitPullRequest" {
			once.Do(func() {
				w.mergesCleanly()
				w.sync()
			})
		}
	})
	wi := w.handedOverWith(false, func() { armed.Store(true) })
	w.until("the closing line", func() bool { w.sync(); return w.count(integrationCompleted, wi) == 1 })
	w.syncRounds(3)
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Fatalf("completions %d", n)
	}
}

// HandOverMergedTx runs while the step that records the hand-over is still
// committing: it waits on the step's row lock on the delivery, then sees the
// hand-over and writes the closing line.
func TestAHandOverBeingCommittedIsSeenUnderTheDeliveryLock(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	var key string
	var handedOver []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT k, state->'handedOver' FROM workflow_runs,
		jsonb_object_keys(state->'handedOver') k WHERE task_id = $1`, wi).Scan(&key, &handedOver); err != nil {
		t.Fatal(err)
	}
	repo, rest, _ := strings.Cut(key, ":")
	head, _, _ := strings.Cut(rest, ":")
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'handedOver' WHERE task_id = $1`, wi)

	ctx := context.Background()
	step, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer step.Rollback(ctx)
	if _, err := step.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{handedOver}', $2) WHERE task_id = $1`,
		wi, handedOver); err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() {
		read <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			return delivery.HandOverMergedTx(ctx, tx, w.org, wi, repo, head)
		})
	}()
	select {
	case err := <-read:
		t.Fatalf("HandOverMergedTx read the delivery while its step was committing (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := step.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Fatalf("completions %d, want 1", n)
	}
}

// A conductor publish recording holds the delivery's row (LockRecordingTx)
// and then updates the pull request; the syncer's first clean reading after
// a hand-over updates the pull request and then reads the delivery locked.
// Interleaved, neither waits on the other in a cycle: no 40P01, and the
// closing line is written once.
func TestTheSyncerHandOverDoesNotDeadlockAPublishRecording(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	ctx := context.Background()
	prID := w.prOf(wi)
	w.mergesCleanly()

	var publishPID int
	publishHasW := make(chan struct{})
	syncerWaits := make(chan struct{})
	syncerErr, publishErr := make(chan error, 1), make(chan error, 1)
	go func() {
		publishErr <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&publishPID); err != nil {
				return err
			}
			// edits.go stepHolds FOR UPDATE on the same workflow_runs row.
			if _, err := delivery.LoadDelivery(ctx, tx, wi); err != nil {
				return err
			}
			close(publishHasW)
			<-syncerWaits
			// publish.go recordPublish shape.
			_, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = 'abc', updated_at = now()
				WHERE task_id = $1 AND state IN ('open', 'draft')`, wi)
			return err
		})
	}()
	<-publishHasW
	go func() { syncerErr <- w.prs.Sync(ctx, w.org, prID) }()
	deadline := time.Now().Add(20 * time.Second)
	for w.count(blockedBy, publishPID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the syncer never waited on the delivery's row")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(syncerWaits)
	for name, ch := range map[string]chan error{"syncer": syncerErr, "publish": publishErr} {
		select {
		case err := <-ch:
			var pe *pgconn.PgError
			if errors.As(err, &pe) {
				t.Errorf("%s: PGERR %s %s", name, pe.Code, pe.Message)
			} else if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s hung", name)
		}
	}
	if n := w.count(integrationCompleted, wi); n != 1 {
		t.Fatalf("completions %d, want the syncer's 1", n)
	}
}

// A person pushes a new head after the hand-over, and that head merges:
// dude's integration is not what made it merge, so no closing line.
func TestAHandedOverIntegrationOvertakenByAPersonIsNotCompleted(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(false)
	w.commitFileIn(w.gh.Repo, delivery.BranchFor(wi, 1), "THEIRS.md", "theirs\n", "A person's fix")
	w.mergesCleanly()
	w.until("the new head read as merging", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND head_sha=$2 AND mergeable_state='clean'`,
			wi, w.gh.SHA(delivery.BranchFor(wi, 1))) == 1
	})
	w.syncRounds(3)
	if n := w.count(integrationCompleted, wi); n != 0 {
		t.Fatalf("%d completions for a head dude did not integrate", n)
	}
}

// Under tell, a person's Resolve conflicts integrated once. The base moves
// into a conflict again: that is a person's to resolve, escalated, and no
// integration starts by itself.
func TestUnderTellAnIntegrationOnceAskedIsNotRepeatedUnasked(t *testing.T) {
	w := newWorld(t)
	wi := w.handedOver(true)
	w.mergesCleanly()
	w.until("the closing line", func() bool { w.sync(); return w.count(integrationCompleted, wi) == 1 })
	before := w.count(escalations, wi)
	w.gh.AdvanceBase("main", "Base moved again")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("the conflict escalation", func() bool {
		w.sync()
		return w.count(escalations, wi) == before+1 && w.escalationReason(wi) == "pull_request_conflict"
	})
	w.syncRounds(3)
	if n := w.count(integrationRuns, wi); n != 1 {
		t.Fatalf("%d integration Runs, want the one asked for", n)
	}
}

// While GitHub has not worked out whether the integrated pull request
// merges, the step reads GET /pulls/{n} and nothing else, backing off
// (1 s, 2 s, 4 s…). Past its bound it waits on the pull request syncer: a
// conflict that syncer reads then escalates the attempted pair.
func TestUnknownMergeabilityIsReadCheaplyWithBackOffThenLeftToTheSyncer(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.conflictWithMovedBase()
	w.until("the integration Run", func() bool { w.sync(); return w.count(integrationRuns, wi) == 1 })
	var other atomic.Int32
	var counting atomic.Bool
	w.gh.Set(func(s *fakegithub.Server) {
		s.MergeableUnknown[1] = true
		s.Intercept = func(r *http.Request) int {
			if counting.Load() && !(r.Method == "GET" && r.URL.Path == "/repos/acme/target/pulls/1") {
				other.Add(1)
			}
			return 0
		}
	})
	polls := func(n int) string {
		return fmt.Sprintf(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='openPullRequest'
			AND (state->'integration'->>'polls')::int = %d`, n)
	}
	w.until("the first read", func() bool { return w.count(polls(1), wi) == 1 })
	counting.Store(true)
	w.until("the second read", func() bool { return w.count(polls(2), wi) == 1 })
	if n := w.count(polls(2)+` AND wake_at > now() + interval '1500 milliseconds'`, wi); n != 1 {
		t.Fatal("the second wait did not double")
	}
	counting.Store(false)
	if n := other.Load(); n != 0 {
		t.Fatalf("%d GitHub requests besides GET /pulls/1 while mergeability was unknown", n)
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{integration,polls}', '8'), wake_at = now() WHERE task_id = $1`, wi)
	w.until("waiting on the pull request", func() bool {
		// The push's webhook: the syncer reads mergeability as unknown.
		w.sync()
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'
			AND state->'integration' IS NULL`, wi) == 1
	})
	if r := w.escalationReason(wi); r != "" {
		t.Fatalf("escalated while GitHub had not said: %s", r)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = false })
	w.until("the attempted pair's escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	if n := w.count(integrationRuns, wi); n != 1 {
		t.Fatalf("integration Runs %d", n)
	}
}
