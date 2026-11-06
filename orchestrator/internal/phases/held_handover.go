package phases

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// heldAsk is a conductor-held reviewer question or repository request
// the handover selects.
type heldAsk struct{ ID, Org, TaskID string }

// heldAskWait is how long the conductor holds a reviewer's question or a
// repository request before dude hands it to the person (Syncer.HeldAskWait;
// zero is delivery.ConductorAnswerWait).
func (s *Syncer) heldAskWait() time.Duration {
	if s.HeldAskWait > 0 {
		return s.HeldAskWait
	}
	return delivery.ConductorAnswerWait
}

// handOverHeldAsks is the sweep's handover of the asks dude gave a
// conductor first that are not a Run's question (design A10, A11): a
// reviewer's question or a repository request the conductor did not settle
// within the wait, whose conductor failed since, has no model, or no
// longer decides, goes to the person (delivery.HandOverReviewQuestionTx,
// HandOverRepositoryRequestTx). The selection is one read-only pass over
// the partial indexes of migration 248; each due item is then handed over
// in its own organization's transaction, which reads it again under its
// locks, so a second pass or a concurrent answer hands nothing twice.
//
// Self-contained beside the writer questions' handover (PR4b-1), so the
// two can be run side by side.
func (s *Syncer) handOverHeldAsks(ctx context.Context) error {
	wait := s.heldAskWait()
	var questions, requests, parks []heldAsk
	if err := s.DB.InSystem(ctx, "phase-sync", func(tx pgx.Tx) error {
		var err error
		if questions, err = collectHeld(ctx, tx, delivery.ReviewQuestionsDueSQL, wait); err != nil {
			return err
		}
		if requests, err = collectHeld(ctx, tx, delivery.RepositoryRequestsDueSQL, wait); err != nil {
			return err
		}
		parks, err = collectHeld(ctx, tx, delivery.IdleParksDueSQL, wait)
		return err
	}); err != nil {
		return err
	}
	s.handOverEach(ctx, questions, wait, delivery.HandOverReviewQuestionTx, "handing a reviewer's question to the person failed", "question")
	s.handOverEach(ctx, requests, wait, delivery.HandOverRepositoryRequestTx, "handing a repository request to the person failed", "request")
	// An idle park its conductor neither steered nor restarted: the person
	// is asked to look at it, as when no conductor decides.
	s.handOverEach(ctx, parks, wait, delivery.HandOverIdleParkTx, "handing an idle park to the person failed", "run")
	return nil
}

// handOverEach hands each item over in its own organization's
// transaction; a failure is logged (as key) and the rest go on.
func (s *Syncer) handOverEach(ctx context.Context, items []heldAsk, wait time.Duration,
	handOver func(context.Context, pgx.Tx, string, string, string, time.Duration) (bool, error), failed, key string) {
	for _, h := range items {
		if err := s.DB.InOrg(ctx, h.Org, func(tx pgx.Tx) error {
			_, err := handOver(ctx, tx, h.Org, h.TaskID, h.ID, wait)
			return err
		}); err != nil {
			s.Log.Warn(failed, key, h.ID, "error", err)
		}
	}
}

func collectHeld(ctx context.Context, tx pgx.Tx, sql string, wait time.Duration) ([]heldAsk, error) {
	rows, err := tx.Query(ctx, sql, wait.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[heldAsk])
}
