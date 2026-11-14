package delivery

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"

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

// withRequiredLenses is a review-only round's lenses: the conductor's,
// then each lens the verdict's gate requires for the pull request's
// changed paths (ReviewersFor, as verdictReviewed reads it) that it left
// out, with ReasonRequired.
func withRequiredLenses(st *State, categories []string, reasons map[string][]string) ([]string, map[string][]string) {
	out := slices.Clone(categories)
	reasons = maps.Clone(reasons)
	for _, lens := range ReviewersFor(st.Policy, st.ChangedPaths) {
		if !slices.Contains(out, lens) {
			out = append(out, lens)
			reasons[lens] = []string{ReasonRequired}
		}
	}
	return out, reasons
}

// A review-only dismissal's grounds, the first word of its reason: the
// finding is wrong (the evidence follows), or a duplicate of another
// finding of the task (its id follows). dismiss_finding's schema is the
// writing mode's; only the reason is read here.
const (
	GroundsWrong     = "wrong"
	GroundsDuplicate = "duplicate"
)

// reviewOnlyGroundsTx reads a review-only task's dismissal reason as its
// grounds, recorded with the dismissal, or refuses it: "wrong: <evidence>",
// or "duplicate: <finding id>" naming another open finding of the task. The
// one it duplicates must be open, so a set of duplicates always leaves one
// open and counted by the verdict: no cycle dismisses them all.
func reviewOnlyGroundsTx(ctx context.Context, tx pgx.Tx, taskID, findingID, reason string) (map[string]any, error) {
	word, rest, _ := strings.Cut(reason, ":")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(strings.TrimSpace(word)) {
	case GroundsWrong:
		if rest == "" {
			return nil, refuseText(rReviewOnlyDismissWrongEvidence)
		}
		return map[string]any{"kind": GroundsWrong, "evidence": clip(oneLine(rest), 1000)}, nil
	case GroundsDuplicate:
		of, _, _ := strings.Cut(rest, " ")
		of = strings.Trim(of, ".,;")
		var exists bool
		if of != "" && of != findingID {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM review_findings WHERE id = $1 AND task_id = $2 AND status = 'open')`,
				of, taskID).Scan(&exists); err != nil {
				return nil, err
			}
		}
		if !exists {
			return nil, refusef(rReviewOnlyDismissDuplicateOf, cmp.Or(of, "no finding"))
		}
		return map[string]any{"kind": GroundsDuplicate, "duplicateOf": of}, nil
	}
	return nil, refuseText(rReviewOnlyDismissGrounds)
}
