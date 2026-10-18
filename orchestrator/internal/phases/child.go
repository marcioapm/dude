package phases

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Persisted policy is checked even when a caller holds an older projection.
func (s *Syncer) childExecutionAllowed(ctx context.Context, r phaseRun) error {
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var source, phase, profile, control, predecessor, replacedBy, stopReason string
		var policy delivery.ChildCapabilities
		if err := tx.QueryRow(ctx, `SELECT COALESCE(source_kind, ''), COALESCE(phase::text, ''),
			COALESCE(child_profile, ''), COALESCE(child_capabilities, '{}'), control::text,
			COALESCE(child_replaces, ''), COALESCE(replaced_by, ''), COALESCE(child_stop_reason, '')
			FROM runs WHERE id = $1`, r.ID).
			Scan(&source, &phase, &profile, &policy, &control, &predecessor, &replacedBy, &stopReason); err != nil {
			return err
		}
		if source == "" {
			return nil
		}
		if source != string(delivery.ChildParentTaskConductor) || replacedBy != "" || stopReason != "" ||
			policy.PublishCode != delivery.Publishes[phase] ||
			profile != delivery.PromptRoleForPhase[phase] || phase == "" || control == "abort" {
			return fmt.Errorf("child execution held: unsupported capability, profile, or stop request")
		}
		if predecessor != "" {
			var terminal bool
			if err := tx.QueryRow(ctx, `SELECT status IN ('completed', 'failed', 'aborted') FROM runs WHERE id = $1`, predecessor).Scan(&terminal); err != nil {
				return err
			}
			if !terminal {
				return fmt.Errorf("child execution held: predecessor is not terminal")
			}
		}
		if delivery.Publishes[phase] {
			d, err := delivery.ReadDelivery(ctx, tx, r.TaskID)
			if err != nil {
				return err
			}
			attempt, err := delivery.EffectiveChildAttemptTx(ctx, tx, r.TaskID, d)
			if err != nil {
				return err
			}
			var ownAttempt int
			if err := tx.QueryRow(ctx, `SELECT attempt FROM runs WHERE id=$1`, r.ID).Scan(&ownAttempt); err != nil {
				return err
			}
			if d == nil || !d.Live() || attempt != ownAttempt {
				return fmt.Errorf("child execution held: delivery attempt is no longer live")
			}
			var parentLive bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs p WHERE p.id = r.parent_run_id
			AND p.task_id = r.task_id AND p.role = 'conductor' AND p.kind = 'agent'
			AND p.status IN ('pending', 'scheduled', 'starting', 'running', 'paused')
			AND p.replaced_by IS NULL AND p.control <> 'abort'
			AND NOT COALESCE((p.lux_state IN ('cancelled', 'terminated', 'failed', 'lost')
				OR p.control = 'abort'), false))
			FROM runs r WHERE r.id = $1`, r.ID).Scan(&parentLive); err != nil {
				return err
			}
			if !parentLive {
				return fmt.Errorf("child execution held: parent is no longer live")
			}
		}
		return nil
	})
}

// Terminal output is summarized only after the artifact sweep clears its exit.
func settleCollectedChildren(ctx context.Context, database *delivery.Store) (int, error) {
	type child struct {
		ID, Org, Status, ProjectID, TaskID, Phase string
		Attempt                                   int
	}
	var children []child
	err := database.DB.InSystem(ctx, "phase-notifier", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, r.status::text, r.project_id, r.task_id, r.phase::text, r.attempt
			FROM runs r WHERE r.source_kind = 'task_conductor' AND r.status IN ('completed','failed','aborted')
			AND r.artifacts_due_at IS NULL AND NOT EXISTS (SELECT 1 FROM child_results c WHERE c.child_run_id = r.id)
			ORDER BY r.ended_at, r.id LIMIT 50`)
		if err != nil {
			return err
		}
		children, err = pgx.CollectRows(rows, pgx.RowToStructByPos[child])
		return err
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range children {
		r := phaseRun{ID: c.ID, Org: c.Org, ProjectID: c.ProjectID, TaskID: c.TaskID, Phase: c.Phase, Attempt: c.Attempt}
		won, err := database.SettleChildWith(ctx, c.Org, c.ID, delivery.ChildOutcome{Status: c.Status}, func(tx pgx.Tx) error {
			if c.Phase == delivery.PhaseReview || c.Phase == delivery.PhaseTest {
				reply, err := delivery.RunReplyTx(ctx, tx, c.ID)
				if err != nil {
					return err
				}
				if err := recordFindingsTx(ctx, tx, c.Org, c.ID, delivery.ParseFindings(reply), nil); err != nil {
					return err
				}
			}
			if c.Phase == delivery.PhaseTest {
				_, err := delivery.RecordVerdictTx(ctx, tx, c.Org, &delivery.State{TaskID: r.TaskID, ProjectID: r.ProjectID, Attempt: r.Attempt, TestRunID: r.ID}, r.ID)
				return err
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		if won {
			n++
		}
	}
	return n, nil
}
