package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// The reconciliation of a fixer's held answers in flight when their
// delivery ended (answers_end.go, answer_reconciliations, migration 271).

// Outcomes of an in-flight answer (answer_reconciliations.outcome).
const (
	reconciledPosted    = "posted"
	reconciledFailed    = "failed"
	reconciledWithheld  = "withheld"
	reconciledUncertain = "uncertain"
)

// answerReconcileBatch bounds the rows one pass takes.
const answerReconcileBatch = 20

// answerReconcileRetry is how long a row whose GitHub read failed
// transiently waits; answerReconcileGiveUp past its creation, such a row
// is settled uncertain rather than retried for ever.
const (
	answerReconcileRetry  = time.Minute
	answerReconcileGiveUp = time.Hour
)

var reconciledTmpl = mustTemplate("wakes/fixer-answers/reconciled")

// reconciledLine tells the conductor what an in-flight answer ended as.
func reconciledLine(runID, label, ended, outcome, why, detail string) string {
	return render(reconciledTmpl, struct{ RunID, Label, Ended, Outcome, Why, Detail string }{runID, label, ended, outcome, why, detail})
}

// settleReconciliationTx settles held answer id's open reconciliation, if
// the delivery's end opened one, as outcome, once: said in the task's Chat
// (EvChatNotice, about answer_reconciled), and the live conductor, if any,
// woken with it. The caller holds the Chat lock and has
// recorded the answer's settling record in tx. No row: nothing.
func settleReconciliationTx(ctx context.Context, tx pgx.Tx, ref RunRef, id, outcome, why, detail string) error {
	var label, ended string
	err := tx.QueryRow(ctx, `UPDATE answer_reconciliations SET settled_at = now(), outcome = $2
		WHERE answer_id = $1 AND settled_at IS NULL RETURNING label, ended`, id, outcome).Scan(&label, &ended)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	line := reconciledLine(ref.RunID, label, ended, outcome, why, detail)
	// Said in the task's Chat whatever the outcome: a stopped or merged
	// delivery often has no live conductor left to wake, and a person reads
	// it there.
	if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: ref.Org, ProjectID: ref.ProjectID,
		TaskID: ref.TaskID, RunID: ref.RunID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
		CorrelationID: ref.TaskID, Payload: map[string]any{"about": "answer_reconciled", "answerId": id, "outcome": outcome,
			"text": line}}); err != nil {
		return err
	}
	return wakeLiveConductorTx(ctx, tx, ref.Org, ref.TaskID, WakeRound, "answer-reconciled:"+id, line)
}

// AnswerReconciler settles the in-flight answers of ended deliveries whose
// poster never recorded them. It runs in the pr-reconciler's pass
// (prs.Syncer.Reconcile), not a loop of its own.
type AnswerReconciler struct {
	DB     *db.DB
	Forges Forges
	Log    *slog.Logger
	// Now is the reconciler's clock: the instant a pass compares due times
	// and the give-up bound against, and a retry's due time, come from it.
	// A row's created_at and first due_at are the database's. nil is
	// time.Now.
	Now func() time.Time
}

// now is the reconciler's clock (Now).
func (a *AnswerReconciler) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *AnswerReconciler) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// answerReconcileDue is the pass's selection: open rows due, oldest first,
// a batch (answer_reconciliations_due_idx). The source's literal, for the
// plan harness.
const answerReconcileDue = `SELECT answer_id, organization_id FROM answer_reconciliations
	WHERE settled_at IS NULL AND due_at <= $2 ORDER BY due_at LIMIT $1`

// answerReconcileLater puts a row whose pass failed off until $2, a time on
// the reconciler's clock (Now), not the database's.
const answerReconcileLater = `UPDATE answer_reconciliations SET due_at = $2
	WHERE answer_id = $1 AND settled_at IS NULL`

// Sweep settles the rows due, and returns how many it settled. One that
// fails is logged and retried later; the batch goes on.
func (a *AnswerReconciler) Sweep(ctx context.Context) (int, error) {
	type due struct{ ID, Org string }
	var todo []due
	// One instant for the whole pass: the rows it takes, the bound it gives
	// up at and the time it retries one at are on the same clock.
	now := a.now()
	if err := a.DB.InSystem(ctx, "answer-reconciler", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, answerReconcileDue, answerReconcileBatch, now)
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[due])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range todo {
		settled, err := a.reconcile(ctx, now, d.Org, d.ID)
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if err != nil {
			a.log().Warn("reconciling an ended delivery's in-flight answer failed", "answer", d.ID, "error", err)
			_ = a.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, answerReconcileLater, d.ID, now.Add(answerReconcileRetry))
				return err
			})
			continue
		}
		if settled {
			n++
		}
	}
	return n, nil
}

// reconcileRow is one row as reconcile reads it.
type reconcileRow struct {
	ref       RunRef
	h         heldOf
	slug      string
	created   time.Time
	ended     string
	settledBy string
}

// reconcile settles one row: dude's records first (its poster may have
// recorded it since), then GitHub, read with no transaction open. now is
// the pass's instant on the reconciler's clock.
func (a *AnswerReconciler) reconcile(ctx context.Context, now time.Time, org, id string) (bool, error) {
	var r reconcileRow
	open := false
	if err := a.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT x.task_id, x.run_id, ru.project_id, x.created_at, x.ended, h.payload,
				COALESCE((SELECT r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
					WHERE pr.id = h.payload->>'prId'), '')
			FROM answer_reconciliations x JOIN runs ru ON ru.id = x.run_id JOIN events h ON h.id = x.answer_id
			WHERE x.answer_id = $1 AND x.settled_at IS NULL`, id).
			Scan(&r.ref.TaskID, &r.ref.RunID, &r.ref.ProjectID, &r.created, &r.ended, &raw, &r.slug)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		open = true
		r.ref.Org, r.h.ID, r.slug = org, id, forge.SlugFromURL(r.slug)
		return json.Unmarshal(raw, &r.h.heldAnswer)
	}); err != nil || !open {
		return false, err
	}
	gh, err := a.Forges.For(ctx, org)
	if err != nil && forge.Transient(err) {
		return a.retryOr(ctx, now, r, err)
	}
	if err != nil || gh == nil || r.slug == "" {
		why := "no GitHub to read it with"
		if err != nil {
			why = err.Error()
		}
		return true, a.settle(ctx, r, reconciledUncertain, uncertainAnswer, why, forge.Posted{}, "")
	}
	if r.h.Kind == answerResolve {
		threads, err := gh.ReviewThreads(ctx, r.slug, r.h.Number)
		switch {
		case forge.Transient(err):
			// A GitHub that failed for a moment (a 502) is asked again,
			// until the give-up bound.
			return a.retryOr(ctx, now, r, err)
		case err != nil:
			// An answer GitHub will not mend by asking again.
			return true, a.settle(ctx, r, reconciledUncertain, uncertainAnswer, err.Error(), forge.Posted{}, "")
		}
		t := forge.ThreadOf(threads, forge.LineCommentID(r.h.Thread))
		switch {
		case t == nil || !t.Resolved:
			// No thread for it, or one not resolved: withheld alike.
			return true, a.settle(ctx, r, reconciledWithheld, withheldBeforePosted, "", forge.Posted{}, "")
		default:
			return true, a.settle(ctx, r, reconciledPosted, "", "", forge.Posted{}, t.ID)
		}
	}
	// A reply: dude's own comment carrying its marker, and nothing else,
	// is evidence it was posted. Never posted again here.
	posted, err := gh.FindAnswer(ctx, r.slug, r.h.Number, id, isLineComment(r.h.InReplyTo))
	switch {
	case errors.Is(err, forge.ErrSearchIncomplete):
		return true, a.settle(ctx, r, reconciledUncertain, uncertainAnswer, "the search for its comment was cut at its bound", forge.Posted{}, "")
	case forge.Transient(err):
		return a.retryOr(ctx, now, r, err)
	case err != nil:
		return true, a.settle(ctx, r, reconciledUncertain, uncertainAnswer, err.Error(), forge.Posted{}, "")
	case posted.ID != "":
		return true, a.settle(ctx, r, reconciledPosted, "", "", posted, "")
	}
	return true, a.settle(ctx, r, reconciledWithheld, withheldBeforePosted, "", forge.Posted{}, "")
}

// isLineComment says a feedback id is a line comment's (its reply is in the
// line's thread).
func isLineComment(id string) bool {
	return len(id) > len("line-comment-") && id[:len("line-comment-")] == "line-comment-"
}

// retryOr leaves a row GitHub did not answer for a later pass, until
// answerReconcileGiveUp past its creation, when it is settled uncertain.
// now is the pass's instant on the reconciler's clock. It says whether it
// settled the row, as reconcile does.
func (a *AnswerReconciler) retryOr(ctx context.Context, now time.Time, r reconcileRow, err error) (bool, error) {
	if now.Sub(r.created) < answerReconcileGiveUp {
		return false, err
	}
	return true, a.settle(ctx, r, reconciledUncertain, uncertainAnswer, err.Error(), forge.Posted{}, "")
}

// settle records the answer's outcome under the Chat lock, unless its
// poster recorded it meanwhile (then its record is the outcome): a reply
// found is recorded as its poster would have (chat.message), a resolution
// found as its action; withheld and uncertain are an EvAnswerFailed
// (withheld, or uncertain). Then the reconciliation is settled and told.
func (a *AnswerReconciler) settle(ctx context.Context, r reconcileRow, outcome, why, detail string, posted forge.Posted, thread string) error {
	return a.DB.InOrg(ctx, r.ref.Org, func(tx pgx.Tx) error {
		if err := LockChat(ctx, tx, r.ref.TaskID); err != nil {
			return err
		}
		if done, err := settledTx(ctx, tx, r.ref.RunID, r.h.ID); err != nil {
			return err
		} else if done {
			var failed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events s WHERE s.run_id = $1 AND s.event_type = '`+EvAnswerFailed+`'
				AND `+settledAnswerID("s")+` = $2)`, r.ref.RunID, r.h.ID).Scan(&failed); err != nil {
				return err
			}
			o := reconciledPosted
			if failed {
				o = reconciledFailed
			}
			return settleReconciliationTx(ctx, tx, r.ref, r.h.ID, o, "", "")
		}
		switch outcome {
		case reconciledPosted:
			if r.h.Kind == answerResolve {
				var head string
				if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT head_sha FROM pull_requests WHERE id = $1), '')`, r.h.PRID).Scan(&head); err != nil {
					return err
				}
				c := PRActionCheck{ref: r.ref, action: ActionResolveThread, prID: r.h.PRID, repo: r.h.Repo, slug: r.slug,
					number: r.h.Number, answerID: r.h.ID, head: head}
				if _, err := RecordPRActionTx(ctx, tx, c, PRActionDone{Thread: thread}); err != nil {
					return err
				}
			} else {
				p := ReplyPost{ref: r.ref, repo: r.h.Repo, number: r.h.Number, prID: r.h.PRID, slug: r.slug, text: r.h.Text,
					inReplyTo: r.h.InReplyTo, by: RoleFixer, answerID: r.h.ID}
				if _, err := RecordReplyTx(ctx, tx, p, posted); err != nil {
					return err
				}
			}
		case reconciledWithheld:
			// Directly: failAnswerTx leaves an answer in reconciliation alone.
			if _, err := ledger.Append(ctx, tx, answerEvent(r.ref, EvAnswerFailed, r.h.ID, map[string]any{"answerId": r.h.ID,
				"kind": r.h.Kind, "error": why, "withheld": true})); err != nil {
				return err
			}
		default:
			if _, err := ledger.Append(ctx, tx, answerEvent(r.ref, EvAnswerFailed, r.h.ID, map[string]any{"answerId": r.h.ID,
				"kind": r.h.Kind, "error": fmt.Sprintf("%s: %s", why, detail), "uncertain": true})); err != nil {
				return err
			}
		}
		return settleReconciliationTx(ctx, tx, r.ref, r.h.ID, outcome, why, detail)
	})
}
