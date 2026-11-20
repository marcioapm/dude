package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A delivery that ends — its pull requests merged or closed
// (pullRequestEnded), or stopped (StopAllTx) — reaches no later step to
// post its fixers' held answers. Under the task's Chat lock, as posting
// is, from dude's records alone (settleHeldOnEndTx), every answer not yet
// settled is:
//
//   - withheld, saying why, if no poster ever claimed it;
//   - otherwise put in answer_reconciliations (migration 268), never
//     withheld: a poster may be writing it to GitHub right now, or its
//     write (a released claim's) may have reached GitHub. The row stops any
//     new claim (claimTx). Its poster records what happened, settling the
//     row in the same transaction (recordPostedTx, Store.failed); a poster
//     that never does is settled by the pr-reconciler's pass once its claim
//     has long expired (ReconcileEndedAnswers), from GitHub: dude's own
//     marked comment, or the thread resolved, is posted; their absence is
//     withheld; a read that cannot tell is uncertain.
//
// A live conductor is woken with what was withheld and what is in flight;
// each in-flight answer's outcome is said in the task's Chat, and to a live
// conductor, once.

// Reasons and outcomes of an answer at its delivery's end.
const (
	withheldMerged       = "the pull request was merged"
	withheldClosed       = "the pull request was closed"
	withheldStopped      = "the delivery was stopped"
	withheldBeforePosted = "the delivery ended before it was posted"
	uncertainAnswer      = "it may have been posted; check the pull request"

	reconciledPosted    = "posted"
	reconciledFailed    = "failed"
	reconciledWithheld  = "withheld"
	reconciledUncertain = "uncertain"
)

// Times of the reconciliation, from the claim: read no sooner than
// answerReconcileAfter past it (twice the lease: no poster still writes
// then); a GitHub read failing for a moment is retried every
// answerReconcileRetry, until answerReconcileGiveUp past the claim.
const (
	answerReconcileAfter  = 2 * answerClaimLease
	answerReconcileRetry  = time.Minute
	answerReconcileGiveUp = time.Hour
)

// endHeldSQL is every held answer of the task's fix Runs neither settled
// nor reconciling ($1), with its Run's project and published heads, and
// its latest claim's time.
var endHeldSQL = `SELECT h.run_id, r.project_id, h.id, h.payload, ` + publishedHeads("r") + `,
		(SELECT c.occurred_at FROM events c WHERE ` + answerRecord("c", "h.run_id", "h.id", EvAnswerClaimed) + ` ORDER BY c.cursor DESC LIMIT 1)
	FROM runs r JOIN events h ON h.run_id = r.id AND ` + isHeld("h") + `
	WHERE r.task_id = $1 AND r.phase = 'fix' AND NOT ` + answerSettled("h") + ` AND NOT ` + reconciling("h") + `
	ORDER BY r.created_at, r.id, h.cursor`

// settleHeldOnEndTx ends the task's held answers as the comment above
// says, for why, or, given states, for the end of the pull request each is
// on. It takes the Chat lock (re-entrant); run again, it finds nothing.
func settleHeldOnEndTx(ctx context.Context, tx pgx.Tx, org, taskID, why string, states []PullRequestState) error {
	if err := LockChat(ctx, tx, taskID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, endHeldSQL, taskID)
	if err != nil {
		return err
	}
	type unsettled struct {
		ref     RunRef
		h       heldOf
		raw     []byte
		commits map[string]string
		claimed *time.Time
	}
	var all []unsettled
	var u unsettled
	u.ref = RunRef{Org: org, TaskID: taskID}
	if _, err := pgx.ForEachRow(rows, []any{&u.ref.RunID, &u.ref.ProjectID, &u.h.ID, &u.raw, &u.commits, &u.claimed}, func() error {
		all = append(all, u)
		return json.Unmarshal(u.raw, &all[len(all)-1].h.heldAnswer)
	}); err != nil {
		return err
	}
	var out []FixerAnswers
	for _, u := range all {
		reason := why
		if states != nil {
			reason = withheldClosed
			for _, p := range states {
				if p.Repo == u.h.Repo && p.Number == u.h.Number && p.State == forge.StateMerged {
					reason = withheldMerged
				}
			}
		}
		if n := len(out); n == 0 || out[n-1].RunID != u.ref.RunID || out[n-1].Ended != reason {
			out = append(out, FixerAnswers{RunID: u.ref.RunID, Commits: u.commits, Cause: CauseEnded, Ended: reason})
		}
		f := &out[len(out)-1]
		f.ids = append(f.ids, u.h.ID)
		if u.claimed != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO answer_reconciliations (answer_id, organization_id, ended, claimed_at, due_at)
				VALUES ($1, $2, $3, $4, $5)`, u.h.ID, org, reason, *u.claimed, u.claimed.Add(answerReconcileAfter)); err != nil {
				return err
			}
			f.InFlight = append(f.InFlight, u.h.label())
			continue
		}
		if err := appendFailedTx(ctx, tx, u.ref, u.h, reason, "withheld"); err != nil {
			return err
		}
		f.Withheld, f.WithheldList = reason, append(f.WithheldList, u.h.label())
	}
	for _, f := range out {
		if err := wakeAnswersTx(ctx, tx, org, taskID, f); err != nil {
			return err
		}
	}
	return nil
}

// appendFailedTx records held answer h as not posted, for why; flag, when
// set, is "withheld" or "uncertain".
func appendFailedTx(ctx context.Context, tx pgx.Tx, ref RunRef, h heldOf, why, flag string) error {
	p := map[string]any{"answerId": h.ID, "kind": h.Kind, "error": why}
	if flag != "" {
		p[flag] = true
	}
	_, err := ledger.Append(ctx, tx, answerEvent(ref, EvAnswerFailed, h.ID, p))
	return err
}

// SettleHeldOnStopTx settles the task's held answers for a stopped
// delivery (settleHeldOnEndTx); StopAllTx calls it.
func SettleHeldOnStopTx(ctx context.Context, tx pgx.Tx, org, taskID string) error {
	return settleHeldOnEndTx(ctx, tx, org, taskID, withheldStopped, nil)
}

// settleHeldOnEnd settles the task's held answers once none of its pull
// requests is open (states).
func (w *steps) settleHeldOnEnd(ctx context.Context, org, taskID string, states []PullRequestState) error {
	if len(states) == 0 || countStates(states).open > 0 {
		return nil
	}
	return w.s.DB.InOrg(ctx, org, func(tx pgx.Tx) error { return settleHeldOnEndTx(ctx, tx, org, taskID, "", states) })
}

// settleReconciliationTx settles held answer id's open reconciliation, if
// it has one, as outcome, and says so once: a Chat notice (a stopped or
// merged delivery often has no conductor left) and a live conductor's
// wake. The caller holds the Chat lock and recorded the answer in tx.
func settleReconciliationTx(ctx context.Context, tx pgx.Tx, ref RunRef, id, outcome, detail string) error {
	var ended string
	var raw []byte
	err := tx.QueryRow(ctx, `UPDATE answer_reconciliations x SET outcome = $2 FROM events h
		WHERE x.answer_id = $1 AND x.outcome IS NULL AND h.id = x.answer_id RETURNING x.ended, h.payload`, id, outcome).Scan(&ended, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	h := heldOf{ID: id}
	if err == nil {
		err = json.Unmarshal(raw, &h.heldAnswer)
	}
	if err != nil {
		return err
	}
	line := reconciledLine(ref.RunID, h.label(), ended, outcome, detail)
	if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: ref.Org, ProjectID: ref.ProjectID,
		TaskID: ref.TaskID, RunID: ref.RunID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
		CorrelationID: ref.TaskID, Payload: map[string]any{"about": "answer_reconciled", "answerId": id, "outcome": outcome, "text": line}}); err != nil {
		return err
	}
	return wakeLiveConductorTx(ctx, tx, ref.Org, ref.TaskID, WakeRound, "answer-reconciled:"+id, line)
}

// ReconcileEndedAnswers settles, from GitHub, the in-flight answers of
// ended deliveries due on the store's clock, at most 20 a pass, and
// returns how many it settled. The pr-reconciler's pass runs it. A row
// that fails is put off answerReconcileRetry; the rest go on.
func (s *Store) ReconcileEndedAnswers(ctx context.Context, forges Forges) (int, error) {
	type due struct{ ID, Org string }
	var todo []due
	now := s.now()
	if err := s.DB.InSystem(ctx, "answer-reconciler", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT answer_id, organization_id FROM answer_reconciliations
			WHERE outcome IS NULL AND due_at <= $1 ORDER BY due_at LIMIT 20`, now)
		if err == nil {
			todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[due])
		}
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range todo {
		settled, err := s.reconcile(ctx, forges, now, d.Org, d.ID)
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if err != nil {
			slog.Warn("reconciling an ended delivery's in-flight answer failed", "answer", d.ID, "error", err)
		}
		if err != nil || !settled {
			_ = s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE answer_reconciliations SET due_at = $2 WHERE answer_id = $1 AND outcome IS NULL`,
					d.ID, now.Add(answerReconcileRetry))
				return err
			})
			continue
		}
		n++
	}
	return n, nil
}

// reconcile settles one row from GitHub, read with no transaction open:
// whether it did, or the read failed for a moment short of the give-up
// bound and it is retried.
func (s *Store) reconcile(ctx context.Context, forges Forges, now time.Time, org, id string) (bool, error) {
	ref := RunRef{Org: org}
	h := heldOf{ID: id}
	var raw []byte
	var slug string
	var claimed time.Time
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT h.task_id, h.run_id, h.project_id, x.claimed_at, h.payload,
				COALESCE((SELECT r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id WHERE pr.id = h.payload->>'prId'), '')
			FROM answer_reconciliations x JOIN events h ON h.id = x.answer_id WHERE x.answer_id = $1 AND x.outcome IS NULL`, id).
			Scan(&ref.TaskID, &ref.RunID, &ref.ProjectID, &claimed, &raw, &slug)
	}); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, &h.heldAnswer); err != nil {
		return false, err
	}
	slug = forge.SlugFromURL(slug)
	settle := func(outcome, detail string, posted forge.Posted, thread *forge.ReviewThread) (bool, error) {
		return true, s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			if outcome == reconciledPosted {
				return recordPostedTx(ctx, tx, ref, h, slug, posted, thread)
			}
			if err := LockChat(ctx, tx, ref.TaskID); err != nil {
				return err
			}
			// Its poster recorded it meanwhile, and settled the row.
			if done, err := settledTx(ctx, tx, ref.RunID, id); err != nil || done {
				return err
			}
			why := withheldBeforePosted
			if outcome == reconciledUncertain {
				why = uncertainAnswer + ": " + detail
			}
			if err := appendFailedTx(ctx, tx, ref, h, why, outcome); err != nil {
				return err
			}
			return settleReconciliationTx(ctx, tx, ref, id, outcome, detail)
		})
	}
	// unreadable is a read that failed: retried while it may mend, short
	// of the give-up bound; uncertain otherwise.
	unreadable := func(err error) (bool, error) {
		if forge.Transient(err) && now.Sub(claimed) < answerReconcileGiveUp {
			return false, nil
		}
		return settle(reconciledUncertain, err.Error(), forge.Posted{}, nil)
	}
	gh, err := forges.For(ctx, org)
	switch {
	case err != nil:
		return unreadable(err)
	case gh == nil || slug == "":
		return settle(reconciledUncertain, "no GitHub to read it with", forge.Posted{}, nil)
	case h.Kind == answerResolve:
		threads, err := gh.ReviewThreads(ctx, slug, h.Number)
		if err != nil {
			return unreadable(err)
		}
		if t := forge.ThreadOf(threads, forge.LineCommentID(h.Thread)); t != nil && t.Resolved {
			return settle(reconciledPosted, "", forge.Posted{}, t)
		}
		return settle(reconciledWithheld, "", forge.Posted{}, nil)
	}
	// A reply: dude's own comment carrying its marker, and nothing else,
	// is evidence it was posted. Never posted again here. A search cut at
	// its bound (forge.ErrSearchIncomplete) is unreadable, so uncertain.
	posted, err := gh.FindAnswer(ctx, slug, h.Number, id, forge.LineCommentID(h.InReplyTo) != 0)
	switch {
	case err != nil:
		return unreadable(err)
	case posted.ID != "":
		return settle(reconciledPosted, "", posted, nil)
	}
	return settle(reconciledWithheld, "", forge.Posted{}, nil)
}
