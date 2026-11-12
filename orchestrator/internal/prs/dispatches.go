package prs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// DispatchReadEvery is how often the supervisor re-reads one dispatched
// workflow's run on GitHub (a bound in code, not a setting).
const DispatchReadEvery = 30 * time.Second

// dispatchesPerPass bounds one pass.
const dispatchesPerPass = 20

// SuperviseDispatches is one pass of the workflow supervisor
// (delivery.SettleDispatchTx): every dispatch still supervised, or owed a
// wake, last read longer than every ago is read on GitHub now, and what it
// saw is stored and decided on. Level-triggered: nothing here waits for a
// webhook; a run that ended while no event came is seen by the next pass.
// Returns how many it took: a full batch (dispatchesPerPass) has its loop
// run again at once, so a backlog is not read 20 per interval.
func (s *Syncer) SuperviseDispatches(ctx context.Context, every time.Duration) (int, error) {
	type due struct{ ID, Org string }
	var batch []due
	if err := s.DB.InSystem(ctx, "workflow-supervisor", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.id, d.organization_id FROM workflow_dispatches d JOIN tasks t ON t.id = d.task_id
			WHERE (d.state IN ('dispatching', 'bound') OR d.outcome IS DISTINCT FROM d.woken_outcome
			       OR d.state = 'completed' AND d.completed_at > now() - $3::interval)
			  AND t.status::text NOT IN ('done', 'aborted', 'failed')
			  AND (d.last_read_at IS NULL OR d.last_read_at < now() - $1::interval)
			ORDER BY d.last_read_at NULLS FIRST, d.id LIMIT $2`, every.String(), dispatchesPerPass, delivery.DispatchFollowAfter.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.ID, &d.Org); err != nil {
				return err
			}
			batch = append(batch, d)
		}
		return rows.Err()
	}); err != nil {
		return 0, err
	}
	for _, d := range batch {
		if err := s.superviseDispatch(ctx, d.Org, d.ID); err != nil {
			s.Log.Warn("supervising a dispatched workflow failed", "dispatch", d.ID, "error", err)
			if ctx.Err() != nil {
				continue
			}
			// "Can't tell" is never silence: the failure counts as a failed
			// read, in its own transaction, and enough in a row wake the
			// conductor "cannot see" (#465).
			failed := err
			if err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
				return delivery.DispatchFailedTx(ctx, tx, d.Org, d.ID, failed)
			}); err != nil {
				s.Log.Warn("recording a dispatched workflow's failed supervision failed", "dispatch", d.ID, "error", err)
			}
		}
	}
	if len(batch) < dispatchesPerPass {
		// Not a full batch: none is left due; the loop waits its interval.
		return 0, nil
	}
	return len(batch), nil
}

func (s *Syncer) superviseDispatch(ctx context.Context, org, id string) error {
	var target delivery.DispatchTarget
	var live bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var err error
		target, live, err = delivery.DispatchTargetTx(ctx, tx, id)
		return err
	}); err != nil || !live {
		return err
	}
	var read delivery.DispatchRead
	if target.Read {
		read = s.readDispatch(ctx, org, target)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return delivery.SettleDispatchTx(ctx, tx, org, id, read)
	})
}

// readDispatch reads the bound run's state on GitHub, no transaction
// open. Any failure, including having no GitHub to read, is "cannot
// tell".
func (s *Syncer) readDispatch(ctx context.Context, org string, t delivery.DispatchTarget) delivery.DispatchRead {
	gh, err := s.Forges.For(ctx, org)
	if err == nil && gh == nil {
		err = errors.New("the organization has no GitHub connected")
	}
	if err != nil {
		return delivery.DispatchRead{Err: err}
	}
	run, err := gh.RunStateOf(ctx, t.Slug, t.RunID)
	if err != nil {
		return delivery.DispatchRead{Err: err}
	}
	return delivery.DispatchRead{Run: &run}
}
