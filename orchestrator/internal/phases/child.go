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
		var source, phase, profile, control string
		var policy delivery.ChildCapabilities
		if err := tx.QueryRow(ctx, `SELECT COALESCE(source_kind, ''), COALESCE(phase::text, ''),
			COALESCE(child_profile, ''), COALESCE(child_capabilities, '{}'), control::text FROM runs WHERE id = $1`, r.ID).
			Scan(&source, &phase, &profile, &policy, &control); err != nil {
			return err
		}
		if source == "" {
			return nil
		}
		if source != string(delivery.ChildParentTaskConductor) || policy.PublishCode || delivery.Publishes[phase] ||
			profile != delivery.PromptRoleForPhase[phase] || phase == "" || control == "abort" {
			return fmt.Errorf("child execution held: unsupported capability, profile, or stop request")
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
		s := &Syncer{DB: database.DB}
		if c.Phase == delivery.PhaseReview || c.Phase == delivery.PhaseTest {
			if err := s.reportFindings(ctx, r); err != nil {
				return n, err
			}
		}
		if c.Phase == delivery.PhaseTest {
			if _, err := database.RecordVerdict(ctx, c.Org, &delivery.State{TaskID: c.TaskID, ProjectID: c.ProjectID, Attempt: c.Attempt, TestRunID: c.ID}, c.ID); err != nil {
				return n, err
			}
		}
		won, err := database.SettleChild(ctx, c.Org, c.ID, delivery.ChildOutcome{Status: c.Status})
		if err != nil {
			return n, err
		}
		if won {
			n++
		}
	}
	return n, nil
}
