package phases

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Events the translator writes: the vocabulary the chat view renders.
const (
	evAgentMessage       = "agent.message"
	evToolCalled         = "agent.tool.called"
	evToolCompleted      = "agent.tool.completed"
	evPlanUpdated        = "agent.plan.updated"
	evModelRequestDone   = "agent.model.request.completed"
	evSessionStarted     = "agent.session.started"
	evSessionStopped     = "agent.session.stopped"
	evRunStarted         = "run.started"
	evRuntimeStopped     = "runtime.stopped"
	evDirectiveDelivered = "run.directive.delivered"
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
	// The running total the agent last reported.
	cost float64
	// Reply text streamed since the last complete message. Chunks are
	// fragments of words; the ledger records messages.
	message strings.Builder
	// Tool calls already recorded, by id, with the name they were recorded
	// under: a command that streams many progress updates is one call in the
	// ledger, and its completion — which carries no title — keeps the name.
	seenCalls map[string]string
}

// load restores what the translator keeps between batches.
func (t *translator) load(ctx context.Context, tx pgx.Tx) error {
	var message string
	if err := tx.QueryRow(ctx, `SELECT agent_session_epoch, agent_cost_usd::float8, agent_message_buffer
		FROM runs WHERE id = $1`, t.run.ID).Scan(&t.sessionEpoch, &t.cost, &message); err != nil {
		return err
	}
	t.message.WriteString(message)
	rows, err := tx.Query(ctx, `SELECT payload->>'callId', payload->>'tool' FROM events WHERE run_id = $1 AND event_type = $2`,
		t.run.ID, evToolCalled)
	if err != nil {
		return err
	}
	calls, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Tool string }])
	t.seenCalls = make(map[string]string, len(calls))
	for _, c := range calls {
		t.seenCalls[c.ID] = c.Tool
	}
	return err
}

// save records the reply in progress, in the batch's transaction, so it
// commits with the cursor that has moved past its chunks.
func (t *translator) save(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET agent_message_buffer = $2 WHERE id = $1`, t.run.ID, t.message.String())
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
		state := str("state")
		if _, err := tx.Exec(ctx, `UPDATE runs SET lux_state = $2,
			started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
			status = CASE WHEN $2 = 'running' AND status IN ('scheduled', 'starting') THEN 'running'::run_status ELSE status END
			WHERE id = $1`, t.run.ID, state); err != nil {
			return err
		}
		if state == "running" && t.run.Status == statusScheduled {
			t.run.Status = statusRunning
			return s.event(ctx, tx, t.run, evRunStarted, ledger.ActorSystem, map[string]any{"status": statusRunning})
		}
		if lux.Terminal(state) {
			return t.ended(ctx, tx, s, state, str("reason"))
		}
	case "git.checkout":
		_, err := tx.Exec(ctx, `UPDATE runs SET base_sha = COALESCE(base_sha, $2) WHERE id = $1`, t.run.ID, str("base"))
		return err
	case "git.push":
		if str("requestId") != t.run.PushRequestID && t.run.PushRequestID != "" {
			return nil
		}
		raw, _ := json.Marshal(d)
		_, err := tx.Exec(ctx, `UPDATE runs SET push_result = $2::jsonb WHERE id = $1`, t.run.ID, raw)
		return err
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
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now()
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
		return t.activity(ctx, tx, s, str("activity"))
	case "lux.input":
		if str("error") != "" {
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE directives SET delivered_at = COALESCE(delivered_at, now()) WHERE id = $1`, str("requestId"))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, t.run, evDirectiveDelivered, ledger.ActorSystem, map[string]any{"directiveId": str("requestId")})
	}
	return nil
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
		return nil
	}
	return s.event(ctx, tx, t.run, evSessionStarted, ledger.ActorAgent,
		map[string]any{"role": t.run.Phase, "externalSessionId": id})
}

// activity tracks the agent's turn. Busy means it took its task; idle after
// busy means it finished. Idle before busy is the agent waiting for its
// first input, and means nothing.
func (t *translator) activity(ctx context.Context, tx pgx.Tx, s *Syncer, activity string) error {
	switch activity {
	case "busy":
		_, err := tx.Exec(ctx, `UPDATE runs SET agent_busy_at = COALESCE(agent_busy_at, now()), turn_done_at = NULL WHERE id = $1`, t.run.ID)
		return err
	case "idle":
		if err := t.flushMessage(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE runs SET turn_done_at = now()
			WHERE id = $1 AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL`, t.run.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, t.run, evSessionStopped, ledger.ActorAgent, map[string]any{"reason": "turn_complete"})
	}
	return nil
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

	switch typ {
	case "agent_message_chunk":
		content, _ := u["content"].(map[string]any)
		if str(content, "type") == "text" {
			t.message.WriteString(str(content, "text"))
		}
		return nil

	case "tool_call", "tool_call_update":
		if err := t.flushMessage(ctx, tx, s); err != nil {
			return err
		}
		status, title, callID := str(u, "status"), str(u, "title"), str(u, "toolCallId")
		input := u["rawInput"]
		// A plan is a milestone, not a tool call: the agent rewrites the whole
		// list each time, and naming the tool is this layer's job.
		if isPlanTool(title) {
			if todos := planTodos(input); todos != nil && status == "in_progress" {
				return s.event(ctx, tx, t.run, evPlanUpdated, ledger.ActorAgent, map[string]any{"todos": todos})
			}
			return nil
		}
		switch status {
		case "in_progress":
			if _, seen := t.seenCalls[callID]; seen {
				return nil
			}
			name := toolName(u)
			t.seenCalls[callID] = name
			return s.event(ctx, tx, t.run, evToolCalled, ledger.ActorAgent,
				map[string]any{"tool": name, "callId": callID, "input": input, "title": title})
		case "completed", "failed":
			st := "completed"
			if status == "failed" {
				st = "error"
			}
			name, seen := t.seenCalls[callID]
			if !seen {
				name = toolName(u)
			}
			return s.event(ctx, tx, t.run, evToolCompleted, ledger.ActorAgent,
				map[string]any{"tool": name, "callId": callID, "status": st, "title": title})
		}
		// "pending" is announced before its input is known; in_progress follows.

	case "usage_update":
		cost, _ := u["cost"].(map[string]any)
		amount, _ := cost["amount"].(float64)
		delta := amount - t.cost
		if delta <= 0 {
			return nil
		}
		t.cost = amount
		if _, err := tx.Exec(ctx, `UPDATE runs SET agent_cost_usd = $2 WHERE id = $1`, t.run.ID, amount); err != nil {
			return err
		}
		return s.event(ctx, tx, t.run, evModelRequestDone, ledger.ActorAgent,
			map[string]any{"costUsd": delta, "tokens": map[string]any{"context": u["used"]}})

	case "turn_end":
		return t.flushMessage(ctx, tx, s)
	}
	return nil
}

// flushMessage records the reply text streamed since the last message.
func (t *translator) flushMessage(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	text := t.message.String()
	t.message.Reset()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return s.event(ctx, tx, t.run, evAgentMessage, ledger.ActorAgent, map[string]any{"text": text})
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
