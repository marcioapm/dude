package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
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
	evSessionReplaced    = "agent.session.replaced"
	evAgentWarning       = "agent.warning"
	evSessionStopped     = "agent.session.stopped"
	evRunStarted         = "run.started"
	evRuntimeStopped     = "runtime.stopped"
	evDirectiveDelivered = "run.directive.delivered"
	evDirectiveAccepted  = "run.directive.accepted"
	evDirectiveFailed    = "run.directive.failed"
	evNetworkRefused     = "agent.network.refused"
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
	// A session's refused names said so far (refused).
	sessionRefused map[string]bool
	// What a Claude Code or Codex turn needs remembered between batches,
	// saved as runs.harness_state.
	harnessState
}

// harnessState is runs.harness_state: Claude Code's running cost for its
// process, the last of it recorded, and its plan as TaskCreate and
// TaskUpdate built it, with the number the next task gets; the half of a
// Claude turn's end that has arrived (claudeTurnEnd), and the idle held
// until the other half; the tokens of the Codex turn in progress; and why
// the turn in progress failed, from a harness line that said so, which its
// end fails the Run with.
type harnessState struct {
	claudeCost     float64
	claudeCostSeen float64
	claudeTasks    []map[string]any
	claudeTaskNext int
	claudeHalf     string
	claudeUsage    *claudeUsage
	claudeIdle     bool
	codexTurn      codexTokens
	turnError      string
	// A talker's turns failed in a row (turnFailed), and whether the turn
	// that ended last was one: a turn that did not fail resets the count.
	failedTurns int
	lastFailed  bool
	// What lux last warned about the agent (lux.warning), until a session
	// it may explain is reported (session).
	warning string
}

type codexTokens struct {
	Input, Cached, Output int64
}

type harnessStateJSON struct {
	ClaudeCost     float64          `json:"claudeCost,omitempty"`
	ClaudeCostSeen float64          `json:"claudeCostSeen,omitempty"`
	ClaudeTasks    []map[string]any `json:"claudeTasks,omitempty"`
	ClaudeTaskNext int              `json:"claudeTaskNext,omitempty"`
	ClaudeHalf     string           `json:"claudeHalf,omitempty"`
	ClaudeUsage    *claudeUsage     `json:"claudeUsage,omitempty"`
	ClaudeIdle     bool             `json:"claudeIdle,omitempty"`
	CodexTurn      *codexTokens     `json:"codexTurn,omitempty"`
	TurnError      string           `json:"turnError,omitempty"`
	FailedTurns    int              `json:"failedTurns,omitempty"`
	LastFailed     bool             `json:"lastFailed,omitempty"`
	Warning        string           `json:"warning,omitempty"`
}

func (h harnessState) MarshalJSON() ([]byte, error) {
	j := harnessStateJSON{ClaudeCost: h.claudeCost, ClaudeCostSeen: h.claudeCostSeen, ClaudeTasks: h.claudeTasks,
		ClaudeTaskNext: h.claudeTaskNext, ClaudeHalf: h.claudeHalf, ClaudeUsage: h.claudeUsage, ClaudeIdle: h.claudeIdle, TurnError: h.turnError,
		FailedTurns: h.failedTurns, LastFailed: h.lastFailed, Warning: h.warning}
	if h.codexTurn != (codexTokens{}) {
		j.CodexTurn = &h.codexTurn
	}
	return json.Marshal(j)
}

func (h *harnessState) UnmarshalJSON(b []byte) error {
	var j harnessStateJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*h = harnessState{claudeCost: j.ClaudeCost, claudeCostSeen: j.ClaudeCostSeen, claudeTasks: j.ClaudeTasks,
		claudeTaskNext: j.ClaudeTaskNext, claudeHalf: j.ClaudeHalf, claudeUsage: j.ClaudeUsage, claudeIdle: j.ClaudeIdle, turnError: j.TurnError,
		failedTurns: j.FailedTurns, lastFailed: j.LastFailed, warning: j.Warning}
	// Legacy tasks used their original one-based position as identity.
	for i, task := range h.claudeTasks {
		id, _ := task["id"].(string)
		if id == "" {
			id = strconv.Itoa(i + 1)
			task["id"] = id
		}
		if n, err := strconv.Atoi(id); err == nil {
			h.claudeTaskNext = max(h.claudeTaskNext, n)
		}
	}
	if j.CodexTurn != nil {
		h.codexTurn = *j.CodexTurn
	}
	return nil
}

// replaying: a resumed agent loading its session replays the conversation,
// which is already recorded.
func (t *translator) replaying(epoch int) bool {
	return epoch > t.sessionEpoch && t.sessionEpoch != 0
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
	var state []byte
	u := &t.usage
	if err := tx.QueryRow(ctx, `SELECT agent_session_epoch, agent_message_buffer, agent_thought_buffer, open_tool_calls,
		agent_cost_usd::float8, context_tokens, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, harness_state
		FROM runs WHERE id = $1`, t.run.ID).
		Scan(&t.sessionEpoch, &message, &thought, &open, &u.cost, &u.context, &u.input, &u.output, &u.cacheRead, &u.cacheWrite, &state); err != nil {
		return err
	}
	if err := json.Unmarshal(state, &t.harnessState); err != nil {
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
	state, err := json.Marshal(t.harnessState)
	if err != nil {
		return err
	}
	// Each open call keeps the time it was first seen open
	// (open_tool_calls_at): what "open for how long" is measured from.
	_, err = tx.Exec(ctx, `UPDATE runs SET agent_message_buffer = $2, agent_thought_buffer = $3,
		agent_cost_usd = $4, context_tokens = $5, input_tokens = $6, output_tokens = $7,
		cache_read_tokens = $8, cache_write_tokens = $9, open_tool_calls = $10,
		open_tool_calls_at = COALESCE((SELECT jsonb_object_agg(c, COALESCE(open_tool_calls_at->c, to_jsonb(now())))
			FROM unnest($10::text[]) c), '{}'),
		agent_active_at = CASE WHEN $11 THEN now() ELSE agent_active_at END,
		idle_nudged_at = CASE WHEN $11 THEN NULL ELSE idle_nudged_at END,
		lux_cursor = COALESCE(NULLIF($12, ''), lux_cursor), lux_after_event = GREATEST(lux_after_event, $13),
		harness_state = $14::jsonb
		WHERE id = $1`,
		t.run.ID, t.message.String(), t.thought.String(), u.cost, u.context, u.input, u.output, u.cacheRead, u.cacheWrite,
		db.NonNil(open), t.active, cursor, afterEvent, state)
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

// backFromAway (SQL, over runs) is a stall clock column moved on by the
// time the Run was away from running (left_running_at), capped at now();
// NULL stays NULL (LEAST alone would make it now()), and a Run never away
// keeps it.
func backFromAway(col string) string {
	return `CASE WHEN ` + col + ` IS NULL OR left_running_at IS NULL THEN ` + col +
		` ELSE LEAST(` + col + ` + (now() - left_running_at), now()) END`
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
		// The stall clocks count only time running. Leaving running stamps
		// left_running_at; the next entry into running moves each clock on
		// by the time away, capped at now, so a move between hosts keeps
		// the running time before it and drops its wait. The clocks: no
		// change in files (files_changed_at), the agent's last activity
		// (agent_active_at), and the last report (stall_reported_at, so
		// run_stalled still compares like with like), and each open call.
		// A first start, never away, dates its files now.
		// A silent report's facts end in its silence's start (stallFacts):
		// when that is the silence the clocks move on, it moves with them,
		// so a move is not a change of facts. Another silence stays put.
		var was string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(lux_state, '') FROM runs WHERE id = $1 FOR UPDATE`, t.run.ID).Scan(&was); err != nil {
			return err
		}
		var facts, since *string
		if state == "running" {
			if err := tx.QueryRow(ctx, `SELECT r.stall_fingerprint, extract(epoch FROM `+silentSince+`)::text
				FROM runs r WHERE r.id = $1 AND r.left_running_at IS NOT NULL`, t.run.ID).Scan(&facts, &since); err != nil && err != pgx.ErrNoRows {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET lux_state = $2,
			started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
			files_changed_at = CASE WHEN $2 = 'running' THEN COALESCE(`+backFromAway("files_changed_at")+`, now())
				ELSE files_changed_at END,
			agent_active_at = CASE WHEN $2 = 'running' THEN `+backFromAway("agent_active_at")+` ELSE agent_active_at END,
			stall_reported_at = CASE WHEN $2 = 'running' THEN `+backFromAway("stall_reported_at")+` ELSE stall_reported_at END,
			open_tool_calls_at = CASE WHEN $2 = 'running' AND left_running_at IS NOT NULL
				THEN COALESCE((SELECT jsonb_object_agg(key, to_jsonb(`+backFromAway("value::timestamptz")+`))
					FROM jsonb_each_text(open_tool_calls_at)), '{}') ELSE open_tool_calls_at END,
			left_running_at = CASE WHEN $2 = 'running' THEN NULL
				WHEN lux_state = 'running' THEN now() ELSE left_running_at END,
			status = CASE WHEN $2 = 'running' AND status IN ('scheduled', 'starting') THEN 'running'::run_status ELSE status END
			WHERE id = $1`, t.run.ID, state); err != nil {
			return err
		}
		// Waiting for a host, or no longer: the Run page reads why again
		// (its waitingReason), as a preview's state change has it do.
		if lux.Waiting(state) || lux.Waiting(was) {
			if err := s.event(ctx, tx, t.run, EvServersChanged, ledger.ActorSystem,
				map[string]any{"taskId": t.run.TaskID, "runId": t.run.ID, "change": "state", "luxState": state}); err != nil {
				return err
			}
		}
		if facts != nil && since != nil && strings.HasSuffix(*facts, "|"+*since) {
			if _, err := tx.Exec(ctx, `UPDATE runs r SET stall_fingerprint = $2 || extract(epoch FROM `+silentSince+`)::text
				WHERE r.id = $1`, t.run.ID, strings.TrimSuffix(*facts, *since)); err != nil {
				return err
			}
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
		if t.run.conductor() {
			// A publish the conductor asked for (edits.go).
			return publishPushed(ctx, tx, str("requestId"), "", f.EventData)
		}
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
	case "git.sync":
		if t.run.conductor() {
			return t.checkoutSynced(ctx, tx, f.EventData)
		}
	case "dns":
		if allowed, _ := d["allowed"].(bool); !allowed {
			return t.refused(ctx, tx, s, strings.TrimSuffix(strings.ToLower(str("name")), "."))
		}
	case "artifact.published":
		return t.artifactPublished(ctx, tx, f)
	}
	if strings.HasPrefix(f.EventType, "server.") {
		// A server of the agent's Run changed (a person started it, it became
		// ready, the Run moved and it stopped): the browser reads it again.
		r := t.run
		return ServerEvent(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, f.EventType, f.EventData)
	}
	return nil
}

// artifactPublished records a file the agent published as soon as lux can
// serve it, while the Run goes on. The final diff is left to the stop-time
// sweep, which records it as the Run's diff; anything outside
// PublishedPrefix (artifacts.paths) is not for people.
func (t *translator) artifactPublished(ctx context.Context, tx pgx.Tx, f lux.Frame) error {
	var ev struct {
		ID string `json:"artifactId"`
		lux.Artifact
	}
	if json.Unmarshal(f.EventData, &ev) != nil || ev.ID == "" ||
		!strings.HasPrefix(ev.Path, lux.PublishedPrefix) || strings.HasPrefix(ev.Path, FinalDiffPrefix) {
		return nil
	}
	art := ev.Artifact
	art.ID, art.Epoch = ev.ID, cmp.Or(f.Epoch, 1)
	return recordArtifact(ctx, tx, artifactRun{t.run.Org, t.run.ProjectID, t.run.TaskID, t.run.ID}, art)
}

// refused records a name lux would not resolve for the Run's agent: kept
// for its project's Network page, and said on the Run the first time. lux
// reports each distinct lookup once, but a replayed stream reports it
// again. What it answered is not kept. Past maxRefusedNames distinct names
// a Run's are neither kept nor said: lux does not bound refused lookups,
// and an agent resolving random names would fill the table and the Run.
// A session's Run has no project page to list them: it is only told, its
// names counted in this follow alone.
func (t *translator) refused(ctx context.Context, tx pgx.Tx, s *Syncer, name string) error {
	if name == "" || len(name) > 253 {
		return nil
	}
	role := delivery.PromptRoleFor(t.run.Phase, t.run.Role)
	if t.run.ProjectID == "" {
		if t.sessionRefused[name] || len(t.sessionRefused) >= maxRefusedNames {
			return nil
		}
		if t.sessionRefused == nil {
			t.sessionRefused = map[string]bool{}
		}
		t.sessionRefused[name] = true
	} else {
		err := tx.QueryRow(ctx, `INSERT INTO agent_egress_refusals (run_id, name, organization_id, project_id, role)
			SELECT $1, $2, $3, $4, $5
			WHERE (SELECT count(*) FROM agent_egress_refusals WHERE run_id = $1) < $6
			ON CONFLICT (run_id, name) DO NOTHING
			RETURNING true`, t.run.ID, name, t.run.Org, t.run.ProjectID, role, maxRefusedNames).Scan(new(bool))
		if err == pgx.ErrNoRows {
			return nil // seen before, or past the cap
		} else if err != nil {
			return err
		}
	}
	return s.event(ctx, tx, t.run, evNetworkRefused, ledger.ActorSystem, map[string]any{"name": name, "role": role})
}

// maxRefusedNames is how many distinct refused names a Run keeps.
const maxRefusedNames = 100

// ended: the container stopped without dude asking — the agent crashed,
// timed out, or its host died. A phase whose turn was already done is
// finished by the sweep, and one dude stopped is dude's business; any other
// has failed. One lux stopped at its time limit is not kept: its running
// time is spent, so a resume would be stopped again at once.
func (t *translator) ended(ctx context.Context, tx pgx.Tx, s *Syncer, state, reason string) error {
	// A conductor or session agent lux can resume, with a session to
	// reload, is parked by the sweep (betweenTurns), not failed.
	if t.run.talker() && !lux.Terminated(state) && t.sessionEpoch > 0 {
		return nil
	}
	if reason == "" {
		reason = state
	}
	why, event := "the agent's run ended before finishing its task: "+reason, reason
	if reason == luxTimeout {
		why = "lux stopped it at its time limit: " + cmp.Or(s.Agent.Timeout, DefaultTimeout) + " of running"
		event = why
	}
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), keep = $3
		WHERE id = $1 AND status NOT IN ('completed', 'failed', 'aborted', 'paused')
		  AND lux_stop_reason IS NULL AND turn_done_at IS NULL`,
		t.run.ID, why, reason != luxTimeout)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.failedTx(ctx, tx, t.run, event)
}

// luxTimeout is the reason lux gives a Run it failed past its spec's timeout.
const luxTimeout = "timeout"

// shimEvent handles what lux's shim reports about the agent. These come in
// the record stream, in order with the agent's own messages — unlike lux's
// lifecycle events, which trail them. The order matters: "idle" must be
// seen after the reply it ends, or the reply is read before it is recorded.
func (t *translator) shimEvent(ctx context.Context, tx pgx.Tx, s *Syncer, typ string, data map[string]any, epoch int) error {
	str := func(k string) string { v, _ := data[k].(string); return v }
	switch typ {
	case "lux.session":
		return t.session(ctx, tx, s, str("sessionId"), epoch)
	case "lux.warning":
		// What lux says went wrong around the agent, as it says it: a
		// session it could not reload among them (session).
		t.warning = str("message")
		return s.event(ctx, tx, t.run, evAgentWarning, ledger.ActorSystem, map[string]any{"message": t.warning})
	case "lux.activity":
		return t.activity(ctx, tx, s, str("activity"), epoch)
	case recordCompacted:
		return t.recordLuxCompaction(ctx, tx, s, data)
	case lux.RecordInputConsumed, lux.RecordInputFailed:
		if str("requestId") == promptRequestID {
			return t.briefingReceipt(ctx, tx, typ, data)
		}
		return t.directiveReceipt(ctx, tx, s, typ, data)
	case lux.RecordInput:
		// The task itself: recorded when the agent has it, as lux delivered
		// it — from its first answer.
		if str("requestId") == promptRequestID {
			if err := t.briefingReceipt(ctx, tx, typ, data); err != nil {
				return err
			}
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

// briefingReceipt applies a receipt for a conductor's first prompt to the
// wake note dude started it with, on directiveReceipt's precedence: heard
// on consumption, or on an acceptance with no receipt to follow, or an
// older lux's handoff; an acceptance with a receipt to follow is not
// heard yet. A failure puts its reasons back to pending. A consumption or
// handoff after a failure wins; an acceptance does not.
func (t *translator) briefingReceipt(ctx context.Context, tx pgx.Tx, typ string, data map[string]any) error {
	if !t.run.conductor() {
		return nil
	}
	phase, _ := data["phase"].(string)
	msg, _ := data["error"].(string)
	receipt, _ := data["receipt"].(bool)
	switch {
	case typ == lux.RecordInputConsumed:
		return delivery.BriefingHeardTx(ctx, tx, t.run.ID, true)
	case typ == lux.RecordInputFailed, phase == lux.InputFailed, phase == "" && msg != "":
		return delivery.BriefingFailedTx(ctx, tx, t.run.ID)
	case phase == "":
		return delivery.BriefingHeardTx(ctx, tx, t.run.ID, true)
	case phase == lux.InputAccepted && !receipt:
		return delivery.BriefingHeardTx(ctx, tx, t.run.ID, false)
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
	// A conductor's steer, read: the conductor is told.
	if err := delivery.SteerSettledTx(ctx, tx, t.run.Org, id, true, ""); err != nil {
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
		// A new process: a Claude turn whose result never came ends with
		// what it had, before the process's totals start again.
		if t.claudeHalf != "" {
			if err := t.claudeTurnEnd(ctx, tx, s); err != nil {
				return err
			}
		}
		t.claudeProcessStarted()
		// The resumed agent has its session back: lux reports it running.
		// Its state event says so too, but trails the agent's records on
		// the stream, the first busy included.
		t.resumeRunning(ctx, tx, s, epoch)
		return t.sessionKept(ctx, tx, s, id)
	}
	t.warning = ""
	role := t.run.Phase
	if t.run.talker() {
		role = t.run.Role
	}
	return s.event(ctx, tx, t.run, evSessionStarted, ledger.ActorAgent,
		map[string]any{"role": role, "externalSessionId": id})
}

// sessionKept checks a resumed agent's session is the one it had. lux
// starts a blank one when the harness cannot reload its own (session/load,
// thread/resume failed), says why in a lux.warning, and reports the new id:
// recorded as agent.session.replaced, and a talker is briefed again, its
// briefing queued as its next input (delivery.Rebriefing). The resume's own
// input (a person's message, or the resume nudge) went first: the session
// is known only after it was sent.
func (t *translator) sessionKept(ctx context.Context, tx pgx.Tx, s *Syncer, id string) error {
	reason := t.warning
	t.warning = ""
	var had string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT CASE event_type WHEN $2 THEN payload->>'externalSessionId' ELSE payload->>'to' END
		FROM events WHERE run_id = $1 AND event_type IN ($2, $3) ORDER BY cursor DESC LIMIT 1), '')`,
		t.run.ID, evSessionStarted, evSessionReplaced).Scan(&had); err != nil {
		return err
	}
	if had == "" || had == id {
		return nil
	}
	payload := map[string]any{"from": had, "to": id, "reason": reason}
	if t.run.talker() {
		ref := t.run.ref()
		briefing, err := delivery.Rebriefing(ctx, tx, ref, reason)
		if err != nil {
			return err
		}
		// A blank session has no other prompt: the briefing goes as the
		// first did, with how it works, its checkouts and tools. The
		// task's images are not sent again; their references read as such.
		text, err := s.talkerPrompt(ctx, tx, t.run, briefing, nil)
		if err != nil {
			return err
		}
		directiveID, _, err := delivery.QueueDirective(ctx, tx, ref, delivery.Directive{Text: text, Scope: "run"})
		if err != nil {
			return err
		}
		// Sent before the messages still waiting (deliverDirectives sends
		// by created_at): the agent reads who it is before what they say.
		if _, err := tx.Exec(ctx, `UPDATE directives d SET created_at = LEAST(d.created_at,
				(SELECT min(o.created_at) - interval '1 millisecond' FROM directives o
				 WHERE o.run_id = d.run_id AND o.id <> d.id AND o.sent_at IS NULL AND o.failed_at IS NULL))
			WHERE d.id = $1`, directiveID); err != nil {
			return err
		}
		payload["directiveId"] = directiveID
	}
	return s.event(ctx, tx, t.run, evSessionReplaced, ledger.ActorSystem, payload)
}

// activity tracks the agent's turn. Busy means it took its task; idle after
// busy means it finished. Idle before busy is the agent waiting for its
// first input, and means nothing.
func (t *translator) activity(ctx context.Context, tx pgx.Tx, s *Syncer, activity string, epoch int) error {
	switch activity {
	case "busy":
		// A new turn: an idle held for a Claude turn's end is past.
		t.claudeIdle = false
		t.lastFailed = false
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
		// Between the halves of a Claude turn's end: held for the second.
		if t.claudeHalf != "" {
			t.claudeIdle = true
			return nil
		}
		return t.idle(ctx, tx, s)
	}
	return nil
}

// idle: the agent's turn is over.
func (t *translator) idle(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	if err := t.flush(ctx, tx, s); err != nil {
		return err
	}
	clear(t.openCalls)
	// A turn's end is when an agent's edits settle.
	s.pokeDiff(t.run.ID)
	// A failed turn's idle: the turn is over, not done (turnFailed).
	if t.lastFailed {
		return nil
	}
	t.failedTurns = 0
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
// when lux has dropped it and the agent goes on without it. repo is the
// name lux reports, the spec's (lux_name of the repository's).
func (t *translator) settleClone(ctx context.Context, tx pgx.Tx, s *Syncer, repo, status, cloneErr string) error {
	if status != "failed" {
		_, err := tx.Exec(ctx, `WITH done AS (
				UPDATE repository_requests q SET status = 'cloned' FROM repositories repo
				WHERE repo.id = q.repository_id AND q.run_id = $1 AND lux_name(repo.name) = $2 AND q.status = 'approved')
			UPDATE runs SET lux_repositories = array_append(lux_repositories, $2),
				lux_pushes = CASE WHEN EXISTS (SELECT 1 FROM task_repositories wr JOIN repositories repo ON repo.id = wr.repository_id
					WHERE wr.task_id = runs.task_id AND lux_name(repo.name) = $2 AND wr.access = 'write')
					THEN array_append(lux_pushes, $2) ELSE lux_pushes END
			WHERE id = $1 AND NOT ($2 = ANY (lux_repositories))`, t.run.ID, repo)
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE repository_requests q SET status = 'failed', error = $3
		FROM repositories repo WHERE repo.id = q.repository_id AND q.run_id = $1 AND lux_name(repo.name) = $2
		  AND q.status = 'approved'`, t.run.ID, repo, cloneErr); err != nil {
		return err
	}
	// Not checked out after all: the task stops naming it.
	if _, err := tx.Exec(ctx, `DELETE FROM task_repositories wr USING repository_requests q, repositories repo
		WHERE q.run_id = $1 AND q.status = 'failed' AND repo.id = q.repository_id AND lux_name(repo.name) = $2
		  AND wr.task_id = q.task_id AND wr.repository_id = q.repository_id`, t.run.ID, repo); err != nil {
		return err
	}
	// A session's: unlinked, or the next resume would try it again for good.
	if _, err := tx.Exec(ctx, `DELETE FROM session_repositories sr USING runs r, repositories repo, projects p
		WHERE r.id = $1 AND sr.session_id = r.session_id AND repo.id = sr.repository_id AND p.id = repo.project_id
		  AND `+delivery.SessionSpecNameSQL+` = $2`, t.run.ID, repo); err != nil {
		return err
	}
	return s.event(ctx, tx, t.run, "repository.clone_failed", ledger.ActorSystem,
		map[string]any{"repository": repo, "error": cloneErr})
}

// agentEvent translates the agent's own protocol messages: ACP (OpenCode
// and the scripted agent), Claude Code's stream-json (claude.*,
// translate_claude.go) and Codex's app-server (codex.*,
// translate_codex.go). An unknown event type is ignored rather than
// guessed at.
func (t *translator) agentEvent(ctx context.Context, tx pgx.Tx, s *Syncer, f lux.Frame) error {
	if strings.HasPrefix(f.Event.Type, "lux.") {
		var d map[string]any
		_ = json.Unmarshal(f.Event.Data, &d)
		return t.shimEvent(ctx, tx, s, f.Event.Type, d, f.Epoch)
	}
	if typ, ok := strings.CutPrefix(f.Event.Type, "claude."); ok {
		return t.claudeEvent(ctx, tx, s, typ, f)
	}
	if typ, ok := strings.CutPrefix(f.Event.Type, "codex."); ok {
		return t.codexEvent(ctx, tx, s, typ, f)
	}
	typ, ok := strings.CutPrefix(f.Event.Type, "acp.")
	if !ok {
		return nil
	}
	// While a resumed agent loads its session it replays the conversation;
	// that replay is already recorded.
	if t.replaying(f.Epoch) {
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
	t.lastFailed = true
	if t.run.talker() {
		t.failedTurns++
		if t.failedTurns <= maxFailedTurnResumes {
			return t.parkFailed(ctx, tx, s, reason)
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), turn_done_at = NULL, keep = true
		WHERE id = $1 AND status IN ('scheduled', 'starting', 'running') AND lux_stop_reason IS NULL`, t.run.ID, reason)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.failedTx(ctx, tx, t.run, reason)
}

// maxFailedTurnResumes is how many times in a row a talker whose turn
// failed is parked to be resumed; a failure past it (a context overflow
// that fails every turn) ends it, as any Run's.
const maxFailedTurnResumes = 2

// parkFailed keeps a talker whose turn failed: the failure is said as any
// Run's (run.failed, kept), and the Run is paused as its warm period's end
// parks it, its lux Run stopped and kept, so the next message resumes it.
func (t *translator) parkFailed(ctx context.Context, tx pgx.Tx, s *Syncer, reason string) error {
	kind := "conductor"
	if t.run.brainstorm() {
		kind = "session"
	}
	tag, err := tx.Exec(ctx, `UPDATE runs SET control = 'pause_graceful', control_requested_at = now(), control_reason = $2,
			dude_pause = $3, turn_done_at = NULL
		WHERE id = $1 AND status IN ('scheduled', 'starting', 'running') AND lux_stop_reason IS NULL AND control = 'none'`,
		t.run.ID, "parked after its turn failed", kind)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if err := s.event(ctx, tx, t.run, "run.failed", ledger.ActorSystem,
		map[string]any{"status": "failed", "error": reason, "kept": true, "failedTurns": t.failedTurns}); err != nil {
		return err
	}
	return s.event(ctx, tx, t.run, evParked, ledger.ActorSystem,
		map[string]any{"reason": kind, "message": "its turn failed", "failedTurns": t.failedTurns})
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

// flushWhole records text a harness sent whole (Claude Code's and Codex's
// messages and thoughts) into buf's event, ending whatever was before.
func (t *translator) flushWhole(ctx context.Context, tx pgx.Tx, s *Syncer, buf *strings.Builder, event, text string) error {
	if err := t.flush(ctx, tx, s); err != nil {
		return err
	}
	buf.WriteString(text)
	return t.flushText(ctx, tx, s, buf, event)
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
