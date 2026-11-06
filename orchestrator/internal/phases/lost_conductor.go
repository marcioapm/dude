package phases

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lostLux is the reason a Run lux no longer has fails with.
const lostLux = "lux no longer has this Run"

// lostConductor replaces conductor r, which lux no longer has, when it
// held nothing in flight and waited on a person
// (delivery.ReplaceLostConductorTx): its question stays open, on a
// placeholder Run started once the person answers. Otherwise it fails as
// before (lostLux). Whichever of the follower and the sweep comes second
// finds it failed, and does nothing.
func (s *Syncer) lostConductor(ctx context.Context, r phaseRun, why string) error {
	s.unfollow(r.ID)
	if r.LuxRunID != "" && !lux.Terminal(r.LuxState) {
		_ = s.Lux.Cancel(ctx, r.LuxRunID)
	}
	var next string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		next, err = delivery.ReplaceLostConductorTx(ctx, tx, r.ref(), why, delivery.Parked)
		return err
	}); err != nil {
		return err
	}
	if next != "" {
		s.Log.Info("lux lost a conductor waiting on a person; its question moved to a placeholder", "run", r.ID, "next", next)
		return nil
	}
	return s.failed(ctx, r, lostLux, false)
}

// placeholder says r is a conductor ReplaceLostConductorTx put in a lost
// Run's slot: paused for a person, with no lux Run yet.
func (r phaseRun) placeholder() bool {
	return r.Status == statusPaused && r.LuxRunID == "" && r.conductor()
}

// startPlaceholder starts a placeholder conductor once what it waited for
// came (resumable: the person answered): briefed now (delivery.LostBriefing),
// pending, so the next sweep submits it, and the answer queued for it is its
// first message.
func (s *Syncer) startPlaceholder(ctx context.Context, r phaseRun) error {
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := delivery.LockChat(ctx, tx, r.TaskID); err != nil {
			return err
		}
		var old string
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT o.id FROM runs o WHERE o.task_id = r.task_id AND o.replaced_by = r.id ORDER BY o.created_at DESC LIMIT 1), '')
			FROM runs r WHERE r.id = $1 AND r.status = 'paused' AND r.lux_run_id IS NULL FOR UPDATE OF r`, r.ID).Scan(&old); err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		}
		// Resumable also accepts explicit Resume. It is not an answer: check
		// again under Chat so neither a stale selection nor control can spend
		// model use while the person still has an open question.
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questions WHERE run_id = $1 AND status = 'open' AND addressee = 'person')`, r.ID).Scan(&open); err != nil {
			return err
		}
		if open {
			_, err := tx.Exec(ctx, `UPDATE runs SET control = 'none', control_requested_at = NULL, control_reason = NULL WHERE id = $1`, r.ID)
			return err
		}
		prompt, err := delivery.LostBriefing(ctx, tx, r.ref(), old)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'pending', prompt = $2, dude_pause = NULL, waiting_since = NULL,
				control = 'none', control_requested_at = NULL, control_reason = NULL
			WHERE id = $1 AND status = 'paused' AND lux_run_id IS NULL`, r.ID, prompt)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if err := s.event(ctx, tx, r, delivery.EvConductorBriefed, ledger.ActorSystem, map[string]any{"text": prompt}); err != nil {
			return err
		}
		return s.event(ctx, tx, r, evUnparked, ledger.ActorSystem, map[string]any{"reason": "person"})
	})
}

// closeFailedQuestions settles, for a conductor r failing in tx with
// reason, the person questions the status change is about to cancel
// (015's trigger): their ids, for questionsClosed once it has.
func closeFailedQuestions(ctx context.Context, tx pgx.Tx, r phaseRun) ([]string, error) {
	if !r.conductor() {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM questions WHERE run_id = $1 AND status = 'open' AND addressee = 'person'
		ORDER BY asked_at, id FOR UPDATE`, r.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// questionsClosed records each question cancelled with conductor r as it
// failed with reason: question.closed {by: lost | run_failed}, and the task
// no longer waiting on a person for them (delivery.EndCancelledWaitTx).
func (s *Syncer) questionsClosed(ctx context.Context, tx pgx.Tx, r phaseRun, reason string, closed []string) error {
	if len(closed) == 0 {
		return nil
	}
	by := delivery.QuestionRunFailed
	if delivery.LostByLux(reason) {
		by = delivery.QuestionLost
	}
	for _, q := range closed {
		if err := s.event(ctx, tx, r, delivery.EvQuestionClosed, ledger.ActorSystem,
			map[string]any{"questionId": q, "by": by, "runId": r.ID}); err != nil {
			return err
		}
	}
	return delivery.EndCancelledWaitTx(ctx, tx, r.Org, r.ProjectID, r.TaskID)
}
