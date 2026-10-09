package fakelux

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The scripted agent's turns are written once, as the ACP updates
// OpenCode's adapter relays (agent, turnEnd). A Run whose spec names the
// claude-code or codex adapter is the same agent on that harness: each
// update is said as lux's adapter for it relays the harness's own output —
// claude.* lines (Claude Code's stream-json, whole messages) or codex.*
// notifications (Codex's app-server items) — in the shapes of lux's
// adapters (lux internal/adapter/claude.go, codex.go) and the CLIs' real
// output (orchestrator/internal/phases/testdata/*-real.jsonl).

const (
	dialectACP    = "acp"
	dialectClaude = "claude-code"
	dialectCodex  = "codex"
)

// dialect is how the Run's agent speaks, by its spec's adapter.
func (r *Run) dialect() string {
	if r.speaks == "" {
		var spec struct {
			Workload struct {
				Adapter string `json:"adapter"`
			} `json:"workload"`
		}
		_ = json.Unmarshal(r.Spec, &spec)
		r.speaks = dialectACP
		if spec.Workload.Adapter == dialectClaude || spec.Workload.Adapter == dialectCodex {
			r.speaks = spec.Workload.Adapter
		}
	}
	return r.speaks
}

// harnessSay is what the agent is saying or thinking on Claude Code or
// Codex, gathered from its chunks: both harnesses send a message whole.
type harnessSay struct {
	message, thought strings.Builder
	// The tools started and not finished, by call id: [title, input JSON].
	tools map[string][2]string
	// Claude Code's running cost for this process.
	cost float64
	turn int
}

// agent records one of the agent's updates, as its harness says it.
// Callers hold s.mu.
func (s *Server) agent(run *Run, update map[string]any) {
	switch run.dialect() {
	case dialectClaude, dialectCodex:
		s.harnessUpdate(run, update)
	default:
		s.recordEvent(run, "acp."+update["sessionUpdate"].(string), update)
	}
}

// turnEnd records the end of the agent's turn: the prompt's response, as
// the ACP adapter relays it, or each harness's own, and with idle the agent
// going idle after it. data is ACP's ({stopReason, usage?, error?}).
// Claude Code's result line is relayed as lux does: claude.turn_end, the
// idle, then the line itself as claude.result. Callers hold s.mu.
func (s *Server) turnEnd(run *Run, data map[string]any, idle bool) {
	switch run.dialect() {
	case dialectClaude:
		s.sayFlush(run)
		say := run.say()
		errText, _ := data["error"].(string)
		if errText != "" {
			s.recordEvent(run, "claude.assistant", map[string]any{"type": "assistant", "session_id": run.SessionID, "error": "unknown",
				"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": errText}}}})
		}
		result := map[string]any{"type": "result", "subtype": "success", "session_id": run.SessionID, "is_error": errText != ""}
		if errText != "" {
			result["result"] = errText
		}
		end := map[string]any{}
		if u, ok := data["usage"].(map[string]any); ok {
			say.cost += 0.01
			usage := map[string]any{"input_tokens": u["inputTokens"], "output_tokens": u["outputTokens"],
				"cache_read_input_tokens": u["cachedReadTokens"], "cache_creation_input_tokens": u["cachedWriteTokens"]}
			result["usage"], end["usage"] = usage, usage
		}
		result["total_cost_usd"] = say.cost
		s.recordEvent(run, "claude.turn_end", end)
		if idle {
			s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
		}
		s.recordEvent(run, "claude.result", result)
		return
	case dialectCodex:
		s.sayFlush(run)
		say := run.say()
		status := "completed"
		switch {
		case data["error"] != nil && data["error"] != "":
			status = "failed"
			s.recordEvent(run, "codex.error", map[string]any{"threadId": run.SessionID, "willRetry": false,
				"error": map[string]any{"message": data["error"]}})
		case data["stopReason"] == "cancelled":
			status = "interrupted"
		}
		end := map[string]any{"status": status}
		if u, ok := data["usage"].(map[string]any); ok {
			cached, _ := u["cachedReadTokens"].(int)
			in, _ := u["inputTokens"].(int)
			usage := map[string]any{
				"last":               map[string]any{"inputTokens": in + cached, "cachedInputTokens": cached, "outputTokens": u["outputTokens"]},
				"modelContextWindow": 200000}
			s.recordEvent(run, "codex.thread/tokenUsage/updated", map[string]any{"threadId": run.SessionID, "tokenUsage": usage})
			end["usage"] = usage
		}
		s.recordEvent(run, "codex.turn_end", end)
		s.recordEvent(run, "codex.turn/completed", map[string]any{"threadId": run.SessionID,
			"turn": map[string]any{"id": fmt.Sprintf("turn-%d", say.turn), "status": status}})
		say.turn++
	default:
		s.recordEvent(run, "acp.turn_end", data)
	}
	if idle {
		s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
	}
}

func (r *Run) say() *harnessSay {
	if r.harness == nil {
		r.harness = &harnessSay{tools: map[string][2]string{}}
	}
	return r.harness
}

// harnessUpdate says one ACP update as Claude Code or Codex would.
// Callers hold s.mu.
func (s *Server) harnessUpdate(run *Run, u map[string]any) {
	say := run.say()
	text := func() string {
		c, _ := u["content"].(map[string]any)
		t, _ := c["text"].(string)
		return t
	}
	switch u["sessionUpdate"] {
	case "agent_message_chunk":
		if say.thought.Len() > 0 {
			s.sayFlush(run)
		}
		say.message.WriteString(text())
		return
	case "agent_thought_chunk":
		if say.message.Len() > 0 {
			s.sayFlush(run)
		}
		say.thought.WriteString(text())
		return
	case "usage_update":
		return
	}
	s.sayFlush(run)
	id, _ := u["toolCallId"].(string)
	switch u["status"] {
	case "in_progress":
		if _, open := say.tools[id]; open {
			return
		}
		title, _ := u["title"].(string)
		input, _ := json.Marshal(u["rawInput"])
		say.tools[id] = [2]string{title, string(input)}
		s.harnessTool(run, id, title, u["rawInput"], false, "")
	case "completed", "failed":
		call, ok := say.tools[id]
		if !ok {
			return
		}
		delete(say.tools, id)
		var input any
		_ = json.Unmarshal([]byte(call[1]), &input)
		raw, _ := u["rawOutput"].(map[string]any)
		out, _ := raw["output"].(string)
		s.harnessTool(run, id, call[0], input, true, out)
	}
}

// sayFlush says what the agent has been saying or thinking, whole.
// Callers hold s.mu.
func (s *Server) sayFlush(run *Run) {
	say := run.say()
	thought, message := say.thought.String(), say.message.String()
	say.thought.Reset()
	say.message.Reset()
	if run.dialect() == dialectClaude {
		var blocks []any
		if thought != "" {
			blocks = append(blocks, map[string]any{"type": "thinking", "thinking": thought, "signature": "fake"})
		}
		if message != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": message})
		}
		if len(blocks) > 0 {
			s.recordEvent(run, "claude.assistant", map[string]any{"type": "assistant", "session_id": run.SessionID,
				"message": map[string]any{"role": "assistant", "content": blocks,
					"usage": map[string]any{"input_tokens": 12, "cache_read_input_tokens": 900, "cache_creation_input_tokens": 88, "output_tokens": 34}}})
		}
		return
	}
	if thought != "" {
		s.codexItem(run, "item/completed", map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_%d", len(run.records)),
			"summary": []any{thought}, "content": []any{}})
	}
	if message != "" {
		s.codexItem(run, "item/completed", map[string]any{"type": "agentMessage", "id": fmt.Sprintf("msg_%d", len(run.records)),
			"text": message, "phase": "final_answer"})
	}
}

// claudeTools are the scripted agent's tool titles (OpenCode's) as Claude
// Code names them.
var claudeTools = map[string]string{"bash": "Bash", "read": "Read", "write": "Write", "edit": "Edit", "todowrite": "TodoWrite", "task": "Task"}

// harnessTool says a tool call starting, or done with its output, as each
// harness reports it. Callers hold s.mu.
func (s *Server) harnessTool(run *Run, id, title string, input any, done bool, out string) {
	if run.dialect() == dialectClaude {
		if !done {
			name := claudeTools[title]
			if name == "" {
				name = title
			}
			s.recordEvent(run, "claude.assistant", map[string]any{"type": "assistant", "session_id": run.SessionID,
				"message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}})
			return
		}
		line := map[string]any{"type": "user", "session_id": run.SessionID, "message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": out, "is_error": false}}}}
		if title == "bash" {
			line["tool_use_result"] = map[string]any{"stdout": out, "stderr": "", "interrupted": false}
		}
		s.recordEvent(run, "claude.user", line)
		return
	}
	args, _ := input.(map[string]any)
	if title == "todowrite" {
		if done {
			return
		}
		todos, _ := args["todos"].([]any)
		var plan []any
		for _, t := range todos {
			m, _ := t.(map[string]any)
			status, _ := m["status"].(string)
			if status == "in_progress" {
				status = "inProgress"
			}
			plan = append(plan, map[string]any{"step": m["content"], "status": status})
		}
		s.recordEvent(run, "codex.turn/plan/updated", map[string]any{"threadId": run.SessionID,
			"turnId": fmt.Sprintf("turn-%d", run.say().turn), "plan": plan})
		return
	}
	status := "inProgress"
	if done {
		status = "completed"
	}
	var item map[string]any
	switch title {
	case "bash":
		cmd, _ := args["cmd"].(string)
		if c, ok := args["command"].(string); ok {
			cmd = c
		}
		item = map[string]any{"type": "commandExecution", "id": id, "command": "/bin/bash -lc '" + cmd + "'",
			"commandActions": []any{map[string]any{"command": cmd, "type": "unknown"}}, "status": status}
		if done {
			item["aggregatedOutput"], item["exitCode"] = out, 0
		}
	case "write", "edit":
		path, _ := args["filePath"].(string)
		content, _ := args["content"].(string)
		item = map[string]any{"type": "fileChange", "id": id, "status": status, "changes": []any{map[string]any{
			"path": path, "kind": map[string]any{"type": "add"}, "diff": content}}}
	default:
		item = map[string]any{"type": "dynamicToolCall", "id": id, "tool": title, "arguments": input, "status": status}
	}
	method := "item/started"
	if done {
		method = "item/completed"
	}
	s.codexItem(run, method, item)
}

func (s *Server) codexItem(run *Run, method string, item map[string]any) {
	s.recordEvent(run, "codex."+method, map[string]any{"threadId": run.SessionID,
		"turnId": fmt.Sprintf("turn-%d", run.say().turn), "item": item})
}
