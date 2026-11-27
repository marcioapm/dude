package delivery

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A conductor's turn that ends with nothing that will wake it again (no Run
// at work, no wait, no open question, no dispatch followed, no pull request,
// no reason or message on its way, no decision taken that the delivery has
// not acted on) is woken once for it. The conductor's turns that end idle
// in a row are one stretch: the first is woken (WakeIdleTurn), the second
// is told in Chat (a chat.notice), and later ones say nothing. A turn that
// ends with something pending, or a question, wait or dispatch since the
// stretch's last idle end, ends the stretch. The stretch is kept on the
// turn ends' own events (agent.session.stopped's idleTurn and idleTurns),
// so it needs no state of its own.

// WakeIdleTurn is the conductor_wakes kind of a conductor turn that ended
// with nothing that will wake it.
const WakeIdleTurn = "idle_turn"

// idleTurnLineChars bounds an idle turn's line: it quotes the start of the
// conductor's last message (idleQuoteChars).
const (
	idleTurnLineChars = 600
	idleQuoteChars    = 300
)

var (
	idleTurnTmpl       = mustTemplate("wakes/idle-turn")
	idleTurnNoticeTmpl = mustTemplate("notices/idle-turn")
)

// UnsentDirective (SQL, over runs r): a message for the Run not yet sent
// to it and not held back: it starts another turn.
const UnsentDirective = `EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ` + HeldBack + `)`

// idleTurnPending (SQL, over tasks t and the conductor's Run r): something
// will wake the conductor later, or the turn did not end idle.
var idleTurnPending = `(` + HoldsTurn + `
	OR ` + waitBlockedElse + `
	OR ` + ConductorRetrying + `
	OR ` + UnsentDirective + `
	OR EXISTS (SELECT 1 FROM conductor_waits cw WHERE cw.task_id = t.id AND cw.ended_at IS NULL)
	OR EXISTS (SELECT 1 FROM runs o WHERE o.task_id = t.id AND o.id <> r.id
		AND o.status IN ('pending', 'scheduled', 'starting', 'running', 'paused'))
	OR EXISTS (SELECT 1 FROM workflow_dispatches wd WHERE wd.task_id = t.id
		AND (wd.state IN ('` + DispatchDispatching + `', '` + DispatchBound + `') OR wd.outcome IS DISTINCT FROM wd.woken_outcome AND wd.outcome IS NOT NULL))
	OR EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = t.id AND pr.state IN ('open', 'draft'))
	OR EXISTS (SELECT 1 FROM conductor_wakes cwk WHERE cwk.task_id = t.id AND cwk.delivered_at IS NULL
		AND cwk.key NOT LIKE '` + rideWakePrefix + `%')
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = t.id AND w.workflow_type = '` + WorkflowType + `'
		AND (w.status = 'running' OR w.state->'decision' ? 'taken')))`

// idleTurnResets (SQL, $1 the conductor's Run, $2 the cursor of its last
// idle turn end): it asked, waited or dispatched since.
const idleTurnResets = `EXISTS (SELECT 1 FROM events e WHERE e.run_id = $1 AND e.cursor > $2
	AND e.event_type IN ('` + EvQuestionAsked + `', '` + EvConductorWaiting + `', '` + EvWorkflowDispatched + `'))`

// personSaid (SQL, $1 the conductor's Run, $2 the cursor of its previous
// turn end, 0 for none): a person's words reached it since: the message it
// was started with, or a Chat message or steer of theirs it was delivered.
// Such a turn answers the person, who reads Chat and replies; it is not
// backstopped. Every chat.message not the agent's is a writer's
// (ChatEvent), whatever principal the API saw. A person's answer to its
// question (question.answered) is a choice, not a conversation, and does
// not count; nor does a conductor's steer (conductorRunId).
const personSaid = `EXISTS (SELECT 1 FROM events e WHERE e.run_id = $1 AND e.cursor > $2 AND (
	e.event_type = '` + EvChatMessage + `' AND e.actor_type <> 'agent' AND NOT e.payload ? 'directiveId'
	OR e.event_type = 'run.directive.delivered' AND EXISTS (SELECT 1 FROM events p WHERE p.run_id = $1
		AND p.event_type IN ('` + EvChatMessage + `', '` + EvRunSteered + `') AND p.actor_type <> 'agent'
		AND p.payload->>'directiveId' = e.payload->>'directiveId' AND NOT p.payload ? 'conductorRunId')))`

// IdleTurnTx is the end of a conductor's turn, in the transaction that
// marks it done: when the task is conducted, live and not ended, the turn
// did not answer a person (personSaid), and nothing will wake its conductor
// (idleTurnPending), the stretch's first idle end records an idle-turn
// wake, its second a notice in Chat. Returns what the turn end's event
// carries, nil when the turn did not end idle.
func IdleTurnTx(ctx context.Context, tx pgx.Tx, ref RunRef) (map[string]any, error) {
	d, err := ReadDelivery(ctx, tx, ref.TaskID)
	if err != nil || !d.Conducted() || Ended(d.TaskStatus) {
		return nil, err
	}
	if halted, err := HaltedTx(ctx, tx, ref.TaskID); err != nil || halted {
		return nil, err
	}
	// The last turn end of this Run: what came since is this turn's input,
	// and the stretch is its, if it ended idle and nothing since ended it.
	var prevCursor int64
	var key string
	var count int
	err = tx.QueryRow(ctx, `SELECT cursor, COALESCE(payload->>'idleTurn', ''), COALESCE((payload->>'idleTurns')::int, 0)
		FROM events WHERE run_id = $1 AND event_type = 'agent.session.stopped' AND payload->>'reason' = 'turn_complete'
		ORDER BY cursor DESC LIMIT 1`, ref.RunID).Scan(&prevCursor, &key, &count)
	if err != nil && !db.IsNotFound(err) {
		return nil, err
	}
	var said bool
	if err := tx.QueryRow(ctx, `SELECT `+personSaid, ref.RunID, prevCursor).Scan(&said); err != nil || said {
		return nil, err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT `+idleTurnPending+` FROM tasks t JOIN runs r ON r.id = $2 WHERE t.id = $1`,
		ref.TaskID, ref.RunID).Scan(&pending); err != nil || pending {
		return nil, err
	}
	if key != "" {
		var reset bool
		if err := tx.QueryRow(ctx, `SELECT `+idleTurnResets, ref.RunID, prevCursor).Scan(&reset); err != nil {
			return nil, err
		}
		if reset {
			key, count = "", 0
		}
	}
	var last string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT payload->>'text' FROM events WHERE run_id = $1 AND event_type = $2
		ORDER BY cursor DESC LIMIT 1), '')`, ref.RunID, evAgentMessage).Scan(&last); err != nil {
		return nil, err
	}
	if key == "" {
		// The stretch is keyed by its Run and where its first idle end is
		// in the Run's events: one wake per stretch, however often called.
		var at int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(cursor), 0) FROM events WHERE run_id = $1`, ref.RunID).Scan(&at); err != nil {
			return nil, err
		}
		key = fmt.Sprintf("idle_turn:%s:%d", ref.RunID, at)
	}
	count++
	woken, err := RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, WakeIdleTurn, key, idleTurnLine(last))
	if err != nil {
		return nil, err
	}
	if !woken && count == 2 {
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: ref.Org, ProjectID: ref.ProjectID,
			TaskID: ref.TaskID, RunID: ref.RunID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
			CorrelationID: ref.TaskID, Payload: map[string]any{"about": WakeIdleTurn, "text": IdleTurnNotice()}}); err != nil {
			return nil, err
		}
	}
	return map[string]any{"idleTurn": key, "idleTurns": count}, nil
}

// idleTurnLine is an idle turn's reason, quoting the start of the
// conductor's last message.
func idleTurnLine(last string) string {
	return render(idleTurnTmpl, struct{ Message string }{clip(oneLine(last), idleQuoteChars)})
}

// IdleTurnNotice is what dude says in Chat when the conductor's turn ended
// idle again after it was woken for it.
func IdleTurnNotice() string { return render(idleTurnNoticeTmpl, nil) }
