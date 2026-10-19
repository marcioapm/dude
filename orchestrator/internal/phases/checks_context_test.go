package phases

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// longFailure is a failing command's output past the ledger's cap: its
// head is noise, and how it ended is at its end, which the context keeps.
var longFailure = strings.Repeat("compiling module ...\n", 400) + "FAIL: TestGreeting\nexit status 2"

// claudeCapture is the real Claude Code 2.1.207 stream-json capture of one
// failing (exit 3) and one passing Bash call, as lux's adapter relays it.
func claudeCapture(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open("testdata/claude-bash-exit.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		typ, _ := line["type"].(string)
		out = append(out, map[string]any{"type": "claude." + typ, "data": line})
	}
	return out
}

// The judge is shown each recorded call and its completion, in order: a
// failing command's status, its exit code where the harness reports one,
// and the end of its output; in every dialect dude speaks.
func TestTheChecksContextCarriesEachDialectsFailingCommand(t *testing.T) {
	opencode := []map[string]any{
		{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "b1", "title": "bash", "kind": "execute",
			"status": "in_progress", "rawInput": map[string]any{"command": "make check"}}},
		{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "b1", "status": "failed",
			"rawOutput": map[string]any{"output": longFailure, "metadata": map[string]any{"output": longFailure, "exit": 2}}}},
	}
	codex := []map[string]any{
		{"type": "codex.item/started", "data": map[string]any{"item": map[string]any{"type": "commandExecution", "id": "c1",
			"command": "/bin/bash -lc 'make check'", "commandActions": []any{}, "status": "inProgress"}}},
		{"type": "codex.item/completed", "data": map[string]any{"item": map[string]any{"type": "commandExecution", "id": "c1",
			"command": "/bin/bash -lc 'make check'", "commandActions": []any{}, "status": "failed", "exitCode": 2, "aggregatedOutput": longFailure}}},
	}
	for _, tc := range []struct {
		name    string
		records []map[string]any
		want    []string
	}{
		{"opencode", opencode, []string{`- bash {"command":"make check"}`, "status: error, exit code 2", "exit status 2"}},
		{"codex", codex, []string{`- bash {"command":"make check"}`, "status: error, exit code 2", "exit status 2"}},
		// Claude Code reports no exit code for Bash: a failure is an error
		// whose output begins "Exit code N"; a pass is completed.
		{"claude-code", claudeCapture(t), []string{"exit 3'", "status: error\n  output:\n    Exit code 3\n    out-a\n    err-a",
			`"command":"echo out-b"`, "status: completed\n  stdout:\n    out-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			w.feed(tc.records...)
			text, err := w.s.checksContext(context.Background(), w.run, []delivery.Check{{Command: "make check", Result: "pass"}})
			if err != nil {
				t.Fatal(err)
			}
			_, calls, _ := strings.Cut(text, "### The tool calls this Run recorded, in order\n")
			for _, want := range tc.want {
				if !strings.Contains(calls, want) {
					t.Errorf("the context's calls lack %q:\n%s", want, clip(calls))
				}
			}
			if tc.name != "claude-code" && strings.Count(calls, "compiling module") > 250 {
				t.Error("the context carries the whole of a long output, not its capped head and tail")
			}
		})
	}
}

// The context is bounded: a Run of many long calls keeps the latest ones,
// says what it left out, and never drops the report.
func TestTheChecksContextIsBoundedKeepingTheLatestCalls(t *testing.T) {
	w := newHarnessWorld(t)
	var records []map[string]any
	for i := range 60 {
		id := "b" + strings.Repeat("x", i%3) + string(rune('A'+i%26)) + string(rune('a'+i/26))
		records = append(records,
			map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": "bash",
				"kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": "step " + id}}},
			map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id,
				"status": "completed", "rawOutput": map[string]any{"output": longFailure, "metadata": map[string]any{"exit": 0}}}})
	}
	w.feed(records...)
	text, err := w.s.checksContext(context.Background(), w.run, []delivery.Check{{Command: "make check", Result: "pass", Evidence: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > 100<<10 {
		t.Errorf("context of %d bytes, over its bound", len(text))
	}
	if !strings.Contains(text, `"command":"make check"`) || !strings.Contains(text, "earlier calls left out for length") {
		t.Errorf("the report or the omission note is missing:\n%s", clip(text))
	}
	last := records[len(records)-2]["data"].(map[string]any)["toolCallId"].(string)
	if !strings.Contains(text, `"command":"step `+last+`"`) {
		t.Errorf("the latest call is not in the context")
	}
	if strings.Contains(text, `"command":"step `+records[0]["data"].(map[string]any)["toolCallId"].(string)+`"`) {
		t.Errorf("the first call was kept over the bound")
	}
}

// A Run of more calls than the bound shows its newest whole calls, in
// order, and counts exactly the earlier ones it left out.
func TestTheChecksContextKeepsTheNewestCallsAndCountsTheRest(t *testing.T) {
	const n = 200
	w := newHarnessWorld(t)
	var records []map[string]any
	for i := range n {
		id := fmt.Sprintf("call%03d", i)
		records = append(records,
			map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": "bash",
				"kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": "step " + id}}},
			map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id,
				"status": "completed", "rawOutput": map[string]any{"output": strings.Repeat("ok\n", 300), "metadata": map[string]any{"exit": 0}}}})
	}
	w.feed(records...)
	text, err := w.s.checksContext(context.Background(), w.run, []delivery.Check{{Command: "make check", Result: "pass"}})
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\n\((\d+) earlier calls left out for length`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no omission count:\n%s", clip(text))
	}
	omitted, _ := strconv.Atoi(m[1])
	var shown []int
	for _, m := range regexp.MustCompile(`"command":"step call(\d{3})"`).FindAllStringSubmatch(text, -1) {
		i, _ := strconv.Atoi(m[1])
		shown = append(shown, i)
	}
	if len(shown) == 0 || omitted == 0 || omitted+len(shown) != n {
		t.Fatalf("%d shown, %d said left out, of %d", len(shown), omitted, n)
	}
	for k, i := range shown {
		if i != omitted+k {
			t.Fatalf("call %d at position %d: want the newest %d in order", i, k, len(shown))
		}
	}
	// The calls fill the bound: one more would not have fit.
	_, calls, _ := strings.Cut(text, "for length; do not assume what they showed.)\n")
	if per := len(calls) / len(shown); len(calls)+per <= checksCallsMax {
		t.Fatalf("%d bytes of calls shown, room for another of %d under %d", len(calls), per, checksCallsMax)
	}
}

func clip(s string) string {
	if len(s) > 3000 {
		return s[:1500] + "\n…\n" + s[len(s)-1500:]
	}
	return s
}
