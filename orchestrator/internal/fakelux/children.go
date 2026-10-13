package fakelux

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"time"
	"unicode/utf8"
)

// A child session (a subagent) of the Run's agent, said as each harness
// says it, driven step by step by a test (POST /fake/runs/{id}/child):
// the parent's call that starts it, its tool calls, its messages, a
// permission it waits on, its end. The shapes are the real ones
// (orchestrator/internal/phases/testdata/*-child.jsonl): OpenCode's ACP
// for the parent's call and lux's opencode.* records for the child;
// Claude Code's lines with parent_tool_use_id and its system task_*
// lines; Codex's notifications on the child's own thread.

// ChildStep is one thing a child does. Op is start, tool, message, wait
// or end; CallID names the parent's call and ChildID the child (a
// session, a task, a thread), the same on every step of one child.
type ChildStep struct {
	Op        string `json:"op"`
	CallID    string `json:"callId"`
	ChildID   string `json:"childId"`
	Title     string `json:"title,omitempty"`
	AgentType string `json:"agentType,omitempty"`
	// tool: the child's call, OpenCode's tool name (bash, read, grep…),
	// its input, and with Done its completion and output (empty is an
	// output). message: its text.
	ToolCallID string         `json:"toolCallId,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Done       bool           `json:"done,omitempty"`
	Output     string         `json:"output,omitempty"`
	Text       string         `json:"text,omitempty"`
	// wait: the permission asked and its patterns; Claude Code and Codex
	// ask none, and say nothing.
	Permission string   `json:"permission,omitempty"`
	Patterns   []string `json:"patterns,omitempty"`
	// end: completed, failed or interrupted.
	Status string `json:"status,omitempty"`
}

// Child says one step of a child of Run id's agent as its harness would.
func (s *Server) Child(id string, step ChildStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil {
		return fmt.Errorf("no such run")
	}
	s.childStep(run, step)
	return nil
}

// childStep says one step of a child in the Run's harness's shapes.
// Callers hold s.mu.
func (s *Server) childStep(run *Run, step ChildStep) {
	switch run.dialect() {
	case dialectClaude:
		s.claudeChild(run, step)
	case dialectCodex:
		s.codexChild(run, step)
	default:
		s.opencodeChild(run, step)
	}
}

func (s *Server) childHandler(w http.ResponseWriter, r *http.Request) {
	var step ChildStep
	if err := json.NewDecoder(r.Body).Decode(&step); err != nil || step.CallID == "" || step.ChildID == "" {
		writeErr(w, 400, "bad_request", "a child step needs op, callId and childId")
		return
	}
	if err := s.Child(r.PathValue("id"), step); err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"said": step.Op})
}

func (st ChildStep) status() string {
	if st.Status == "" {
		return "completed"
	}
	return st.Status
}

// opencodeChild: the parent's task call over ACP (kind think, the
// description as title), the child as lux relays OpenCode's bus.
func (s *Server) opencodeChild(run *Run, st ChildStep) {
	oc := func(typ string, data map[string]any) { s.recordEvent(run, "opencode."+typ, data) }
	input := map[string]any{"description": st.Title, "prompt": st.Title, "subagent_type": st.AgentType}
	switch st.Op {
	case "start":
		s.recordEvent(run, "acp.tool_call", map[string]any{"sessionUpdate": "tool_call", "toolCallId": st.CallID, "title": "task",
			"kind": "think", "status": "pending", "rawInput": map[string]any{}})
		s.recordEvent(run, "acp.tool_call_update", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": st.CallID,
			"status": "in_progress", "kind": "think", "title": st.Title, "rawInput": input})
		oc("session", map[string]any{"id": st.ChildID, "parentID": run.SessionID, "title": st.Title + " (@" + st.AgentType + " subagent)"})
		oc("task", map[string]any{"sessionID": run.SessionID, "callID": st.CallID, "childSessionID": st.ChildID,
			"model": opencodeModel, "status": "running"})
	case "tool":
		part := map[string]any{"sessionID": st.ChildID, "messageID": "msg_" + st.ToolCallID, "callID": st.ToolCallID, "tool": st.Tool,
			"status": "running", "title": st.Tool}
		if in, truncated := clippedInput(st.Input); in != "" {
			part["input"] = in
			if truncated {
				part["truncated"] = true
			}
		}
		oc("part", part)
		if st.Done {
			done := map[string]any{}
			for k, v := range part {
				done[k] = v
			}
			done["status"], done["output"] = "completed", st.Output
			oc("part", done)
		}
	case "message":
		oc("message", map[string]any{"sessionID": st.ChildID, "messageID": "msg_" + st.ChildID, "role": "assistant", "text": st.Text,
			"cost": 0.002, "tokens": map[string]any{"input": 5, "output": 4, "reasoning": 0, "cacheRead": 7979, "cacheWrite": 137}})
	case "wait":
		// lux says an ask nothing answered twice, together: the record, and
		// a warning for a person.
		oc("permission", map[string]any{"sessionID": st.ChildID, "permissionID": "per_" + st.ChildID, "permission": st.Permission,
			"patterns": st.Patterns, "callID": st.ToolCallID, "answered": "none"})
		s.recordEvent(run, "lux.warning", map[string]any{"message": fmt.Sprintf(
			"an OpenCode subagent (%s) asked for permission %s; nothing can answer it", st.ChildID, st.Permission)})
	case "end":
		status := "completed"
		if st.status() != "completed" {
			status = "error"
		}
		oc("task", map[string]any{"sessionID": run.SessionID, "callID": st.CallID, "childSessionID": st.ChildID,
			"model": opencodeModel, "status": status})
		s.recordEvent(run, "acp.tool_call_update", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": st.CallID,
			"status": "completed", "title": st.Title, "content": []any{map[string]any{"type": "content", "content": map[string]any{
				"type": "text", "text": fmt.Sprintf("<task id=%q state=%q>\n<task_result>\n%s\n</task_result>\n</task>", st.ChildID, status, st.Text)}}}})
	}
}

// opencodeModel is a child's model as lux relays it: OpenCode's object.
var opencodeModel = map[string]any{"modelID": "claude-haiku-4.5", "providerID": "llmproxy-anthropic"}

// clippedInput is a part's input as lux relays it: its JSON, cut at 300
// bytes on a character boundary, and whether it was cut; "" for none.
func clippedInput(in map[string]any) (string, bool) {
	if len(in) == 0 {
		return "", false
	}
	b, _ := json.Marshal(in)
	if len(b) <= 300 {
		return string(b), false
	}
	cut := 300
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]), true
}

// claudeTools are OpenCode's tool names as Claude Code's.
func claudeName(tool string) string {
	if name := claudeTools[tool]; name != "" {
		return name
	}
	return tool
}

// claudeChild: the parent's Agent call, the child's lines marked with its
// id, and Claude Code's system task_* lines.
func (s *Server) claudeChild(run *Run, st ChildStep) {
	line := func(typ string, data map[string]any) {
		data["type"], data["session_id"] = typ, run.SessionID
		s.recordEvent(run, "claude."+typ, data)
	}
	child := func(typ string, content []any) {
		line(typ, map[string]any{"parent_tool_use_id": st.CallID, "subagent_type": st.AgentType, "task_description": st.Title,
			"message": map[string]any{"role": typ, "content": content}})
	}
	switch st.Op {
	case "start":
		line("assistant", map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": st.CallID, "name": "Agent",
			"input": map[string]any{"description": st.Title, "prompt": st.Title, "subagent_type": st.AgentType}}}}})
		line("system", map[string]any{"subtype": "task_started", "task_id": st.ChildID, "tool_use_id": st.CallID,
			"description": st.Title, "subagent_type": st.AgentType, "task_type": "local_agent", "prompt": st.Title})
		child("user", []any{map[string]any{"type": "text", "text": st.Title}})
	case "tool":
		child("assistant", []any{map[string]any{"type": "tool_use", "id": st.ToolCallID, "name": claudeName(st.Tool), "input": st.Input}})
		if st.Done {
			child("user", []any{map[string]any{"type": "tool_result", "tool_use_id": st.ToolCallID, "content": st.Output}})
		}
		line("system", map[string]any{"subtype": "task_progress", "task_id": st.ChildID, "tool_use_id": st.CallID,
			"description": st.Title, "subagent_type": st.AgentType, "last_tool_name": claudeName(st.Tool),
			"usage": map[string]any{"total_tokens": 15218, "tool_uses": 1, "duration_ms": 3689}})
	case "message":
		child("assistant", []any{map[string]any{"type": "text", "text": st.Text}})
	case "end":
		line("system", map[string]any{"subtype": "task_updated", "task_id": st.ChildID, "patch": map[string]any{"status": st.status()}})
		line("system", map[string]any{"subtype": "task_notification", "task_id": st.ChildID, "tool_use_id": st.CallID,
			"status": st.status(), "summary": st.Text, "usage": map[string]any{"total_tokens": 15136, "tool_uses": 1, "duration_ms": 4971}})
		line("user", map[string]any{"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result",
			"tool_use_id": st.CallID, "content": []any{map[string]any{"type": "text", "text": st.Text}}}}},
			"tool_use_result": map[string]any{"status": st.status(), "agentId": st.ChildID, "agentType": st.AgentType}})
	}
}

// codexChild: the parent's spawnAgent call and the subAgentActivity tying
// the agent's thread to it, the agent's own notifications on its thread,
// and the parent's wait on it, open until it ends.
func (s *Server) codexChild(run *Run, st ChildStep) {
	turn := fmt.Sprintf("turn-%d", run.say().turn)
	on := func(thread, method string, params map[string]any) {
		params["threadId"] = thread
		s.recordEvent(run, "codex."+method, params)
	}
	collab := func(id, tool, status string, receivers []any) map[string]any {
		return map[string]any{"type": "collabAgentToolCall", "id": id, "tool": tool, "status": status, "senderThreadId": run.SessionID,
			"receiverThreadIds": receivers, "prompt": st.Title, "model": "gpt-6-sol", "agentsStates": map[string]any{}}
	}
	wait := "wait_" + st.CallID
	switch st.Op {
	case "start":
		on(run.SessionID, "item/started", map[string]any{"turnId": turn, "item": collab(st.CallID, "spawnAgent", "inProgress", []any{})})
		on(run.SessionID, "item/completed", map[string]any{"turnId": turn, "item": map[string]any{"type": "subAgentActivity",
			"id": st.CallID, "kind": "started", "agentThreadId": st.ChildID, "agentPath": "/root/" + st.AgentType}})
		on(run.SessionID, "item/completed", map[string]any{"turnId": turn,
			"item": collab(st.CallID, "spawnAgent", "completed", []any{st.ChildID})})
		on(st.ChildID, "turn/started", map[string]any{"turn": map[string]any{"id": "turn-" + st.ChildID, "status": "inProgress"}})
		on(run.SessionID, "item/started", map[string]any{"turnId": turn, "item": collab(wait, "wait", "inProgress", []any{st.ChildID})})
	case "tool":
		cmd := fmt.Sprint(st.Input["command"])
		item := map[string]any{"type": "commandExecution", "id": st.ToolCallID, "command": "/bin/zsh -lc '" + cmd + "'",
			"commandActions": []any{map[string]any{"type": "unknown", "command": cmd}}, "status": "inProgress"}
		on(st.ChildID, "item/started", map[string]any{"item": item})
		if st.Done {
			done := map[string]any{}
			for k, v := range item {
				done[k] = v
			}
			done["status"], done["aggregatedOutput"], done["exitCode"] = "completed", st.Output, 0
			on(st.ChildID, "item/completed", map[string]any{"item": done})
		}
	case "message":
		on(st.ChildID, "item/completed", map[string]any{"item": map[string]any{"type": "agentMessage", "id": "msg_" + st.ChildID,
			"text": st.Text, "phase": "final_answer"}})
		on(st.ChildID, "thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"last": map[string]any{
			"inputTokens": 8426, "cachedInputTokens": 6912, "outputTokens": 5}, "modelContextWindow": 353400}})
	case "end":
		on(st.ChildID, "turn/completed", map[string]any{"turn": map[string]any{"id": "turn-" + st.ChildID, "status": st.status()}})
		on(run.SessionID, "item/completed", map[string]any{"turnId": turn, "item": collab(wait, "wait", "completed", []any{st.ChildID})})
	}
}

// playSubagent is lux-fake's script line `subagent` or `subagent ask` as
// lux relays it from lux-fake's OpenCode bus (lux cmd/lux-fake/subagent.go
// and internal/adapter/opencode_children.go), record for record: no ACP
// call for the task (lux-fake says none), a child that reads a.txt and
// runs bash, then the task's completion. Ids are lux-fake's:
// ses_<parent>_child<n>, toolu_task<n>, toolu_read<n>, toolu_bash<n>. With
// ask, the bash call asks permission bash for "cat /etc/hostname"; lux
// answers a child's ask itself, so it is recorded allowed, with its
// warning, and the call completes. Callers hold s.mu.
func (s *Server) playSubagent(run *Run, line string) {
	if line == "" {
		return
	}
	run.children++
	n := run.children
	parent := run.SessionID
	child, task := fmt.Sprintf("ses_%s_child%d", parent, n), fmt.Sprintf("toolu_task%d", n)
	read, bash, msg := fmt.Sprintf("toolu_read%d", n), fmt.Sprintf("toolu_bash%d", n), fmt.Sprintf("msg_%s_%d", parent, n)
	now := time.Now().UnixMilli()
	oc := func(typ string, data map[string]any) { s.recordEvent(run, "opencode."+typ, data) }
	model := map[string]any{"modelID": "lux-fake", "providerID": "lux-fake"}
	part := func(call, tool, status, input string, done map[string]any) {
		p := map[string]any{"sessionID": child, "messageID": msg, "callID": call, "tool": tool, "status": status, "input": input,
			"time": map[string]any{"start": now}}
		if done != nil {
			p["time"] = map[string]any{"start": now, "end": now}
			maps.Copy(p, done)
		}
		oc("part", p)
	}
	readIn, bashIn := `{"filePath":"a.txt"}`, `{"command":"cat /etc/hostname","description":"Read the hostname"}`

	oc("session", map[string]any{"id": child, "parentID": parent, "title": "fake subagent (@general subagent)", "createdAt": now})
	oc("task", map[string]any{"sessionID": parent, "callID": task, "childSessionID": child, "model": model, "status": "running"})
	part(read, "read", "running", readIn, nil)
	part(read, "read", "completed", readIn, map[string]any{"title": "a.txt", "output": "<path>a.txt</path>\n<content>\n1: hello\n</content>"})
	part(bash, "bash", "running", bashIn, nil)
	if line == "subagent ask" {
		oc("permission", map[string]any{"sessionID": child, "permissionID": fmt.Sprintf("per_fake%d", n), "permission": "bash",
			"patterns": []any{"cat /etc/hostname"}, "callID": bash, "answered": "allow"})
		s.recordEvent(run, "lux.warning", map[string]any{"message": fmt.Sprintf(
			"opencode: a subagent session %s asked for bash on cat /etc/hostname; lux allowed it", child)})
	}
	part(bash, "bash", "completed", bashIn, map[string]any{"title": "Read the hostname", "output": "fakehost\n"})
	oc("message", map[string]any{"sessionID": child, "messageID": msg, "role": "assistant", "text": "the child read a.txt: hello",
		"cost": 0.001, "tokens": map[string]any{"input": 10, "output": 5, "reasoning": 0, "cacheRead": 2, "cacheWrite": 1}, "completed": now})
	oc("task", map[string]any{"sessionID": parent, "callID": task, "childSessionID": child, "model": model, "status": "completed"})
}
