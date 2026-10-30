package phases

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// EvCostReported is the ledger event for a changed cost from lux
// (RunCostReported in packages/domain/src/events/types.ts).
const EvCostReported = "run.cost.reported"

// Costs keeps what lux says each agent's Run cost.
//
// lux prices a Run with its cost plugins — the LLM proxy's metered AI
// models, the host's compute — every two minutes, and settles the price
// over up to seven days: status goes pending → incomplete → complete →
// final. A Run is read until its cost is final, or until it ended more
// than costPatience ago, when lux will have given up settling it.
type Costs struct {
	DB  *db.DB
	Lux lux.Client
	Log *slog.Logger
	// How often a Run's cost is read (DUDE_LUX_COST_EVERY); zero is 2m, lux's
	// own tick.
	Every time.Duration
	// Runs read per sweep; zero is 50.
	Batch int
}

// lux stops settling a Run's cost after seven days; a day's margin.
const costPatience = 8 * 24 * time.Hour

// A Run lux does not know is asked about again this rarely: it may never
// come back, and it drops off the list costPatience after it ended.
const costNotFoundBackoff = time.Hour

type costRun struct {
	ID, Org, ProjectID, TaskID, LuxRunID string
	// A recovery's publisher (run_recoveries, recover.go), not a runs row:
	// its cost is kept on the recovery, and counted for the Run it
	// recovered by the cost functions (migration 233).
	Recovery bool
}

func (c *Costs) every() time.Duration {
	if c.Every > 0 {
		return c.Every
	}
	return 2 * time.Minute
}

// Sweep reads the cost of each Run due, a few at a time. A Run lux fails
// on is asked again later and never holds up the rest.
func (c *Costs) Sweep(ctx context.Context) (int, error) {
	batch := c.Batch
	if batch <= 0 {
		batch = 50
	}
	var due []costRun
	if err := c.DB.InSystem(ctx, "lux-cost", func(tx pgx.Tx) error {
		// Only Runs with a read still due have a next_at (060): an agent's Run
		// on lux, not final, not past costPatience.
		// A session's Run has no project or task: "" (ledger writes NULL).
		// Recoveries' publishers alongside, by the same next_at.
		rows, err := tx.Query(ctx, `SELECT * FROM (
				SELECT id, organization_id, COALESCE(project_id, ''), COALESCE(task_id, ''), lux_run_id, false, lux_cost_next_at FROM runs
				WHERE lux_cost_next_at <= now() ORDER BY lux_cost_next_at LIMIT $1) r
			UNION ALL SELECT * FROM (
				SELECT c.id, c.organization_id, '', c.task_id, c.publisher_lux_run_id, true, c.lux_cost_next_at
				FROM run_recoveries c
				WHERE c.lux_cost_next_at <= now() AND c.publisher_lux_run_id IS NOT NULL ORDER BY c.lux_cost_next_at LIMIT $1) c
			ORDER BY 7 LIMIT $1`, batch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (costRun, error) {
			var r costRun
			var at time.Time
			err := row.Scan(&r.ID, &r.Org, &r.ProjectID, &r.TaskID, &r.LuxRunID, &r.Recovery, &at)
			return r, err
		})
		return err
	}); err != nil {
		return 0, err
	}
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, r := range due {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			luxErr, dbErr := c.read(ctx, r)
			if luxErr != nil && ctx.Err() == nil {
				c.log().Warn("reading a Run's cost from lux", "run", r.ID, "lux_run", r.LuxRunID, "error", luxErr)
			}
			// A Run whose next read could not be put off would be due again at
			// once: that is the loop's error, so it waits before sweeping again.
			if dbErr != nil {
				mu.Lock()
				errs = append(errs, dbErr)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return len(due), errors.Join(errs...)
}

func (c *Costs) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// read asks lux for one Run's cost and stores the answer. lux's error and
// the database's are apart: only the second leaves the Run due at once.
// The Run leaves the work list (next_at NULL) once lux says final or it
// ended more than costPatience ago, whether lux answered or not.
func (c *Costs) read(ctx context.Context, r costRun) (luxErr, dbErr error) {
	cost, err := c.Lux.Cost(ctx, r.LuxRunID)
	if err != nil {
		wait := c.every()
		if lux.IsNotFound(err) {
			wait = costNotFoundBackoff
		}
		return err, c.putOff(ctx, r, wait)
	}
	return nil, c.store(ctx, r, cost)
}

// putOff schedules the next read of a Run lux did not answer for.
func (c *Costs) putOff(ctx context.Context, r costRun, wait time.Duration) error {
	if r.Recovery {
		return c.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE run_recoveries SET lux_cost_next_at = CASE
					WHEN ended_at < now() - make_interval(secs => $3) THEN NULL
					ELSE now() + make_interval(secs => $2) END
				WHERE id = $1`,
				r.ID, wait.Seconds(), costPatience.Seconds())
			return err
		})
	}
	return c.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET lux_cost_next_at = CASE
				WHEN ended_at < now() - make_interval(secs => $3) THEN NULL
				ELSE now() + make_interval(secs => $2) END
			WHERE id = $1`,
			r.ID, wait.Seconds(), costPatience.Seconds())
		return err
	})
}

// store keeps lux's answer on the Run, schedules its next read, and
// appends EvCostReported when an amount or the status changed.
func (c *Costs) store(ctx context.Context, r costRun, cost lux.RunCost) error {
	ai, hasAI := cost.FamilyUSD(lux.FamilyAI)
	compute, hasCompute := cost.FamilyUSD(lux.FamilyCompute)
	if r.Recovery {
		return c.storeRecovery(ctx, r, cost.Status, decimalOrNil(ai, hasAI), decimalOrNil(compute, hasCompute))
	}
	return c.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var changed bool
		// A Run that took over its predecessor's lux Run is charged only for
		// the cost reported after its inherited base.
		err := tx.QueryRow(ctx, `WITH old AS (
				SELECT lux_ai_usd, lux_compute_usd, lux_cost_status FROM runs WHERE id = $1 FOR UPDATE)
			UPDATE runs SET lux_ai_usd = CASE WHEN $2::numeric IS NOT NULL THEN GREATEST($2::numeric - lux_ai_base, 0) END,
				lux_compute_usd = CASE WHEN $3::numeric IS NOT NULL THEN GREATEST($3::numeric - lux_compute_base, 0) END, lux_cost_status = $4,
				lux_cost_read_at = now(), lux_cost_next_at = CASE
					WHEN $4 = 'final' OR runs.ended_at < now() - make_interval(secs => $6) THEN NULL
					ELSE now() + make_interval(secs => $5) END
			FROM old WHERE runs.id = $1
			RETURNING old.lux_ai_usd IS DISTINCT FROM runs.lux_ai_usd OR old.lux_compute_usd IS DISTINCT FROM runs.lux_compute_usd
				OR old.lux_cost_status IS DISTINCT FROM $4`,
			r.ID, decimalOrNil(ai, hasAI), decimalOrNil(compute, hasCompute), cost.Status, c.every().Seconds(),
			costPatience.Seconds()).Scan(&changed)
		if err != nil || !changed {
			return err
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{
			Type: EvCostReported, OrganizationID: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID,
			ActorType: ledger.ActorSystem, ActorID: "lux", Source: ledger.SourceRunner, CorrelationID: r.TaskID,
			Payload: map[string]any{"aiUsd": jsonDecimal(ai, hasAI), "computeUsd": jsonDecimal(compute, hasCompute),
				"status": cost.Status},
		})
		return err
	})
}

// storeRecovery keeps lux's answer for a recovery's publisher on the
// recovery, as store keeps a Run's, until final or costPatience after the
// recovery ended. It writes no event: the run.cost.reported of the Run it
// recovered is that Run's own lux cost, which this is not.
func (c *Costs) storeRecovery(ctx context.Context, r costRun, status string, ai, compute any) error {
	return c.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE run_recoveries SET lux_ai_usd = $2::numeric, lux_compute_usd = $3::numeric, lux_cost_status = $4,
				lux_cost_read_at = now(), lux_cost_next_at = CASE
					WHEN $4 = 'final' OR ended_at < now() - make_interval(secs => $6) THEN NULL
					ELSE now() + make_interval(secs => $5) END
			WHERE id = $1`, r.ID, ai, compute, status, c.every().Seconds(), costPatience.Seconds())
		return err
	})
}

func decimalOrNil(d lux.Decimal, ok bool) any {
	if !ok {
		return nil
	}
	return string(d)
}

// jsonDecimal writes lux's amount into the event as a JSON number, digit
// for digit; nil (null) when lux reported none.
func jsonDecimal(d lux.Decimal, ok bool) any {
	if !ok {
		return nil
	}
	return json.Number(d)
}
