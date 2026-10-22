package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The task's row and its delivery's are locked delivery first, task second
// by every transaction that takes both (LockEscalationTx). These tests hold
// a child start or restart between its two locks while a person's Close,
// Archive or a status change to an ended status runs, so an inversion shows
// as Postgres's deadlock (40P01) on every run rather than under load.

func isDeadlock(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "40P01"
}

// waitForLockWaiter returns once a backend in the test's database waits on
// a heavyweight lock: the ender is queued behind the held transaction. An
// error when ended closes first (the ender finished without waiting) or
// ctx ends.
func (w *childWorld) waitForLockWaiter(ctx context.Context, ended <-chan struct{}) error {
	for {
		var n int
		if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			return fmt.Errorf("polling for a lock waiter: %w", err)
		}
		if n > 0 {
			return nil
		}
		select {
		case <-ended:
			return errors.New("the ender ended without waiting on a lock")
		case <-ctx.Done():
			return fmt.Errorf("the ender never waited on a lock: %w", ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// The two orders themselves, with nothing of dude's code: Postgres refuses
// one side with 40P01. What the code-path tests below would see on an
// inversion.
func TestOpposedTaskAndDeliveryLocksDeadlock(t *testing.T) {
	w := newChildWorld(t)
	ctx, cancel := context.WithTimeout(w.ctx, raceTimeout)
	defer cancel()
	begin := func() pgx.Tx {
		tx, err := w.s.DB.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, w.org); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	taskFirst, deliveryFirst := begin(), begin()
	if _, err := taskFirst.Exec(ctx, `SELECT 1 FROM tasks WHERE id = 'wi_life' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDelivery(ctx, deliveryFirst, "wi_life"); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() {
		_, err := LoadDelivery(ctx, taskFirst, "wi_life")
		errs <- err
	}()
	if err := w.waitForLockWaiter(ctx, nil); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := deliveryFirst.Exec(ctx, `SELECT 1 FROM tasks WHERE id = 'wi_life' FOR NO KEY UPDATE`)
		errs <- err
	}()
	deadlocks := 0
	for range 2 {
		var err error
		select {
		case err = <-errs:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if isDeadlock(err) {
			deadlocks++
			continue
		}
		if err != nil {
			t.Fatalf("not a deadlock: %v", err)
		}
	}
	if deadlocks != 1 {
		t.Fatalf("deadlocks %d, want 1", deadlocks)
	}
}

// enders are the person's and the workflow's ways of ending a task that
// lock its delivery and then its row.
var enders = []struct {
	name string
	end  func(ctx context.Context, tx pgx.Tx, org string) error
}{
	{"close", func(ctx context.Context, tx pgx.Tx, org string) error {
		_, err := CloseTx(ctx, tx, org, CloseInput{TaskID: "wi_life", Resolution: ResolutionWontDo,
			Actor: Actor{ActorType: "system", ActorID: "test"}})
		return err
	}},
	{"archive", func(ctx context.Context, tx pgx.Tx, org string) error {
		_, err := ArchiveTaskTx(ctx, tx, org, ArchiveInput{CloseInput: CloseInput{TaskID: "wi_life",
			Resolution: ResolutionWontDo, Actor: Actor{ActorType: "system", ActorID: "test"}}})
		return err
	}},
	{"set_status", func(ctx context.Context, tx pgx.Tx, org string) error {
		_, err := SetTaskStatusTx(ctx, tx, org, "prj_life", "wi_life", "", "aborted", "a person stopped it")
		return err
	}},
}

// raceTimeout bounds one race: both transactions, their SQL and the lock
// witness.
const raceTimeout = 15 * time.Second

// raceEnder runs hold in a transaction that pauses at its hook, starts the
// ender once the hook is reached, lets hold go on once the ender waits on
// a lock, and returns both transactions' errors. Both run under one
// deadline context, which their SQL takes. On every exit the hook is
// released and both goroutines joined before the context is cancelled, so
// each transaction ends on a live connection and none outlives it into the
// fixture's cleanup. err is the harness's failure: the hook never
// reached, the ender never waiting on a lock, the deadline.
func (w *childWorld) raceEnder(end func(context.Context, pgx.Tx, string) error,
	hold func(ctx context.Context, tx pgx.Tx, between func()) error) (holdErr, endErr, err error) {
	ctx, cancel := context.WithTimeout(w.ctx, raceTimeout)
	defer cancel()
	reached, release := make(chan struct{}), make(chan struct{})
	held, ended := make(chan struct{}), make(chan struct{})
	var reachOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var wg sync.WaitGroup
	var hErr, eErr error
	// ctx stays live until both goroutines are joined: a COMMIT or ROLLBACK
	// on a cancelled context makes pgx abandon the connection, and its
	// backend can hold the row locks for a moment after this returns.
	// raceTimeout still bounds a stuck transaction, since all SQL takes ctx.
	defer func() {
		unblock()
		wg.Wait()
		holdErr, endErr = hErr, eErr
		if err == nil && ctx.Err() != nil {
			err = fmt.Errorf("the race outlived its deadline: %w", ctx.Err())
		}
		cancel()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(held)
		hErr = w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			return hold(ctx, tx, func() {
				reachOnce.Do(func() { close(reached) })
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		})
	}()
	select {
	case <-reached:
	case <-held:
		return nil, nil, fmt.Errorf("the held transaction ended before its hook: %v", hErr)
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("the held transaction never reached its hook: %w", ctx.Err())
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ended)
		eErr = w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error { return end(ctx, tx, w.org) })
	}()
	if werr := w.waitForLockWaiter(ctx, ended); werr != nil {
		return nil, nil, werr
	}
	return nil, nil, nil
}

// The harness's own failures unwind: an ender refused before it waits on
// any lock, or a held transaction that fails before its hook, end the race
// within its deadline with the harness's error, and the held transaction's
// locks are free once it returns.
func TestTheRaceHarnessUnwindsWhenTheRaceNeverHappens(t *testing.T) {
	refused := errors.New("refused before any lock")
	cases := []struct {
		name string
		end  func(context.Context, pgx.Tx, string) error
		hold func(context.Context, pgx.Tx, func()) error
		want string
	}{
		{"an ender that never waits",
			func(context.Context, pgx.Tx, string) error { return refused },
			func(ctx context.Context, tx pgx.Tx, between func()) error {
				if _, _, err := LockEscalationTx(ctx, tx, "wi_life"); err != nil {
					return err
				}
				between()
				return nil
			},
			"the ender ended without waiting on a lock"},
		{"a hold that fails before its hook",
			func(context.Context, pgx.Tx, string) error { return nil },
			func(context.Context, pgx.Tx, func()) error { return refused },
			"the held transaction ended before its hook"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newChildWorld(t)
			start := time.Now()
			holdErr, endErr, err := w.raceEnder(c.end, c.hold)
			if took := time.Since(start); took > raceTimeout/3 {
				t.Errorf("the race took %s to fail", took)
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("harness error %v (hold %v, end %v), want %q", err, holdErr, endErr, c.want)
			}
			if _, err := w.owner.Exec(w.ctx, `SELECT 1 FROM workflow_runs WHERE id = 'wf_life' FOR UPDATE NOWAIT`); err != nil {
				t.Fatalf("the delivery is still locked after the race returned: %v", err)
			}
		})
	}
}

// A conductor's start_agent and a person ending the task at once: the
// start, between the locks it takes, is never refused for a deadlock, nor
// is the ending. The start goes first and makes its child; the ending,
// waiting on it, finds that child working and stops it (Close, Archive)
// or ends it as the task ends (a status change aborts children).
func TestAChildStartAndATaskEndingSerialiseWithoutADeadlock(t *testing.T) {
	for _, e := range enders {
		t.Run(e.name, func(t *testing.T) {
			w := newChildWorld(t)
			// Parked at its start with no pull request open: the child is
			// the only Run Close and Archive find live.
			w.exec(`UPDATE tasks SET status = 'queued' WHERE id = 'wi_life'`)
			w.exec(`UPDATE pull_requests SET state = 'closed' WHERE id = 'pr_life'`)
			in := w.in
			in.Key = "raced"
			var child string
			startErr, endErr, err := w.raceEnder(e.end, func(ctx context.Context, tx pgx.Tx, between func()) error {
				var err error
				child, err = startChildTx(ctx, tx, in, between)
				return err
			})
			if err != nil {
				t.Fatalf("race: %v (start %v, %s %v)", err, startErr, e.name, endErr)
			}
			if isDeadlock(startErr) || isDeadlock(endErr) {
				t.Fatalf("deadlock: start %v, %s %v", startErr, e.name, endErr)
			}
			if startErr != nil || child == "" {
				t.Fatalf("start: %q %v", child, startErr)
			}
			assertEnderAfterChild(t, w, e.name, endErr, child)
		})
	}
}

// The same for a conductor restarting its writer child: the restart's
// writer admission locks the delivery.
func TestAChildRestartAndATaskEndingSerialiseWithoutADeadlock(t *testing.T) {
	for _, e := range enders {
		t.Run(e.name, func(t *testing.T) {
			w := newChildWorld(t)
			in := w.in
			in.Profile, in.Capabilities.PublishCode, in.Key = "implementer", true, "writer"
			writer, err := w.s.StartChild(w.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			w.exec(`UPDATE tasks SET status = 'queued' WHERE id = 'wi_life'`)
			w.exec(`UPDATE pull_requests SET state = 'closed' WHERE id = 'pr_life'`)
			var next string
			restartErr, endErr, err := w.raceEnder(e.end, func(ctx context.Context, tx pgx.Tx, between func()) error {
				var err error
				next, err = restartChildTx(ctx, tx, w.org, "wi_life", writer, "again", between)
				return err
			})
			if err != nil {
				t.Fatalf("race: %v (restart %v, %s %v)", err, restartErr, e.name, endErr)
			}
			if isDeadlock(restartErr) || isDeadlock(endErr) {
				t.Fatalf("deadlock: restart %v, %s %v", restartErr, e.name, endErr)
			}
			if restartErr != nil || next == "" {
				t.Fatalf("restart: %q %v", next, restartErr)
			}
			assertEnderAfterChild(t, w, e.name, endErr, next)
		})
	}
}

// A Run's restart order without its Chat lock (delivery → task → that Run)
// and a person's Close or Archive of the task at once: held between its
// task lock and its Run lock, the restart takes the Run once the ending
// waits, and neither is refused for a deadlock. The ending then stops the
// Run. Pins StopAllTx's phase Runs after the delivery, which Chat alone
// would mask.
func TestARunRestartAndATaskEndingSerialiseWithoutADeadlock(t *testing.T) {
	for _, e := range enders[:2] {
		t.Run(e.name, func(t *testing.T) {
			w := newChildWorld(t)
			w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
				VALUES ('run_impl', $1, 'prj_life', 'wi_life', 3, 'running', 'implement', 'implementer')`, w.org)
			restartErr, endErr, err := w.raceEnder(e.end, func(ctx context.Context, tx pgx.Tx, between func()) error {
				if _, _, err := LockEscalationTx(ctx, tx, "wi_life"); err != nil {
					return err
				}
				between()
				// RestartRunTx's own lock on the Run it replaces.
				_, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id = 'run_impl' FOR UPDATE`)
				return err
			})
			if err != nil {
				t.Fatalf("race: %v (restart %v, %s %v)", err, restartErr, e.name, endErr)
			}
			if isDeadlock(restartErr) || isDeadlock(endErr) {
				t.Fatalf("deadlock: restart %v, %s %v", restartErr, e.name, endErr)
			}
			if restartErr != nil || endErr != nil {
				t.Fatalf("restart %v, %s %v", restartErr, e.name, endErr)
			}
			var status string
			if err := w.owner.QueryRow(w.ctx, `SELECT status::text FROM runs WHERE id = 'run_impl'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "aborted" {
				t.Fatalf("the phase Run after the %s is %s", e.name, status)
			}
		})
	}
}

// A person's Stop as the API's takes it (api.stopTask): every live Run
// stopped and kept, the task aborted, the delivery halted.
func stopTaskTx(ctx context.Context, tx pgx.Tx, org string) error {
	stopped, err := StopAllTx(ctx, tx, org, "wi_life", "", true, nil)
	if err != nil {
		return err
	}
	if _, err := SetTaskStatusTx(ctx, tx, org, stopped.ProjectID, "wi_life", "", "aborted", "a person stopped the task"); err != nil {
		return err
	}
	return HaltDeliveryTx(ctx, tx, org, stopped.Delivery, stopped.Conductor, "a person stopped the task", "system", "test")
}

// The syncer's restart of a phase Run the conductor started (replaceRound,
// retireForSecrets: RestartRunTx with no lock of the caller's) and a
// person's Close, Archive or Stop at once. The replacement's insert takes
// a key share on the conductor's row (conductor_run_id, parent_run_id),
// which the ending locks before the delivery; held once it has the task's
// locks, the restart goes on once the ending waits, and neither is refused
// for a deadlock. The restart goes first; the ending then stops the
// replacement and ends the task.
func TestTheSyncersRestartOfAnAttributedRunAndATaskEndingSerialise(t *testing.T) {
	endings := append(enders[:2:2], struct {
		name string
		end  func(ctx context.Context, tx pgx.Tx, org string) error
	}{"stop", stopTaskTx})
	for _, e := range endings {
		t.Run(e.name, func(t *testing.T) {
			w := newChildWorld(t)
			w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, conductor_run_id)
				VALUES ('run_impl', $1, 'prj_life', 'wi_life', 3, 'running', 'review', 'reviewer', 'run_parent')`, w.org)
			w.exec(`UPDATE workflow_runs SET step = 'review', state = jsonb_set(state, '{pendingRunIds}', '["run_impl"]') WHERE id = 'wf_life'`)
			var next string
			restartErr, endErr, err := w.raceEnder(e.end, func(ctx context.Context, tx pgx.Tx, between func()) error {
				var err error
				next, err = restartRunTx(ctx, tx, w.org, "wi_life", Restart{RunID: "run_impl", ByDude: true,
					Note: "a secret is no longer given", Actor: Writer{ActorType: "system", ActorID: "dude"}}, between)
				return err
			})
			if err != nil {
				t.Fatalf("race: %v (restart %v, %s %v)", err, restartErr, e.name, endErr)
			}
			if isDeadlock(restartErr) || isDeadlock(endErr) {
				t.Fatalf("deadlock: restart %v, %s %v", restartErr, e.name, endErr)
			}
			if restartErr != nil || endErr != nil || next == "" {
				t.Fatalf("restart %q %v, %s %v", next, restartErr, e.name, endErr)
			}
			var old, replacedBy, fresh, conductor, task string
			if err := w.owner.QueryRow(w.ctx, `SELECT o.status::text, COALESCE(o.replaced_by, ''), n.status::text,
					COALESCE(n.conductor_run_id, ''), t.status::text
				FROM runs o, runs n, tasks t WHERE o.id = 'run_impl' AND n.id = $1 AND t.id = 'wi_life'`, next).
				Scan(&old, &replacedBy, &fresh, &conductor, &task); err != nil {
				t.Fatal(err)
			}
			if old != "aborted" || replacedBy != next || fresh != "aborted" || conductor != "run_parent" || task != "aborted" {
				t.Fatalf("after the %s: old %s replaced by %q (want %q), replacement %s of conductor %q, task %s",
					e.name, old, replacedBy, next, fresh, conductor, task)
			}
		})
	}
}

func assertEnderAfterChild(t *testing.T, w *childWorld, ender string, endErr error, child string) {
	t.Helper()
	if endErr != nil {
		t.Fatalf("%s: %v", ender, endErr)
	}
	want := "parent-ended"
	if ender != "set_status" {
		// Close and Archive stop the child as they end the task (StopAllTx).
		want = "aborted"
	}
	var stop string
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(child_stop_reason, '') FROM runs WHERE id = $1`, child).Scan(&stop); err != nil {
		t.Fatal(err)
	}
	if stop != want {
		t.Fatalf("child after the %s: stop reason %q, want %q", ender, stop, want)
	}
}
