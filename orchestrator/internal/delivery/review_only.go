package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// Review only: a task that reviews a person's pull request and changes no
// code. Its source's mode is the one truth, fixed once delivery starts and
// not delivery state an agent could change, and every path that would
// write to its branch asks it here.

// reviewOnlyTx is the number of the pull request a review-only task
// reviews, 0 when the task is not review only.
func reviewOnlyTx(ctx context.Context, tx pgx.Tx, taskID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT COALESCE(number, 0) FROM task_sources WHERE task_id = $1 AND mode = $2`,
		taskID, ModeReviewOnly).Scan(&n)
	if db.IsNotFound(err) {
		return 0, nil
	}
	if n == 0 && err == nil {
		n = -1
	}
	return n, err
}

// refuseReviewOnlyWriterTx refuses a writer (an implementer, fixer or
// simplifier, a publish) on a review-only task. createPhaseRunTx asks it for
// every writer Run, whatever made it; the paths that make no Run ask it
// themselves.
func refuseReviewOnlyWriterTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	n, err := reviewOnlyTx(ctx, tx, taskID)
	if err != nil || n == 0 {
		return err
	}
	return refusef(rReviewOnlyNoWriter, max(n, 0))
}

// ReviewOnlyTx says the task reviews its pull request and changes no code.
func ReviewOnlyTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	n, err := reviewOnlyTx(ctx, tx, taskID)
	return n != 0, err
}
