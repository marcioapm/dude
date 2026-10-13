package orchestrator_test

// Unblocking and start when unblocked (docs/design/task-relations.md,
// "Unblocking", "Start when unblocked"): a merge releases what waits on
// it, and a request its owner set fires later, in a worker, after every
// blocker is read again.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// autoStart sets the task's start when unblocked as the world's person.
func (w *world) autoStart(task, how string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person) VALUES ($1, $2, $3, $4)`,
		task, w.org, how, w.relPerson())
}

func (w *world) fireAutoStarts() int {
	w.t.Helper()
	n, err := (&delivery.AutoStarts{DB: w.app, Starter: w.runtime}).Sweep(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) events(task, typ string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, typ)
}

// mergeFor delivers blocker to its pull request and merges it, as GitHub
// would; the reconciler notices.
func (w *world) mergeFor(blocker string) {
	w.t.Helper()
	w.deliver(blocker)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.gh.Merge(1)
	w.until("the blocker to be merged", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(blocker) == "done"
	})
}

// A merge releases what waits on it: task.unblocked and dude's notice on
// the dependant, which does not start without a request.
func TestAMergeReleasesWhatWaitsOnIt(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.mergeFor(blocker)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.unblocked'
		AND payload->>'by' = $2 AND payload->>'pr' = '1'`, task, w.keyOf(blocker)); n != 1 {
		t.Fatalf("task.unblocked on the dependant: %d", n)
	}
	want := w.keyOf(blocker) + " merged — " + w.keyOf(task) + " can start. Nothing started it: Deliver or Talk it through when you're ready."
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'text' = $2`, task, want); n != 1 {
		t.Fatalf("the Chat notice %q is missing", want)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("an unblocked task started by itself")
	}
}

// Done with nothing merged releases nobody: no task.unblocked, and the
// dependant is told it still blocks, by whom it was marked done.
func TestMarkDoneWithNothingMergedReleasesNobody(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	tom := w.person("Tom")
	mustExec(t, w.owner, `UPDATE tasks SET status = 'review' WHERE id = $1`, blocker)
	if status, body := w.callAs(tom, "/internal/tasks/"+blocker+"/done", map[string]any{}); status != 200 {
		t.Fatalf("mark done: %d %v", status, body)
	}
	if w.events(task, "task.unblocked") != 0 {
		t.Fatal("done with nothing merged released its dependant")
	}
	if n := w.count(`SELECT count(*) FROM events e JOIN api_keys k ON k.person_id = e.payload->>'person'
		WHERE e.task_id = $1 AND e.event_type = 'task.blocker_unmerged' AND e.payload->>'by' = $2 AND k.id = $3`,
		task, w.keyOf(blocker), tom); n != 1 {
		t.Fatalf("the dependant was not told it is still blocked, by Tom")
	}
	status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{})
	refusedBlocked(t, "deliver", status, body,
		w.keyOf(task)+" is blocked by "+w.keyOf(blocker)+" (done, nothing merged): it can start once that's merged.")
}

// Path 11: the merge marks the request due and starts nothing in its own
// transaction; the worker re-reads every blocker and starts it the way its
// owner chose, through the start a person makes.
func TestStartWhenUnblockedFiresAfterTheMergeNotInIt(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "deliver")
	// Blocked: nothing is due, so the worker fires nothing.
	if w.fireAutoStarts() != 0 {
		t.Fatal("a request fired while its task was blocked")
	}
	w.mergeFor(blocker)
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1 AND due_at IS NOT NULL`, task); n != 1 {
		t.Fatalf("the merge did not mark the request due")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatal("the merge's transaction started the task")
	}
	if w.fireAutoStarts() != 1 {
		t.Fatal("the worker took no request")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND NOT (state ? 'decider')`, task); n != 1 {
		t.Fatal("the request did not start a Deliver")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_started' AND payload->>'how' = 'deliver'
		AND payload->>'by' = $2 AND payload->>'setBy' = $3`, task, w.keyOf(blocker), w.relPerson()); n != 1 {
		t.Fatal("task.auto_started was not recorded")
	}
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, task); n != 0 {
		t.Fatal("a fired request was kept")
	}
	w.until("the implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
}

// Refused at fire time: a blocker added since, still open, means nothing
// starts, the refusal is said in Chat, and the request waits, no longer due.
func TestStartWhenUnblockedRechecksEveryBlockerWhenItFires(t *testing.T) {
	w := newWorld(t)
	first, second, task := w.task(), w.task(), w.task()
	w.blocks(first, task)
	w.autoStart(task, "deliver")
	w.mergeFor(first)
	w.blocks(second, task)
	w.fireAutoStarts()
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatal("a request fired past an open blocker")
	}
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1 AND due_at IS NULL`, task); n != 1 {
		t.Fatal("a refused request was not kept waiting")
	}
	if w.startRefusals(task, "auto_start") != 1 || w.events(task, "task.auto_start_refused") != 1 {
		t.Fatal("the refusal was not recorded")
	}
	want := w.keyOf(first) + " merged, but " + w.keyOf(task) + " is still blocked by " + w.keyOf(second) +
		" (not started): it starts when that merges, as Rel asked."
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'text' = $2`, task, want); n != 1 {
		t.Fatalf("the refusal's notice %q is missing", want)
	}
	// Once that one merges too, it fires.
	w.mergeByHand(second)
	mustExec(t, w.owner, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, task)
	w.fireAutoStarts()
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 1 {
		t.Fatal("the request did not fire once nothing was open")
	}
}

// Two workers fire a request once.
func TestStartWhenUnblockedFiresOnce(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "talk")
	w.onModel("conductor", "fake/scripted")
	w.mergeByHand(blocker)
	mustExec(t, w.owner, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, task)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = (&delivery.AutoStarts{DB: w.app, Starter: w.runtime}).Sweep(context.Background())
		}()
	}
	wg.Wait()
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->>'decider' = 'conductor'`, task); n != 1 {
		t.Fatalf("%d deliveries", n)
	}
	if w.events(task, "task.auto_started") != 1 {
		t.Fatalf("%d task.auto_started", w.events(task, "task.auto_started"))
	}
	// Talk it through starts the conductor too, told who asked.
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 1 {
		t.Fatalf("a fired talk made %d conductors", n)
	}
}

// Two pre-existing blockers, one merges: the task is not released and its
// request is not due. Only the last blocker's merge releases it.
func TestOneOfTwoBlockersMergingReleasesNothing(t *testing.T) {
	w := newWorld(t)
	first, second, task := w.task(), w.task(), w.task()
	w.blocks(first, task)
	w.blocks(second, task)
	w.autoStart(task, "deliver")
	w.markMergedDone(first)
	if n := w.events(task, "task.unblocked"); n != 0 {
		t.Fatalf("%d task.unblocked while %s is open", n, w.keyOf(second))
	}
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1 AND due_at IS NULL`, task); n != 1 {
		t.Fatal("the request was marked due with a blocker open")
	}
	w.markMergedDone(second)
	if n := w.events(task, "task.unblocked"); n != 1 {
		t.Fatalf("%d task.unblocked once both merged", n)
	}
}

// markMergedDone gives task a merged pull request and moves it to done the
// way the reconciler does, which releases what waits on it.
func (w *world) markMergedDone(task string) {
	w.t.Helper()
	w.mergedPR(task)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, task, "", "done", "pull requests merged")
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// mergedPR gives an unfinished task a merged pull request and puts it in review.
func (w *world) mergedPR(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE tasks SET status = 'review' WHERE id = $1`, task)
	mustExec(w.t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
			head_branch, base_branch, head_sha, title, state)
		VALUES ('pr_m_' || $1, $2, $3, $1, $4, 9000 + (SELECT number FROM tasks WHERE id = $1), 'u', 'h', 'main', 's', 't', 'merged')`,
		task, w.org, w.project, w.repoID)
}

// orgTx is an open transaction as the app role in the world's
// organisation, for tests that interleave two; rolled back at cleanup
// unless committed.
type orgTx struct {
	pgx.Tx
	w   *world
	pid int64
}

func (w *world) openTx() *orgTx {
	w.t.Helper()
	ctx := context.Background()
	tx, err := w.app.Pool.Begin(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, w.org); err != nil {
		w.t.Fatal(err)
	}
	o := &orgTx{Tx: tx, w: w}
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&o.pid); err != nil {
		w.t.Fatal(err)
	}
	return o
}

// async runs f, which uses this transaction, in a goroutine.
func (o *orgTx) async(f func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	return done
}

// finishedOrWaiting returns once f (from async) has returned, with its
// error, or once this transaction's backend waits on a lock.
func (o *orgTx) finishedOrWaiting(done <-chan error) (bool, error) {
	o.w.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		select {
		case err := <-done:
			return true, err
		default:
		}
		if o.w.count(`SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND wait_event_type = 'Lock'`, o.pid) == 1 {
			return false, nil
		}
		if time.Now().After(deadline) {
			o.w.t.Fatal("the transaction neither finished nor waited on a lock")
		}
		select {
		case err := <-done:
			return true, err
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// result is f's error, once it returns.
func (o *orgTx) result(done <-chan error) error {
	o.w.t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		o.w.t.Fatal("the transaction did not finish")
		return nil
	}
}

// moveToDone is the merge step's move of task to done, in tx.
func (w *world) moveToDone(tx pgx.Tx, task string) error {
	_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, task, "", "done", "pull requests merged")
	return err
}

// Both blockers of one task move to done in overlapping transactions: the
// second waits on the first, sees it merged, and releases the task once.
func TestTwoBlockersMergingTogetherReleaseTheTaskOnce(t *testing.T) {
	w := newWorld(t)
	first, second, task := w.task(), w.task(), w.task()
	w.blocks(first, task)
	w.blocks(second, task)
	w.autoStart(task, "deliver")
	w.mergedPR(first)
	w.mergedPR(second)
	ctx := context.Background()
	tx1, tx2 := w.openTx(), w.openTx()
	if err := w.moveToDone(tx1.Tx, first); err != nil {
		t.Fatal(err)
	}
	moved := tx2.async(func() error { return w.moveToDone(tx2.Tx, second) })
	// tx2 either finishes its move, or waits on a lock tx1 holds; either
	// way tx1 commits next, and tx2 after it.
	finished, err2 := tx2.finishedOrWaiting(moved)
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !finished {
		err2 = tx2.result(moved)
	}
	if err2 != nil {
		t.Fatal(err2)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.events(task, "task.unblocked"); n != 1 {
		t.Fatalf("%d task.unblocked after both blockers merged", n)
	}
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1 AND due_at IS NOT NULL`, task); n != 1 {
		t.Fatal("the request was never marked due")
	}
	if w.fireAutoStarts() != 1 || w.fireAutoStarts() != 0 {
		t.Fatal("the request did not fire exactly once")
	}
}

// firstOf is the error of whichever of a and b returns first, and which.
func (w *world) firstOf(a, b <-chan error) (aFirst bool, err error) {
	w.t.Helper()
	select {
	case err := <-a:
		return true, err
	case err := <-b:
		return false, err
	case <-time.After(10 * time.Second):
		w.t.Fatal("neither transaction finished")
		return false, nil
	}
}

// A person acts on a delivery parked on its blocker (Chat, Start anyway:
// its delivery's row, then its task's, as LockEscalationTx takes them)
// while the blocker merges: both commit, and the task is released once.
func TestAMergeAndAPersonActingOnTheParkedDependantBothCommit(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.deliver(task)
	w.blocks(blocker, task)
	w.until("the delivery to park", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'`, task) == 1
	})
	w.mergedPR(blocker)
	ctx := context.Background()
	person, merge := w.openTx(), w.openTx()
	if _, err := delivery.LoadDelivery(ctx, person, task); err != nil {
		t.Fatal(err)
	}
	merged := merge.async(func() error { return w.moveToDone(merge.Tx, blocker) })
	if finished, err := merge.finishedOrWaiting(merged); finished {
		t.Fatalf("the merge signalled a delivery the person holds: %v", err)
	}
	acted := person.async(func() error { return delivery.LockTaskTx(ctx, person, task) })
	if personFirst, err := w.firstOf(acted, merged); !personFirst || err != nil {
		t.Fatalf("person first %v: %v", personFirst, err)
	}
	if err := person.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := merge.result(merged); err != nil {
		t.Fatalf("the merge: %v", err)
	}
	if err := merge.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.events(task, "task.unblocked"); n != 1 {
		t.Fatalf("%d task.unblocked", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1 AND s.name = 'relations.unblocked'`, task); n != 1 {
		t.Fatalf("%d relations.unblocked signals", n)
	}
}

// The worker fires a request (its task_auto_starts row, then the task's
// guard) while a blocker added since merges: both commit, the fire is
// refused on the blocker it saw open, and the merge marks the request due
// again, so the next sweep starts the task.
func TestAMergeAndTheWorkerFiringItsDependantBothCommit(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "deliver")
	mustExec(t, w.owner, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, task)
	w.mergedPR(blocker)
	ctx := context.Background()
	fire, merge := w.openTx(), w.openTx()
	if _, err := fire.Exec(ctx, `SELECT 1 FROM task_auto_starts WHERE task_id = $1 FOR UPDATE`, task); err != nil {
		t.Fatal(err)
	}
	merged := merge.async(func() error { return w.moveToDone(merge.Tx, blocker) })
	if finished, err := merge.finishedOrWaiting(merged); finished {
		t.Fatalf("the merge marked due a request the worker holds: %v", err)
	}
	guarded := fire.async(func() error { return delivery.RefuseBlockedTx(ctx, fire, task) })
	fireFirst, err := w.firstOf(guarded, merged)
	if _, blocked := delivery.AsBlocked(err); !fireFirst || !blocked {
		t.Fatalf("fire first %v: %v", fireFirst, err)
	}
	// As fire takes a refusal: the request waits, not due.
	if _, err := fire.Exec(ctx, `UPDATE task_auto_starts SET due_at = NULL WHERE task_id = $1`, task); err != nil {
		t.Fatal(err)
	}
	if err := fire.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := merge.result(merged); err != nil {
		t.Fatalf("the merge: %v", err)
	}
	if err := merge.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if w.fireAutoStarts() != 1 {
		t.Fatal("the merge's release of the request was lost")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 1 {
		t.Fatalf("%d deliveries", n)
	}
}

// Two merges whose dependants cross: B blocks A and D, A blocks D, and A
// was started past B. A holds its own row while B's merge releases B's
// dependants (D before A, by id), then A's merge releases D: both commit,
// and D, free once both are, is released once.
func TestTwoMergesWithCrossedDependantsBothCommit(t *testing.T) {
	w := newWorld(t)
	d := w.task()
	a, b := w.task(), w.task()
	w.blocks(b, a)
	w.blocks(b, d)
	w.blocks(a, d)
	w.mergedPR(a)
	w.mergedPR(b)
	ctx := context.Background()
	ta, tb := w.openTx(), w.openTx()
	if _, err := ta.Exec(ctx, `UPDATE tasks SET updated_at = now() WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	mergedB := tb.async(func() error { return w.moveToDone(tb.Tx, b) })
	finishedB, errB := tb.finishedOrWaiting(mergedB)
	mergedA := ta.async(func() error { return w.moveToDone(ta.Tx, a) })
	if !finishedB {
		var bFirst bool
		if bFirst, errB = w.firstOf(mergedB, mergedA); !bFirst {
			t.Fatalf("A's merge returned first, with B's waiting: %v", errB)
		}
	}
	if errB != nil {
		t.Fatalf("B's merge: %v", errB)
	}
	if err := tb.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ta.result(mergedA); err != nil {
		t.Fatalf("A's merge: %v", err)
	}
	if err := ta.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.events(d, "task.unblocked"); n != 1 {
		t.Fatalf("%d task.unblocked on D", n)
	}
}

// A request that fails to fire for a reason other than a blocker ends: no
// row is left to fire on some later merge, and Chat and Activity say why.
func TestStartWhenUnblockedThatFailsEnds(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "deliver")
	// A project policy that does not decode: StartTx fails, not the guard.
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = '{"maxReviewIterations": "many"}' WHERE id = $1`, w.project)
	w.markMergedDone(blocker)
	if w.fireAutoStarts() != 1 {
		t.Fatal("the worker took no request")
	}
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, task); n != 0 {
		t.Fatal("a request that failed to fire was kept")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_start_cancelled'
		AND payload->>'why' = 'failed' AND payload->>'error' LIKE '%maxReviewIterations%'`, task); n != 1 {
		t.Fatal("task.auto_start_cancelled {why: failed, error} is missing")
	}
	var text string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'text' FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
		AND payload->>'about' = 'auto_start_failed'`, task).Scan(&text)
	prefix := w.keyOf(blocker) + " merged, but " + w.keyOf(task) + " could not start: "
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, ". Start it from its page.") {
		t.Fatalf("the Chat notice is %q, want %q…", text, prefix)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatal("a failed start made a delivery")
	}
}

// requestKept says the task's request is still standing and due, with
// attempts counted fires, and that nothing ended it or told Chat it failed.
func (w *world) requestKept(task string, attempts int) {
	w.t.Helper()
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1 AND due_at IS NOT NULL AND fire_attempts = $2`,
		task, attempts); n != 1 {
		w.t.Fatalf("the request is not kept due with %d attempts", attempts)
	}
	if w.events(task, "task.auto_start_cancelled") != 0 ||
		w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'auto_start_failed'`, task) != 0 {
		w.t.Fatal("the request was ended as failed")
	}
}

func (w *world) futureRequestUntouched(task string, attempts int, worker *delivery.AutoStarts) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE task_auto_starts SET due_at = now() + interval '1 hour' WHERE task_id = $1`, task)
	var before, after time.Time
	if err := w.owner.QueryRow(context.Background(), `SELECT due_at FROM task_auto_starts WHERE task_id = $1`, task).Scan(&before); err != nil {
		w.t.Fatal(err)
	}
	if n, err := worker.Sweep(context.Background()); err != nil || n != 0 {
		w.t.Fatalf("future request selected: n=%d err=%v", n, err)
	}
	w.requestKept(task, attempts)
	if err := w.owner.QueryRow(context.Background(), `SELECT due_at FROM task_auto_starts WHERE task_id = $1`, task).Scan(&after); err != nil || !after.Equal(before) {
		w.t.Fatalf("future due time changed: %v -> %v, %v", before, after, err)
	}
}

// dueNow makes the task's request due now, as the next sweep finds it.
func (w *world) dueNow(task string) {
	mustExec(w.t, w.owner, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, task)
}

// A fire that loses a deadlock is the database's failure, not the start's:
// the request stays due, Chat is told nothing, and the next sweep starts
// the task. The deadlock is real: a writer holds the task's row and then
// wants the request's, while the fire holds the request's and its guard
// wants the task's; the fire waited first, so it is the one aborted.
func TestStartWhenUnblockedThatLosesADeadlockIsRetried(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "deliver")
	w.markMergedDone(blocker)
	ctx := context.Background()
	other := w.openTx()
	if _, err := other.Exec(ctx, `SELECT 1 FROM tasks WHERE id = $1 FOR NO KEY UPDATE`, task); err != nil {
		t.Fatal(err)
	}
	swept := make(chan error, 1)
	go func() {
		_, err := (&delivery.AutoStarts{DB: w.app, Starter: w.runtime}).Sweep(ctx)
		swept <- err
	}()
	w.until("the fire to wait on the task's row", func() bool {
		return w.count(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> $1
			AND wait_event_type = 'Lock' AND query LIKE '%FROM tasks WHERE id = $1 FOR NO KEY UPDATE%'`, other.pid) == 1
	})
	locked := other.async(func() error {
		_, err := other.Exec(ctx, `SELECT 1 FROM task_auto_starts WHERE task_id = $1 FOR UPDATE`, task)
		return err
	})
	if err := other.result(locked); err != nil {
		t.Fatalf("the writer lost the deadlock, not the fire: %v", err)
	}
	if err := other.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-swept; err != nil {
		t.Fatal(err)
	}
	w.requestKept(task, 0)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatal("the deadlocked fire started the task")
	}
	w.dueNow(task)
	if w.fireAutoStarts() != 1 || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task) != 1 {
		t.Fatal("the retried request did not start the task")
	}
}

// failingStarter is a start that fails with err.
type failingStarter struct{ err error }

func (f failingStarter) StartInTx(context.Context, pgx.Tx, workflow.StartOptions) (string, bool, error) {
	return "", false, f.err
}

// Transient failures of the start (serialisation, a statement timeout, a
// connection reset) never end a request, however many times they happen.
func TestStartWhenUnblockedFailingTransientlyIsKept(t *testing.T) {
	for _, cause := range []error{
		fmt.Errorf("start: %w", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}),
		fmt.Errorf("start: %w", &pgconn.PgError{Code: "40001", Message: "could not serialize access"}),
		fmt.Errorf("start: %w", &pgconn.PgError{Code: "57014", Message: "statement timeout"}),
		&pgconn.PgError{Code: "40001", Message: "could not serialize access"},
		&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
		&pgconn.PgError{Code: "08006", Message: "connection failure"},
		fmt.Errorf("read: %w", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}),
		fmt.Errorf("receive message: %w", io.ErrUnexpectedEOF),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			w := newWorld(t)
			blocker, task := w.task(), w.task()
			w.blocks(blocker, task)
			w.autoStart(task, "deliver")
			w.markMergedDone(blocker)
			for range 7 {
				if _, err := (&delivery.AutoStarts{DB: w.app, Starter: failingStarter{cause}}).Sweep(context.Background()); err != nil {
					t.Fatal(err)
				}
				w.requestKept(task, 0)
				w.futureRequestUntouched(task, 0, &delivery.AutoStarts{DB: w.app, Starter: failingStarter{cause}})
				w.dueNow(task)
			}
		})
	}
}

// An unknown failure is retried, counted, and ends the request as failed
// at the fifth, with the error, saying so in Chat.
func TestStartWhenUnblockedFailingUnknownlyEndsAfterItsBound(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "deliver")
	w.markMergedDone(blocker)
	failing := &delivery.AutoStarts{DB: w.app, Starter: failingStarter{errors.New("the starter broke")}}
	for i := 1; i < 5; i++ {
		if _, err := failing.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.requestKept(task, i)
		if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1 AND due_at > now()`, task); n != 1 {
			t.Fatal("a retried request is due at once, not after a pause")
		}
		w.futureRequestUntouched(task, i, failing)
		w.dueNow(task)
	}
	if _, err := failing.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, task); n != 0 {
		t.Fatal("the request outlived its bound")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_start_cancelled'
		AND payload->>'why' = 'failed' AND payload->>'error' = 'the starter broke'`, task); n != 1 {
		t.Fatal("task.auto_start_cancelled {why: failed, error} is missing")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'auto_start_failed'`, task); n != 1 {
		t.Fatal("Chat was not told")
	}
}

// A delivery parked at its implement step resumes when its conductor
// removes the last blocker: the removal signals it, nothing merged.
func TestAParkedDeliveryResumesWhenItsConductorRemovesTheLastBlocker(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	blocker, task := w.task(), w.task()
	w.deliver(task)
	// The task's conductor, live, and the edge as its Run's.
	token, hash := agenttools.NewToken()
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, base_refs, mcp_token_hash)
		VALUES ('run_c_'||$1, $2, $3, $1, 1, 'paused', 'agent', 'conductor', '{}', $4)`, task, w.org, w.project, hash)
	mustExec(t, w.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_run_id)
		VALUES ($1, $2, $3, 'blocks', 'run_c_'||$3)`, w.org, blocker, task)
	w.until("the delivery to park", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'`, task) == 1
	})
	req, _ := http.NewRequest("POST", tools.URL+"/tools/update_task",
		strings.NewReader(`{"removeRelations":[{"kind":"blocked_by","task":"`+w.keyOf(blocker)+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("update_task removeRelations: %d %s", res.StatusCode, b)
	}
	if n := w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1 AND s.name = 'relations.unblocked'`, task); n != 1 {
		t.Fatalf("%d relations.unblocked signals", n)
	}
	w.until("the parked delivery's implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
}

// The backend's removal of a last blocker (relations.ts releasedByRemoval)
// writes its signal by hand: the same row workflow.SignalTx writes (a sig_
// id, the name, an object payload, no idempotency key) and the same wake.
// A parked delivery resumes on exactly that row, consuming it.
func TestAParkedDeliveryResumesOnTheBackendsSignal(t *testing.T) {
	w := newWorld(t)
	task, wf := w.parkedAndUnblocked()
	w.backendSignal(wf)
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'running', wake_at = NULL
		WHERE id = $1 AND status = 'waiting' AND awaiting_signals ? 'relations.unblocked' AND (wait_until IS NULL OR wait_until <= now())`, wf)
	w.until("the parked delivery's implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
	w.backendSignalConsumed()
}

// The backend's row alone, without its wake, resumes the delivery too: the
// claim takes a waiting run with an unconsumed signal it awaits. The park's
// own recheck is minutes away, so nothing else resumes it in the test.
func TestAParkedDeliveryResumesOnTheBackendsSignalRowAlone(t *testing.T) {
	w := newWorld(t)
	task, wf := w.parkedAndUnblocked()
	w.backendSignal(wf)
	w.until("the parked delivery's implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
	w.backendSignalConsumed()
}

// parkedAndUnblocked is a delivery parked at its implement step on a
// blocker whose edge is then removed, with no signal: its task and its
// delivery.
func (w *world) parkedAndUnblocked() (task, wf string) {
	w.t.Helper()
	blocker := w.task()
	task = w.task()
	w.deliver(task)
	w.blocks(blocker, task)
	w.until("the delivery to park", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'`, task) == 1
	})
	mustExec(w.t, w.owner, `DELETE FROM task_relations WHERE from_task_id = $1 AND to_task_id = $2`, blocker, task)
	wf = w.str(`SELECT id FROM workflow_runs WHERE task_id = $1`, task)
	return task, wf
}

const backendSignalID = "sig_0mv5thr4sdb3067ee91a44dca"

// backendSignal inserts the row releasedByRemoval writes.
func (w *world) backendSignal(wf string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO workflow_signals (id, organization_id, workflow_run_id, name, payload, idempotency_key)
		VALUES ($1, $2, $3, 'relations.unblocked', '{"removed": true}'::jsonb, NULL)`, backendSignalID, w.org, wf)
}

func (w *world) backendSignalConsumed() {
	w.t.Helper()
	if n := w.count(`SELECT count(*) FROM workflow_signals WHERE id = $1 AND consumed_at IS NOT NULL`, backendSignalID); n != 1 {
		w.t.Fatal("the backend's signal was not consumed by the resumed delivery")
	}
}

// The owner cancels while the worker takes the request: the worker skips
// the row the cancel holds, and once the cancel commits nothing starts.
// (The cancel is the backend's DELETE, made here in an open transaction.)
func TestAnOwnersCancelRacingTheWorkerStartsNothing(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "deliver")
	w.markMergedDone(blocker)
	ctx := context.Background()
	cancel, err := w.app.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cancel.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, w.org); err != nil {
		t.Fatal(err)
	}
	if tag, err := cancel.Exec(ctx, `DELETE FROM task_auto_starts WHERE task_id = $1`, task); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("the cancel: %v %v", tag, err)
	}
	// The sweep sees the due row (the cancel is not committed) but cannot
	// take it, and does not wait for it.
	swept := make(chan int, 1)
	go func() {
		n, _ := (&delivery.AutoStarts{DB: w.app, Starter: w.runtime}).Sweep(ctx)
		swept <- n
	}()
	select {
	case n := <-swept:
		if n != 1 {
			t.Fatalf("the sweep found %d due requests, want the one being cancelled", n)
		}
	case <-time.After(10 * time.Second):
		_ = cancel.Rollback(ctx)
		t.Fatal("the sweep waited on the owner's cancel")
	}
	if err := cancel.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w.fireAutoStarts()
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatal("a cancelled request started its task")
	}
	if w.events(task, "task.auto_started") != 0 || w.events(task, "task.auto_start_cancelled") != 0 {
		t.Fatal("the skipped request was fired or ended by the worker")
	}
}

// A delivery parked at its implement step (path 5) resumes when its
// blocker merges: a person chose to start it already.
func TestAParkedDeliveryResumesWhenItsBlockerMerges(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.deliver(task)
	w.blocks(blocker, task)
	w.until("the delivery to park", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'`, task) == 1
	})
	w.mergeFor(blocker)
	w.until("the parked delivery's implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
}

// A conducted task's live conductor is woken when the task can start; it
// starts nothing.
func TestTheConductorIsWokenWhenItsTaskCanStart(t *testing.T) {
	w := conducting(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	// A conductor, asked about the task in Chat after an override was
	// refused: it is live though the task has not started.
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, base_refs)
		VALUES ('run_c_'||$1, $2, $3, $1, 1, 'paused', 'agent', 'conductor', '{}')`, task, w.org, w.project)
	w.mergeByHand(blocker)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'review' WHERE id = $1`, blocker)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, blocker, "", "done", "pull requests merged")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'unblocked'`, task); n != 1 {
		t.Fatal("the conductor was not woken")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatal("the wake started the task")
	}
}

// Any start consumes the request: Deliver here.
func TestAnyStartEndsTheRequest(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	w.autoStart(task, "talk")
	if status, body := w.callAs(w.person("Ana"), "/internal/tasks/"+task+"/deliver", map[string]any{"override": true}); status != 201 {
		t.Fatalf("Start anyway: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_start_cancelled'
		AND payload->>'why' = 'started'`, task); n != 1 {
		t.Fatal("the start did not end the request")
	}
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, task); n != 0 {
		t.Fatal("the request outlived the start")
	}
}
