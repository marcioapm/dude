package phases

import (
	"context"
	"encoding/json"
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
	}
	_ = json.Unmarshal(f.Event.Data, &n)
	switch typ {
	case "item/started":
		if name := codexToolName(n.Item); name != "" {
			t.active = true
			t.resumeOutput(ctx, tx, s, f.Epoch)
			return t.codexCall(ctx, tx, s, name, n.Item)
		}
	case "item/completed":
		return t.codexItemDone(ctx, tx, s, n.Item, f.Epoch)
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
	}
	return ""
}

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

// codexCall records a tool call when it starts, once.
func (t *translator) codexCall(ctx context.Context, tx pgx.Tx, s *Syncer, name string, it codexItem) error {
	if err := t.flush(ctx, tx, s); err != nil {
		return err
	}
	if _, seen := t.seenCalls[it.ID]; seen {
		return nil
	}
	t.seenCalls[it.ID] = name
	t.openCalls[it.ID] = true
	return s.event(ctx, tx, t.run, evToolCalled, ledger.ActorAgent,
		map[string]any{"tool": name, "callId": it.ID, "input": codexArgs(it), "title": it.Type})
}

// codexItemDone records a finished item: a message, a thought, or a tool
// call's end.
func (t *translator) codexItemDone(ctx context.Context, tx pgx.Tx, s *Syncer, it codexItem, epoch int) error {
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
	case "contextCompaction":
		// Codex says only that it compacted: no trigger, no tokens.
		return t.holdCompaction(ctx, tx, s, map[string]any{})
	}
	name := codexToolName(it)
	if name == "" {
		return nil
	}
	t.active = true
	if _, seen := t.seenCalls[it.ID]; !seen {
		// Reported only once finished: its completion is the whole call.
		if err := t.codexCall(ctx, tx, s, name, it); err != nil {
			return err
		}
	}
	delete(t.openCalls, it.ID)
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
	if isEdit(name) {
		s.pokeDiff(t.run.ID)
	}
	return s.event(ctx, tx, t.run, evToolCompleted, ledger.ActorAgent, payload)
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
