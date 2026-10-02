// Package workflow is the durable workflow runtime (plan §21 option A).
//
// The property that matters: waiting is free. A workflow parked on a human,
// a CI run or a PR review holds no goroutine, no connection and no model
// context. It is a row. When the awaited thing happens a signal lands in the
// inbox, and a poller picks the workflow up where it left off.
//
// Durability comes from committing the decision before acting on the next
// one: each step returns a transition, the transition is persisted, and a
// crash between two steps resumes from the last committed one. Steps must
// therefore be safe to run again — their side effects are keyed so a repeat
// finds the work already done.
//
// Concurrency rests on three things:
//   - FOR UPDATE SKIP LOCKED, so pollers never claim the same run;
//   - a lease (locked_by/locked_until), so a poller that dies releases work;
//   - UNIQUE (organization_id, workflow_type, idempotency_key), so a repeated
//     start returns the original run instead of forking a second one.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// How long a poller may hold a claimed run before another may take it.
const leaseDuration = 60 * time.Second

const defaultMaxAttempts = 5

// Signal is something delivered to a parked workflow: a finished phase, PR
// feedback, a person's answer.
type Signal struct {
	ID         string          `json:"signalId"`
	Name       string          `json:"name"`
	Payload    json.RawMessage `json:"payload"`
	ReceivedAt time.Time       `json:"receivedAt"`
}

// StepContext is what a step sees.
type StepContext struct {
	WorkflowRunID  string
	OrganizationID string
	// The step running.
	Step string
	// The workflow's state as the last step left it.
	State json.RawMessage
	// Signals received since the last step, among those it was waiting for.
	Signals []Signal
	// Retries of this step so far; zero on the first try.
	Attempt int
}

// Result is a step's transition.
type Result struct {
	// The next step, or "" to complete the workflow.
	Next string
	// The new state, marshalled to JSON. Nil keeps the current state.
	State any
	// Park until this time before running Next.
	SleepUntil time.Time
	// Park until one of these signals arrives.
	AwaitSignals []string
}

type Step func(context.Context, StepContext) (Result, error)

type Definition struct {
	Type        string
	InitialStep string
	Steps       map[string]Step
	// Attempts before a failing step is dead-lettered.
	MaxAttempts int
}

// Run is a workflow run's durable state.
type Run struct {
	ID              string
	Type            string
	OrganizationID  string
	Status          string
	Step            string
	State           json.RawMessage
	Attempt         int
	LastError       string
	AwaitingSignals []string
}

type Runtime struct {
	db       *db.DB
	pollerID string
	log      *slog.Logger

	mu   sync.RWMutex
	defs map[string]*Definition
}

func New(database *db.DB, pollerID string, log *slog.Logger) *Runtime {
	return &Runtime{db: database, pollerID: pollerID, log: log, defs: map[string]*Definition{}}
}

func (r *Runtime) Register(d *Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defs[d.Type] = d
}

func (r *Runtime) definition(typ string) *Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defs[typ]
}

// StartOptions describes a workflow to start.
type StartOptions struct {
	Type           string
	OrganizationID string
	// Deduplicates starts: a repeat returns the original run.
	IdempotencyKey string
	Input          any
	TaskID         string
}

// Start starts a workflow, or returns the existing run for the same key.
//
// The insert relies on the unique constraint rather than read-then-write, so
// two concurrent starts cannot both create a run.
func (r *Runtime) Start(ctx context.Context, o StartOptions) (id string, deduplicated bool, err error) {
	def := r.definition(o.Type)
	if def == nil {
		return "", false, fmt.Errorf("unknown workflow type: %s", o.Type)
	}
	input, err := json.Marshal(o.Input)
	if err != nil {
		return "", false, err
	}
	err = r.db.InOrg(ctx, o.OrganizationID, func(tx pgx.Tx) error {
		id, deduplicated, err = r.start(ctx, tx, def, o, input)
		return err
	})
	return id, deduplicated, err
}

// StartTx is Start in the caller's transaction, for a start that must
// commit with something else: a task taken up again and its new delivery.
func (r *Runtime) StartTx(ctx context.Context, tx pgx.Tx, o StartOptions) (string, error) {
	def := r.definition(o.Type)
	if def == nil {
		return "", fmt.Errorf("unknown workflow type: %s", o.Type)
	}
	input, err := json.Marshal(o.Input)
	if err != nil {
		return "", err
	}
	id, _, err := r.start(ctx, tx, def, o, input)
	return id, err
}

func (r *Runtime) start(ctx context.Context, tx pgx.Tx, def *Definition, o StartOptions, input []byte) (id string, deduplicated bool, err error) {
	err = tx.QueryRow(ctx, `
		INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id)
		VALUES ($1, $2, $3, $4, 'running', $5, $6::jsonb, $7)
		ON CONFLICT (organization_id, workflow_type, idempotency_key) DO NOTHING
		RETURNING id`,
		ids.New(ids.WorkflowRun), o.OrganizationID, o.Type, o.IdempotencyKey, def.InitialStep, input,
		db.Nullable(o.TaskID)).Scan(&id)
	if !db.IsNotFound(err) {
		return id, false, err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM workflow_runs WHERE workflow_type = $1 AND idempotency_key = $2`,
		o.Type, o.IdempotencyKey).Scan(&id)
	if db.IsNotFound(err) {
		// Conflicted, yet the existing run is invisible: the tenant is
		// wrong, or it was deleted in between. Say so plainly.
		return "", true, fmt.Errorf("workflow start %s/%s conflicted, but the existing run is not visible to %s",
			o.Type, o.IdempotencyKey, o.OrganizationID)
	}
	return id, true, err
}

// ErrNotFound is returned for a workflow run the organization cannot see.
var ErrNotFound = errors.New("workflow run not found")

// Signal delivers a signal.
//
// Signals are durable and order-independent: one may arrive before the
// workflow parks to wait for it, so it is stored and consumed on the next
// step rather than dropped. Scoped to the acting organization, because a run
// id alone must not confer authority over the run.
func (r *Runtime) Signal(ctx context.Context, organizationID, runID, name string, payload any, idempotencyKey string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.db.InOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workflow_runs WHERE id = $1)`, runID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO workflow_signals (id, organization_id, workflow_run_id, name, payload, idempotency_key)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)
			ON CONFLICT (workflow_run_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING`,
			ids.New(ids.WorkflowSignal), organizationID, runID, name, body, db.Nullable(idempotencyKey)); err != nil {
			return err
		}
		// Wake a run parked on this signal. One parked on a timer keeps it.
		_, err := tx.Exec(ctx, `
			UPDATE workflow_runs SET status = 'running', wake_at = NULL
			WHERE id = $1 AND status = 'waiting' AND awaiting_signals ? $2`, runID, name)
		return err
	})
}

// Abort stops a live workflow run.
func (r *Runtime) Abort(ctx context.Context, organizationID, runID, reason string) error {
	return r.db.InOrg(ctx, organizationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE workflow_runs SET status = 'aborted', last_error = $2, wake_at = NULL, locked_by = NULL, locked_until = NULL
			WHERE id = $1 AND status IN ('running', 'waiting')`, runID, reason)
		return err
	})
}

// Get returns a run, or ErrNotFound when the organization cannot see it.
func (r *Runtime) Get(ctx context.Context, organizationID, runID string) (*Run, error) {
	var run Run
	err := r.db.InOrg(ctx, organizationID, func(tx pgx.Tx) error {
		var lastError *string
		var awaiting []string
		err := tx.QueryRow(ctx, `
			SELECT id, workflow_type, organization_id, status::text, step, state, attempt, last_error,
			       ARRAY(SELECT jsonb_array_elements_text(awaiting_signals))
			FROM workflow_runs WHERE id = $1`, runID).
			Scan(&run.ID, &run.Type, &run.OrganizationID, &run.Status, &run.Step, &run.State, &run.Attempt, &lastError, &awaiting)
		if db.IsNotFound(err) {
			return ErrNotFound
		}
		if lastError != nil {
			run.LastError = *lastError
		}
		run.AwaitingSignals = awaiting
		return err
	})
	if err != nil {
		return nil, err
	}
	return &run, nil
}

type claimed struct {
	Run
	signals []Signal
}

// Tick claims and advances up to limit runnable workflows across all
// tenants, concurrently, and waits for them. Returns how many it advanced.
// For tests and tools; the orchestrator's loop uses Dispatch.
func (r *Runtime) Tick(ctx context.Context, limit int) (int, error) {
	runs, err := r.claim(ctx, limit)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for i := range runs {
		wg.Add(1)
		go func(run *claimed) {
			defer wg.Done()
			r.advanceWithLease(ctx, run)
		}(&runs[i])
	}
	wg.Wait()
	return len(runs), nil
}

// Dispatch claims as many runnable workflows as there are free slots and
// starts each without waiting for the others.
//
// Not waiting is the point. A step that calls a forge which has stopped
// answering holds its slot until the request times out; if the loop waited
// for the whole batch, that one step would stall every other organization's
// workflows behind it — found when a test's forge went away mid-delivery and
// the next test's pull request took a minute to open.
func (r *Runtime) Dispatch(ctx context.Context, slots chan struct{}) (int, error) {
	free := cap(slots) - len(slots)
	if free == 0 {
		return 0, nil
	}
	runs, err := r.claim(ctx, free)
	if err != nil {
		return 0, err
	}
	for i := range runs {
		slots <- struct{}{}
		go func(run claimed) {
			defer func() { <-slots }()
			r.advanceWithLease(ctx, &run)
		}(runs[i])
	}
	return len(runs), nil
}

// claim takes runnable workflows with their awaited signals in one query:
// fetching signals per run would cost a transaction each for rows this
// query already visited.
func (r *Runtime) claim(ctx context.Context, limit int) ([]claimed, error) {
	var out []claimed
	err := r.db.InSystem(ctx, "workflow-poller", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH candidate AS (
				SELECT id FROM workflow_runs
				WHERE status IN ('running', 'waiting')
				  AND (locked_until IS NULL OR locked_until < now())
				  AND (
				    status = 'running'
				    -- A timer that elapsed: a backoff retry or a sleep. Such
				    -- runs await no signal, so this cannot resume one blocked
				    -- on a person.
				    OR (wake_at IS NOT NULL AND wake_at <= now())
				    -- A signal it is waiting for is already in the inbox.
				    OR EXISTS (SELECT 1 FROM workflow_signals s
				               WHERE s.workflow_run_id = workflow_runs.id AND s.consumed_at IS NULL
				                 AND workflow_runs.awaiting_signals @> to_jsonb(s.name))
				  )
				ORDER BY wake_at NULLS FIRST, created_at
				LIMIT $1
				FOR UPDATE SKIP LOCKED
			), taken AS (
				UPDATE workflow_runs w
				SET locked_by = $2, locked_until = now() + $3::interval, status = 'running'
				FROM candidate WHERE w.id = candidate.id
				RETURNING w.id, w.workflow_type, w.organization_id, w.step, w.state, w.attempt, w.awaiting_signals
			)
			SELECT t.id, t.workflow_type, t.organization_id, t.step, t.state, t.attempt,
			       ARRAY(SELECT jsonb_array_elements_text(t.awaiting_signals)),
			       COALESCE((SELECT jsonb_agg(jsonb_build_object('signalId', s.id, 'name', s.name,
			                                  'payload', s.payload, 'receivedAt', s.received_at) ORDER BY s.received_at)
			                 FROM workflow_signals s
			                 WHERE s.workflow_run_id = t.id AND s.consumed_at IS NULL
			                   AND t.awaiting_signals @> to_jsonb(s.name)), '[]'::jsonb)
			FROM taken t`,
			limit, r.pollerID, leaseDuration.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c claimed
			var signals json.RawMessage
			if err := rows.Scan(&c.ID, &c.Type, &c.OrganizationID, &c.Step, &c.State, &c.Attempt,
				&c.AwaitingSignals, &signals); err != nil {
				return err
			}
			if err := json.Unmarshal(signals, &c.signals); err != nil {
				return err
			}
			c.Status = "running"
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// advanceWithLease runs one step, renewing the lease while it executes: a
// step that talks to a forge or lux can outlast a fixed lease, and losing it
// mid-step would let another poller run the same step at once.
func (r *Runtime) advanceWithLease(ctx context.Context, run *claimed) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(leaseDuration / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				// A failed renewal is not fatal: the write-back is guarded on
				// the lease, so losing it makes the transition a no-op.
				_ = r.db.InOrg(ctx, run.OrganizationID, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE workflow_runs SET locked_until = now() + $3::interval
						WHERE id = $1 AND locked_by = $2`, run.ID, r.pollerID, leaseDuration.String())
					return err
				})
			}
		}
	}()
	defer close(done)
	r.advance(ctx, run)
}

func (r *Runtime) advance(ctx context.Context, run *claimed) {
	def := r.definition(run.Type)
	if def == nil {
		r.fail(ctx, &run.Run, fmt.Sprintf("unknown workflow type: %s", run.Type), true, nil)
		return
	}
	step := def.Steps[run.Step]
	if step == nil {
		r.fail(ctx, &run.Run, fmt.Sprintf("unknown step: %s", run.Step), true, def)
		return
	}

	result, err := r.runStep(ctx, step, StepContext{
		WorkflowRunID:  run.ID,
		OrganizationID: run.OrganizationID,
		Step:           run.Step,
		State:          run.State,
		Signals:        run.signals,
		Attempt:        run.Attempt,
	})
	if err != nil {
		r.log.Warn("workflow step failed", "workflow", run.ID, "step", run.Step, "attempt", run.Attempt, "error", err)
		r.fail(ctx, &run.Run, err.Error(), false, def)
		return
	}

	var state []byte
	if result.State != nil {
		if state, err = json.Marshal(result.State); err != nil {
			r.fail(ctx, &run.Run, "marshal state: "+err.Error(), true, def)
			return
		}
	}
	awaiting, _ := json.Marshal(db.NonNil(result.AwaitSignals))

	err = r.db.InOrg(ctx, run.OrganizationID, func(tx pgx.Tx) error {
		// Guarded on this poller still holding the lease and the run still
		// being live: if it was aborted mid-step, or the lease lapsed and
		// another poller took over, this transition must not land.
		var tag string
		var qerr error
		if result.Next == "" {
			qerr = tx.QueryRow(ctx, `
				UPDATE workflow_runs
				SET status = 'completed', wake_at = NULL, awaiting_signals = '[]'::jsonb, attempt = 0,
				    locked_by = NULL, locked_until = NULL, state = COALESCE($3::jsonb, state)
				WHERE id = $1 AND status IN ('running', 'waiting') AND locked_by = $2 AND locked_until > now()
				RETURNING id`, run.ID, r.pollerID, nullableJSON(state)).Scan(&tag)
		} else {
			status := "running"
			var wake *time.Time
			if !result.SleepUntil.IsZero() {
				wake = &result.SleepUntil
			}
			if wake != nil || len(result.AwaitSignals) > 0 {
				status = "waiting"
			}
			qerr = tx.QueryRow(ctx, `
				UPDATE workflow_runs
				SET step = $3, state = COALESCE($4::jsonb, state), status = $5::workflow_run_status,
				    wake_at = $6, awaiting_signals = $7::jsonb, attempt = 0, last_error = NULL,
				    locked_by = NULL, locked_until = NULL
				WHERE id = $1 AND status IN ('running', 'waiting') AND locked_by = $2 AND locked_until > now()
				RETURNING id`, run.ID, r.pollerID, result.Next, nullableJSON(state), status, wake, awaiting).Scan(&tag)
		}
		if db.IsNotFound(qerr) {
			return errLeaseLost
		}
		if qerr != nil {
			return qerr
		}
		// The transition is durable, so the signals that produced it are
		// retired in the same transaction: separately, a crash between the
		// two would re-deliver them or lose them.
		if len(run.signals) > 0 {
			sigIDs := make([]string, len(run.signals))
			for i, s := range run.signals {
				sigIDs[i] = s.ID
			}
			_, err := tx.Exec(ctx, `UPDATE workflow_signals SET consumed_at = now()
				WHERE id = ANY($1) AND consumed_at IS NULL`, sigIDs)
			return err
		}
		return nil
	})
	if errors.Is(err, errLeaseLost) {
		// Aborted, or another poller owns it now; it must not also record a
		// failure for a run it no longer holds.
		return
	}
	if err != nil {
		r.fail(ctx, &run.Run, "commit transition: "+err.Error(), false, def)
	}
}

var errLeaseLost = errors.New("lease lost")

// runStep runs a step, turning a panic into an error so one bad step fails
// its own run rather than the poller.
func (r *Runtime) runStep(ctx context.Context, step Step, sc StepContext) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("step panicked: %v", p)
		}
	}()
	return step(ctx, sc)
}

// fail records a step failure: retry with backoff, or dead-letter once the
// attempt budget is spent. A dead-lettered run stays inspectable, so a person
// can see why work stopped.
func (r *Runtime) fail(ctx context.Context, run *Run, message string, fatal bool, def *Definition) {
	maxAttempts := defaultMaxAttempts
	if def != nil && def.MaxAttempts > 0 {
		maxAttempts = def.MaxAttempts
	}
	attempt := run.Attempt + 1
	err := r.db.InOrg(ctx, run.OrganizationID, func(tx pgx.Tx) error {
		if fatal || attempt >= maxAttempts {
			_, err := tx.Exec(ctx, `
				UPDATE workflow_runs SET status = 'dead_lettered', attempt = $2, last_error = $3,
				       wake_at = NULL, locked_by = NULL, locked_until = NULL
				WHERE id = $1 AND status IN ('running', 'waiting')`, run.ID, attempt, message)
			return err
		}
		// The awaited signals are kept across a retry, so a run parked on
		// them can still be woken by one rather than only by its timer.
		awaiting, _ := json.Marshal(db.NonNil(run.AwaitingSignals))
		_, err := tx.Exec(ctx, `
			UPDATE workflow_runs SET status = 'waiting', attempt = $2, last_error = $3,
			       wake_at = now() + $4::interval, awaiting_signals = $5::jsonb,
			       locked_by = NULL, locked_until = NULL
			WHERE id = $1 AND status IN ('running', 'waiting')`,
			run.ID, attempt, message, Backoff(attempt).String(), awaiting)
		return err
	})
	if err != nil {
		r.log.Error("recording workflow failure failed", "workflow", run.ID, "error", err)
	}
}

// Backoff is exponential with a one-minute ceiling.
func Backoff(attempt int) time.Duration {
	d := time.Second << max(0, attempt-1)
	if d > time.Minute || d <= 0 {
		return time.Minute
	}
	return d
}

func nullableJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}
