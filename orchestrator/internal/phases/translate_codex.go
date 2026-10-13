package phases

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// codexEvent translates Codex's app-server notifications, as lux's codex
// adapter relays them (codex.<method>, the params as sent, and
// codex.turn_end with the turn's last usage report), into the events
// OpenCode's turns make. Items are read whole, from item/completed (an
// agent message, a reasoning summary) or item/started then item/completed
// (a tool call); the deltas streamed meanwhile are not needed.
func (t *translator) codexEvent(ctx context.Context, tx pgx.Tx, s *Syncer, typ string, f lux.Frame) error {
	if t.replaying(f.Epoch) {
		return nil
	}
	var n struct {
		Item       codexItem       `json:"item"`
		TurnID     string          `json:"turnId"`
		Plan       []codexStep     `json:"plan"`
		TokenUsage *codexUsage     `json:"tokenUsage"`
		Error      *codexError     `json:"error"`
		WillRetry  bool            `json:"willRetry"`
		Status     string          `json:"status"`
		Usage      json.RawMessage `json:"usage"`
		ThreadID   string          `json:"threadId"`
		Thread     struct {
			ID             string `json:"id"`
			ParentThreadID string `json:"parentThreadId"`
		} `json:"thread"`
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(f.Event.Data, &n)
	// The Run's own thread is its session (lux.session), else the first
	// started with no parent, else the first heard from; a notification on
	// any other is a child's.
	if typ == "thread/started" && n.Thread.ID != "" && n.Thread.ParentThreadID == "" && t.codexThread == "" {
		t.codexThread = n.Thread.ID
	}
	if t.SessionID != "" {
		t.codexThread = t.SessionID
	} else if t.codexThread == "" {
		t.codexThread = n.ThreadID
	}
	if n.ThreadID != "" && n.ThreadID != t.codexThread {
		return t.codexChildEvent(ctx, tx, s, typ, n.ThreadID, n.Item, n.TokenUsage, n.Turn.Status, f.Epoch)
	}
	switch typ {
	case "item/started":
		if name := codexToolName(n.Item); name != "" {
			t.active = true
			t.resumeOutput(ctx, tx, s, f.Epoch)
			return t.codexCall(ctx, tx, s, nil, name, n.Item)
		}
	case "item/completed":
		if n.Item.Type == "subAgentActivity" {
			return t.codexSubAgent(ctx, tx, s, nil, n.Item)
		}
		return t.codexItemDone(ctx, tx, s, nil, n.Item, f.Epoch)
	case "turn/plan/updated":
		t.active = true
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
		todos := make([]any, 0, len(n.Plan))
		for _, step := range n.Plan {
			todos = append(todos, map[string]any{"content": step.Step, "status": codexStatus(step.Status)})
		}
		return s.event(ctx, tx, t.run, evPlanUpdated, ledger.ActorAgent, map[string]any{"todos": todos, "callId": "plan-" + n.TurnID})
	case "thread/tokenUsage/updated":
		return t.codexRequestUsage(ctx, tx, s, n.TokenUsage)
	case "error":
		// Codex retries some failures itself (willRetry); the last error
		// before a failed turn is why it failed.
		if n.Error != nil && !n.WillRetry {
			t.turnError = n.Error.Message
		}
	case "turn_end":
		return t.codexTurnEnd(ctx, tx, s, n.Status)
	}
	return nil
}

// codexItem is the part of a ThreadItem (codex app-server's schema) the
// translator reads.
type codexItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	// agentMessage
	Text string `json:"text"`
	// reasoning
	Summary []string `json:"summary"`
	Content []string `json:"content"`
	// commandExecution
	Command          string                     `json:"command"`
	CommandActions   []struct{ Command string } `json:"commandActions"`
	AggregatedOutput *string                    `json:"aggregatedOutput"`
	ExitCode         *int                       `json:"exitCode"`
	Status           string                     `json:"status"`
	// fileChange
	Changes []struct {
		Path string `json:"path"`
		Diff string `json:"diff"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
	} `json:"changes"`
	// mcpToolCall
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *codexError `json:"error"`
	// webSearch
	Query string `json:"query"`
	// collabAgentToolCall (Tool is spawnAgent, sendInput, resumeAgent,
	// wait or closeAgent) and subAgentActivity
	Prompt            string   `json:"prompt"`
	Model             string   `json:"model"`
	SenderThreadID    string   `json:"senderThreadId"`
	ReceiverThreadIDs []string `json:"receiverThreadIds"`
	AgentsStates      map[string]struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"agentsStates"`
	Kind          string `json:"kind"`
	AgentThreadID string `json:"agentThreadId"`
	AgentPath     string `json:"agentPath"`
}

type codexError struct {
	Message string `json:"message"`
}

type codexStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type codexUsage struct {
	Last struct {
		InputTokens       int64 `json:"inputTokens"`
		CachedInputTokens int64 `json:"cachedInputTokens"`
		OutputTokens      int64 `json:"outputTokens"`
	} `json:"last"`
	ModelContextWindow int64 `json:"modelContextWindow"`
}

// codexStatus is a plan step's status in the todo vocabulary the chat reads.
func codexStatus(s string) string {
	if s == "inProgress" {
		return "in_progress"
	}
	return s
}

// codexToolName is the name a Codex item that is a tool call is recorded
// under, as the chat's tool cards know them; "" for an item that is not
// one. dude's own MCP tools go by the tool's name, as on OpenCode;
// another server's by server_tool.
func codexToolName(it codexItem) string {
	switch it.Type {
	case "commandExecution":
		return "bash"
	case "fileChange":
		return "edit"
	case "webSearch":
		return "websearch"
	case "mcpToolCall":
		if it.Server == "dude" {
			return it.Tool
		}
		return it.Server + "_" + it.Tool
	case "dynamicToolCall":
		return it.Tool
	case "collabAgentToolCall":
		if name, ok := codexCollabTools[it.Tool]; ok {
			return name
		}
		return it.Tool
	}
	return ""
}

// codexCollabTools names Codex's agent tools as the chat's cards know
// them: spawning one is a task, as OpenCode's and Claude Code's are.
var codexCollabTools = map[string]string{"spawnAgent": "task", "sendInput": "send_input", "resumeAgent": "resume_agent",
	"wait": "wait", "closeAgent": "close_agent"}

// codexArgs are a tool item's arguments in the shape the chat's tool cards
// summarise: a command line, a file path, a query, or the MCP arguments.
func codexArgs(it codexItem) any {
	switch it.Type {
	case "commandExecution":
		return map[string]any{"command": codexCommand(it)}
	case "fileChange":
		var paths []string
		changes := make([]any, 0, len(it.Changes))
		for _, c := range it.Changes {
			paths = append(paths, c.Path)
			changes = append(changes, map[string]any{"path": c.Path, "kind": c.Kind.Type})
		}
		return map[string]any{"file_path": strings.Join(paths, ", "), "changes": changes}
	case "webSearch":
		return map[string]any{"query": it.Query}
	case "collabAgentToolCall":
		args := map[string]any{}
		if it.Prompt != "" {
			args["description"], args["prompt"] = codexTitle(it.Prompt), it.Prompt
		}
		if it.Model != "" {
			args["model"] = it.Model
		}
		if len(it.ReceiverThreadIDs) > 0 {
			args["agents"] = it.ReceiverThreadIDs
		}
		return args
	}
	var args any
	_ = json.Unmarshal(it.Arguments, &args)
	return args
}

// codexCommand is the command a person typed it as: Codex runs each in a
// login shell (/bin/zsh -lc '…') and reports what it parsed out of it.
func codexCommand(it codexItem) string {
	if len(it.CommandActions) == 1 && it.CommandActions[0].Command != "" {
		return it.CommandActions[0].Command
	}
	for _, shell := range []string{" -lc ", " -c "} {
		if _, rest, ok := strings.Cut(it.Command, shell); ok && strings.HasPrefix(it.Command, "/") {
			if len(rest) > 1 && rest[0] == '\'' && rest[len(rest)-1] == '\'' && !strings.Contains(rest[1:len(rest)-1], "'") {
				return rest[1 : len(rest)-1]
			}
			return rest
		}
	}
	return it.Command
}

// codexCall records a tool call when it starts, once: the Run's own (c
// nil) or a child's. A spawn is remembered until its agent's thread is
// known (codexSubAgent).
func (t *translator) codexCall(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState, name string, it codexItem) error {
	if c == nil {
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
	}
	if it.Type == "collabAgentToolCall" && it.Tool == "spawnAgent" {
		t.codexSpawned(c, it)
	}
	return t.callStarted(ctx, tx, s, c, map[string]any{"tool": name, "callId": it.ID, "input": codexArgs(it), "title": it.Type})
}

// codexItemDone records a finished item: a message, a thought, or a tool
// call's end; the Run's own (c nil) or a child's, whose thoughts are not
// recorded.
func (t *translator) codexItemDone(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState, it codexItem, epoch int) error {
	switch {
	case c != nil && it.Type == "agentMessage":
		return t.childMessage(ctx, tx, s, c, it.Text, 0, nil)
	case c != nil && it.Type == "reasoning":
		t.touchChild(c, "thinking", nil)
		return nil
	}
	switch it.Type {
	case "agentMessage":
		t.active = true
		t.resumeOutput(ctx, tx, s, epoch)
		return t.flushWhole(ctx, tx, s, &t.message, evAgentMessage, it.Text)
	case "reasoning":
		t.active = true
		t.resumeOutput(ctx, tx, s, epoch)
		text := strings.Join(it.Summary, "\n\n")
		if strings.TrimSpace(text) == "" {
			text = strings.Join(it.Content, "\n\n")
		}
		return t.flushWhole(ctx, tx, s, &t.thought, evAgentThought, text)
	}
	name := codexToolName(it)
	if name == "" {
		return nil
	}
	t.active = true
	// Reported only once finished: its completion is the whole call.
	if err := t.codexCall(ctx, tx, s, c, name, it); err != nil {
		return err
	}
	if it.Type == "collabAgentToolCall" && it.Tool == "spawnAgent" {
		t.codexSpawnedThreads(c, it)
	}
	st := "completed"
	if it.Status == "failed" || it.Status == "declined" || it.Error != nil {
		st = "error"
	}
	payload := map[string]any{"tool": name, "callId": it.ID, "status": st}
	if it.ExitCode != nil {
		payload["exitCode"] = *it.ExitCode
	}
	var out string
	switch {
	case it.AggregatedOutput != nil:
		out = *it.AggregatedOutput
	case it.Type == "fileChange":
		var diffs []string
		for _, c := range it.Changes {
			diffs = append(diffs, c.Diff)
		}
		out = strings.Join(diffs, "\n")
	case it.Error != nil:
		out = it.Error.Message
	case it.Result != nil:
		var b strings.Builder
		for _, c := range it.Result.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		out = b.String()
	}
	if out != "" {
		payload["output"] = capOutput(out)
	}
	return t.callEnded(ctx, tx, s, c, payload)
}

// codexRequestUsage: Codex reports each model request's tokens as it ends.
// Its input is the conversation's size then; the turn's totals are summed
// for its end. Codex knows no prices: cost is lux's to report.
func (t *translator) codexRequestUsage(ctx context.Context, tx pgx.Tx, s *Syncer, u *codexUsage) error {
	if u == nil {
		return nil
	}
	t.codexTurn.Input += u.Last.InputTokens
	t.codexTurn.Cached += u.Last.CachedInputTokens
	t.codexTurn.Output += u.Last.OutputTokens
	if u.Last.InputTokens == 0 || u.Last.InputTokens == t.usage.context {
		return nil
	}
	t.usage.context = u.Last.InputTokens
	return s.event(ctx, tx, t.run, evModelRequestDone, ledger.ActorAgent,
		map[string]any{"contextTokens": t.usage.context, "contextWindow": u.ModelContextWindow})
}

// codexTurnEnd: the turn's tokens, summed over its requests (OpenAI counts
// cached input within input; dude counts it apart, as Anthropic does). A
// failed turn fails the Run with the error Codex gave up on.
func (t *translator) codexTurnEnd(ctx context.Context, tx pgx.Tx, s *Syncer, status string) error {
	if err := t.flush(ctx, tx, s); err != nil {
		return err
	}
	u := t.codexTurn
	t.codexTurn = codexTokens{}
	in := u.Input - u.Cached
	t.usage.input += in
	t.usage.output += u.Output
	t.usage.cacheRead += u.Cached
	if err := s.event(ctx, tx, t.run, evModelRequestDone, ledger.ActorAgent, map[string]any{
		"turn": true, "contextTokens": t.usage.context,
		"tokens": map[string]any{"input": in, "output": u.Output, "cacheRead": u.Cached, "cacheWrite": int64(0)}}); err != nil {
		return err
	}
	e := t.turnError
	t.turnError = ""
	if status != "failed" {
		return nil
	}
	if e == "" {
		e = "Codex ended the turn as failed"
	}
	return t.turnFailed(ctx, tx, s, e)
}

// codexSpawn is a spawnAgent call whose agent's thread is not known yet.
type codexSpawn struct {
	CallID string `json:"callId"`
	Title  string `json:"title,omitempty"`
	Model  string `json:"model,omitempty"`
}

// codexTitle is a spawned agent's title: its prompt's first line, cut.
func codexTitle(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	return cut(line, 120)
}

// codexSender is the thread a child's or the Run's call is made on.
func (t *translator) codexSender(c *childState) string {
	if c == nil {
		return t.codexThread
	}
	return c.ID
}

// codexSpawned remembers a spawnAgent call on its thread until the agent
// it spawns says which thread it is (codexSubAgent), in order: several
// may be spawned before any says.
func (t *translator) codexSpawned(c *childState, it codexItem) {
	sender := t.codexSender(c)
	if t.childOfCall(it.ID) != nil || slices.ContainsFunc(t.codexSpawns[sender], func(p codexSpawn) bool { return p.CallID == it.ID }) {
		return
	}
	if t.codexSpawns == nil {
		t.codexSpawns = map[string][]codexSpawn{}
	}
	t.codexSpawns[sender] = append(t.codexSpawns[sender], codexSpawn{CallID: it.ID, Title: codexTitle(it.Prompt), Model: it.Model})
}

// codexSpawnedThreads: a finished spawnAgent call names its agent's
// thread; one not bound yet is bound to it.
func (t *translator) codexSpawnedThreads(c *childState, it codexItem) {
	for _, thread := range it.ReceiverThreadIDs {
		if t.child(thread) == nil {
			t.codexBind(c, thread, it.ID)
		}
	}
}

// codexBind makes thread the child of the spawn call callID on the thread
// of parent (nil: the Run's own); callID "" takes the first spawn waiting
// there, and with none the child's call is unknown. A thread already
// known stays as it is.
func (t *translator) codexBind(parent *childState, thread, callID string) *childState {
	if c := t.child(thread); c != nil {
		return c
	}
	sender := t.codexSender(parent)
	pending := t.codexSpawns[sender]
	i := slices.IndexFunc(pending, func(p codexSpawn) bool { return callID == "" || p.CallID == callID })
	spawn := codexSpawn{CallID: callID}
	if i >= 0 {
		spawn = pending[i]
		t.codexSpawns[sender] = slices.Delete(pending, i, i+1)
		if len(t.codexSpawns[sender]) == 0 {
			delete(t.codexSpawns, sender)
		}
	}
	c := &childState{ID: thread, CallID: spawn.CallID, Kind: childSpawn, Title: spawn.Title, Model: spawn.Model}
	if parent != nil {
		c.ParentChildID = parent.ID
	}
	return t.addChild(c)
}

// codexSubAgent reads a subAgentActivity item on parent's thread (nil:
// the Run's own). started binds the agent's thread to the spawn call it
// follows; interrupted ends it. Codex 0.144.1 sends no item for the spawn
// call itself, only this one under the call's id: the spawn is then
// recorded from it, a task call already done, so the child has its call.
func (t *translator) codexSubAgent(ctx context.Context, tx pgx.Tx, s *Syncer, parent *childState, it codexItem) error {
	if it.AgentThreadID == "" {
		return nil
	}
	switch it.Kind {
	case "started":
		if t.child(it.AgentThreadID) == nil && len(t.codexSpawns[t.codexSender(parent)]) == 0 {
			spawn := codexItem{Type: "collabAgentToolCall", ID: it.ID, Tool: "spawnAgent", Prompt: it.AgentPath,
				ReceiverThreadIDs: []string{it.AgentThreadID}}
			if err := t.codexItemDone(ctx, tx, s, parent, spawn, 0); err != nil {
				return err
			}
		}
		// Its id is its spawn call's when Codex says so; else the first
		// spawn still waiting for its agent.
		callID := ""
		if slices.ContainsFunc(t.codexSpawns[t.codexSender(parent)], func(p codexSpawn) bool { return p.CallID == it.ID }) {
			callID = it.ID
		}
		c := t.codexBind(parent, it.AgentThreadID, callID)
		if c.Title == "" {
			c.Title = it.AgentPath
		}
		return t.startChild(ctx, tx, s, c)
	case "interrupted":
		if c := t.child(it.AgentThreadID); c != nil {
			return t.endChild(ctx, tx, s, c, "interrupted", "", nil, 0)
		}
	}
	return nil
}

// codexChildEvent translates a notification on a child's thread as the
// Run's own are, nested under the spawn call: its tool calls, messages
// and tokens, and its turn's end, which ends it. A new turn (an agent
// sent more input) takes it up again. Its errors are its own, never the
// Run's turn's.
func (t *translator) codexChildEvent(ctx context.Context, tx pgx.Tx, s *Syncer, typ, thread string, it codexItem,
	usage *codexUsage, turnStatus string, epoch int) error {
	switch typ {
	case "turn/started", "item/started", "item/completed", "thread/tokenUsage/updated", "turn/completed":
	default:
		return nil
	}
	c := t.child(thread)
	if c == nil {
		// Its agent said nothing yet of which spawn it is: the first
		// spawn waiting on the Run's thread.
		c = t.codexBind(nil, thread, "")
	}
	switch typ {
	case "turn/started":
		c.Ended = false
		return t.startChild(ctx, tx, s, c)
	case "item/started":
		if name := codexToolName(it); name != "" {
			t.resumeOutput(ctx, tx, s, epoch)
			return t.codexCall(ctx, tx, s, c, name, it)
		}
	case "item/completed":
		if it.Type == "subAgentActivity" {
			return t.codexSubAgent(ctx, tx, s, c, it)
		}
		return t.codexItemDone(ctx, tx, s, c, it, epoch)
	case "thread/tokenUsage/updated":
		if usage != nil {
			c.Tokens.Input += usage.Last.InputTokens
			c.Tokens.Cached += usage.Last.CachedInputTokens
			c.Tokens.Output += usage.Last.OutputTokens
		}
	case "turn/completed":
		status := "completed"
		switch turnStatus {
		case "failed":
			status = "failed"
		case "interrupted":
			status = "interrupted"
		}
		return t.endChild(ctx, tx, s, c, status, "", nil, 0)
	}
	return nil
}
