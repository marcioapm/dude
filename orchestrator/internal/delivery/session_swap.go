package delivery

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A change of the session's tier or harness never interrupts its agent: at
// the next member's message after it, an agent between turns that runs on
// something else is ended and a fresh one, briefed with the conversation,
// takes the message as its first input (ReplaceBrainstorm).

// EvSessionModelApplied is a fresh agent started on the session's new
// model. Payload: { from, to, tier: { name } | null, model, harness }.
const EvSessionModelApplied = "session.model.applied"

// QuestionReplaced is question.closed's by for a question left open when
// a fresh agent took over.
const QuestionReplaced = "replaced"

// betweenTurns (SQL, over runs r): a submitted session agent not in a
// turn: parked or paused, or running with its turn over (done, or waiting
// on a person).
const betweenTurns = `(r.lux_run_id IS NOT NULL AND (r.status = 'paused'
	OR (r.status IN ('scheduled', 'starting', 'running') AND (r.turn_done_at IS NOT NULL OR r.waiting_since IS NOT NULL))))`

// BrainstormSwap is what a fresh agent would run on, when the session's
// live agent runRunID runs on something else and is between turns; nil
// otherwise. The comparison is what the agent requests (model, effort,
// harness), not the tier's name: a renamed or equivalent tier changes
// nothing it runs on. A choice that resolves to no usable tier swaps
// nothing, since a fresh agent would only fail.
func BrainstormSwap(ctx context.Context, tx pgx.Tx, runID, sessionID string) (*SessionModelApplied, error) {
	var between bool
	var model, effort, harness string
	var orgModels json.RawMessage
	err := tx.QueryRow(ctx, `SELECT `+betweenTurns+`, COALESCE(r.model, ''), COALESCE(r.effort, ''), COALESCE(r.harness, ''),
			o.default_agent_models
		FROM runs r JOIN organizations o ON o.id = r.organization_id WHERE r.id = $1`, runID).
		Scan(&between, &model, &effort, &harness, &orgModels)
	if err != nil || !between || model == "" {
		return nil, err
	}
	chosen, err := LoadSessionModel(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	settings := chosen.Over(ResolveRole(RoleBrainstorm, orgModels))
	tier, noTier, err := TierFor(ctx, tx, RoleBrainstorm, settings)
	if err != nil || noTier != "" {
		return nil, err
	}
	// The scripted agent on OpenCode is recorded as "scripted".
	if harness == "scripted" {
		harness = HarnessOpenCode
	}
	if tier.Model == model && tier.Effort == effort && settings.HarnessName() == harness {
		return nil, nil
	}
	return &SessionModelApplied{Tier: tier.Name, Model: tier.Model, Harness: settings.HarnessName()}, nil
}

// SessionModelApplied is the model a fresh agent is started on.
type SessionModelApplied struct{ Tier, Model, Harness string }

// ReplaceBrainstorm ends the session's agent ref for the model it no
// longer matches and starts a fresh one briefed with the conversation and
// message (shown in Chat as shown, if any); what the old one never read is
// handed on after it. A question it left open is closed, said so in the
// ledger before the briefing is built, so the new agent reads that it went
// unanswered. Returns the new Run. The caller holds the session's lock and
// has settled any question the message answers.
func ReplaceBrainstorm(ctx context.Context, tx pgx.Tx, ref RunRef, to SessionModelApplied, w Writer, message, shown string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM questions WHERE run_id = $1 AND status = 'open' ORDER BY asked_at`, ref.RunID)
	if err != nil {
		return "", err
	}
	open, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	for _, q := range open {
		if _, err := ledger.Append(ctx, tx, ref.Event(EvQuestionClosed, ledger.ActorSystem,
			map[string]any{"questionId": q, "by": QuestionReplaced})); err != nil {
			return "", err
		}
	}
	// Aborted, not completed: the syncer then cancels its lux Run, idle or
	// parked (Syncer.end), and never keeps a talker. Its open questions are
	// cancelled by runs_cancel_open_questions.
	const why = "the session's model changed"
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
		control_reason = $2, ended_at = now(), keep = false WHERE id = $1 AND status IN ('scheduled', 'starting', 'running', 'paused')`,
		ref.RunID, why)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		if _, err := ledger.Append(ctx, tx, ref.Event("run.completed", ledger.ActorSystem,
			map[string]any{"status": "aborted", "reason": why})); err != nil {
			return "", err
		}
	}
	next, err := StartBrainstorm(ctx, tx, ref.Org, ref.SessionID, w, message, shown)
	if err != nil {
		return "", err
	}
	if _, err := SessionHandOver(ctx, tx, ref); err != nil {
		return "", err
	}
	var tier any
	if to.Tier != "" {
		tier = map[string]any{"name": to.Tier}
	}
	return next, SessionEvent(ctx, tx, RunRef{Org: ref.Org, SessionID: ref.SessionID, RunID: next}, EvSessionModelApplied,
		ledger.ActorSystem, "dude", map[string]any{"from": ref.RunID, "to": next, "tier": tier, "model": db.Nullable(to.Model),
			"harness": to.Harness})
}
