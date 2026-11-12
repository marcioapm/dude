package delivery

import (
	"context"
	"errors"
	"fmt"

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

// ReviewOnlyNotYet refuses starting a review-only task: the step that ends
// its delivery (the verdict) is not here yet, and without it the review
// loop would go on to a writer the guard refuses.
type ReviewOnlyNotYet struct{ Number int }

func (e ReviewOnlyNotYet) Error() string { return fmt.Sprintf(rReviewOnlyNotYet, e.Number) }

// IsReviewOnlyNotYet says err is ReviewOnlyNotYet.
func IsReviewOnlyNotYet(err error) bool { return errors.As(err, new(ReviewOnlyNotYet)) }

// RefuseReviewOnlyStartTx is every start path's refusal of a review-only
// task (Deliver, Talk it through, a Chat start, Start when unblocked,
// Start over, Resume, Try again).
// PR 4 (the verdict step) removes this refusal.
func RefuseReviewOnlyStartTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	n, err := reviewOnlyTx(ctx, tx, taskID)
	if err != nil || n == 0 {
		return err
	}
	return ReviewOnlyNotYet{Number: max(n, 0)}
}

// RefuseReviewOnlyPRActionTx refuses action, a write to the pull request's
// branch, on a review-only task, in the words its conductor is refused
// with: a person's Update branch too.
func RefuseReviewOnlyPRActionTx(ctx context.Context, tx pgx.Tx, taskID, action string) error {
	n, err := reviewOnlyTx(ctx, tx, taskID)
	if err != nil || n == 0 {
		return err
	}
	return refusef(rReviewOnlyPRAction, max(n, 0), action)
}

// ReviewOnlyTx says the task reviews its pull request and changes no code.
func ReviewOnlyTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	n, err := reviewOnlyTx(ctx, tx, taskID)
	return n != 0, err
}
