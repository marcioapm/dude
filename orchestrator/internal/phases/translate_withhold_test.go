package phases

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// githubReadResult is a GitHub read's result as the tool server returns
// it, longer than the ledger's output cap: its note first, then the
// canary at the start of the quoted body.
func githubReadResult(canary string, escaped bool) string {
	const id = "ab12cd34"
	filler := strings.Repeat("Retries count twice. ", 300)
	out, _ := delivery.GitHubReadJSON(map[string]any{
		"body":     delivery.ExternalText(id, canary+" "+filler),
		"comments": []any{map[string]any{"author": delivery.ExternalText(id, "bo"), "body": delivery.ExternalText(id, filler)}},
		"external": delivery.GitHubReadNote(id),
		"issue":    map[string]any{"number": 7, "title": delivery.ExternalText(id, filler)},
	})
	s := string(out)
	if escaped {
		// As a client writing JSON in ASCII only (Python's json.dumps) has it.
		s = strings.NewReplacer("⟦", `\u27e6`, "⟧", `\u27e7`, "…", `\u2026`).Replace(s)
	}
	return s
}

// A GitHub read's result is withheld from the ledger by what it carries,
// whatever name, status or event the harness reports it under: the
// Run's own call and a child's, a completion, an error, and the updates
// that carry output before it.
func TestGitHubReadOutputIsWithheldWhateverTheHarnessCallsIt(t *testing.T) {
	// Short enough to sit whole in a 200-byte head, after the note.
	const canary = "gh-body-canary"
	result := githubReadResult(canary, false)
	if !strings.Contains(result[:200], canary) {
		t.Fatalf("the canary is past the 200-byte head: %s", result[:200])
	}
	acp := func(status, title, kind string, output string) map[string]any {
		u := map[string]any{"toolCallId": "read1", "status": status, "title": title, "kind": kind}
		if output != "" {
			u["content"] = []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": output}}}
			u["rawOutput"] = map[string]any{"output": output}
		}
		return map[string]any{"type": "acp.tool_call_update", "data": u}
	}
	claudeUse := func(name string) map[string]any {
		return map[string]any{"type": "claude.assistant", "data": map[string]any{"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "read1", "name": name, "input": map[string]any{}}}}}}
	}
	claudeResult := func(isError bool, toolUseResult any) map[string]any {
		data := map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "read1",
			"content": result, "is_error": isError}}}}
		if toolUseResult != nil {
			data["tool_use_result"] = toolUseResult
		}
		return map[string]any{"type": "claude.user", "data": data}
	}
	codexItem := func(typ, status string) map[string]any {
		return map[string]any{"type": "codex." + typ, "data": map[string]any{"item": map[string]any{"type": "mcpToolCall", "id": "read1",
			"server": "github-reader", "tool": "issue_read", "status": status,
			"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": result}}}}}}
	}
	opencodePart := func(tool, status, output string) map[string]any {
		return map[string]any{"type": "opencode.part", "data": map[string]any{"sessionID": "child1", "tool": tool, "callID": "read1",
			"status": status, "output": output, "error": output}}
	}
	opencodeChild := map[string]any{"type": "opencode.session", "data": map[string]any{"id": "child1", "parentID": "parent1"}}
	for _, tc := range []struct {
		name    string
		records []map[string]any
		// the completion event, when the case has one with output
		completed string
	}{
		{"acp named only when pending", []map[string]any{
			acp("pending", "dude_github_issue", "other", ""), acp("completed", "", "other", result)}, evToolCompleted},
		{"acp refusal named only when pending", []map[string]any{
			acp("pending", "dude_github_issue", "other", ""), acp("failed", "", "other", "GitHub refused reading acme/target#7: github 403: "+canary+" "+strings.Repeat("x", 5000))}, evToolCompleted},
		{"acp aliased", []map[string]any{
			acp("in_progress", "read_issue", "fetch", ""), acp("completed", "read_issue", "fetch", result)}, evToolCompleted},
		// A harness that records only the head of a large result.
		{"acp aliased cut at 200 bytes", []map[string]any{
			acp("in_progress", "read_issue", "fetch", ""), acp("completed", "read_issue", "fetch", result[:200])}, ""},
		{"acp described title", []map[string]any{
			acp("in_progress", "Read issue 7 of acme/target", "other", ""), acp("completed", "", "other", result)}, evToolCompleted},
		{"acp failed", []map[string]any{acp("in_progress", "github", "other", ""), acp("failed", "", "other", result)}, evToolCompleted},
		{"acp progress carrying output", []map[string]any{
			acp("in_progress", "read_issue", "other", ""), acp("in_progress", "read_issue", "other", result), acp("completed", "", "other", "")}, ""},
		{"claude aliased", []map[string]any{claudeUse("mcp__dude-staging__read_issue"), claudeResult(false, nil)}, evToolCompleted},
		{"claude error", []map[string]any{claudeUse("mcp__gh__issue"), claudeResult(true, nil)}, evToolCompleted},
		{"claude bash through the socket", []map[string]any{claudeUse("Bash"),
			claudeResult(false, map[string]any{"stdout": result, "stderr": ""})}, evToolCompleted},
		{"codex aliased", []map[string]any{codexItem("item/completed", "completed")}, evToolCompleted},
		{"codex failed", []map[string]any{codexItem("item/completed", "failed")}, evToolCompleted},
		{"codex started carrying output", []map[string]any{codexItem("item/started", "inProgress")}, ""},
		{"opencode child aliased", []map[string]any{opencodeChild, opencodePart("gh_issue", "completed", result)}, evChildToolCompleted},
		{"opencode child error", []map[string]any{opencodeChild, opencodePart("gh_issue", "error", result)}, evChildToolCompleted},
		{"opencode child running carrying output", []map[string]any{opencodeChild, opencodePart("gh_issue", "running", result)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			w.feed(tc.records...)
			events := w.events()
			encoded, _ := json.Marshal(events)
			if strings.Contains(string(encoded), canary) {
				t.Fatalf("GitHub's text reached the ledger: %s", encoded)
			}
			if tc.completed == "" {
				return
			}
			completed := ofType(events, tc.completed)
			if len(completed) != 1 {
				t.Fatalf("%d %s events: %s", len(completed), tc.completed, encoded)
			}
			for _, k := range []string{"stdout", "stderr"} {
				if _, ok := completed[0].Payload[k]; ok {
					t.Errorf("%s kept: %v", k, completed[0].Payload)
				}
			}
			out, _ := completed[0].Payload["output"].(map[string]any)
			if out["withheld"] != true || out["bytes"].(float64) < 5000 {
				t.Fatalf("not withheld as {bytes, withheld}: %v", completed[0].Payload)
			}
		})
	}
}

// Output that is not a GitHub read is kept, even when it names the
// tools, quotes text the same way, or holds the note's words alone.
func TestOtherOutputIsKeptBesideGitHubReads(t *testing.T) {
	for _, output := range []string{
		"go test ./internal/agenttools -run github_issue: ok",
		delivery.ExternalText("ab12cd34", "a quote that is not a read"),
		"Text from GitHub is between brackets, the docs say.",
	} {
		w := newHarnessWorld(t)
		w.feed(map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": "c1", "title": "bash", "kind": "execute",
			"status": "completed", "rawOutput": map[string]any{"output": output}}})
		completed := ofType(w.events(), evToolCompleted)
		if len(completed) != 1 || completed[0].Payload["output"].(map[string]any)["head"] != output {
			t.Errorf("%q: %v", output, completed)
		}
	}
}

// A client that writes JSON in ASCII only escapes the marker: the result
// is still known for a read's.
func TestAnEscapedGitHubReadIsWithheld(t *testing.T) {
	const canary = "private-github-body-canary"
	w := newHarnessWorld(t)
	w.feed(map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": "c1", "title": "bash", "kind": "execute",
		"status": "completed", "rawOutput": map[string]any{"output": githubReadResult(canary, true)}}})
	encoded, _ := json.Marshal(w.events())
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("GitHub's text reached the ledger: %s", encoded)
	}
}
