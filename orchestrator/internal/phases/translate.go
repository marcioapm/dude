package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Events the translator writes: the vocabulary the chat view renders.
const (
	evAgentMessage       = "agent.message"
	evAgentThought       = "agent.thought"
	evPromptDelivered    = "agent.prompt.delivered"
	evToolCalled         = "agent.tool.called"
	evToolCompleted      = "agent.tool.completed"
	evPlanUpdated        = "agent.plan.updated"
	evModelRequestDone   = "agent.model.request.completed"
	evSessionStarted     = "agent.session.started"
	evSessionStopped     = "agent.session.stopped"
	evRunStarted         = "run.started"
	evRuntimeStopped     = "runtime.stopped"
	evDirectiveDelivered = "run.directive.delivered"
	evDirectiveAccepted  = "run.directive.accepted"
	evDirectiveFailed    = "run.directive.failed"
	// inputConsumed: directiveReceipt's name for a lux.input.consumed.
	inputConsumed = "consumed"
)

// translator turns one lux Run's output into dude's ledger, and into the
// Run's own state: whether its turn is done, what its checkout started from,
// what its push did.
//
// It sees frames in order, in batches, each batch inside the transaction
// that advances the cursor past it. What it keeps in memory between frames
// (the reply being streamed, the tool calls seen) is either saved with the
// cursor or rebuilt from the database when following starts, so a restart
// neither repeats nor loses anything.
type translator struct {
	run phaseRun
	// The placement whose agent session is established. A resumed agent
	// replays its conversation while it loads, and that replay is already in
	// the ledger.
	sessionEpoch int
	// Reply text streamed since the last complete message. Chunks are
	// fragments of words; the ledger records messages.
	message strings.Builder
	// Thinking streamed since the last complete thought, kept the same way.
	thought strings.Builder
	// Tool calls already recorded, by id, with the name they were recorded
	// under: a command that streams many progress updates is one call in the
	// ledger, and its completion — which carries no title — keeps the name.
	// A plan's calls are here too, as planCall, so their completions are
	// known for what they are.
	seenCalls map[string]string
	// The task is recorded once per Run, whatever lux acknowledges again.
	promptSeen bool
	// What the agent reported using, written with the cursor.
	usage runUsage
	// The agent did something in this batch: it is not idle.
	active bool
	// Tool calls started and not yet finished, by id, written with the
	// cursor: an agent in a long command is working, however quiet.
	openCalls map[string]bool
	// The epochs whose first busy and first output were looked for and
	// settled, so each is looked for once per placement and not on every
	// chunk; and those looked for in this batch and not settled yet, looked
	// for again in the next (resumes.go).
	busyEpoch, outputEpoch int
	unsettled              map[unsettledKey]bool
	// Resumes to follow up once the batch commits, by epoch: true to read
	// lux's placements first (resumes.go).
	resumes map[int]bool
}

// runUsage is the Run's usage as the agent reported it. Context is the
// conversation's latest size — stamped on each message, so the chat shows
// how full it was then — and cost the running total; the token counts are
// sums over its turns.
type runUsage struct {
	cost                                 float64
	context                              int64
	input, output, cacheRead, cacheWrite int64
}

// planCall marks a plan's calls in seenCalls: recorded as plans, not tools.
const planCall = "(plan)"

// promptRequestID is the id lux acknowledges the task itself under, as
// opposed to a person's steering.
const promptRequestID = "prompt"

// load restores what the translator keeps between batches.
func (t *translator) load(ctx context.Context, tx pgx.Tx) error {
	var message, thought string
	var open []string
	u := &t.usage
	if err := tx.QueryRow(ctx, `SELECT agent_session_epoch, agent_message_buffer, agent_thought_buffer, open_tool_calls,
		agent_cost_usd::float8, context_tokens, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		FROM runs WHERE id = $1`, t.run.ID).
		Scan(&t.sessionEpoch, &message, &thought, &open, &u.cost, &u.context, &u.input, &u.output, &u.cacheRead, &u.cacheWrite); err != nil {
		return err
	}
	t.message.WriteString(message)
	t.thought.WriteString(thought)
	t.openCalls = make(map[string]bool, len(open))
	for _, id := range open {
		t.openCalls[id] = true
	}
	rows, err := tx.Query(ctx, `SELECT event_type, COALESCE(payload->>'callId', ''), COALESCE(payload->>'tool', '')
		FROM events WHERE run_id = $1 AND event_type IN ($2, $3, $4)`, t.run.ID, evToolCalled, evPlanUpdated, evPromptDelivered)
	if err != nil {
		return err
	}
	seen, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Type, ID, Tool string }])
	t.seenCalls = make(map[string]string, len(seen))
	for _, e := range seen {
		switch e.Type {
		case evToolCalled:
			t.seenCalls[e.ID] = e.Tool
		case evPlanUpdated:
			t.seenCalls[e.ID] = planCall
		case evPromptDelivered:
			t.promptSeen = true
		}
	}
	return err
}

// save records what the translator holds — the reply and thought in
// progress, the usage so far, the tool calls still running, whether the
// agent did anything — with the cursor past the frames that produced it,
// in one update in the batch's transaction. cursor "" keeps the stored one.
func (t *translator) save(ctx context.Context, tx pgx.Tx, cursor string, afterEvent int64) error {
	u := t.usage
	open := slices.Sorted(maps.Keys(t.openCalls))
	_, err := tx.Exec(ctx, `UPDATE runs SET agent_message_buffer = $2, agent_thought_buffer = $3,
		agent_cost_usd = $4, context_tokens = $5, input_tokens = $6, output_tokens = $7,
		cache_read_tokens = $8, cache_write_tokens = $9, open_tool_calls = $10,
		agent_active_at = CASE WHEN $11 THEN now() ELSE agent_active_at END,
		idle_nudged_at = CASE WHEN $11 THEN NULL ELSE idle_nudged_at END,
		lux_cursor = COALESCE(NULLIF($12, ''), lux_cursor), lux_after_event = GREATEST(lux_after_event, $13)
		WHERE id = $1`,
		t.run.ID, t.message.String(), t.thought.String(), u.cost, u.context, u.input, u.output, u.cacheRead, u.cacheWrite,
		db.NonNil(open), t.active, cursor, afterEvent)
	t.active = false
	t.unsettled = nil
	return err
}

func (t *translator) apply(ctx context.Context, tx pgx.Tx, s *Syncer, f lux.Frame) error {
	switch f.Kind {
	case "lux":
		return t.luxEvent(ctx, tx, s, f)
	case "record":
		if f.Event != nil {
			return t.agentEvent(ctx, tx, s, f)
		}
		// Plain stdout is the reply text, which the structured events already
		// carry; stderr is diagnostics, not conversation.
		return nil
	case "gap":
		return s.event(ctx, tx, t.run, evRuntimeStopped, ledger.ActorSystem,
			map[string]any{"status": "lost", "reason": f.Reason})
	}
	return nil
}

// luxEvent handles lux's own lifecycle events.
func (t *translator) luxEvent(ctx context.Context, tx pgx.Tx, s *Syncer, f lux.Frame) error {
	var d map[string]any
	_ = json.Unmarshal(f.EventData, &d)
	str := func(k string) string { v, _ := d[k].(string); return v }

	switch f.EventType {
	case "state":
		// A Run lux is moving to another host stops on the way, and is
		// resumed by lux itself: recorded as resuming, not over.
		state := lux.Recorded(str("state"), str("reason"))
		if _, err := tx.Exec(ctx, `UPDATE runs SET lux_state = $2,
			started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
			status = CASE WHEN $2 = 'running' AND status IN ('scheduled', 'starting') THEN 'running'::run_status ELSE status END
			WHERE id = $1`, t.run.ID, state); err != nil {
			return err
		}
		if state == "running" {
			t.resumeRunning(ctx, tx, s, f.Epoch)
		}
		if state == "running" && t.run.Status == statusScheduled {
			t.run.Status = statusRunning
			return s.event(ctx, tx, t.run, evRunStarted, ledger.ActorSystem, map[string]any{"status": statusRunning})
		}
		if lux.Terminal(state) {
			return t.ended(ctx, tx, s, state, str("reason"))
		}
	case "git.clone":
		if err := s.event(ctx, tx, t.run, "git.clone", ledger.ActorSystem, d); err != nil {
			return err
		}
		// A repository added at a resume (only those carry a request id):
		// the approval for it, on this Run, is settled by name — cloned, or
		// failed, when lux has dropped it and the agent goes on without it.
		if str("requestId") == "" {
			return nil
		}
		if err := t.settleClone(ctx, tx, s, str("repo"), str("status"), str("error")); err != nil {
			return err
		}
		// The turn it held, if it ended waiting on this one, is done now.
		return t.turnDone(ctx, tx, s, true)
	case "git.checkout":
		// What each checkout started from, the first time it was made; a
		// resume leaves the checkout as the agent left it.
		if _, err := tx.Exec(ctx, `UPDATE runs SET base_shas = jsonb_build_object($2::text, $3::text) || base_shas
			WHERE id = $1`, t.run.ID, str("repo"), str("base")); err != nil {
			return err
		}
		return s.event(ctx, tx, t.run, "git.checkout", ledger.ActorSystem, d)
	case "git.push":
		// Only the push this finish asked for — recorded or not yet (lux can
		// answer before dude has written that it asked): one an earlier turn
		// asked for, before the Run was aborted and taken back up, is not
		// this work.
		if str("requestId") != cmp.Or(t.run.PushRequestID, pushRequest(t.run)) {
			return nil
		}
		raw, _ := json.Marshal(d)
		_, err := tx.Exec(ctx, `UPDATE runs SET push_result = $2::jsonb WHERE id = $1`, t.run.ID, raw)
		return err
	}
	if strings.HasPrefix(f.EventType, "server.") {
		// A server of the agent's Run changed (a person started it, it became
		// ready, the Run moved and it stopped): the browser reads it again.
		r := t.run
		return ServerEvent(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, f.EventType, f.EventData)
	}
	return nil
}

// ended: the container stopped without dude asking — the agent crashed,
// timed out, or its host died. A phase whose turn was already done is
// finished by the sweep, and one dude stopped is dude's business; any other
// has failed.
func (t *translator) ended(ctx context.Context, tx pgx.Tx, s *Syncer, state, reason string) error {
	if reason == "" {
		reason = state
	}
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), keep = true
		WHERE id = $1 AND status NOT IN ('completed', 'failed', 'aborted', 'paused')
		  AND lux_stop_reason IS NULL AND turn_done_at IS NULL`,
		t.run.ID, "the agent's run ended before finishing its task: "+reason)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.event(ctx, tx, t.run, "run.failed", ledger.ActorSystem, map[string]any{"status": "failed", "error": reason})
}

// shimEvent handles what lux's shim reports about the agent. These come in
// the record stream, in order with the agent's own messages — unlike lux's
// lifecycle events, which trail them. The order matters: "idle" must be
// seen after the reply it ends, or the reply is read before it is recorded.
func (t *translator) shimEvent(ctx context.Context, tx pgx.Tx, s *Syncer, typ string, data map[string]any, epoch int) error {
	str := func(k string) string { v, _ := data[k].(string); return v }
	switch typ {
	case "lux.session":
		return t.session(ctx, tx, s, str("sessionId"), epoch)
	case "lux.activity":
		return t.activity(ctx, tx, s, str("activity"), epoch)
	case lux.RecordInputConsumed, lux.RecordInputFailed:
		if str("requestId") == promptRequestID {
			return nil
		}
		return t.directiveReceipt(ctx, tx, s, typ, data)
	case lux.RecordInput:
		// The task itself: recorded when the agent has it, as lux delivered
		// it — from its first answer.
		if str("requestId") == promptRequestID {
			// Only a first answer that has the task: accepted, or an older
			// lux's phase-less handoff. Failed and unknown phases are not.
			if phase := str("phase"); str("error") != "" || (phase != "" && phase != lux.InputAccepted) {
				return nil
			}
			// Once per Run: an agent resumed on another host is not given its
			// task again, but a lux that acknowledged it again would repeat it.
			if t.promptSeen {
				return nil
			}
			t.promptSeen = true
			// lux caps the text it relays; a prompt it cut says so.
			payload := map[string]any{"text": str("text")}
			if truncated, _ := data["truncated"].(bool); truncated {
				payload["truncated"] = true
			}
			// Where this harness lands input: what the composer promises a steer.
			if lands := landsOf(data); lands != "" {
				payload["lands"] = lands
			}
			// The images it was given with the task, for the prompt turn.
			images, err := delivery.PromptAttachmentInfo(ctx, tx, t.run.ID)
			if err != nil {
				return err
			}
			if len(images) > 0 {
				payload["attachments"] = images
			}
			return s.event(ctx, tx, t.run, evPromptDelivered, ledger.ActorSystem, payload)
		}
		return t.directiveReceipt(ctx, tx, s, typ, data)
	}
	return nil
}

// interruptAloneOf (SQL): d is an undelivered "Interrupt now" sent as the
// interrupt alone, resending the instruction whose words f ($1, of Run $2)
// carries: it settles with f.
const interruptAloneOf = `f.id = $1 AND f.run_id = $2 AND f.interrupt_only IS FALSE
	AND d.run_id = f.run_id AND d.resends = COALESCE(f.resends, f.id) AND d.interrupt_only AND d.delivered_at IS NULL`

// directiveReceipt records what lux says became of a person's steer, typ
// being the record (lux.RecordInput and those following it):
//
//   - lux.input phase "accepted": the harness took it; lands says when the
//     agent reads it (next_step, or next_turn for a harness that reads only
//     between turns). With receipt false no lux.input.consumed follows, so
//     it is delivered now.
//   - lux.input.consumed: the agent's next model step has it in context.
//   - lux.input with no phase and no error: a lux from before the phases,
//     which acknowledges once, on handoff. Delivered then, as it always was.
//   - lux.input phase "failed" (never accepted), lux.input.failed (after
//     it was), or an older lux's lux.input with an error: it will not reach
//     the agent.
//
// Each transition happens once per directive and Run however often lux
// repeats a receipt (a reconnect, a resumed shim): the update is guarded on
// the state it moves from, and the event is written only when a row moved.
//
// Delivered is final: a failure after it changes nothing. A failure is
// final against "accepted" (with or without receipt), which only says the
// harness took it. A delivery (consumed, or an older lux's handoff) after a
// failure wins: it is the agent's own report that it has the words, so the
// failure and its error are cleared with it and run.directive.delivered is
// written.
func (t *translator) directiveReceipt(ctx context.Context, tx pgx.Tx, s *Syncer, typ string, data map[string]any) error {
	id, _ := data["requestId"].(string)
	msg, _ := data["error"].(string)
	var phase string
	switch typ {
	case lux.RecordInputConsumed:
		phase = inputConsumed
	case lux.RecordInputFailed:
		phase = lux.InputFailed
	default:
		phase, _ = data["phase"].(string)
		if phase != "" && phase != lux.InputAccepted && phase != lux.InputFailed {
			return nil // lux.input carries no other phase
		}
		if phase == "" && msg != "" {
			phase = lux.InputFailed
		}
	}
	if phase == lux.InputFailed {
		if msg == "" {
			msg = "lux could not deliver it"
		}
		return failDirectiveTx(ctx, tx, s, t.run, id, msg)
	}
	receipt, _ := data["receipt"].(bool)
	if phase == lux.InputAccepted {
		lands := landsOf(data)
		tag, err := tx.Exec(ctx, `UPDATE directives SET accepted_at = now(), lands = NULLIF($3, '')
			WHERE id = $1 AND run_id = $2 AND accepted_at IS NULL AND delivered_at IS NULL AND failed_at IS NULL`, id, t.run.ID, lands)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			payload := map[string]any{"directiveId": id, "receipt": receipt}
			if lands != "" {
				payload["lands"] = lands
			}
			if err := s.event(ctx, tx, t.run, evDirectiveAccepted, ledger.ActorSystem, payload); err != nil {
				return err
			}
		}
		if receipt {
			return nil
		}
	}
	// Accepted with no receipt to follow is delivered now, unless it failed
	// first; a consumed or handoff receipt delivers even a failed one.
	overridesFailure := phase != lux.InputAccepted
	tag, err := tx.Exec(ctx, `UPDATE directives SET delivered_at = now(), accepted_at = COALESCE(accepted_at, now()),
			failed_at = NULL, error = NULL
		WHERE id = $1 AND run_id = $2 AND delivered_at IS NULL AND (failed_at IS NULL OR $3)`, id, t.run.ID, overridesFailure)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	payload := map[string]any{"directiveId": id}
	// Only a consumed receipt says when the agent read it; the other two
	// say only that it was handed over.
	if phase == inputConsumed {
		payload["read"] = true
	}
	if err := s.event(ctx, tx, t.run, evDirectiveDelivered, ledger.ActorSystem, payload); err != nil {
		return err
	}
	// A wake note read after its failure was counted: its reasons, back to
	// pending, are heard and not told again.
	if err := delivery.WakesHeardTx(ctx, tx, id); err != nil {
		return err
	}
	// An "Interrupt now" sent as the interrupt alone (interrupt_only, see
	// deliverDirectives) has no receipt of its own: it is delivered with
	// the directive carrying its words, on the same precedence, with an
	// event flagged interruptOnly: the words were read once, here.
	return settleInterrupts(ctx, tx, s, t.run, nil, `UPDATE directives d
			SET delivered_at = now(), accepted_at = COALESCE(d.accepted_at, now()), failed_at = NULL, error = NULL
		FROM directives f WHERE `+interruptAloneOf+` AND (d.failed_at IS NULL OR $3)
		RETURNING d.id`, id, t.run.ID, overridesFailure)
}

// landsOf is where an accepted answer says the harness lands input,
// next_step or next_turn; "" for anything else.
func landsOf(data map[string]any) string {
	if lands, _ := data["lands"].(string); lands == "next_step" || lands == "next_turn" {
		return lands
	}
	return ""
}

// session records the placement the agent's session is established in. The
// first establishes the session; later ones are resumes, which continue it.
func (t *translator) session(ctx context.Context, tx pgx.Tx, s *Syncer, id string, epoch int) error {
	if id == "" || epoch <= t.sessionEpoch {
		return nil
	}
	first := t.sessionEpoch == 0
	t.sessionEpoch = epoch
	if _, err := tx.Exec(ctx, `UPDATE runs SET agent_session_epoch = $2 WHERE id = $1`, t.run.ID, epoch); err != nil {
		return err
	}
	if !first {
		// The resumed agent has its session back: lux reports it running.
		// Its state event says so too, but trails the agent's records on
		// the stream, the first busy included.
		t.resumeRunning(ctx, tx, s, epoch)
		return nil
	}
	role := t.run.Phase
	if t.run.conductor() {
		role = delivery.RoleConductor
	}
	return s.event(ctx, tx, t.run, evSessionStarted, ledger.ActorAgent,
		map[string]any{"role": role, "externalSessionId": id})
}

// activity tracks the agent's turn. Busy means it took its task; idle after
// busy means it finished. Idle before busy is the agent waiting for its
// first input, and means nothing.
func (t *translator) activity(ctx context.Context, tx pgx.Tx, s *Syncer, activity string, epoch int) error {
	switch activity {
	case "busy":
		// Working again: no longer waiting, and whatever it was waiting on
		// has been given to it. A new turn has nothing running yet, and its
		// quiet is counted from its start — but only the model doing
		// something answers a nudge, not a turn the nudge itself started.
		clear(t.openCalls)
		t.resumeBusy(ctx, tx, s, epoch)
		_, err := tx.Exec(ctx, `UPDATE runs SET agent_busy_at = COALESCE(agent_busy_at, now()), turn_done_at = NULL,
			waiting_since = NULL, agent_active_at = now() WHERE id = $1`, t.run.ID)
		return err
	case "idle":
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
		clear(t.openCalls)
		// A turn's end is when an agent's edits settle.
		s.pokeDiff(t.run.ID)
		// A turn that ended with something open for a person — a question, a
		// repository it asked for — is not done: the agent waits, and the
		// answer starts its next turn. The syncer parks it if the wait is
		// long.
		tag, err := tx.Exec(ctx, `UPDATE runs r SET waiting_since = COALESCE(r.waiting_since, now())
			WHERE r.id = $1 AND r.agent_busy_at IS NOT NULL AND `+delivery.HoldsTurn, t.run.ID)
		if err != nil || tag.RowsAffected() > 0 {
			return err
		}
		return t.turnDone(ctx, tx, s, false)
	}
	return nil
}

// turnDone marks the agent's turn done, once. settling: only a turn that
// ended waiting on a repository that has now arrived (or failed to), with
// nothing else holding it — lux reports the clone after the agent's own
// records, so the resumed turn can end before it.
func (t *translator) turnDone(ctx context.Context, tx pgx.Tx, s *Syncer, settling bool) error {
	tag, err := tx.Exec(ctx, `UPDATE runs r SET turn_done_at = now(), waiting_since = NULL
		WHERE r.id = $1 AND r.status IN ('scheduled', 'starting', 'running')
		  AND r.agent_busy_at IS NOT NULL AND r.turn_done_at IS NULL
		  AND (NOT $2 OR (r.waiting_since IS NOT NULL AND NOT `+delivery.HoldsTurn+`))`, t.run.ID, settling)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.event(ctx, tx, t.run, evSessionStopped, ledger.ActorAgent, map[string]any{"reason": "turn_complete"})
}

// settleClone records what became of a repository added at a resume: the
// approval for it, on this Run, is settled by name — cloned, or failed,
// when lux has dropped it and the agent goes on without it.
func (t *translator) settleClone(ctx context.Context, tx pgx.Tx, s *Syncer, repo, status, cloneErr string) error {
	if status != "failed" {
		_, err := tx.Exec(ctx, `WITH done AS (
				UPDATE repository_requests q SET status = 'cloned' FROM repositories repo
				WHERE repo.id = q.repository_id AND q.run_id = $1 AND repo.name = $2 AND q.status = 'approved')
			UPDATE runs SET lux_repositories = array_append(lux_repositories, $2),
				lux_pushes = CASE WHEN EXISTS (SELECT 1 FROM task_repositories wr JOIN repositories repo ON repo.id = wr.repository_id
					WHERE wr.task_id = runs.task_id AND repo.name = $2 AND wr.access = 'write')
					THEN array_append(lux_pushes, $2) ELSE lux_pushes END
			WHERE id = $1 AND NOT ($2 = ANY (lux_repositories))`, t.run.ID, repo)
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE repository_requests q SET status = 'failed', error = $3
		FROM repositories repo WHERE repo.id = q.repository_id AND q.run_id = $1 AND repo.name = $2
		  AND q.status = 'approved'`, t.run.ID, repo, cloneErr); err != nil {
		return err
	}
	// Not checked out after all: the task stops naming it.
	if _, err := tx.Exec(ctx, `DELETE FROM task_repositories wr USING repository_requests q, repositories repo
		WHERE q.run_id = $1 AND q.status = 'failed' AND repo.id = q.repository_id AND repo.name = $2
		  AND wr.task_id = q.task_id AND wr.repository_id = q.repository_id`, t.run.ID, repo); err != nil {
		return err
	}
	return s.event(ctx, tx, t.run, "repository.clone_failed", ledger.ActorSystem,
		map[string]any{"repository": repo, "error": cloneErr})
}

// agentEvent translates the agent's own protocol messages. Only ACP is
// spoken (the adapter dude configures for OpenCode and the scripted agent);
// an unknown event type is ignored rather than guessed at.
func (t *translator) agentEvent(ctx context.Context, tx pgx.Tx, s *Syncer, f lux.Frame) error {
	if strings.HasPrefix(f.Event.Type, "lux.") {
		var d map[string]any
		_ = json.Unmarshal(f.Event.Data, &d)
		return t.shimEvent(ctx, tx, s, f.Event.Type, d, f.Epoch)
	}
	typ, ok := strings.CutPrefix(f.Event.Type, "acp.")
	if !ok {
		return nil
	}
	// While a resumed agent loads its session it replays the conversation;
	// that replay is already recorded.
	if f.Epoch > t.sessionEpoch && t.sessionEpoch != 0 {
		return nil
	}
	var u map[string]any
	_ = json.Unmarshal(f.Event.Data, &u)
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	// What the model says, thinks or does is activity; a usage report or a
	// turn ending (a nudge cancels one) is not.
	t.active = t.active || slices.Contains([]string{"agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan"}, typ)
	// The first thing it says or does after a resume ends the resume's
	// timing.
	if slices.Contains([]string{"agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update"}, typ) {
		t.resumeOutput(ctx, tx, s, f.Epoch)
	}

	switch typ {
	case "agent_message_chunk":
		// A reply ends a thought.
		if err := t.flushText(ctx, tx, s, &t.thought, evAgentThought); err != nil {
			return err
		}
		content, _ := u["content"].(map[string]any)
		if str(content, "type") == "text" {
			t.message.WriteString(str(content, "text"))
		}
		return nil

	case "agent_thought_chunk":
		if err := t.flushText(ctx, tx, s, &t.message, evAgentMessage); err != nil {
			return err
		}
		content, _ := u["content"].(map[string]any)
		if str(content, "type") == "text" {
			t.thought.WriteString(str(content, "text"))
		}
		return nil

	case "tool_call", "tool_call_update":
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
		status, title, callID := str(u, "status"), str(u, "title"), str(u, "toolCallId")
		input := u["rawInput"]
		// A plan is a milestone, not a tool call: the agent rewrites the whole
		// list each time, and naming the tool is this layer's job.
		if isPlanTool(title) || t.seenCalls[callID] == planCall {
			if todos := planTodos(input); todos != nil && status == "in_progress" {
				t.seenCalls[callID] = planCall
				return s.event(ctx, tx, t.run, evPlanUpdated, ledger.ActorAgent, map[string]any{"todos": todos, "callId": callID})
			}
			return nil
		}
		switch status {
		case "pending":
			// Announced before its input is known; in_progress follows.
			t.openCalls[callID] = true
		case "in_progress":
			t.openCalls[callID] = true
			if _, seen := t.seenCalls[callID]; seen {
				return nil
			}
			name := toolName(u)
			t.seenCalls[callID] = name
			return s.event(ctx, tx, t.run, evToolCalled, ledger.ActorAgent,
				map[string]any{"tool": name, "callId": callID, "input": input, "title": title})
		case "completed", "failed":
			delete(t.openCalls, callID)
			st := "completed"
			if status == "failed" {
				st = "error"
			}
			// A tool some agents report only once it has finished has no
			// recorded start; its completion is the whole call.
			name, seen := t.seenCalls[callID]
			if !seen {
				name = toolName(u)
			}
			payload := map[string]any{"tool": name, "callId": callID, "status": st, "title": title}
			maps.Copy(payload, toolResult(u))
			// It changed files: the live diff is read shortly.
			if isEdit(name) {
				s.pokeDiff(t.run.ID)
			}
			return s.event(ctx, tx, t.run, evToolCompleted, ledger.ActorAgent, payload)
		}

	case "usage_update":
		return t.usageUpdate(ctx, tx, s, u)

	case "turn_end":
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
		if err := t.turnUsage(ctx, tx, s, u); err != nil {
			return err
		}
		// lux sets error when the agent answered the turn's session/prompt
		// with an error rather than a stop reason: the turn never happened,
		// or broke off. A turn dude cancelled (a nudge, an interrupt) ends
		// with stopReason "cancelled" and no error, and is not a failure.
		if e := str(u, "error"); e != "" {
			return t.turnFailed(ctx, tx, s, e)
		}
	}
	return nil
}

// The failure and stream cursor commit together, before a following idle or
// queued input can turn a failed prompt into completed work.
func (t *translator) turnFailed(ctx context.Context, tx pgx.Tx, s *Syncer, agentErr string) error {
	var model, tier string
	var produced bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(model, ''), COALESCE(model_tier, ''), EXISTS (SELECT 1 FROM events
		WHERE run_id = $1 AND event_type IN ($2, $3, $4, $5)) FROM runs WHERE id = $1`,
		t.run.ID, evAgentMessage, evAgentThought, evToolCalled, evPlanUpdated).Scan(&model, &tier, &produced); err != nil {
		return err
	}
	reason := turnFailure(agentErr, tier, model, produced)
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), turn_done_at = NULL, keep = true
		WHERE id = $1 AND status IN ('scheduled', 'starting', 'running') AND lux_stop_reason IS NULL`, t.run.ID, reason)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.event(ctx, tx, t.run, "run.failed", ledger.ActorSystem, map[string]any{"status": "failed", "error": reason})
}

// turnFailure says why a turn failed, for a person. OpenCode answers a
// model the proxy will not serve with a generic API error (-32603, "Cannot
// connect to API"), which reads as a network fault; when the agent did
// nothing at all, the model its tier requested is the likelier cause, and
// both are named.
func turnFailure(agentErr, tier, model string, produced bool) string {
	if !produced && model != "" && (strings.Contains(agentErr, "-32603") || strings.Contains(agentErr, "APIError")) {
		on := fmt.Sprintf("model %q", model)
		if tier != "" {
			on = fmt.Sprintf("%s, which requested %q from the LLM proxy", tier, model)
		}
		return fmt.Sprintf("the agent could not start its turn on %s (check the tier's model in Models, "+
			"or send it a test message there): %s", on, agentErr)
	}
	return "the agent's turn failed: " + agentErr
}

// flush records whatever reply or thought was streaming: something else
// happening ends both.
func (t *translator) flush(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	if err := t.flushText(ctx, tx, s, &t.thought, evAgentThought); err != nil {
		return err
	}
	return t.flushText(ctx, tx, s, &t.message, evAgentMessage)
}

// flushText records the text streamed into buf since it was last recorded,
// as one event, with the context size at that point.
func (t *translator) flushText(ctx context.Context, tx pgx.Tx, s *Syncer, buf *strings.Builder, event string) error {
	text := buf.String()
	buf.Reset()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	payload := map[string]any{"text": text}
	if t.usage.context > 0 {
		payload["contextTokens"] = t.usage.context
	}
	return s.event(ctx, tx, t.run, event, ledger.ActorAgent, payload)
}

// usageUpdate: ACP's running report of the conversation's size, and of cost
// when the agent knows prices. Recorded whenever either moves — a cost of
// zero means unknown, not free, and must not hide the tokens.
func (t *translator) usageUpdate(ctx context.Context, tx pgx.Tx, s *Syncer, u map[string]any) error {
	used := int64(number(u["used"]))
	cost, _ := u["cost"].(map[string]any)
	amount := number(cost["amount"])
	delta := amount - t.usage.cost
	if (used == 0 || used == t.usage.context) && delta <= 0 {
		return nil
	}
	if used > 0 {
		t.usage.context = used
	}
	payload := map[string]any{"contextTokens": t.usage.context, "contextWindow": int64(number(u["size"]))}
	if delta > 0 {
		t.usage.cost = amount
		payload["costUsd"] = delta
	}
	return s.event(ctx, tx, t.run, evModelRequestDone, ledger.ActorAgent, payload)
}

// turnUsage: the tokens a whole turn used, which ACP reports only with the
// turn's end. Summed onto the Run; the event carries the turn's own numbers.
func (t *translator) turnUsage(ctx context.Context, tx pgx.Tx, s *Syncer, u map[string]any) error {
	usage, _ := u["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	in, out := int64(number(usage["inputTokens"])), int64(number(usage["outputTokens"]))
	cr, cw := int64(number(usage["cachedReadTokens"])), int64(number(usage["cachedWriteTokens"]))
	t.usage.input += in
	t.usage.output += out
	t.usage.cacheRead += cr
	t.usage.cacheWrite += cw
	return s.event(ctx, tx, t.run, evModelRequestDone, ledger.ActorAgent, map[string]any{
		"turn": true, "contextTokens": t.usage.context,
		"tokens": map[string]any{"input": in, "output": out, "cacheRead": cr, "cacheWrite": cw}})
}

func number(v any) float64 {
	f, _ := v.(float64)
	return f
}

// toolName: ACP carries a kind (read, edit, execute…) and a title that for
// OpenCode starts as the tool's own name. The title is what a person
// recognises, so it is the name when it is a single word.
func toolName(u map[string]any) string {
	title, _ := u["title"].(string)
	if title != "" && !strings.ContainsAny(title, " /.") {
		return title
	}
	if kind, _ := u["kind"].(string); kind != "" && kind != "other" {
		return kind
	}
	return "tool"
}

func isPlanTool(name string) bool {
	switch strings.ToLower(strings.ReplaceAll(name, "_", "")) {
	case "todowrite", "updateplan", "todo":
		return true
	}
	return false
}

func planTodos(input any) []any {
	m, _ := input.(map[string]any)
	todos, _ := m["todos"].([]any)
	return todos
}
