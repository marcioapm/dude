package delivery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
// One caller at a time per (task, repository, number, head): each holds a
// session advisory lock on a connection of its own from before it reads
// the pull_request_rerequests row until after it records the outcome, and
// does every GitHub read and request for that head inside it. A caller that
// finds the lock held skips that pull request, finishes the others, and
// returns ErrRerequestBusy, so its step runs again later. A live holder
// keeps the lock however long its GitHub calls take, or while its process
// is suspended; only its session ending (the process dying, or the
// connection to Postgres breaking) frees the lock early. The holder checks
// that session before every POST, the per-login retries after a 422
// included, and records the outcome and its event in one transaction on it.
//
// Not guaranteed: GitHub and the database cannot commit together, so this
// is at most one caller's requests in flight at a time, not exactly one
// request per head. Before asking, the holder writes who it is asking
// (`asking`); a holder that ends without finishing leaves the row
// unfinished, and the next to take the lock starts from what GitHub says
// then. Of the lost holder's logins, one GitHub no longer counts as owing a
// review is recorded as requested without asking again; one it still does
// (refused, or never sent: the two cannot be told apart) is asked once
// more, with anyone else stale. A request already on the wire when its
// holder's session broke can still reach GitHub after the next holder has
// asked: a second request to that login, at most.
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
		err := c.run(ctx, st, gh, p.Slug)
		if errors.Is(err, errClaimBusy) {
			busy = true
			continue
		}
		if err != nil {
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

// errClaimBusy: another session holds the head's lock, or this caller's
// own session, and with it the lock, ended mid-way.
var errClaimBusy = errors.New("re-request held by another caller")

type rerequestClaim struct {
	s                       *Store
	org, taskID, repo, head string
	number                  int
	token                   string
	// The session holding the lock; every claim write goes through it.
	conn *pgxpool.Conn
	// Who a lost holder was asking, when this claim took over from one.
	asking []string
}

// run is one pull request's re-request, under its head's lock.
func (c *rerequestClaim) run(ctx context.Context, st *State, gh *forge.GitHub, slug string) error {
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	done, err := c.take(ctx)
	if err != nil || done {
		return err
	}
	payload, err := c.ask(ctx, gh, slug)
	if err != nil {
		return err
	}
	return c.finish(ctx, st, payload)
}

// lock takes a pool connection and the head's lock on it, or returns
// errClaimBusy. unlock frees both; a connection whose unlock failed is
// closed rather than pooled, which frees the lock too.
func (c *rerequestClaim) lock(ctx context.Context) (unlock func(), err error) {
	conn, err := c.s.DB.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	// Not a transaction lock: it must outlive the transactions below and
	// the GitHub calls between them.
	key := c.lockKey()
	var held bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&held)
	if err != nil || !held {
		conn.Release()
		if err == nil {
			err = errClaimBusy
		}
		return nil, err
	}
	c.conn = conn
	return func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key); err != nil {
			_ = conn.Hijack().Close(uctx)
			return
		}
		conn.Release()
	}, nil
}

// lockKey is the head's advisory lock, as text for hashtextextended.
func (c *rerequestClaim) lockKey() string {
	return fmt.Sprintf("pull_request_rerequest:%s:%s:%d:%s", c.taskID, c.repo, c.number, c.head)
}

// alive is nil while the session holding the lock is. A session lock is
// released only by its session, so this is the holder still owning it.
func (c *rerequestClaim) alive(ctx context.Context) error {
	if err := c.conn.Ping(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", errClaimBusy, err)
	}
	return nil
}

func (c *rerequestClaim) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	err := db.InOrgOn(ctx, c.conn, c.org, fn)
	if err != nil && ctx.Err() == nil && c.conn.Conn().IsClosed() {
		return fmt.Errorf("%w: %v", errClaimBusy, err)
	}
	return err
}

// take claims the row under the lock: a new one, or one a holder that
// ended left unfinished. done when the head's re-request is finished.
func (c *rerequestClaim) take(ctx context.Context) (done bool, err error) {
	c.token = ids.New("rrc")
	err = c.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO pull_request_rerequests (organization_id, task_id, repo, number, head, token)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, c.org, c.taskID, c.repo, c.number, c.head, c.token)
		if err != nil || tag.RowsAffected() == 1 {
			return err
		}
		err = tx.QueryRow(ctx, `UPDATE pull_request_rerequests SET token = $5, claimed_at = now()
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4 AND finished_at IS NULL
			RETURNING COALESCE(asking, '{}')`,
			c.taskID, c.repo, c.number, c.head, c.token).Scan(&c.asking)
		if db.IsNotFound(err) {
			done = true
			return nil
		}
		return err
	})
	return done, err
}

// ask does the GitHub side under the lock. Returns the event's payload,
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
		asked, refused, why, err = requestEach(ctx, gh, slug, c.number, stale.Logins, c.alive)
		if err != nil {
			return nil, err
		}
	}
	payload["reviewersRequested"], payload["reviewersRefused"] = db.NonNil(append(taken, asked...)), db.NonNil(refused)
	if why != "" {
		payload["error"] = why
	}
	return payload, nil
}

// mark writes who the holder is about to ask, before asking.
func (c *rerequestClaim) mark(ctx context.Context, logins []string) error {
	all := append(slices.Clone(c.asking), logins...)
	slices.Sort(all)
	return c.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pull_request_rerequests SET asking = $6
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4 AND token = $5 AND finished_at IS NULL`,
			c.taskID, c.repo, c.number, c.head, c.token, slices.Compact(all))
		if err == nil && tag.RowsAffected() == 0 {
			return errClaimBusy
		}
		return err
	})
}

// finish records the outcome, and its event when there is one, together.
func (c *rerequestClaim) finish(ctx context.Context, st *State, payload map[string]any) error {
	var outcome any // SQL NULL when nobody was asked
	if payload != nil {
		outcome = payload
	}
	return c.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pull_request_rerequests SET outcome = $6, finished_at = now()
			WHERE task_id = $1 AND repo = $2 AND number = $3 AND head = $4 AND token = $5 AND finished_at IS NULL`,
			c.taskID, c.repo, c.number, c.head, c.token, outcome)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errClaimBusy
		}
		if payload == nil {
			return nil
		}
		return emitTx(ctx, tx, c.org, st, EvPullRequestReviewRerequested, payload)
	})
}

// requestEach asks for every login at once, and when GitHub refuses the
// whole request — it does for one login it will not ask (no longer a
// collaborator, say) — for each alone, so one refusal costs only that one.
// Returns who was asked, who was not, and GitHub's last refusal. before
// runs ahead of every POST; its error ends the sequence without that POST.
func requestEach(ctx context.Context, gh *forge.GitHub, slug string, number int, logins []string,
	before func(context.Context) error) (asked, refused []string, why string, err error) {
	if err := before(ctx); err != nil {
		return nil, nil, "", err
	}
	rerr := gh.RequestReviewers(ctx, slug, number, logins)
	if rerr == nil {
		return logins, nil, "", nil
	}
	var e *forge.Error
	if len(logins) == 1 || !errors.As(rerr, &e) || e.Status != 422 {
		return nil, logins, rerr.Error(), nil
	}
	for _, l := range logins {
		if err := before(ctx); err != nil {
			return nil, nil, "", err
		}
		if rerr := gh.RequestReviewers(ctx, slug, number, []string{l}); rerr != nil {
			refused, why = append(refused, l), rerr.Error()
		} else {
			asked = append(asked, l)
		}
	}
	return asked, refused, why, nil
}
