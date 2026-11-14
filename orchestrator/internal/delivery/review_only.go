package delivery

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
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
// then each lens the verdict's gate requires that it left out, with
// ReasonRequired. The gate reads the paths as verdictReviewed does
// (prPaths at the round's head), which an author's push may have changed
// since adoption; the paths at adoption stand where GitHub would not
// compare (prPaths gives none).
func (w *steps) withRequiredLenses(ctx context.Context, sc workflow.StepContext, st *State, categories []string,
	reasons map[string][]string) ([]string, map[string][]string, error) {
	paths := slices.Clone(st.ChangedPaths)
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		return nil, nil, err
	}
	if gh != nil && len(st.PullRequestIDs) > 0 {
		states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range states {
			if head := cmp.Or(st.Heads[p.Repo], p.HeadSHA); p.Slug != "" && head != "" {
				_, live, _, err := prPaths(ctx, gh, GateFacts{Repo: p.Repo, Slug: p.Slug, Head: head}, st.Source.BaseBranch)
				if err != nil {
					return nil, nil, err
				}
				paths = append(paths, live...)
			}
		}
	}
	out := slices.Clone(categories)
	reasons = maps.Clone(reasons)
	for _, lens := range ReviewersFor(st.Policy, paths) {
		if !slices.Contains(out, lens) {
			out = append(out, lens)
			reasons[lens] = []string{ReasonRequired}
		}
	}
	return out, reasons, nil
}

// minWrongEvidence is the fewest runes, trimmed, of "wrong:" evidence.
const minWrongEvidence = 10

// reviewOnlyGroundsTx reads a review-only task's dismissal reason (only
// the reason: dismiss_finding's schema is the writing mode's) as its
// grounds, recorded with the dismissal, or refuses it: "wrong: <evidence>",
// or "duplicate: <finding id>" naming another open finding of the task, so
// a set of duplicates always leaves one open and counted by the verdict.
// Concurrent dismissals are serialized by liveConductor's chat lock.
func reviewOnlyGroundsTx(ctx context.Context, tx pgx.Tx, taskID, findingID, reason string) (map[string]any, error) {
	word, rest, _ := strings.Cut(reason, ":")
	rest = strings.TrimSpace(rest)
	switch kind := strings.ToLower(strings.TrimSpace(word)); kind {
	case "wrong":
		if len([]rune(rest)) < minWrongEvidence || !strings.ContainsFunc(rest, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			return nil, refuseText(rReviewOnlyDismissWrongEvidence)
		}
		return map[string]any{"kind": kind, "evidence": clip(oneLine(rest), 1000)}, nil
	case "duplicate":
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
		return map[string]any{"kind": kind, "duplicateOf": of}, nil
	}
	return nil, refuseText(rReviewOnlyDismissGrounds)
}
