package orchestrator_test

// Unblocking and start when unblocked (docs/design/task-relations.md,
// "Unblocking", "Start when unblocked"): a merge releases what waits on
// it, and a request its owner set fires later, in a worker, after every
// blocker is read again.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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
	begin := func() pgx.Tx {
		tx, err := w.app.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, w.org); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	done := func(tx pgx.Tx, task string) error {
		_, err := delivery.SetTaskStatusTx(ctx, tx, w.org, w.project, task, "", "done", "pull requests merged")
		return err
	}
	tx1, tx2 := begin(), begin()
	defer tx1.Rollback(ctx)
	defer tx2.Rollback(ctx)
	var pid2 uint32
	if err := tx2.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid2); err != nil {
		t.Fatal(err)
	}
	if err := done(tx1, first); err != nil {
		t.Fatal(err)
	}
	moved := make(chan error, 1)
	go func() { moved <- done(tx2, second) }()
	// tx2 either finishes its move, or waits on a lock tx1 holds; either
	// way tx1 commits next, and tx2 after it.
	waiting := func() bool {
		return w.count(`SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND wait_event_type = 'Lock'`, int64(pid2)) == 1
	}
	var err2 error
	finished := false
	for deadline := time.Now().Add(10 * time.Second); !finished && !waiting(); {
		if time.Now().After(deadline) {
			t.Fatal("tx2 neither finished nor waited on a lock")
		}
		select {
		case err2 = <-moved:
			finished = true
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !finished {
		select {
		case err2 = <-moved:
		case <-time.After(10 * time.Second):
			t.Fatal("tx2 did not finish after tx1 committed")
		}
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
// A parked delivery resumes on exactly that row.
func TestAParkedDeliveryResumesOnTheBackendsSignal(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.deliver(task)
	w.blocks(blocker, task)
	w.until("the delivery to park", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'`, task) == 1
	})
	mustExec(t, w.owner, `DELETE FROM task_relations WHERE from_task_id = $1 AND to_task_id = $2`, blocker, task)
	var wf string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM workflow_runs WHERE task_id = $1`, task).Scan(&wf)
	mustExec(t, w.owner, `INSERT INTO workflow_signals (id, organization_id, workflow_run_id, name, payload, idempotency_key)
		VALUES ('sig_0mv5thr4sdb3067ee91a44dca', $1, $2, 'relations.unblocked', '{"removed": true}'::jsonb, NULL)`, w.org, wf)
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'running', wake_at = NULL
		WHERE id = $1 AND status = 'waiting' AND awaiting_signals ? 'relations.unblocked' AND (wait_until IS NULL OR wait_until <= now())`, wf)
	w.until("the parked delivery's implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
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
