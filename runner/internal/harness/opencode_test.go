package harness

import (
	"context"
	"strings"
	"testing"
)

// Fixtures captured from a real `opencode run --format json` session. They are
// verbatim rather than hand-written: the first version of this normalizer was
// written against a guessed schema and silently produced zero events, because
// nothing checked the guess against what the harness actually emits.
const (
	lineStepStart = `{"type":"step_start","timestamp":1790069960670,"sessionID":"ses_abc","part":{"id":"prt_1","messageID":"msg_1","sessionID":"ses_abc","type":"step-start"}}`

	lineText = `{"type":"text","timestamp":1790069960670,"sessionID":"ses_abc","part":{"id":"prt_2","messageID":"msg_1","type":"text","text":"OK","time":{"start":1,"end":2}}}`

	lineToolRunning = `{"type":"tool_use","sessionID":"ses_abc","part":{"id":"prt_3","tool":"bash","callID":"call_1","state":{"status":"running","input":{"command":"git status"}}}}`

	lineToolCompleted = `{"type":"tool_use","sessionID":"ses_abc","part":{"id":"prt_3","tool":"bash","callID":"call_1","state":{"status":"completed","input":{"command":"git status"},"title":"git status"}}}`

	lineStepFinish = `{"type":"step_finish","sessionID":"ses_abc","part":{"id":"prt_4","reason":"stop","type":"step-finish","tokens":{"total":7967,"input":3,"output":4,"reasoning":0,"cache":{"write":7960,"read":0}},"cost":0.021}}`

	linePlanRunning = `{"type":"tool_use","sessionID":"ses_abc","part":{"id":"prt_5","tool":"todowrite","callID":"call_2","state":{"status":"running","input":{"todos":[{"content":"Read the test","status":"in_progress"}]}}}}`

	linePlanCompleted = `{"type":"tool_use","sessionID":"ses_abc","part":{"id":"prt_5","tool":"todowrite","callID":"call_2","state":{"status":"completed","input":{"todos":[{"content":"Read the test","status":"completed"},{"content":"Fix it","status":"in_progress"}]}}}}`

	// `--print-logs` writes these to stderr; they must never parse as events.
	lineLog = `timestamp=2026-09-22T09:39:03.675Z level=INFO run=fde5d370 message="creating instance"`
)

// The plan is a milestone, not a tool call: naming the tool is this layer's
// job so no reader has to know a harness spells it `todowrite`.
func TestNormalizePlanToolBecomesPlanUpdated(t *testing.T) {
	ev, ok := normalizeLine(linePlanCompleted)
	if !ok {
		t.Fatal("expected a completed plan tool to normalize")
	}
	if ev.Type != "agent.plan.updated" {
		t.Fatalf("type = %q, want agent.plan.updated", ev.Type)
	}

	todos, ok := ev.Payload["todos"].([]any)
	if !ok {
		t.Fatalf("todos = %#v, want a list", ev.Payload["todos"])
	}
	if len(todos) != 2 {
		t.Errorf("len(todos) = %d, want 2", len(todos))
	}
	// The whole list travels each time; a caller replaces rather than merges.
	first, _ := todos[0].(map[string]any)
	if first["content"] != "Read the test" {
		t.Errorf("first todo = %v, want \"Read the test\"", first["content"])
	}
}

// The call and its completion carry the same list, so forwarding both would
// publish the plan twice for every rewrite.
func TestNormalizeSkipsTheRunningPlanTool(t *testing.T) {
	if _, ok := normalizeLine(linePlanRunning); ok {
		t.Error("expected a running plan tool to produce no event")
	}
}

func TestNormalizePlanToolWithoutTodosIsNotAnEvent(t *testing.T) {
	line := `{"type":"tool_use","sessionID":"s","part":{"tool":"todowrite","callID":"c","state":{"status":"completed","input":{}}}}`
	if _, ok := normalizeLine(line); ok {
		t.Error("expected a plan tool with no todo list to produce no event")
	}
}

func TestPlanToolNamesAreMatchedLoosely(t *testing.T) {
	// Harnesses spell it differently, and one harness changes its mind
	// between versions; the shape check is what actually guards the payload.
	for _, name := range []string{"todowrite", "TodoWrite", "todo_write", "update_plan"} {
		if !isPlanTool(name) {
			t.Errorf("isPlanTool(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"bash", "read", "write", ""} {
		if isPlanTool(name) {
			t.Errorf("isPlanTool(%q) = true, want false", name)
		}
	}
}

func TestNormalizeTextBecomesAgentMessage(t *testing.T) {
	ev, ok := normalizeLine(lineText)
	if !ok {
		t.Fatal("expected a text line to normalize")
	}
	if ev.Type != "agent.message" {
		t.Errorf("type = %q, want agent.message", ev.Type)
	}
	if ev.Payload["text"] != "OK" {
		t.Errorf("text = %v, want OK", ev.Payload["text"])
	}
}

func TestNormalizeToolLifecycle(t *testing.T) {
	// One wire type carries its own lifecycle in state.status, so the same
	// event name must split into a call and a completion.
	running, ok := normalizeLine(lineToolRunning)
	if !ok || running.Type != "agent.tool.called" {
		t.Fatalf("running tool = %q/%v, want agent.tool.called", running.Type, ok)
	}
	if running.Payload["tool"] != "bash" {
		t.Errorf("tool = %v, want bash", running.Payload["tool"])
	}

	completed, ok := normalizeLine(lineToolCompleted)
	if !ok || completed.Type != "agent.tool.completed" {
		t.Fatalf("completed tool = %q/%v, want agent.tool.completed", completed.Type, ok)
	}
	if completed.Payload["status"] != "completed" {
		t.Errorf("status = %v, want completed", completed.Payload["status"])
	}
}

func TestNormalizeStepFinishCarriesCost(t *testing.T) {
	// Token usage and cost are the input to cost accounting; dropping them
	// makes a Run's spend unknowable after the fact.
	ev, ok := normalizeLine(lineStepFinish)
	if !ok {
		t.Fatal("expected step_finish to normalize")
	}
	if ev.Type != "agent.model.request.completed" {
		t.Errorf("type = %q", ev.Type)
	}
	if ev.Payload["costUsd"] != 0.021 {
		t.Errorf("costUsd = %v, want 0.021", ev.Payload["costUsd"])
	}
	if ev.Payload["tokens"] == nil {
		t.Error("tokens missing")
	}
}

func TestNormalizeSkipsNonEvents(t *testing.T) {
	for name, line := range map[string]string{
		"step_start": lineStepStart,
		"log line":   lineLog,
		"empty":      "",
		"not json":   "Done.",
		"bad json":   `{"type":`,
	} {
		if _, ok := normalizeLine(line); ok {
			t.Errorf("%s should not normalize into an event", name)
		}
	}
}

func TestNormalizeSkipsEmptyText(t *testing.T) {
	blank := `{"type":"text","sessionID":"ses_abc","part":{"type":"text","text":"   "}}`
	if _, ok := normalizeLine(blank); ok {
		t.Error("whitespace-only text should not become a message")
	}
}

// fakeExec records the command it was given and replays canned output.
type fakeExec struct {
	gotCmd   []string
	gotEnv   map[string]string
	lines    []string
	exitCode int
}

func (f *fakeExec) ExecStream(_ context.Context, _ string, cmd []string,
	env map[string]string, onLine func(string)) (int, error) {
	f.gotCmd = cmd
	f.gotEnv = env
	for _, l := range f.lines {
		onLine(l)
	}
	return f.exitCode, nil
}

func TestRunCollectsEventsAndOutput(t *testing.T) {
	exec := &fakeExec{
		lines: []string{lineStepStart, lineToolRunning, lineToolCompleted, lineText, lineStepFinish},
	}

	var streamed []string
	result, err := NewOpenCode(exec).Run(context.Background(), Spec{
		ContainerID: "c1",
		WorkDir:     "/workspace/repos/dude",
		Model:       "anthropic/claude-sonnet-5",
		Agent:       "orchestrator",
		Prompt:      "do the thing",
		Env:         map[string]string{"ANTHROPIC_API_KEY": "secret"},
	}, func(ev Event) { streamed = append(streamed, ev.Type) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// step_start is not a milestone; the other four are.
	want := []string{
		"agent.tool.called",
		"agent.tool.completed",
		"agent.message",
		"agent.model.request.completed",
	}
	if len(streamed) != len(want) {
		t.Fatalf("streamed %v, want %v", streamed, want)
	}
	for i, w := range want {
		if streamed[i] != w {
			t.Errorf("event[%d] = %q, want %q", i, streamed[i], w)
		}
	}

	// Events must also arrive in the result, so a caller that does not stream
	// still gets the full picture.
	if len(result.Events) != len(want) {
		t.Errorf("result.Events = %d, want %d", len(result.Events), len(want))
	}
	// Raw output is kept verbatim for debugging an odd session.
	if !strings.Contains(result.Output, "step_start") {
		t.Error("raw output should retain non-event lines")
	}
}

func TestRunBuildsTheExpectedCommand(t *testing.T) {
	exec := &fakeExec{}
	_, err := NewOpenCode(exec).Run(context.Background(), Spec{
		ContainerID: "c1",
		WorkDir:     "/workspace/repos/dude",
		Model:       "anthropic/claude-sonnet-5",
		Agent:       "reviewer",
		Prompt:      "review it",
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	joined := strings.Join(exec.gotCmd, " ")
	// A login shell re-reads the profile and loses the image's PATH, which
	// hides the harness binary entirely.
	if !strings.HasPrefix(joined, "sh -c ") {
		t.Errorf("expected a non-login shell, got %q", joined)
	}
	for _, want := range []string{
		`cd "/workspace/repos/dude"`,
		"--format' 'json",
		"--model' 'anthropic/claude-sonnet-5",
		"--agent' 'reviewer",
		"review it",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("command missing %q\ngot: %s", want, joined)
		}
	}
}

func TestRunQuotesPromptsSafely(t *testing.T) {
	// A prompt is untrusted text from a Work Item; it must not be able to
	// terminate the quoting and run its own command.
	exec := &fakeExec{}
	_, err := NewOpenCode(exec).Run(context.Background(), Spec{
		ContainerID: "c1",
		WorkDir:     "/workspace",
		Prompt:      `'; touch /tmp/pwned; echo '`,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	joined := strings.Join(exec.gotCmd, " ")
	if strings.Contains(joined, "; touch /tmp/pwned;") && !strings.Contains(joined, `'\''`) {
		t.Errorf("prompt was not escaped: %s", joined)
	}
}

func TestRunReportsFailureExitCode(t *testing.T) {
	exec := &fakeExec{exitCode: 1, lines: []string{lineText}}
	result, err := NewOpenCode(exec).Run(context.Background(), Spec{ContainerID: "c1"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
}

func TestRunPassesCredentialsThrough(t *testing.T) {
	exec := &fakeExec{}
	env := map[string]string{"ANTHROPIC_API_KEY": "secret", "ANTHROPIC_BASE_URL": "https://proxy"}
	_, err := NewOpenCode(exec).Run(context.Background(), Spec{ContainerID: "c1", Env: env}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exec.gotEnv["ANTHROPIC_API_KEY"] != "secret" {
		t.Error("credentials were not passed to the exec")
	}
}
