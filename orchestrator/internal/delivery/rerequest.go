package delivery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
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
// not, is recorded as pull_request.review_rerequested.
//
// Once per (task, repository, number, head): the caller that inserts the
// pull_request_rerequests row is the only one that contacts GitHub for that
// head, and it records the outcome and the event in one transaction, only
// while its token still holds the row. Another caller meanwhile skips that
// pull request, finishes the others, and returns ErrRerequestBusy, so its
// step runs again later.
//
// GitHub and the database cannot commit together. Before its POST the
// holder writes who it is asking (`asking`). A holder that dies, or fails
// to record, after that leaves the row unfinished; once released (a
// failure it returns) or after rerequestClaimTTL (a crash), the next
// caller takes it over and starts from what GitHub says then. Of the lost
// holder's logins, one GitHub no longer counts as owing a review is
// recorded as requested without asking again; one it still does (refused,
// or never sent: the two cannot be told apart) is asked once more, with
// anyone else stale. A lost holder's POST can therefore repeat only for a
// login GitHub did not take.
func (s *Store) RerequestReviews(ctx context.Context, org string, st *State, gh *forge.GitHub, heads map[string]string) error {
	if gh == nil || len(heads) == 0 {
		return nil
	}
	states, err := s.PullRequestStates(ctx, org, st.PullRequestIDs)
	if err != nil {
		return err
	}
	busy := false
	for _, p := range states {
		head := heads[p.Repo]
		if head == "" || p.Slug == "" || p.State == forge.StateMerged || p.State == forge.StateClosed {
			continue
		}
		c := rerequestClaim{s: s, org: org, taskID: st.TaskID, repo: p.Repo, number: p.Number, head: head}
		got, err := c.take(ctx)
		if err != nil {
			return err
		}
		if got == claimDone {
			continue
		}
		if got == claimBusy {
			busy = true
			continue
		}
		payload, err := c.ask(ctx, gh, p.Slug)
		if err == nil {
			err = c.finish(ctx, st, payload)
		}
		if errors.Is(err, errClaimLost) {
			busy = true
			continue
		}
		if err != nil {
			c.release(context.WithoutCancel(ctx))
			return err
		}
	}
	if busy {
		return ErrRerequestBusy
	}
	return nil
}

// ErrRerequestBusy: another caller holds a pull request's re-request for
// its head; the step runs again shortly and finds it finished.
var ErrRerequestBusy = workflow.Wait{After: 5 * time.Second,
	Why: "another worker is asking this pull request's reviewers again; waiting for it"}

// rerequestClaimTTL is how long an unfinished claim stays its holder's.
// The token, not the clock, fences recording: a holder that outlives it
// may still reach GitHub, but records nothing.
const rerequestClaimTTL = 2 * time.Minute

type claimState int

const (
	claimOwned claimState = iota
	claimDone
	claimBusy
)

var errClaimLost = errors.New("re-request claim taken over")

type rerequestClaim struct {
	s                       *Store
	org, taskID, repo, head string
	number                  int
	token                   string
	// Who a lost holder was asking, when this claim took over from one.
	asking []string
}

// take claims the re-request: a new row, or one whose holder released it
// or outlived rerequestClaimTTL without finishing. A concurrent INSERT of
// the same key waits on the first's commit, then claims nothing.
func (c *rerequestClaim) take(ctx context.Context) (claimState, error) {
	c.token = ids.New("rrc")
	state := claimBusy
	err := c.s.DB.InOrg(ctx, c.org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO pull_request_rerequests (organization_id, task_id, repo, number, head, token)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, c.org, c.taskID, c.repo, c.number, c.head, c.token)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			state = claimOwned
			return nil
		}
		err = tx.QueryRow(ctx, `UPDATE pull_request_rerequests SET token = $5, claimed_at = now()
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4
			  AND finished_at IS NULL AND claimed_at < now() - $6::interval
			RETURNING COALESCE(asking, '{}')`,
			c.taskID, c.repo, c.number, c.head, c.token, rerequestClaimTTL.String()).Scan(&c.asking)
		if err == nil {
			state = claimOwned
			return nil
		}
		if !db.IsNotFound(err) {
			return err
		}
		var finished bool
		if err := tx.QueryRow(ctx, `SELECT finished_at IS NOT NULL FROM pull_request_rerequests
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4`,
			c.taskID, c.repo, c.number, c.head).Scan(&finished); err != nil {
			return err
		}
		if finished {
			state = claimDone
		}
		return nil
	})
	return state, err
}

// ask does the GitHub side under the claim. Returns the event's payload,
// nil when there was nobody to ask.
func (c *rerequestClaim) ask(ctx context.Context, gh *forge.GitHub, slug string) (map[string]any, error) {
	payload := map[string]any{"number": c.number, "repo": c.repo, "head": c.head}
	stale, err := gh.StaleReviewers(ctx, slug, c.number)
	switch {
	case err != nil:
		payload["error"] = err.Error()
		return payload, nil
	case stale.Head != c.head:
		// Someone pushed on top of dude's commit since: the head is
		// theirs, and so is asking for a review of it.
		return nil, nil
	}
	// A lost holder's login GitHub no longer counts as owing a review took
	// its request (or has reviewed this head since): not asked again.
	var taken []string
	for _, l := range c.asking {
		if !slices.ContainsFunc(stale.Logins, func(s string) bool { return strings.EqualFold(s, l) }) {
			taken = append(taken, l)
		}
	}
	if len(stale.Logins) == 0 && len(taken) == 0 {
		return nil, nil
	}
	var asked, refused []string
	var why string
	if len(stale.Logins) > 0 {
		if err := c.mark(ctx, stale.Logins); err != nil {
			return nil, err
		}
		asked, refused, why = requestEach(ctx, gh, slug, c.number, stale.Logins)
	}
	payload["reviewersRequested"], payload["reviewersRefused"] = db.NonNil(append(taken, asked...)), db.NonNil(refused)
	if why != "" {
		payload["error"] = why
	}
	return payload, nil
}

// mark writes who the holder is about to ask, before asking, while the
// claim is still its own.
func (c *rerequestClaim) mark(ctx context.Context, logins []string) error {
	all := append(slices.Clone(c.asking), logins...)
	slices.Sort(all)
	return c.s.DB.InOrg(ctx, c.org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pull_request_rerequests SET asking = $6
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4 AND token = $5 AND finished_at IS NULL`,
			c.taskID, c.repo, c.number, c.head, c.token, slices.Compact(all))
		if err == nil && tag.RowsAffected() == 0 {
			return errClaimLost
		}
		return err
	})
}

// finish records the outcome, and its event when there is one, together,
// only while the claim is still this caller's.
func (c *rerequestClaim) finish(ctx context.Context, st *State, payload map[string]any) error {
	var outcome any // SQL NULL when nobody was asked
	if payload != nil {
		outcome = payload
	}
	return c.s.DB.InOrg(ctx, c.org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pull_request_rerequests SET outcome = $6, finished_at = now()
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4 AND token = $5 AND finished_at IS NULL`,
			c.taskID, c.repo, c.number, c.head, c.token, outcome)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errClaimLost
		}
		if payload == nil {
			return nil
		}
		return emitTx(ctx, tx, c.org, st, EvPullRequestReviewRerequested, payload)
	})
}

// release gives up a claim its holder failed to finish, so the next caller
// takes it over at once rather than after rerequestClaimTTL. Best effort:
// unreleased, it lapses.
func (c *rerequestClaim) release(ctx context.Context) {
	_ = c.s.DB.InOrg(ctx, c.org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE pull_request_rerequests SET claimed_at = '-infinity'
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4 AND token = $5 AND finished_at IS NULL`,
			c.taskID, c.repo, c.number, c.head, c.token)
		return err
	})
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
