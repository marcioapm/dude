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

// reviewOnlyTx says whether the task is review only, and the number of
// the pull request it reviews (0 when its source has none).
func reviewOnlyTx(ctx context.Context, tx pgx.Tx, taskID string) (number int, ok bool, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE(number, 0) FROM task_sources WHERE task_id = $1 AND mode = $2`,
		taskID, ModeReviewOnly).Scan(&number)
	if db.IsNotFound(err) {
		return 0, false, nil
	}
	return number, err == nil, err
}

// refuseReviewOnlyWriterTx refuses a writer (an implementer, fixer or
// simplifier, a publish) on a review-only task. createPhaseRunTx asks it for
// every writer Run, whatever made it; the paths that make no Run ask it
// themselves.
func refuseReviewOnlyWriterTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	n, ok, err := reviewOnlyTx(ctx, tx, taskID)
	if err != nil || !ok {
		return err
	}
	return refusef(rReviewOnlyNoWriter, n)
}

// RefuseReviewOnlyPRActionTx refuses action, a write to the pull request's
// branch, on a review-only task, in the words its conductor is refused
// with: a person's Update branch too.
func RefuseReviewOnlyPRActionTx(ctx context.Context, tx pgx.Tx, taskID, action string) error {
	n, ok, err := reviewOnlyTx(ctx, tx, taskID)
	if err != nil || !ok {
		return err
	}
	return refusef(rReviewOnlyPRAction, n, action)
}

// ReviewOnlyTx says the task reviews its pull request and changes no code.
func ReviewOnlyTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	_, ok, err := reviewOnlyTx(ctx, tx, taskID)
	return ok, err
}
