package phases

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/jackc/pgx/v5"

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
	// Thinking streamed since the last complete thought, kept the same way.
	thought strings.Builder
	// The conversation's size as the agent last reported it, stamped on each
	// message so the chat can show how full the context was at that point.
	context int64
	// Tool calls already recorded, by id, with the name they were recorded
	// under: a command that streams many progress updates is one call in the
	// ledger, and its completion — which carries no title — keeps the name.
	seenCalls map[string]string
}

// load restores what the translator keeps between batches.
func (t *translator) load(ctx context.Context, tx pgx.Tx) error {
	var message, thought string
	if err := tx.QueryRow(ctx, `SELECT agent_session_epoch, agent_cost_usd::float8, agent_message_buffer,
		agent_thought_buffer, context_tokens FROM runs WHERE id = $1`, t.run.ID).
		Scan(&t.sessionEpoch, &t.cost, &message, &thought, &t.context); err != nil {
		return err
	}
	t.message.WriteString(message)
	t.thought.WriteString(thought)
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
	_, err := tx.Exec(ctx, `UPDATE runs SET agent_message_buffer = $2, agent_thought_buffer = $3 WHERE id = $1`,
		t.run.ID, t.message.String(), t.thought.String())
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
		// The task itself: recorded when the agent has it, as lux delivered it.
		if str("requestId") == promptRequestID {
			// Once per Run: an agent resumed on another host is not given its
			// task again, but a lux that acknowledged it again would repeat it.
			var seen bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type = $2)`,
				t.run.ID, evPromptDelivered).Scan(&seen); err != nil || seen {
				return err
			}
			// lux caps the text it relays; a prompt it cut says so.
			payload := map[string]any{"text": str("text")}
			if truncated, _ := data["truncated"].(bool); truncated {
				payload["truncated"] = true
			}
			return s.event(ctx, tx, t.run, evPromptDelivered, ledger.ActorSystem, payload)
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
		if err := t.flush(ctx, tx, s); err != nil {
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
		// A reply ends a thought.
		if err := t.flushThought(ctx, tx, s); err != nil {
			return err
		}
		content, _ := u["content"].(map[string]any)
		if str(content, "type") == "text" {
			t.message.WriteString(str(content, "text"))
		}
		return nil

	case "agent_thought_chunk":
		if err := t.flushMessage(ctx, tx, s); err != nil {
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
			// A completion for a call never recorded is a plan update's: plans
			// are recorded as plans, and OpenCode's completions carry no title
			// to recognise them by.
			name, seen := t.seenCalls[callID]
			if !seen {
				return nil
			}
			payload := map[string]any{"tool": name, "callId": callID, "status": st, "title": title}
			maps.Copy(payload, toolResult(u))
			return s.event(ctx, tx, t.run, evToolCompleted, ledger.ActorAgent, payload)
		}
		// "pending" is announced before its input is known; in_progress follows.

	case "usage_update":
		return t.usageUpdate(ctx, tx, s, u)

	case "turn_end":
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
		return t.turnUsage(ctx, tx, s, u)
	}
	return nil
}

// flush records whatever reply or thought was streaming: something else
// happening ends both.
func (t *translator) flush(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	if err := t.flushThought(ctx, tx, s); err != nil {
		return err
	}
	return t.flushMessage(ctx, tx, s)
}

// flushMessage records the reply text streamed since the last message, with
// the context size at that point.
func (t *translator) flushMessage(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	text := t.message.String()
	t.message.Reset()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	payload := map[string]any{"text": text}
	if t.context > 0 {
		payload["contextTokens"] = t.context
	}
	return s.event(ctx, tx, t.run, evAgentMessage, ledger.ActorAgent, payload)
}

// flushThought records the thinking streamed since the last thought.
func (t *translator) flushThought(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	text := t.thought.String()
	t.thought.Reset()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return s.event(ctx, tx, t.run, evAgentThought, ledger.ActorAgent, map[string]any{"text": text})
}

// usageUpdate: ACP's running report of the conversation's size, and of cost
// when the agent knows prices. Recorded whenever either moves — a cost of
// zero means unknown, not free, and must not hide the tokens.
func (t *translator) usageUpdate(ctx context.Context, tx pgx.Tx, s *Syncer, u map[string]any) error {
	used := int64(number(u["used"]))
	cost, _ := u["cost"].(map[string]any)
	amount := number(cost["amount"])
	delta := amount - t.cost
	if (used == 0 || used == t.context) && delta <= 0 {
		return nil
	}
	if used > 0 {
		t.context = used
	}
	payload := map[string]any{"contextTokens": t.context, "contextWindow": int64(number(u["size"]))}
	if delta > 0 {
		t.cost = amount
		payload["costUsd"] = delta
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET context_tokens = $2, agent_cost_usd = $3 WHERE id = $1`,
		t.run.ID, t.context, t.cost); err != nil {
		return err
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
	if _, err := tx.Exec(ctx, `UPDATE runs SET input_tokens = input_tokens + $2, output_tokens = output_tokens + $3,
		cache_read_tokens = cache_read_tokens + $4, cache_write_tokens = cache_write_tokens + $5 WHERE id = $1`,
		t.run.ID, in, out, cr, cw); err != nil {
		return err
	}
	return s.event(ctx, tx, t.run, evModelRequestDone, ledger.ActorAgent, map[string]any{
		"turn": true, "contextTokens": t.context,
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
