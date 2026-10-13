package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// claudeEvent translates Claude Code's stream-json lines, as lux's
// claude-code adapter relays them (claude.<type>, each the whole line, and
// claude.turn_end with the turn's usage), into the events OpenCode's turns
// make. Messages are whole (no --include-partial-messages): an assistant
// line carries its content blocks — text, thinking, tool_use — and a user
// line the tool_result blocks answering them.
func (t *translator) claudeEvent(ctx context.Context, tx pgx.Tx, s *Syncer, typ string, f lux.Frame) error {
	if t.replaying(f.Epoch) {
		return nil
	}
	var line claudeLine
	_ = json.Unmarshal(f.Event.Data, &line)
	// A subagent's own line: its parent's Agent call names it.
	if line.ParentToolUseID != "" {
		return t.claudeChildLine(ctx, tx, s, typ, line, f.Epoch)
	}
	switch typ {
	case "system":
		return t.claudeTask(ctx, tx, s, line)
	case "assistant":
		t.active = true
		t.resumeOutput(ctx, tx, s, f.Epoch)
		if line.Error != "" {
			t.turnError = claudeText(line.Message.Content)
		}
		for _, b := range line.Message.Content {
			if err := t.claudeBlock(ctx, tx, s, b); err != nil {
				return err
			}
		}
		// The conversation's size when this step was asked: everything it
		// read, cached or not.
		if u := line.Message.Usage; u != nil {
			if n := u.InputTokens + u.CacheRead + u.CacheWrite; n > 0 {
				t.usage.context = n
			}
		}
	case "user":
		for _, b := range line.Message.Content {
			if b.Type != "tool_result" {
				continue
			}
			t.active = true
			if err := t.claudeResult(ctx, tx, s, b, line.ToolUseResult); err != nil {
				return err
			}
		}
	case "result":
		// The turn's cost, as Claude Code's running total for this process:
		// a resumed one starts again from zero, so a total lower than the
		// last is a new process's.
		t.claudeCost = line.TotalCostUSD
		if e := line.failure(); e != "" {
			t.turnError = e
		}
		return t.claudeTurnHalf(ctx, tx, s, "result", nil)
	case "turn_end":
		var end struct {
			Usage *claudeUsage `json:"usage"`
		}
		_ = json.Unmarshal(f.Event.Data, &end)
		return t.claudeTurnHalf(ctx, tx, s, "turn_end", end.Usage)
	}
	return nil
}

// claudeLine is the part of a stream-json line the translator reads.
type claudeLine struct {
	Message struct {
		Content []claudeBlock `json:"content"`
		Usage   *claudeUsage  `json:"usage"`
	} `json:"message"`
	// A user line's tool result as the tool returned it (stdout and stderr
	// apart for Bash).
	ToolUseResult json.RawMessage `json:"tool_use_result"`
	// An assistant line Claude Code wrote itself for a failed request
	// (model_not_found…), its text the reason.
	Error        string   `json:"error"`
	IsError      bool     `json:"is_error"`
	Subtype      string   `json:"subtype"`
	Result       string   `json:"result"`
	Errors       []string `json:"errors"`
	TerminalWhy  string   `json:"terminal_reason"`
	TotalCostUSD float64  `json:"total_cost_usd"`
	// A subagent's: on each of its lines, the parent's Agent call and what
	// it was asked; on the system task_* lines, its task and how it went.
	ParentToolUseID string        `json:"parent_tool_use_id"`
	SubagentType    string        `json:"subagent_type"`
	TaskDescription string        `json:"task_description"`
	TaskID          string        `json:"task_id"`
	ToolUseID       string        `json:"tool_use_id"`
	Description     string        `json:"description"`
	Status          string        `json:"status"`
	Summary         string        `json:"summary"`
	LastToolName    string        `json:"last_tool_name"`
	TaskUsage       claudeTaskUse `json:"usage"`
}

// claudeTaskUse is a system task_* line's usage. A result line's usage
// (another shape) is read from claude.turn_end, not here.
type claudeTaskUse struct {
	TotalTokens int64   `json:"total_tokens"`
	DurationMS  float64 `json:"duration_ms"`
}

// failure is why a result line says its turn failed, "" for one that did
// not. A success-subtype error carries its reason in result, the error_*
// subtypes in errors. A turn aborted by an interrupt (aborted_streaming,
// aborted_tools) is an error to Claude Code and not a failure to dude.
func (l claudeLine) failure() string {
	if !l.IsError || l.TerminalWhy == "aborted_streaming" || l.TerminalWhy == "aborted_tools" {
		return ""
	}
	if l.Result != "" {
		return l.Result
	}
	if len(l.Errors) > 0 {
		return strings.Join(l.Errors, "; ")
	}
	return "Claude Code ended the turn as failed (" + cmp.Or(l.Subtype, "no reason given") + ")"
}

type claudeBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CacheRead    int64 `json:"cache_read_input_tokens"`
	CacheWrite   int64 `json:"cache_creation_input_tokens"`
}

func claudeText(blocks []claudeBlock) string {
	var b strings.Builder
	for _, c := range blocks {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// claudeBlock records one block of an assistant line. Text and thinking
// are whole blocks: each is recorded at once, ending whatever was before.
func (t *translator) claudeBlock(ctx context.Context, tx pgx.Tx, s *Syncer, b claudeBlock) error {
	switch b.Type {
	case "text":
		return t.flushWhole(ctx, tx, s, &t.message, evAgentMessage, b.Text)
	case "thinking":
		return t.flushWhole(ctx, tx, s, &t.thought, evAgentThought, b.Thinking)
	case "tool_use":
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
		var input map[string]any
		_ = json.Unmarshal(b.Input, &input)
		if todos, ok := t.claudePlan(b.Name, input); ok {
			t.seenCalls[b.ID] = planCall
			if todos == nil {
				return nil
			}
			return s.event(ctx, tx, t.run, evPlanUpdated, ledger.ActorAgent, map[string]any{"todos": todos, "callId": b.ID})
		}
		name := claudeToolName(b.Name)
		return t.callStarted(ctx, tx, s, nil, map[string]any{"tool": name, "callId": b.ID, "input": input, "title": b.Name})
	}
	return nil
}

// claudeResult records a tool's result: the end of the call it answers.
func (t *translator) claudeResult(ctx context.Context, tx pgx.Tx, s *Syncer, b claudeBlock, raw json.RawMessage) error {
	if t.seenCalls[b.ToolUseID] == planCall {
		return nil
	}
	st := "completed"
	if b.IsError {
		st = "error"
	}
	payload := map[string]any{"tool": "tool", "callId": b.ToolUseID, "status": st}
	maps.Copy(payload, claudeOutput(b, raw))
	return t.callEnded(ctx, tx, s, nil, payload)
}

// claudeOutput is a tool result in dude's vocabulary (toolResult's): Bash's
// stdout and stderr apart, from tool_use_result, and otherwise the text
// the model was shown.
func claudeOutput(b claudeBlock, raw json.RawMessage) map[string]any {
	out := map[string]any{}
	var shell struct {
		Stdout *string `json:"stdout"`
		Stderr *string `json:"stderr"`
	}
	if json.Unmarshal(raw, &shell) == nil && (shell.Stdout != nil || shell.Stderr != nil) {
		if shell.Stdout != nil && *shell.Stdout != "" {
			out["stdout"] = capOutput(*shell.Stdout)
		}
		if shell.Stderr != nil && *shell.Stderr != "" {
			out["stderr"] = capOutput(*shell.Stderr)
		}
		return out
	}
	var text string
	if json.Unmarshal(b.Content, &text) != nil {
		var blocks []claudeBlock
		_ = json.Unmarshal(b.Content, &blocks)
		text = claudeText(blocks)
	}
	if text != "" {
		out["output"] = capOutput(text)
	}
	return out
}

// claudeTools maps Claude Code's tool names onto the names the chat's tool
// cards know (OpenCode's). A tool not here passes through by its own name.
var claudeTools = map[string]string{
	"Bash": "bash", "Read": "read", "Edit": "edit", "MultiEdit": "multiedit", "Write": "write",
	"Grep": "grep", "Glob": "glob", "WebFetch": "webfetch", "WebSearch": "websearch",
	"Task": "task", "Agent": "task", "NotebookEdit": "edit",
}

// claudeToolName is the name a Claude Code tool call is recorded under.
// dude's own MCP tools (mcp__dude__<tool>) are recorded by the tool's own
// name, as OpenCode's are; another server's keep the server (server_tool,
// OpenCode's form).
func claudeToolName(name string) string {
	if mapped, ok := claudeTools[name]; ok {
		return mapped
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		server, tool, _ := strings.Cut(rest, "__")
		if server == "dude" {
			return tool
		}
		return server + "_" + tool
	}
	return name
}

// claudePlan reports whether a tool call is the agent's plan, and the plan
// it makes now (nil for a call that changes nothing to show). Claude Code
// plans with TodoWrite (the whole list each time) or, from 2.1, with
// TaskCreate and TaskUpdate (one task at a time, numbered from 1 in order
// of creation within its process): those are kept on the translator under
// that number, and the whole list recorded each time, as a todowrite would.
func (t *translator) claudePlan(name string, input map[string]any) ([]any, bool) {
	switch name {
	case "TodoWrite":
		return planTodos(input), true
	case "TaskCreate":
		subject, _ := input["subject"].(string)
		t.claudeTaskNext++
		t.claudeTasks = append(t.claudeTasks, map[string]any{"id": strconv.Itoa(t.claudeTaskNext), "content": subject, "status": "pending"})
		t.claudeTasks = boundTasks(t.claudeTasks)
	case "TaskUpdate":
		id, _ := input["taskId"].(string)
		i := slices.IndexFunc(t.claudeTasks, func(task map[string]any) bool { return task["id"] == id })
		if i < 0 {
			return nil, true
		}
		task := t.claudeTasks[i]
		if st, ok := input["status"].(string); ok {
			if st == "deleted" {
				st = "cancelled"
			}
			task["status"] = st
		}
		if subject, ok := input["subject"].(string); ok {
			task["content"] = subject
		}
	case "TaskList", "TaskGet":
		return nil, true
	default:
		return nil, false
	}
	todos := make([]any, len(t.claudeTasks))
	for i, task := range t.claudeTasks {
		todo := maps.Clone(task)
		delete(todo, "id")
		todos[i] = todo
	}
	return todos, true
}

// maxClaudeTasks bounds the plan kept in harness_state: past it, the
// oldest finished tasks are dropped (the plan shows the rest).
const maxClaudeTasks = 200

func boundTasks(tasks []map[string]any) []map[string]any {
	for over := len(tasks) - maxClaudeTasks; over > 0; over-- {
		i := slices.IndexFunc(tasks, func(task map[string]any) bool {
			return task["status"] == "completed" || task["status"] == "cancelled"
		})
		if i < 0 {
			break
		}
		tasks = slices.Delete(tasks, i, i+1)
	}
	return tasks
}

// claudeProcessStarted: a resumed agent is a new Claude Code process. Its
// running cost starts again from zero, and its tasks are numbered from 1
// again, so neither is read against the last process's.
func (t *translator) claudeProcessStarted() {
	t.claudeCostSeen, t.claudeCost = 0, 0
	t.claudeTasks, t.claudeTaskNext = nil, 0
}

// claudeTurnHalf records one half of a Claude turn's end. lux relays a
// result line as claude.turn_end (the usage), the agent going idle, then
// claude.result (the cost, and the failure if any): the turn ends on the
// second half, whichever it is, with a batch between them or not. The idle
// between them is held until then, so a failed turn is failed before its
// idle could mark it done.
func (t *translator) claudeTurnHalf(ctx context.Context, tx pgx.Tx, s *Syncer, half string, usage *claudeUsage) error {
	// The same half again: the last turn's other half never came (its
	// process ended between them). That turn ends with what it had.
	if t.claudeHalf == half {
		if err := t.claudeTurnEnd(ctx, tx, s); err != nil {
			return err
		}
	}
	if half == "turn_end" {
		t.claudeUsage = usage
	}
	if t.claudeHalf == "" {
		if err := t.flush(ctx, tx, s); err != nil {
			return err
		}
		t.claudeHalf = half
		return nil
	}
	return t.claudeTurnEnd(ctx, tx, s)
}

// claudeTurnEnd: the turn's tokens, as Claude Code's result line counts
// them for this turn, and its cost, the change in the process's running
// total. A turn that ended on a failed request fails the Run, as an ACP
// turn that answered with an error does; one that did not runs the idle
// held for it.
func (t *translator) claudeTurnEnd(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	if err := t.flush(ctx, tx, s); err != nil {
		return err
	}
	u, idle := t.claudeUsage, t.claudeIdle
	t.claudeHalf, t.claudeUsage, t.claudeIdle = "", nil, false
	cost := t.claudeCost - t.claudeCostSeen
	if t.claudeCost < t.claudeCostSeen {
		cost = t.claudeCost
	}
	t.claudeCostSeen = t.claudeCost
	payload := map[string]any{"turn": true, "contextTokens": t.usage.context}
	if u != nil {
		t.usage.input += u.InputTokens
		t.usage.output += u.OutputTokens
		t.usage.cacheRead += u.CacheRead
		t.usage.cacheWrite += u.CacheWrite
		payload["tokens"] = map[string]any{"input": u.InputTokens, "output": u.OutputTokens, "cacheRead": u.CacheRead, "cacheWrite": u.CacheWrite}
	}
	if cost > 0 {
		t.usage.cost += cost
		payload["costUsd"] = cost
	}
	if err := s.event(ctx, tx, t.run, evModelRequestDone, ledger.ActorAgent, payload); err != nil {
		return err
	}
	if e := t.turnError; e != "" {
		t.turnError = ""
		return t.turnFailed(ctx, tx, s, e)
	}
	if idle {
		return t.idle(ctx, tx, s)
	}
	return nil
}

// claudeChildOf is the subagent the parent's call callID started, made
// from what its line says when task_started has not named it yet.
func (t *translator) claudeChildOf(callID string, line claudeLine) *childState {
	if c := t.childOfCall(callID); c != nil {
		return c
	}
	c := &childState{ID: cmp.Or(line.TaskID, callID), CallID: callID, Kind: childTask,
		Title: cmp.Or(line.Description, line.TaskDescription), AgentType: line.SubagentType}
	// A subagent's own Agent call starts a child of that subagent.
	for _, p := range t.Children {
		if _, ok := p.Calls[callID]; ok {
			c.ParentChildID = p.ID
		}
	}
	return t.addChild(c)
}

// claudeTask reads Claude Code's system lines about a subagent:
// task_started names it, task_progress says it works, task_notification
// ends it. task_updated repeats the end and adds nothing.
func (t *translator) claudeTask(ctx context.Context, tx pgx.Tx, s *Syncer, line claudeLine) error {
	if line.ToolUseID == "" {
		return nil
	}
	switch line.Subtype {
	case "task_started":
		return t.startChild(ctx, tx, s, t.claudeChildOf(line.ToolUseID, line))
	case "task_progress":
		c := t.claudeChildOf(line.ToolUseID, line)
		if err := t.startChild(ctx, tx, s, c); err != nil {
			return err
		}
		t.touchChild(c, strings.TrimSpace(line.LastToolName+" "+line.Description), nil)
	case "task_notification":
		var tokens map[string]any
		if n := line.TaskUsage.TotalTokens; n > 0 {
			tokens = map[string]any{"total": n}
		}
		return t.endChild(ctx, tx, s, t.claudeChildOf(line.ToolUseID, line), claudeTaskStatus(line.Status), line.Summary,
			tokens, line.TaskUsage.DurationMS/1000)
	}
	return nil
}

// claudeTaskStatus is a subagent's end in dude's words.
func claudeTaskStatus(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "failed", "error":
		return "failed"
	}
	return "interrupted"
}

// claudePlanTools are the tools claudePlan reads as the agent's plan.
var claudePlanTools = []string{"TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet"}

// claudeChildLine translates a subagent's own line as the parent's are,
// nested under it: its tool calls, their results, its replies. Its
// thinking, its plan and its prompt (the user line that starts it) are
// not recorded.
func (t *translator) claudeChildLine(ctx context.Context, tx pgx.Tx, s *Syncer, typ string, line claudeLine, epoch int) error {
	c := t.claudeChildOf(line.ParentToolUseID, line)
	if err := t.startChild(ctx, tx, s, c); err != nil {
		return err
	}
	switch typ {
	case "assistant":
		t.resumeOutput(ctx, tx, s, epoch)
		var text strings.Builder
		for _, b := range line.Message.Content {
			switch {
			case b.Type == "text":
				text.WriteString(b.Text)
			case b.Type == "tool_use" && !slices.Contains(claudePlanTools, b.Name):
				var input map[string]any
				_ = json.Unmarshal(b.Input, &input)
				if err := t.callStarted(ctx, tx, s, c, map[string]any{"tool": claudeToolName(b.Name), "callId": b.ID,
					"input": input, "title": b.Name}); err != nil {
					return err
				}
			}
		}
		if text.Len() == 0 {
			return nil
		}
		var tokens map[string]any
		if u := line.Message.Usage; u != nil {
			tokens = map[string]any{"input": u.InputTokens, "output": u.OutputTokens, "cacheRead": u.CacheRead, "cacheWrite": u.CacheWrite}
		}
		return t.childMessage(ctx, tx, s, c, text.String(), 0, tokens, false)
	case "user":
		for _, b := range line.Message.Content {
			if _, open := c.Calls[b.ToolUseID]; b.Type != "tool_result" || !open {
				continue // a plan call's result, or the subagent's prompt
			}
			st := "completed"
			if b.IsError {
				st = "error"
			}
			payload := map[string]any{"callId": b.ToolUseID, "status": st}
			maps.Copy(payload, claudeOutput(b, line.ToolUseResult))
			if err := t.callEnded(ctx, tx, s, c, payload); err != nil {
				return err
			}
		}
	}
	return nil
}
