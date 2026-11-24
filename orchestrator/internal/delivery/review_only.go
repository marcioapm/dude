package delivery

import (
	"cmp"
	"context"
	"fmt"
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

// retryableLensFailure is an infrastructure failure, never an intentional stop
// or a reviewer's judgement about the code. Unknown failures stay incomplete.
func retryableLensFailure(reason string) bool {
	r := strings.ToLower(reason)
	if strings.Contains(r, "stopped by") || strings.Contains(r, "conductor") || strings.Contains(r, "aborted") {
		return false
	}
	return strings.HasPrefix(r, "the setup script of ") ||
		strings.Contains(r, "init-failed") ||
		strings.Contains(r, "maximum budget") || strings.Contains(r, "budget limit") ||
		strings.Contains(r, "temporary model error") || strings.Contains(r, "rate limit") ||
		strings.Contains(r, "api error: 529") || strings.Contains(r, "service unavailable") ||
		strings.Contains(r, "overloaded") || strings.Contains(r, "bad gateway") ||
		strings.Contains(r, "connection closed mid-response") || strings.Contains(r, "stream disconnected") ||
		strings.Contains(r, "host lost") || strings.Contains(r, "host loss") ||
		strings.Contains(r, "lux lost") || strings.Contains(r, "placement lost")
}

// minWrongEvidence is the fewest runes, trimmed, of "wrong:" evidence.
const minWrongEvidence = 10

// saysSomething says trimmed grounds ("wrong:" evidence, a suggestion's
// why) are at least minWrongEvidence runes with a letter or digit.
func saysSomething(s string) bool {
	return len([]rune(s)) >= minWrongEvidence && strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

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
		if !saysSomething(rest) {
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
	case kindSuggestion:
		gate, why, ok := suggestionGrounds(rest)
		if !ok {
			return nil, refuseText(rReviewOnlyDismissSuggestion)
		}
		return map[string]any{"kind": kind, "gate": gate, "why": clip(oneLine(why), 1000)}, nil
	}
	return nil, refuseText(rReviewOnlyDismissGrounds)
}

// kindSuggestion is the word a review-only conductor's reason starts with
// to keep a finding as a suggestion ("suggestion: gate <N>: <why>"), and
// the kind its grounds are recorded as.
const kindSuggestion = "suggestion"

// suggestionGates are the gates of contracts/review-only's rubric a
// finding may fail to be kept as a suggestion, by number, named as the
// contract titles them (lower case): being introduced at medium or above
// is what makes a finding a candidate at all.
var suggestionGates = map[int]string{
	2: "shipped path",
	3: "proof",
	4: "test quality",
	5: "declared behaviour",
	6: "proportionate",
	7: "current",
}

// suggestionGrounds reads "gate <N>: <why>", the rest of a "suggestion:"
// reason: N one of suggestionGates, why saying something.
func suggestionGrounds(rest string) (gate int, why string, ok bool) {
	word, rest, _ := strings.Cut(strings.TrimSpace(rest), " ")
	num, why, found := strings.Cut(rest, ":")
	num, why = strings.TrimSpace(num), strings.TrimSpace(why)
	if !strings.EqualFold(word, "gate") || !found || len(num) != 1 || num[0] < '0' || num[0] > '9' {
		return 0, "", false
	}
	gate = int(num[0] - '0')
	if _, known := suggestionGates[gate]; !known || !saysSomething(why) {
		return 0, "", false
	}
	return gate, why, true
}

// keptText is the sentence that gives a kept suggestion's gate and why,
// in dude's thread reply and its inline comment.
func keptText(gate, why string) string {
	return fmt.Sprintf("Kept as a suggestion (%s): %s.", gate, why)
}

// keptSuggestion reads a finding's resolution note as a review-only
// conductor's "suggestion:" dismissal (DismissedByConductor, then the
// reason reviewOnlyGroundsTx accepted): the name of the gate it fails and
// why. The note's prefix is the one mark of a kept suggestion: it holds
// no verdict, and is never shown as withdrawn.
func keptSuggestion(note string) (gate, why string, ok bool) {
	reason, conductor := strings.CutPrefix(note, DismissedByConductor)
	word, rest, found := strings.Cut(reason, ":")
	if !conductor || !found || !strings.EqualFold(strings.TrimSpace(word), kindSuggestion) {
		return "", "", false
	}
	n, why, ok := suggestionGrounds(rest)
	return suggestionGates[n], strings.TrimRight(why, "."), ok
}
