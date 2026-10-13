package delivery

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// RerequestReviews asks again for a review of each open pull request whose
// head is now a commit dude pushed (heads, by repository), from everyone
// whose latest review is on an older head and who is not asked already
// (forge.StaleReviewers). GitHub drops a reviewer from requested_reviewers
// when they submit, and a reviewer that reviews only when requested would
// otherwise never see the fix.
//
// Best effort, as the request at opening: GitHub refusing, or failing to
// say who reviewed, never fails the step; what was asked, and what was
// not, is recorded as pull_request.review_rerequested, once per pull
// request and head, so a replayed step asks nobody twice.
func (s *Store) RerequestReviews(ctx context.Context, org string, st *State, gh *forge.GitHub, heads map[string]string) error {
	if gh == nil || len(heads) == 0 {
		return nil
	}
	states, err := s.PullRequestStates(ctx, org, st.PullRequestIDs)
	if err != nil {
		return err
	}
	for _, p := range states {
		head := heads[p.Repo]
		if head == "" || p.Slug == "" || p.State == forge.StateMerged || p.State == forge.StateClosed {
			continue
		}
		done, err := s.rerequested(ctx, org, st.TaskID, p.Repo, p.Number, head)
		if err != nil || done {
			return err
		}
		payload := map[string]any{"number": p.Number, "repo": p.Repo, "head": head}
		stale, err := gh.StaleReviewers(ctx, p.Slug, p.Number)
		switch {
		case err != nil:
			payload["error"] = err.Error()
		case stale.Head != head:
			// Someone pushed on top of dude's commit since: the head is
			// theirs, and so is asking for a review of it.
			continue
		case len(stale.Logins) == 0:
			continue
		default:
			asked, refused, why := requestEach(ctx, gh, p.Slug, p.Number, stale.Logins)
			payload["reviewersRequested"], payload["reviewersRefused"] = db.NonNil(asked), db.NonNil(refused)
			if why != "" {
				payload["error"] = why
			}
		}
		if err := s.Emit(ctx, org, st, EvPullRequestReviewRerequested, payload); err != nil {
			return err
		}
	}
	return nil
}

// rerequested says whether the re-request for this pull request and head
// is on record already.
func (s *Store) rerequested(ctx context.Context, org, taskID, repo string, number int, head string) (bool, error) {
	var done bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
			AND payload->>'repo' = $3 AND (payload->>'number')::int = $4 AND payload->>'head' = $5)`,
			taskID, EvPullRequestReviewRerequested, repo, number, head).Scan(&done)
	})
	return done, err
}

// requestEach asks for every login at once, and when GitHub refuses the
// whole request — it does for one login it will not ask (no longer a
// collaborator, say) — for each alone, so one refusal costs only that one.
// Returns who was asked, who was not, and GitHub's last refusal.
func requestEach(ctx context.Context, gh *forge.GitHub, slug string, number int, logins []string) (asked, refused []string, why string) {
	err := gh.RequestReviewers(ctx, slug, number, logins)
	if err == nil {
		return logins, nil, ""
	}
	var e *forge.Error
	if len(logins) == 1 || !errors.As(err, &e) || e.Status != 422 {
		return nil, logins, err.Error()
	}
	for _, l := range logins {
		if err := gh.RequestReviewers(ctx, slug, number, []string{l}); err != nil {
			refused, why = append(refused, l), err.Error()
		} else {
			asked = append(asked, l)
		}
	}
	return asked, refused, why
}
