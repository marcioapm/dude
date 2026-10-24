package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// ownerChanges is the owner sweep's event types, a literal matching its
// partial index (events_session_owner_changes_idx).
const ownerChanges = "('session.owner_left', 'session.owner_returned', 'person.removed')"

var (
	sessionOwnerLeftTold     = delivery.MustText("notices/told/session-owner-left")
	sessionOwnerReturnedTold = delivery.MustText("notices/told/session-owner-returned")
)

type ownerChange struct {
	Cursor  int64
	Org     string
	Type    string
	Session string
	Person  string
}

// sweepOwners does what follows a person leaving the organisation or
// coming back, from the ledger, after the backend's transaction committed:
//   - session.owner_left / session.owner_returned: the session's live agent
//     is told who owns it now, unless a later owner change was recorded for
//     the session: one that changed since is skipped.
//   - person.removed: every open question put to the person by name, in any
//     session, is withdrawn while they are still removed, so no session
//     waits on someone who can no longer answer.
//
// Each change is marked done (session_owner_done) in the transaction that
// tells the agent, under the session's lock, so it is told once whatever
// sweeps race or restart; a withdrawal is idempotent and marked after it.
// The watermark moves as the session sweep's does. Returns how many it read.
func (n *Notifier) sweepOwners(ctx context.Context) (int, error) {
	var found []ownerChange
	if err := n.DB.InSystem(ctx, "session-owners", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT e.cursor, e.organization_id, e.event_type, COALESCE(e.session_id, ''),
				COALESCE(e.payload->>'owner', e.payload->>'personId', '')
			FROM events e
			WHERE e.cursor > (SELECT after_cursor FROM session_owner_sweep)
			  AND e.event_type IN `+ownerChanges+`
			  AND NOT EXISTS (SELECT 1 FROM session_owner_done d WHERE d.cursor = e.cursor)
			ORDER BY e.cursor LIMIT 100`)
		if err != nil {
			return err
		}
		found, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ownerChange])
		return err
	}); err != nil {
		return 0, err
	}
	var errs []error
	for _, c := range found {
		var err error
		if c.Type == "person.removed" {
			err = n.withdrawFromRemoved(ctx, c)
		} else {
			err = n.tellOwnerChange(ctx, c)
		}
		if err != nil {
			errs = append(errs, n.ownerFailed(ctx, c, err))
		}
	}
	return len(found), errors.Join(errors.Join(errs...), n.advanceOwners(ctx))
}

// ownerAttempts is how many times a change is tried before it may be given
// up, and ownerGiveUpAfter how long after its first failure: both must be
// reached, so an error lasting a few 2 s sweeps gives nothing up.
const (
	ownerAttempts    = 5
	ownerGiveUpAfter = 10 * time.Minute
)

// ownerFailed counts a failed attempt at c; once both bounds are reached it
// logs c and marks it done, so a change that fails every time does not
// hold the watermark (and, a hundred of them, every later change) for
// good. Returns what to report: nil once given up.
func (n *Notifier) ownerFailed(ctx context.Context, c ownerChange, cause error) error {
	failed := fmt.Errorf("%s %d: %w", c.Type, c.Cursor, cause)
	var attempts int
	var givenUp bool
	if err := n.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO session_owner_attempts (cursor, organization_id, attempts) VALUES ($1, $2, 1)
			ON CONFLICT (cursor) DO UPDATE SET attempts = session_owner_attempts.attempts + 1
			RETURNING attempts, attempts >= $3 AND first_failed_at < now() - make_interval(secs => $4)`,
			c.Cursor, c.Org, ownerAttempts, ownerGiveUpAfter.Seconds()).Scan(&attempts, &givenUp); err != nil {
			return err
		}
		if !givenUp {
			return nil
		}
		_, err := markOwnerDone(ctx, tx, c)
		return err
	}); err != nil {
		return errors.Join(failed, err)
	}
	if !givenUp {
		return failed
	}
	n.Log.Warn("session owner change given up", "cursor", c.Cursor, "type", c.Type, "attempts", attempts, "error", cause)
	return nil
}

func (n *Notifier) tellOwnerChange(ctx context.Context, c ownerChange) error {
	left := c.Type == delivery.EvSessionOwnerLeft
	return n.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		if c.Session != "" {
			if err := delivery.LockSession(ctx, tx, c.Session); err != nil {
				return err
			}
		}
		if marked, err := markOwnerDone(ctx, tx, c); err != nil || !marked {
			return err
		}
		// Every change of a session's owner records one of these under
		// the session's lock (passOnSessions, returnSessions, take-ownership,
		// a handover), so one recorded after this is what "changed since" is.
		var changed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events l WHERE l.session_id = $1 AND l.cursor > $2
				AND l.event_type IN ('session.owner_left', 'session.owner_returned', 'session.owner_taken', 'session.owner_changed'))`,
			c.Session, c.Cursor).Scan(&changed); err != nil || changed {
			return err
		}
		name, err := delivery.PersonName(ctx, tx, c.Person)
		if err != nil {
			return err
		}
		text := sessionOwnerReturnedTold.Render(name)
		if left {
			text = sessionOwnerLeftTold.Render(name)
		}
		return delivery.TellBrainstorm(ctx, tx, c.Org, c.Session, text)
	})
}

// withdrawFromRemoved withdraws, session by session under its lock, what
// was put to a removed person by name; someone back by then answers it.
func (n *Notifier) withdrawFromRemoved(ctx context.Context, c ownerChange) error {
	var asked []string
	if err := n.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT r.session_id FROM questions q JOIN runs r ON r.id = q.run_id
			WHERE q.to_person = $1 AND q.status = 'open' AND r.session_id IS NOT NULL ORDER BY 1`, c.Person)
		if err != nil {
			return err
		}
		asked, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		return err
	}
	var errs []error
	for _, id := range asked {
		errs = append(errs, n.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
			if err := delivery.LockSession(ctx, tx, id); err != nil {
				return err
			}
			var removed bool
			if err := tx.QueryRow(ctx, `SELECT count(*) > 0 FROM people WHERE id = $1 AND removed_at IS NOT NULL`, c.Person).
				Scan(&removed); err != nil || !removed {
				return err
			}
			return delivery.WithdrawQuestionsTo(ctx, tx, c.Org, id, c.Person)
		}))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return n.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		_, err := markOwnerDone(ctx, tx, c)
		return err
	})
}

// markOwnerDone records the change as done; false when another sweep did.
func markOwnerDone(ctx context.Context, tx pgx.Tx, c ownerChange) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO session_owner_done (cursor, organization_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		c.Cursor, c.Org)
	return tag.RowsAffected() == 1, err
}

// advanceOwners moves the owner sweep's watermark past what is done and
// settled, never past a change not done yet, and forgets the done below it.
func (n *Notifier) advanceOwners(ctx context.Context) error {
	return n.DB.InSystem(ctx, "session-owners", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE session_owner_sweep SET after_cursor = GREATEST(after_cursor, LEAST(
				COALESCE((SELECT max(d.cursor) FROM session_owner_done d JOIN events e ON e.cursor = d.cursor
				          WHERE e.occurred_at < now() - make_interval(secs => $1)), 0),
				COALESCE((SELECT min(e.cursor) - 1 FROM events e
				          WHERE e.cursor > session_owner_sweep.after_cursor AND e.event_type IN `+ownerChanges+`
				            AND NOT EXISTS (SELECT 1 FROM session_owner_done d WHERE d.cursor = e.cursor)), 9223372036854775807)))`,
			settle.Seconds()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM session_owner_done WHERE cursor <= (SELECT after_cursor FROM session_owner_sweep)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM session_owner_attempts WHERE cursor <= (SELECT after_cursor FROM session_owner_sweep)`)
		return err
	})
}
