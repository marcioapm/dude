package phases

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lifecycleGate holds the follower's batch once its first lifecycle UPDATE
// of the Run has returned: the batch owns the Run's row and has written it
// once, so its next UPDATE re-checks the row's foreign keys.
type lifecycleGate struct {
	claimed atomic.Bool
	pid     atomic.Uint32
	entered chan struct{}
	release chan struct{}
}

type lifecycleKey struct{}

func (g *lifecycleGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(q.SQL, "UPDATE runs r SET lux_state") {
		return context.WithValue(ctx, lifecycleKey{}, true)
	}
	return ctx
}

func (g *lifecycleGate) TraceQueryEnd(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(lifecycleKey{}) == nil || q.Err != nil || !g.claimed.CompareAndSwap(false, true) {
		return
	}
	g.pid.Store(c.PgConn().PID())
	close(g.entered)
	select {
	case <-g.release:
	case <-ctx.Done():
	}
}

func isDeadlock(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "40P01"
}

// childFamily is a live parent (run_parent: the task's conductor, or a
// session's brainstorm), its child run_child, which both parent_run_id
// and, for a task, conductor_run_id name, still scheduled, and a live
// investigator sibling run_sibling. A task's is an ordinary task, running,
// with its delivery waiting on the conductor. Returns the child as the
// syncer follows it.
func childFamily(w *resumeWorld, session bool) phaseRun {
	w.t.Helper()
	child := phaseRun{ID: "run_child", Org: w.run.Org, Status: statusScheduled, Control: "none",
		LuxRunID: "lrun_child", LuxState: "scheduled", Attempt: 1}
	if session {
		child.SessionID = "ssn_family"
		child.Phase, child.Role, child.SourceKind = delivery.PhaseInvestigate, "investigator", string(delivery.ChildParentSession)
		w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_family', $1, 'Research')`, w.run.Org)
		w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, lux_run_id, lux_state)
			VALUES ('run_parent', $1, 'ssn_family', 1, 'running', 'agent', 'brainstorm', 'lrun_parent', 'running')`, w.run.Org)
		w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, phase, role, source_kind,
			parent_run_id, parent_session_id, child_profile, child_capabilities, lux_run_id, lux_state)
			VALUES ('run_child', $1, 'ssn_family', 1, 'scheduled', 'investigate', 'investigator', 'session',
			'run_parent', 'ssn_family', 'investigator', '{}', 'lrun_child', 'scheduled'),
			('run_sibling', $1, 'ssn_family', 1, 'running', 'investigate', 'investigator', 'session',
			'run_parent', 'ssn_family', 'investigator', '{}', 'lrun_sibling', 'running')`, w.run.Org)
		return child
	}
	child.ProjectID, child.TaskID = w.run.ProjectID, w.run.TaskID
	child.Phase, child.Role, child.SourceKind = delivery.PhaseImplement, "implementer", string(delivery.ChildParentTaskConductor)
	w.exec(`UPDATE tasks SET status = 'running' WHERE id = $1`, w.run.TaskID)
	w.exec(`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_family', $1, $2, 'task.delivery', 'family', 'waiting', 'conductorDecision',
		jsonb_build_object('attempt', 1, 'taskId', $2::text, 'projectId', $3::text))`, w.run.Org, w.run.TaskID, w.run.ProjectID)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, lux_run_id, lux_state)
		VALUES ('run_parent', $1, $2, $3, 1, 'running', 'agent', 'conductor', 'lrun_parent', 'running')`,
		w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, source_kind,
		parent_run_id, conductor_run_id, child_profile, child_capabilities, lux_run_id, lux_state)
		VALUES ('run_child', $1, $2, $3, 1, 'scheduled', 'implement', 'implementer', 'task_conductor',
		'run_parent', 'run_parent', 'implementer', '{"publishCode":true}', 'lrun_child', 'scheduled'),
		('run_sibling', $1, $2, $3, 1, 'running', 'investigate', 'investigator', 'task_conductor',
		'run_parent', 'run_parent', 'investigator', '{}', 'lrun_sibling', 'running')`,
		w.run.Org, w.run.ProjectID, w.run.TaskID)
	return child
}

// raceLifecycleBatch has the syncer apply one batch of the child's frames
// (state starting, then running, then a record with a cursor) in one
// transaction, held after its first lifecycle UPDATE. It then runs op on a
// pool of its own, lets the batch go on once op waits on a lock the batch
// holds (pg_blocking_pids), and returns both errors. Both goroutines are
// joined before it returns; err is the harness's own failure.
func raceLifecycleBatch(w *resumeWorld, child phaseRun, op func(context.Context, *db.DB) error) (batchErr, opErr, err error) {
	w.t.Helper()
	app := w.s.DB
	gate := &lifecycleGate{entered: make(chan struct{}), release: make(chan struct{})}
	w.traced(gate)
	tr := &translator{run: child}
	if err := w.s.DB.InOrg(w.ctx, child.Org, func(tx pgx.Tx) error { return tr.load(w.ctx, tx) }); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	defer cancel()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(gate.release) }) }
	var wg sync.WaitGroup
	var bErr, oErr error
	batchDone, opDone := make(chan struct{}), make(chan struct{})
	// Join before cancellation and before reading the workers' errors.
	defer func() {
		unblock()
		wg.Wait()
		batchErr, opErr = bErr, oErr
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(batchDone)
		bErr = w.s.applyBatch(ctx, tr, []lux.Frame{luxState(1, "starting"), luxState(1, "running"),
			cursorFrame(record(1, "lux.warning", map[string]any{"message": "after the lifecycle"}), "c-family")})
	}()
	select {
	case <-gate.entered:
	case <-batchDone:
		return batchErr, nil, errors.New("the batch ended before its first lifecycle update returned")
	case <-ctx.Done():
		return nil, nil, errors.New("the batch never reached its first lifecycle update")
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(opDone)
		oErr = op(ctx, app)
	}()
	for blocked := false; !blocked; {
		if err := w.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND $1::int = ANY (pg_blocking_pids(pid)))`, int32(gate.pid.Load())).Scan(&blocked); err != nil {
			return nil, nil, err
		}
		if blocked {
			break
		}
		select {
		case <-opDone:
			return nil, opErr, errors.New("the operation ended without waiting on the batch")
		case <-ctx.Done():
			return nil, nil, errors.New("the operation never waited on the batch")
		case <-time.After(5 * time.Millisecond):
		}
	}
	unblock()
	wg.Wait()
	return bErr, oErr, nil
}

// assertBatchCommitted fails unless the child's batch committed whole: its
// lifecycle (running, started, one run.started) and its cursor.
func assertBatchCommitted(t *testing.T, w *resumeWorld) {
	t.Helper()
	var luxState, status, cursor string
	var started bool
	var startedEvents int
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(lux_state, ''), status::text, started_at IS NOT NULL, COALESCE(lux_cursor, ''),
		(SELECT count(*) FROM events WHERE run_id = 'run_child' AND event_type = $1)
		FROM runs WHERE id = 'run_child'`, evRunStarted).Scan(&luxState, &status, &started, &cursor, &startedEvents); err != nil {
		t.Fatal(err)
	}
	if luxState != "running" || status != "running" || !started || cursor != "c-family" || startedEvents != 1 {
		t.Errorf("the batch: lux state %s, status %s, started %v, cursor %q, %d run.started; want running, running, started, c-family, 1",
			luxState, status, started, cursor, startedEvents)
	}
}

// childStop is a Run's control and child stop reason.
func childStop(t *testing.T, w *resumeWorld, id string) (control, reason string, keep bool) {
	t.Helper()
	if err := w.owner.QueryRow(w.ctx, `SELECT control::text, COALESCE(child_stop_reason, ''), keep FROM runs WHERE id = $1`, id).
		Scan(&control, &reason, &keep); err != nil {
		t.Fatal(err)
	}
	return control, reason, keep
}

// A person aborting a conductor's (or a session's) child while the syncer
// records a batch of that child's lifecycle in one transaction. The batch
// holds the child's row and has updated it once; the abort takes the
// parent's row, then (a task's child) the delivery and the task, and waits
// for the child. The batch's second UPDATE re-checks the child's foreign
// keys to the parent (KEY SHARE), which the parent's lock must not
// exclude: both commit, the child is aborted, its parent and sibling stay
// live, and nothing else about the task changes.
func TestAChildAbortBesideItsLifecycleBatchDoesNotDeadlock(t *testing.T) {
	for _, parent := range []string{"task conductor", "session"} {
		t.Run(parent, func(t *testing.T) {
			w := newResumeWorld(t)
			child := childFamily(w, parent == "session")
			snapshot := func() string {
				var raw string
				if err := w.owner.QueryRow(w.ctx, `SELECT jsonb_build_object(
					't', (SELECT status FROM tasks WHERE id = $1),
					'w', (SELECT jsonb_build_object('status', status, 'step', step, 'state', state, 'attempt', attempt)
						FROM workflow_runs WHERE task_id = $1),
					'attempts', (SELECT jsonb_object_agg(id, attempt) FROM runs WHERE id IN ('run_parent', 'run_child', 'run_sibling')))::text`,
					w.run.TaskID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			batchErr, abortErr, err := raceLifecycleBatch(w, child, func(ctx context.Context, app *db.DB) error {
				return app.InOrg(ctx, child.Org, func(tx pgx.Tx) error { return delivery.AbortChildTx(ctx, tx, child.TaskID, child.ID) })
			})
			if err != nil {
				t.Fatalf("race: %v (batch %v, abort %v)", err, batchErr, abortErr)
			}
			if batchErr != nil || abortErr != nil {
				t.Fatalf("batch (deadlock %v): %v; abort (deadlock %v): %v",
					isDeadlock(batchErr), batchErr, isDeadlock(abortErr), abortErr)
			}
			if control, reason, keep := childStop(t, w, "run_child"); control != "abort" || reason != "aborted" || keep {
				t.Errorf("the child: control %s, stop reason %q, keep %v; want abort, aborted, not kept", control, reason, keep)
			}
			assertBatchCommitted(t, w)
			for _, id := range []string{"run_parent", "run_sibling"} {
				var status string
				if err := w.owner.QueryRow(w.ctx, `SELECT status::text FROM runs WHERE id = $1`, id).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if control, reason, _ := childStop(t, w, id); status != "running" || control != "none" || reason != "" {
					t.Errorf("%s: status %s, control %s, stop reason %q; want it live", id, status, control, reason)
				}
			}
			if after := snapshot(); after != before {
				t.Errorf("the task or its delivery changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// The other holders of a child's parent that then wait for the child's
// row, each beside the child's lifecycle batch as above: a person's Stop
// (StopAllTx: LockLiveWorkTx's conductor, then each child), a person's
// abort of the conductor (AbortConductorTx, MarkChildParentEndedTx, as the
// API and every conductor hand-over take them) and EndChildParent of an
// ended task conductor or brainstorm. None is refused for a deadlock, the
// batch commits whole, and the child is stopped as the holder stops it.
func TestAParentEndingBesideItsChildsLifecycleBatchDoesNotDeadlock(t *testing.T) {
	for _, c := range []struct {
		name    string
		session bool
		// before runs ahead of the race, on the fixture.
		before func(w *resumeWorld)
		op     func(ctx context.Context, app *db.DB, w *resumeWorld) error
		reason string
	}{
		{"stop", false, nil, func(ctx context.Context, app *db.DB, w *resumeWorld) error {
			return app.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
				_, err := delivery.StopAllTx(ctx, tx, w.run.Org, w.run.TaskID, "a person stopped the task", true, nil)
				return err
			})
		}, "aborted"},
		{"conductor abort", false, nil, func(ctx context.Context, app *db.DB, w *resumeWorld) error {
			return app.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
				if err := delivery.LockChat(ctx, tx, w.run.TaskID); err != nil {
					return err
				}
				ref := delivery.RunRef{Org: w.run.Org, ProjectID: w.run.ProjectID, TaskID: w.run.TaskID, RunID: "run_parent"}
				if err := delivery.AbortConductorTx(ctx, tx, ref, "a person aborted it", false); err != nil {
					return err
				}
				return delivery.MarkChildParentEndedTx(ctx, tx, w.run.TaskID, "run_parent")
			})
		}, "parent-ended"},
		{"end task child parent", false, func(w *resumeWorld) {
			w.exec(`UPDATE runs SET status = 'completed', ended_at = now() WHERE id = 'run_parent'`)
		}, func(ctx context.Context, app *db.DB, w *resumeWorld) error {
			return (&delivery.Store{DB: app}).EndChildParent(ctx, w.run.Org, w.run.TaskID, "run_parent")
		}, "parent-ended"},
		{"end session child parent", true, func(w *resumeWorld) {
			w.exec(`UPDATE runs SET status = 'completed', ended_at = now() WHERE id = 'run_parent'`)
		}, func(ctx context.Context, app *db.DB, w *resumeWorld) error {
			return (&delivery.Store{DB: app}).EndChildParent(ctx, w.run.Org, "", "run_parent")
		}, "parent-ended"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newResumeWorld(t)
			child := childFamily(w, c.session)
			if c.before != nil {
				c.before(w)
			}
			batchErr, opErr, err := raceLifecycleBatch(w, child, func(ctx context.Context, app *db.DB) error { return c.op(ctx, app, w) })
			if err != nil {
				t.Fatalf("race: %v (batch %v, %s %v)", err, batchErr, c.name, opErr)
			}
			if batchErr != nil || opErr != nil {
				t.Fatalf("batch (deadlock %v): %v; %s (deadlock %v): %v",
					isDeadlock(batchErr), batchErr, c.name, isDeadlock(opErr), opErr)
			}
			if control, reason, keep := childStop(t, w, "run_child"); control != "abort" || reason != c.reason || keep {
				t.Errorf("the child: control %s, stop reason %q, keep %v; want abort, %s, not kept", control, reason, keep, c.reason)
			}
			assertBatchCommitted(t, w)
		})
	}
}
