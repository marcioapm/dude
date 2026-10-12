package orchestrator_test

// Unblocking and start when unblocked (docs/design/task-relations.md,
// "Unblocking", "Start when unblocked"): a merge releases what waits on
// it, and a request its owner set fires later, in a worker, after every
// blocker is read again.

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

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
