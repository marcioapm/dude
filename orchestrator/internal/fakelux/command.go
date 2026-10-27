package fakelux

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// withChecks gives a writer's behaviour the checks report its role asks
// for when the test that decided it named none (Behaviour.NoChecksReport).
func (b *Behaviour) withChecks(spec map[string]any) {
	labels, _ := spec["labels"].(map[string]any)
	switch labels["dude.phase"] {
	case "implement", "fix", "simplify":
	default:
		return
	}
	if b.Checks == "" && !b.NoChecksReport {
		b.Checks = fakeagent.NoChecks
	}
}

// atCommits writes into a scripted command its first repository's main as
// the fake's repository has it now (fakeagent.AtMain). Callers hold s.mu.
func (s *Server) atCommits(run *Run, command string) string {
	if !strings.Contains(command, fakeagent.AtMain) {
		return command
	}
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	repos := specRepos(spec)
	if len(repos) == 0 {
		return command
	}
	return strings.ReplaceAll(command, fakeagent.AtMain, head(s.repoPath(repos[0].URL), "main"))
}

// command plays one shell command the agent runs, c = [command, output,
// exit code], as its harness reports it: OpenCode over ACP with the exit
// in rawOutput.metadata.exit and status failed when non-zero; Codex as a
// commandExecution with its exitCode; Claude Code as a Bash tool_use whose
// result has is_error and content beginning "Exit code N" when it failed,
// as Claude Code 2.1.207 does (phases/testdata/claude-bash-exit.ndjson).
// Callers hold s.mu.
func (s *Server) command(run *Run, id string, c [3]string) {
	exit, _ := strconv.Atoi(c[2])
	input := map[string]any{"command": c[0], "description": "Run " + c[0]}
	switch run.dialect() {
	case dialectClaude:
		s.sayFlush(run)
		s.recordEvent(run, "claude.assistant", map[string]any{"type": "assistant", "session_id": run.SessionID,
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": input}}}})
		content, result := c[1], any(map[string]any{"stdout": c[1], "stderr": "", "interrupted": false})
		if exit != 0 {
			content = "Exit code " + c[2] + "\n" + c[1]
			result = "Error: " + content
		}
		s.recordEvent(run, "claude.user", map[string]any{"type": "user", "session_id": run.SessionID,
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result",
				"tool_use_id": id, "content": content, "is_error": exit != 0}}},
			"tool_use_result": result})
	case dialectCodex:
		s.sayFlush(run)
		item := map[string]any{"type": "commandExecution", "id": id, "command": "/bin/bash -lc '" + c[0] + "'",
			"commandActions": []any{map[string]any{"command": c[0], "type": "unknown"}}, "status": "inProgress"}
		s.codexItem(run, "item/started", item)
		status := "completed"
		if exit != 0 {
			status = "failed"
		}
		done := map[string]any{}
		for k, v := range item {
			done[k] = v
		}
		done["status"], done["aggregatedOutput"], done["exitCode"] = status, c[1], exit
		s.codexItem(run, "item/completed", done)
	default:
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": "bash", "kind": "execute",
			"status": "in_progress", "rawInput": input})
		status := "completed"
		if exit != 0 {
			status = "failed"
		}
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": status,
			"content":   []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": c[1]}}},
			"rawOutput": map[string]any{"output": c[1], "metadata": map[string]any{"output": c[1], "exit": exit, "truncated": false}}})
	}
}
