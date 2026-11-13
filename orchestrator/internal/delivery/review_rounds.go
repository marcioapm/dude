package delivery

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A review-only task's re-review rounds (design §6). After its verdict the
// delivery waits on the pull request (awaitReviewOnly). When the author
// pushes, dude's standing approval of an older commit is dismissed at once
// (an approval must never stand for code dude has not seen), and after a
// debounce (ReviewDebounce, 3 minutes) the next round reviews the newest
// head: through the review loop's own machinery, to a new verdict there.
// A push while a round runs never aborts it: its verdict is abandoned at
// the head check (decideVerdict's row 0), and the wait starts the next.

// The re-review rounds' ledger events, on the task.
const (
	// dude dismissed its own approval: {pullRequestId, reviewId, commitId, head, why}.
	EvReviewDismissed = "pull_request.review_dismissed"
	// GitHub refused that dismissal (branch protection), never retried:
	// {pullRequestId, reviewId, commitId, head, why, status, message}.
	EvReviewDismissRefused = "pull_request.review_dismiss_refused"
	// The author pushed: the next round starts at dueAt {pullRequestId, head, dueAt}.
	EvReviewRoundDue = "review.round_due"
	// The next round started at head {pullRequestId, head, round, fresh}.
	EvReviewRoundStarted = "review.round_started"
	// A bound stopped the rounds {pullRequestId, head, why, rounds|cost}.
	EvReviewRoundsStopped = "review.rounds_stopped"
)

// reviewDebounce is the wait after the author's last push before the next
// round (owner decision, 2026-11-11: 3 minutes).
const reviewDebounce = 3 * time.Minute

func (s *Store) reviewDebounce() time.Duration { return cmp.Or(s.ReviewDebounce, reviewDebounce) }

// reviewedHead is the head the task's last review round reviewed in repo.
func (st *State) reviewedHead(repo string) string {
	return cmp.Or(st.RoundHeads[repo], st.Heads[repo])
}

// newHead handles a head the author pushed (head ≠ the reviewed head):
// dude's approval of another commit is dismissed, and the next round is
// due after the debounce, or a bound stops it. A further push restarts
// the debounce. The verdict owed at the old head is dropped: it can never
// be posted there.
func (w *steps) newHead(ctx context.Context, sc workflow.StepContext, st *State, gh *forge.GitHub, pr PullRequestState, settings forge.Settings) error {
	head := pr.HeadSHA
	if st.Verdict != nil && st.Verdict.Due != "" && st.Verdict.Due != head {
		st.Verdict.Due = ""
	}
	if st.Verdict == nil {
		st.Verdict = &VerdictState{}
	}
	if err := w.dismissApprovals(ctx, sc, st, gh, pr, head, "head_moved"); err != nil {
		return err
	}
	if st.RoundDueHead == head && st.RoundDueAt != nil {
		return nil
	}
	if why, payload, err := w.roundsBound(ctx, sc, st, settings); err != nil || why != "" {
		if err != nil || st.RoundsStopped == why {
			return err
		}
		st.RoundsStopped, st.RoundDueAt, st.RoundDueHead = why, nil, ""
		payload["pullRequestId"], payload["head"], payload["why"] = pr.ID, head, why
		if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewRoundsStopped, payload); err != nil {
			return err
		}
		line := fmt.Sprintf("dude stopped re-reviewing after %d rounds; re-request to continue", st.Round)
		if why == "cost" {
			line = fmt.Sprintf("dude stopped re-reviewing: this review reached its cost cap after %d rounds; re-request to continue", st.Round)
		}
		w.statusComment(ctx, sc, st, pr, line)
		return nil
	}
	at := time.Now().Add(w.s.reviewDebounce())
	st.RoundDueAt, st.RoundDueHead = &at, head
	if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewRoundDue, map[string]any{"pullRequestId": pr.ID, "head": head, "dueAt": at}); err != nil {
		return err
	}
	w.statusComment(ctx, sc, st, pr, fmt.Sprintf("Waiting %s for more pushes before re-reviewing %s", spoken(w.s.reviewDebounce()), short(head)))
	return nil
}

// spoken is d as the status comment says it: "3 minutes", "2s".
func spoken(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if d == time.Minute {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.Round(time.Millisecond).String()
}

// startRound starts round N+1 at the newest head: the review step, as the
// loop's next round, its lenses going on from their last round by the
// existing rules (goesOn), or fresh. A head that does not descend from the
// reviewed one (a force-push) is reviewed whole, by fresh lenses.
func (w *steps) startRound(ctx context.Context, sc workflow.StepContext, st *State, pr PullRequestState) (workflow.Result, error) {
	head := pr.HeadSHA
	from := st.reviewedHead(pr.Repo)
	st.RoundDueAt, st.RoundDueHead = nil, ""
	st.Heads = maps.Clone(st.Heads)
	if st.Heads == nil {
		st.Heads = map[string]string{}
	}
	st.Heads[pr.Repo] = head
	if st.RoundHeads == nil {
		st.RoundHeads = map[string]string{pr.Repo: from}
	}
	change, err := w.roundChange(ctx, sc, st, PhaseReview, st.RoundHeads)
	if err != nil {
		return workflow.Result{}, err
	}
	fresh := !change.Measured || !change.Descends
	if fresh {
		// The whole branch: a range from a commit the head does not descend
		// from would review the rewrite as a diff against lost history.
		st.RoundHeads = nil
	}
	st.Verdict.Due, st.ReweighAt, st.ReviewChoice = "", nil, nil
	if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewRoundStarted, map[string]any{"pullRequestId": pr.ID, "head": head,
		"round": st.Round + 1, "fresh": fresh}); err != nil {
		return workflow.Result{}, err
	}
	w.statusComment(ctx, sc, st, pr, fmt.Sprintf("Re-reviewing %s (round %d)…", short(head), st.Round+1))
	return workflow.Result{Next: "review", State: st}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "re-reviewing "+short(head))
}

// roundsBound says whether a bound stops the next round: "rounds" past
// the organization's maxReviewRounds (counted on st.Round, the attempt's
// completed rounds), "cost" with its reviewCostCap reached by the
// attempt's Runs; "" for neither, with the payload's figure.
func (w *steps) roundsBound(ctx context.Context, sc workflow.StepContext, st *State, settings forge.Settings) (string, map[string]any, error) {
	if max := settings.MaxReviewRounds; max > 0 && st.Round >= max {
		return "rounds", map[string]any{"rounds": st.Round}, nil
	}
	if settings.ReviewCostCap <= 0 {
		return "", nil, nil
	}
	var spent float64
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(sum(m.cost_usd + m.machine_usd), 0) FROM runs r, run_metrics(r.id) m
			WHERE r.task_id = $1 AND r.attempt = $2`, st.TaskID, st.Attempt).Scan(&spent)
	}); err != nil {
		return "", nil, err
	}
	if spent >= settings.ReviewCostCap {
		return "cost", map[string]any{"cost": spent}, nil
	}
	return "", nil, nil
}

// ownReview is one of dude's reviews the task submitted.
type ownReview struct {
	ID       int64
	Event    string
	CommitID string
}

// dismissApprovals dismisses each of dude's own approvals on the pull
// request at a commit other than head, once: one already dismissed, or
// whose dismissal GitHub refused, is never tried again (decision 3). A
// refusal is recorded and named in the status comment, and never blocks
// anything. A REQUEST_CHANGES or a COMMENT is superseded by dude's newer
// review and is not dismissed.
func (w *steps) dismissApprovals(ctx context.Context, sc workflow.StepContext, st *State, gh *forge.GitHub, pr PullRequestState, head, why string) error {
	if gh == nil {
		return nil
	}
	var approvals []ownReview
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT (s.payload->>'reviewId')::bigint, s.payload->>'event', s.payload->>'head' FROM events s
			WHERE s.task_id = $1 AND s.event_type = $2 AND s.payload->>'pullRequestId' = $3 AND s.payload->>'event' = $4
			  AND s.payload->>'head' <> $5 AND s.payload ? 'reviewId'
			  AND NOT EXISTS (SELECT 1 FROM events d WHERE d.task_id = s.task_id AND d.event_type IN ($6, $7)
			    AND d.payload->>'reviewId' = s.payload->>'reviewId')
			ORDER BY s.occurred_at, s.id`, st.TaskID, EvReviewSubmitted, pr.ID, forge.EventApprove, head, EvReviewDismissed, EvReviewDismissRefused)
		if err != nil {
			return err
		}
		approvals, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ownReview])
		return err
	}); err != nil {
		return err
	}
	for _, a := range approvals {
		payload := map[string]any{"pullRequestId": pr.ID, "reviewId": a.ID, "commitId": a.CommitID, "head": head, "why": why}
		msg := fmt.Sprintf("Head moved to %s; re-reviewing.", short(head))
		if why != "head_moved" {
			msg = fmt.Sprintf("Superseded by dude's review at %s.", short(head))
		}
		err := gh.DismissReview(ctx, pr.Slug, pr.Number, a.ID, msg)
		switch {
		case err == nil:
			if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewDismissed, payload); err != nil {
				return err
			}
		case forge.DismissRefused(err) || forge.Refused(err) && !forge.Transient(err):
			var e *forge.Error
			payload["status"], payload["message"] = 0, err.Error()
			if errors.As(err, &e) {
				payload["status"], payload["message"] = e.Status, e.Message
			}
			if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewDismissRefused, payload); err != nil {
				return err
			}
			// Named under the status comment's next line, which follows.
			st.Verdict.DismissRefused = fmt.Sprintf("Could not dismiss the approval at %s: %s", short(a.CommitID), payload["message"])
		default:
			// GitHub did not answer: tried again at the next wait or verdict;
			// never blocks.
			slog.Warn("dismissing dude's stale approval failed", "pr", pr.ID, "review", a.ID, "error", err)
		}
	}
	return nil
}
