package phases

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// harnessWorld is one running Run, a translator for it, and the dude
// events it writes, read back in order.
type harnessWorld struct {
	t     *testing.T
	s     *Syncer
	tr    *translator
	owner *pgx.Conn
	run   phaseRun
	seen  int64
}

func newHarnessWorld(t *testing.T) *harnessWorld {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state, agent_busy_at)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'running', 'lrun_1', 'running', now())`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	run := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "implement", Status: statusRunning}
	w := &harnessWorld{t: t, s: &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, owner: owner, run: run}
	w.restart()
	return w
}

// restart is the orchestrator following the Run afresh: a new translator,
// loaded from what the last batch saved.
func (w *harnessWorld) restart() {
	w.tr = &translator{run: w.run}
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error { return w.tr.load(context.Background(), tx) }); err != nil {
		w.t.Fatal(err)
	}
}

// feed gives the translator records as lux sends them, {type, data} each,
// in one batch.
func (w *harnessWorld) feed(records ...map[string]any) {
	w.t.Helper()
	ctx := context.Background()
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		for _, r := range records {
			data, _ := json.Marshal(r["data"])
			f := lux.Frame{Kind: "record", Epoch: 1, Event: &lux.RecordEvent{Type: r["type"].(string), Data: data}}
			if err := w.tr.apply(ctx, tx, w.s, f); err != nil {
				return err
			}
		}
		return w.tr.save(ctx, tx, "", 0)
	}); err != nil {
		w.t.Fatal(err)
	}
}

type harnessEvent struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// events are the dude events written since the last call, in order.
func (w *harnessWorld) events() []harnessEvent {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT cursor, event_type, payload FROM events WHERE run_id = $1 AND cursor > $2 ORDER BY cursor`,
		w.run.ID, w.seen)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []harnessEvent
	for rows.Next() {
		var e harnessEvent
		if err := rows.Scan(&w.seen, &e.Type, &e.Payload); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// recorded reads a file of records lux sent for a real run
// (scripts/real_harnesses.py), one {type, data} a line: a name in
// testdata, or an absolute path.
func recorded(t *testing.T, name string) []map[string]any {
	t.Helper()
	if !filepath.IsAbs(name) {
		name = filepath.Join("testdata", name)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r map[string]any
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func ofType(events []harnessEvent, typ string) []harnessEvent {
	var out []harnessEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// toolWith finds the call of tool whose argument arg contains want, and
// its completion.
func toolWith(events []harnessEvent, tool, arg, want string) (harnessEvent, harnessEvent, bool) {
	for _, e := range ofType(events, evToolCalled) {
		args, _ := e.Payload["input"].(map[string]any)
		if got, _ := args[arg].(string); e.Payload["tool"] != tool || !strings.Contains(got, want) {
			continue
		}
		for _, d := range ofType(events, evToolCompleted) {
			if d.Payload["callId"] == e.Payload["callId"] {
				return e, d, true
			}
		}
		return e, harnessEvent{}, false
	}
	return harnessEvent{}, harnessEvent{}, false
}

func headOf(p map[string]any, key string) string {
	m, _ := p[key].(map[string]any)
	s, _ := m["head"].(string)
	return s
}

// A real Claude Code turn (claude-sonnet-5 at high effort, recorded through
// lux's adapter's shapes) becomes the events an OpenCode turn makes: its
// thinking, its reply, each tool under the name the chat's cards know with
// its arguments and result, its plan, dude's own MCP tool by its name, and
// the turn's usage and cost at its end.
func TestARealClaudeCodeTurnIsTranslated(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(recorded(t, "claude-code-real.jsonl")...)
	events := w.events()

	thoughts := ofType(events, evAgentThought)
	if len(thoughts) == 0 || strings.TrimSpace(thoughts[0].Payload["text"].(string)) == "" {
		t.Errorf("no thought with text: %v", thoughts)
	}
	messages := ofType(events, evAgentMessage)
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Payload["text"].(string), "software factory") {
		t.Errorf("messages = %v", messages)
	}
	// Claude Code 2.1.207 offers no Grep or Glob tool here (it searches
	// with rg in Bash); their names are in the table test below.
	for _, tc := range []struct{ tool, arg, want, output string }{
		{"read", "file_path", "README.md", "A software factory"},
		{"bash", "command", "git log --oneline -1", ""},
		{"write", "file_path", "/tmp/hx-scratch/notes.txt", ""},
		{"edit", "file_path", "/tmp/hx-scratch/notes.txt", ""},
		{"emit_event", "type", "progress", "recorded"},
	} {
		_, done, ok := toolWith(events, tc.tool, tc.arg, tc.want)
		if !ok {
			t.Errorf("%s: no call with %s %q and its completion", tc.tool, tc.arg, tc.want)
			continue
		}
		if done.Payload["status"] != "completed" {
			t.Errorf("%s: status %v", tc.tool, done.Payload["status"])
		}
		if out := headOf(done.Payload, "output") + headOf(done.Payload, "stdout"); !strings.Contains(out, tc.output) {
			t.Errorf("%s: output %q, want %q in it", tc.tool, out, tc.output)
		}
	}
	if _, done, _ := toolWith(events, "bash", "command", "git log"); !strings.Contains(headOf(done.Payload, "stdout"), "harness") {
		t.Errorf("bash: no stdout apart: %v", done.Payload)
	}
	plans := ofType(events, evPlanUpdated)
	if len(plans) == 0 {
		t.Fatal("no plan")
	}
	last := plans[len(plans)-1].Payload["todos"].([]any)
	for _, todo := range last {
		if todo.(map[string]any)["status"] != "completed" {
			t.Errorf("the last plan has an unfinished step: %v", last)
		}
	}
	for _, e := range ofType(events, evToolCalled) {
		if name := e.Payload["tool"].(string); strings.HasPrefix(name, "Task") || strings.HasPrefix(name, "mcp__") {
			t.Errorf("recorded as a tool: %s", name)
		}
	}
	ends := slices.DeleteFunc(ofType(events, evModelRequestDone), func(e harnessEvent) bool { return e.Payload["turn"] != true })
	if len(ends) != 1 {
		t.Fatalf("turn ends = %v", ends)
	}
	tokens, _ := ends[0].Payload["tokens"].(map[string]any)
	turnCost, _ := ends[0].Payload["costUsd"].(float64)
	if output, _ := tokens["output"].(float64); output <= 0 || number(tokens["cacheRead"]) <= 0 || turnCost <= 0 ||
		number(ends[0].Payload["contextTokens"]) <= 0 {
		t.Errorf("turn end = %v", ends[0].Payload)
	}
	var cost float64
	var out int64
	if err := w.owner.QueryRow(context.Background(), `SELECT agent_cost_usd::float8, output_tokens FROM runs WHERE id = $1`, w.run.ID).Scan(&cost, &out); err != nil {
		t.Fatal(err)
	}
	if cost != turnCost || out != int64(number(tokens["output"])) {
		t.Errorf("run: cost %v output %d, want the turn's", cost, out)
	}
	if n := len(w.tr.openCalls); n != 0 {
		t.Errorf("%d calls left open", n)
	}
}

// A real Codex turn (gpt-6-sol at high effort) likewise: its reasoning
// summaries, its messages, its commands as bash, its plan, dude's own MCP
// tool by its name, and the turn's tokens at its end. (Codex's app-server
// gives a model it does not know no apply_patch tool, so this turn edits
// through the shell; a fileChange's shape is in the table test below.)
func TestARealCodexTurnIsTranslated(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(recorded(t, "codex-real.jsonl")...)
	events := w.events()

	thoughts := ofType(events, evAgentThought)
	if len(thoughts) == 0 || strings.TrimSpace(thoughts[0].Payload["text"].(string)) == "" {
		t.Errorf("no thought with text: %v", thoughts)
	}
	messages := ofType(events, evAgentMessage)
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Payload["text"].(string), "software factory") {
		t.Errorf("messages = %v", messages)
	}
	for _, tc := range []struct{ tool, arg, want, output string }{
		{"bash", "command", "git log --oneline -1", "harness"},
		{"bash", "command", "README.md", "A software factory"},
		{"emit_event", "type", "progress", "recorded"},
	} {
		_, done, ok := toolWith(events, tc.tool, tc.arg, tc.want)
		if !ok {
			t.Errorf("%s: no call with %s %q and its completion", tc.tool, tc.arg, tc.want)
			continue
		}
		if done.Payload["status"] != "completed" || !strings.Contains(headOf(done.Payload, "output"), tc.output) {
			t.Errorf("%s: completion %v, want %q in its output", tc.tool, done.Payload, tc.output)
		}
	}
	if _, done, _ := toolWith(events, "bash", "command", "git log"); done.Payload["exitCode"] != float64(0) {
		t.Errorf("bash: exit code %v", done.Payload["exitCode"])
	}
	plans := ofType(events, evPlanUpdated)
	if len(plans) < 2 {
		t.Fatalf("plans = %v", plans)
	}
	statuses := map[string]bool{}
	for _, p := range plans {
		for _, todo := range p.Payload["todos"].([]any) {
			statuses[todo.(map[string]any)["status"].(string)] = true
		}
	}
	// Codex's inProgress is the todo vocabulary's in_progress.
	if !statuses["pending"] || !statuses["in_progress"] || !statuses["completed"] || len(statuses) != 3 {
		t.Errorf("plan statuses = %v", statuses)
	}
	ends := slices.DeleteFunc(ofType(events, evModelRequestDone), func(e harnessEvent) bool { return e.Payload["turn"] != true })
	if len(ends) != 1 {
		t.Fatalf("turn ends = %v", ends)
	}
	tokens := ends[0].Payload["tokens"].(map[string]any)
	// The sums of the recording's 14 requests (its turn_end's total):
	// 214784 input of which 139520 cached, counted apart.
	if tokens["input"] != float64(75264) || tokens["cacheRead"] != float64(139520) || tokens["output"] != float64(1846) {
		t.Errorf("turn end = %v, want input 75264, cacheRead 139520, output 1846", ends[0].Payload)
	}
	if n := len(w.tr.openCalls); n != 0 {
		t.Errorf("%d calls left open", n)
	}
}

// Each Codex turn's tokens are its own requests' sums: the next turn
// starts again from zero, across a restart.
func TestEachCodexTurnSumsItsOwnRequests(t *testing.T) {
	w := newHarnessWorld(t)
	request := func(in, cached, out int) map[string]any {
		return map[string]any{"type": "codex.thread/tokenUsage/updated", "data": map[string]any{"tokenUsage": map[string]any{
			"last": map[string]any{"inputTokens": in, "cachedInputTokens": cached, "outputTokens": out}, "modelContextWindow": 258400}}}
	}
	end := map[string]any{"type": "codex.turn_end", "data": map[string]any{"status": "completed"}}
	w.feed(request(100, 40, 5), request(200, 150, 7))
	w.restart()
	w.feed(end, request(300, 250, 11), end)
	var got [][3]any
	for _, e := range ofType(w.events(), evModelRequestDone) {
		if e.Payload["turn"] == true {
			tk := e.Payload["tokens"].(map[string]any)
			got = append(got, [3]any{tk["input"], tk["cacheRead"], tk["output"]})
		}
	}
	if want := [][3]any{{110.0, 190.0, 12.0}, {50.0, 250.0, 11.0}}; !slices.Equal(got, want) {
		t.Errorf("turns (input, cacheRead, output) = %v, want %v", got, want)
	}
	var in, cached, out int64
	if err := w.owner.QueryRow(context.Background(), `SELECT input_tokens, cache_read_tokens, output_tokens FROM runs WHERE id = $1`,
		w.run.ID).Scan(&in, &cached, &out); err != nil {
		t.Fatal(err)
	}
	if in != 160 || cached != 440 || out != 23 {
		t.Errorf("run: input %d cacheRead %d output %d, want 160 440 23", in, cached, out)
	}
}

// Each mapped tool, line by line, as each harness names it and as the
// chat's cards know it, with an unknown tool passed through by its name:
// the shapes are the recorded ones, trimmed.
func TestEachHarnessesToolsAreNamedAsTheChatKnowsThem(t *testing.T) {
	claudeUse := func(id, name string, input map[string]any) map[string]any {
		return map[string]any{"type": "claude.assistant", "data": map[string]any{"type": "assistant", "message": map[string]any{
			"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}}}
	}
	codexStart := func(item map[string]any) map[string]any {
		return map[string]any{"type": "codex.item/started", "data": map[string]any{"item": item}}
	}
	for _, tc := range []struct {
		record   map[string]any
		tool     string
		arg, val string
	}{
		{claudeUse("c1", "Read", map[string]any{"file_path": "/w/a.go"}), "read", "file_path", "/w/a.go"},
		{claudeUse("c2", "Bash", map[string]any{"command": "go test"}), "bash", "command", "go test"},
		{claudeUse("c3", "Edit", map[string]any{"file_path": "/w/a.go"}), "edit", "file_path", "/w/a.go"},
		{claudeUse("c4", "Write", map[string]any{"file_path": "/w/b.go"}), "write", "file_path", "/w/b.go"},
		{claudeUse("c5", "Grep", map[string]any{"pattern": "TODO"}), "grep", "pattern", "TODO"},
		{claudeUse("c6", "Glob", map[string]any{"pattern": "**/*.go"}), "glob", "pattern", "**/*.go"},
		{claudeUse("c7", "mcp__dude__ask_person", map[string]any{"question": "Ok?"}), "ask_person", "question", "Ok?"},
		{claudeUse("c8", "mcp__github__get_issue", map[string]any{"n": "1"}), "github_get_issue", "n", "1"},
		{claudeUse("c9", "SomethingNew", map[string]any{"x": "y"}), "SomethingNew", "x", "y"},
		{codexStart(map[string]any{"type": "commandExecution", "id": "x1", "command": "/bin/bash -lc 'go test ./...'",
			"commandActions": []any{map[string]any{"command": "go test ./...", "type": "unknown"}}, "status": "inProgress"}), "bash", "command", "go test ./..."},
		{codexStart(map[string]any{"type": "fileChange", "id": "x2", "status": "inProgress",
			"changes": []any{map[string]any{"path": "/w/a.go", "kind": map[string]any{"type": "update"}, "diff": "@@"}}}), "edit", "file_path", "/w/a.go"},
		{codexStart(map[string]any{"type": "mcpToolCall", "id": "x3", "server": "dude", "tool": "ask_person",
			"arguments": map[string]any{"question": "Ok?"}, "status": "inProgress"}), "ask_person", "question", "Ok?"},
		{codexStart(map[string]any{"type": "mcpToolCall", "id": "x4", "server": "github", "tool": "get_issue",
			"arguments": map[string]any{"n": "1"}, "status": "inProgress"}), "github_get_issue", "n", "1"},
	} {
		w := newHarnessWorld(t)
		w.feed(tc.record)
		called := ofType(w.events(), evToolCalled)
		if len(called) != 1 {
			t.Errorf("%v: %d calls", tc.record, len(called))
			continue
		}
		args, _ := called[0].Payload["input"].(map[string]any)
		if called[0].Payload["tool"] != tc.tool || args[tc.arg] != tc.val {
			t.Errorf("recorded %v, want %s with %s %q", called[0].Payload, tc.tool, tc.arg, tc.val)
		}
	}
}

// Codex's patch is an edit, its diff the output; a command that failed is
// an error with its exit code; an MCP call that failed, an error with
// Codex's message (the shapes of codex app-server's ThreadItem).
func TestCodexsEditsAndFailuresAreRecordedAsTheChatKnowsThem(t *testing.T) {
	w := newHarnessWorld(t)
	done := func(item map[string]any) map[string]any {
		return map[string]any{"type": "codex.item/completed", "data": map[string]any{"item": item}}
	}
	change := map[string]any{"type": "fileChange", "id": "p1", "status": "completed",
		"changes": []any{map[string]any{"path": "/w/notes.txt", "kind": map[string]any{"type": "update", "move_path": nil},
			"diff": "@@ -1 +1 @@\n-first\n+second\n"}}}
	w.feed(map[string]any{"type": "codex.item/started", "data": map[string]any{"item": map[string]any{
		"type": "fileChange", "id": "p1", "status": "inProgress", "changes": change["changes"]}}}, done(change),
		done(map[string]any{"type": "commandExecution", "id": "c1", "command": "/bin/bash -lc 'false'", "commandActions": []any{},
			"status": "failed", "exitCode": 1, "aggregatedOutput": "boom"}),
		done(map[string]any{"type": "mcpToolCall", "id": "m1", "server": "dude", "tool": "ask_person", "arguments": map[string]any{},
			"status": "failed", "error": map[string]any{"message": "no such run"}}))
	got := map[string]map[string]any{}
	for _, e := range ofType(w.events(), evToolCompleted) {
		got[e.Payload["callId"].(string)] = e.Payload
	}
	if p := got["p1"]; p["tool"] != "edit" || p["status"] != "completed" || !strings.Contains(headOf(p, "output"), "+second") {
		t.Errorf("patch: %v", p)
	}
	if p := got["c1"]; p["tool"] != "bash" || p["status"] != "error" || p["exitCode"] != float64(1) || headOf(p, "output") != "boom" {
		t.Errorf("command: %v", p)
	}
	if p := got["m1"]; p["tool"] != "ask_person" || p["status"] != "error" || headOf(p, "output") != "no such run" {
		t.Errorf("mcp: %v", p)
	}
}

// Claude Code's TodoWrite is the plan, whole each time, as OpenCode's is.
func TestClaudeCodesTodoWriteIsThePlan(t *testing.T) {
	w := newHarnessWorld(t)
	todos := []any{map[string]any{"content": "Read", "status": "completed", "activeForm": "Reading"},
		map[string]any{"content": "Write", "status": "in_progress", "activeForm": "Writing"}}
	w.feed(map[string]any{"type": "claude.assistant", "data": map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "TodoWrite", "input": map[string]any{"todos": todos}}}}}},
		map[string]any{"type": "claude.user", "data": map[string]any{"type": "user", "message": map[string]any{
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}}}})
	events := w.events()
	if len(events) != 1 || events[0].Type != evPlanUpdated || len(events[0].Payload["todos"].([]any)) != 2 {
		t.Errorf("events = %v", events)
	}
}

// A plan built task by task outlives a restart of the orchestrator: the
// tasks so far are saved with the cursor, so a later TaskUpdate still
// names the right one; as does Claude Code's running cost, so the next
// turn's is its own.
func TestClaudeCodesTasksAndCostOutliveARestart(t *testing.T) {
	w := newHarnessWorld(t)
	use := func(id, name string, input map[string]any) map[string]any {
		return map[string]any{"type": "claude.assistant", "data": map[string]any{"message": map[string]any{
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}}}
	}
	result := func(cost float64) map[string]any {
		return map[string]any{"type": "claude.result", "data": map[string]any{"type": "result", "total_cost_usd": cost}}
	}
	end := map[string]any{"type": "claude.turn_end", "data": map[string]any{"usage": map[string]any{"input_tokens": 5, "output_tokens": 7}}}
	w.feed(use("a", "TaskCreate", map[string]any{"subject": "one"}), use("b", "TaskCreate", map[string]any{"subject": "two"}), end, result(0.25))
	w.restart()
	w.feed(use("c", "TaskUpdate", map[string]any{"taskId": "2", "status": "in_progress"}), end, result(0.75))
	events := w.events()
	plans := ofType(events, evPlanUpdated)
	want := `[{"content":"one","status":"pending"},{"content":"two","status":"in_progress"}]`
	if got, _ := json.Marshal(plans[len(plans)-1].Payload["todos"]); string(got) != want {
		t.Errorf("plan = %s, want %s", got, want)
	}
	var costs []any
	for _, e := range ofType(events, evModelRequestDone) {
		costs = append(costs, e.Payload["costUsd"])
	}
	if !slices.Equal(costs, []any{0.25, 0.5}) {
		t.Errorf("turn costs = %v, want 0.25 then 0.5", costs)
	}
	// A resumed process counts from zero again: its first total is its own.
	w.feed(end, result(0.1))
	if e := ofType(w.events(), evModelRequestDone); len(e) != 1 || e[0].Payload["costUsd"] != 0.1 {
		t.Errorf("after a new process: %v", e)
	}
}

// claudeResultLine is a Claude Code result line as lux relays it.
func claudeResultLine(fields map[string]any) map[string]any {
	data := map[string]any{"type": "result", "subtype": "success"}
	maps.Copy(data, fields)
	return map[string]any{"type": "claude.result", "data": data}
}

var (
	claudeEnd = map[string]any{"type": "claude.turn_end", "data": map[string]any{"usage": map[string]any{"input_tokens": 5, "output_tokens": 7}}}
	luxIdle   = map[string]any{"type": "lux.activity", "data": map[string]any{"activity": "idle"}}
	luxBusy   = map[string]any{"type": "lux.activity", "data": map[string]any{"activity": "busy"}}
)

func (w *harnessWorld) status() (status string, turnDone bool) {
	w.t.Helper()
	if err := w.owner.QueryRow(context.Background(), `SELECT status::text, turn_done_at IS NOT NULL FROM runs WHERE id = $1`,
		w.run.ID).Scan(&status, &turnDone); err != nil {
		w.t.Fatal(err)
	}
	return status, turnDone
}

// lux relays Claude Code's result line as claude.turn_end, the agent
// going idle, then claude.result (lux internal/adapter/claude.go): each
// turn ends once, on the second of the pair, with its own cost and tokens
// — whether the pair arrives in one batch or two, across a restart — and
// is done only then.
func TestAClaudeTurnEndsWithTheResultLuxSendsAfterIt(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(claudeEnd, luxIdle, claudeResultLine(map[string]any{"total_cost_usd": 0.25}))
	if _, done := w.status(); !done {
		t.Error("the first turn is not done after its result")
	}
	w.feed(luxBusy, claudeEnd, luxIdle)
	if st, done := w.status(); st != "running" || done {
		t.Errorf("before its result, the second turn is %s, done %v", st, done)
	}
	w.restart()
	w.feed(claudeResultLine(map[string]any{"total_cost_usd": 0.75}))
	if _, done := w.status(); !done {
		t.Error("the second turn is not done after its result")
	}
	var costs []any
	for _, e := range ofType(w.events(), evModelRequestDone) {
		if tokens, _ := e.Payload["tokens"].(map[string]any); e.Payload["turn"] != true || tokens["output"] != float64(7) {
			t.Errorf("turn end %v", e.Payload)
		}
		costs = append(costs, e.Payload["costUsd"])
	}
	if !slices.Equal(costs, []any{0.25, 0.5}) {
		t.Errorf("turn costs = %v, want [0.25 0.5]", costs)
	}
	var cost float64
	var out int64
	if err := w.owner.QueryRow(context.Background(), `SELECT agent_cost_usd::float8, output_tokens FROM runs WHERE id = $1`, w.run.ID).Scan(&cost, &out); err != nil {
		t.Fatal(err)
	}
	if cost != 0.75 || out != 14 {
		t.Errorf("run: cost %v output %d, want 0.75 and 14", cost, out)
	}
}

// A failure only the result line carries (an API error mid-turn:
// error_during_execution, no synthetic reply before it) fails its own
// turn, never done, and not the next. One an interrupt caused does not.
func TestAClaudeResultThatFailedFailsItsOwnTurn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines [][]map[string]any
		want  string
	}{
		{"error in result", [][]map[string]any{{claudeEnd, luxIdle, claudeResultLine(map[string]any{"is_error": true, "result": "API Error: 500"})}}, "failed"},
		{"error_during_execution", [][]map[string]any{{claudeEnd, luxIdle},
			{claudeResultLine(map[string]any{"is_error": true, "subtype": "error_during_execution", "errors": []any{"API Error: 529 overloaded"}})}}, "failed"},
		{"interrupted", [][]map[string]any{{claudeEnd, claudeResultLine(map[string]any{"is_error": true, "subtype": "error_during_execution",
			"terminal_reason": "aborted_streaming", "errors": []any{"[ede_diagnostic] turn aborted"}})}}, "running"},
	} {
		w := newHarnessWorld(t)
		for _, batch := range tc.lines {
			w.feed(batch...)
		}
		st, done := w.status()
		if st != tc.want || done {
			t.Errorf("%s: %s, done %v; want %s", tc.name, st, done, tc.want)
		}
		if st == "failed" {
			var reason string
			_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE id = $1`, w.run.ID).Scan(&reason)
			if !strings.Contains(reason, "API Error") {
				t.Errorf("%s: reason %q", tc.name, reason)
			}
		}
	}
	// The failed turn's error is not carried into the next.
	w := newHarnessWorld(t)
	w.feed(claudeEnd, claudeResultLine(map[string]any{"is_error": true, "terminal_reason": "aborted_tools", "result": "aborted"}))
	w.feed(luxBusy, claudeEnd, luxIdle, claudeResultLine(map[string]any{"result": "done"}))
	if st, done := w.status(); st != "running" || !done {
		t.Errorf("the turn after an interrupted one: %s, done %v", st, done)
	}
}

// The recording is in lux's order: its last two records are the turn's
// end, then the result line.
func TestTheClaudeRecordingEndsAsLuxRelaysAResult(t *testing.T) {
	recs := recorded(t, "claude-code-real.jsonl")
	if got := []any{recs[len(recs)-2]["type"], recs[len(recs)-1]["type"]}; !slices.Equal(got, []any{"claude.turn_end", "claude.result"}) {
		t.Errorf("last two records = %v", got)
	}
}

// A turn that ends on a failed request fails the Run with the harness's
// reason: Claude Code's synthetic reply, Codex's last error.
func TestAFailedTurnOnEitherHarnessFailsTheRun(t *testing.T) {
	for name, records := range map[string][]map[string]any{
		"claude-code": {
			{"type": "claude.assistant", "data": map[string]any{"error": "model_not_found", "message": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "There's an issue with the selected model (claude-nope-9)."}}}}},
			{"type": "claude.turn_end", "data": map[string]any{}},
			{"type": "claude.result", "data": map[string]any{"is_error": true, "result": "There's an issue with the selected model (claude-nope-9)."}},
		},
		"codex": {
			{"type": "codex.error", "data": map[string]any{"error": map[string]any{"message": "Reconnecting... 2/5"}, "willRetry": true}},
			{"type": "codex.error", "data": map[string]any{"error": map[string]any{"message": "unexpected status 401 Unauthorized"}, "willRetry": false}},
			{"type": "codex.turn_end", "data": map[string]any{"status": "failed"}},
		},
	} {
		w := newHarnessWorld(t)
		w.feed(records...)
		var status, reason string
		if err := w.owner.QueryRow(context.Background(), `SELECT status::text, COALESCE(error, '') FROM runs WHERE id = $1`, w.run.ID).Scan(&status, &reason); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"claude-code": "claude-nope-9", "codex": "401 Unauthorized"}[name]
		if status != "failed" || !strings.Contains(reason, want) {
			t.Errorf("%s: %s %q", name, status, reason)
		}
	}
}

// A Run the orchestrator follows again after a restart replays nothing:
// the same records twice are one call each.
func TestARepeatedToolStartIsOneCall(t *testing.T) {
	w := newHarnessWorld(t)
	start := map[string]any{"type": "codex.item/started", "data": map[string]any{"item": map[string]any{
		"type": "commandExecution", "id": "x1", "command": "ls", "status": "inProgress"}}}
	w.feed(start)
	w.restart()
	w.feed(start)
	if n := len(ofType(w.events(), evToolCalled)); n != 1 {
		t.Errorf("%d calls", n)
	}
}

// Prints the dude events a file of records lux sent makes, one JSON a line
// prefixed EVENT=, for scripts/real_harnesses.py. Only when
// DUDE_TRANSLATE_FILE names the file.
func TestPrintATranslation(t *testing.T) {
	path := os.Getenv("DUDE_TRANSLATE_FILE")
	if path == "" {
		t.Skip("DUDE_TRANSLATE_FILE not set")
	}
	w := newHarnessWorld(t)
	w.feed(recorded(t, path)...)
	for _, e := range w.events() {
		b, _ := json.Marshal(e)
		fmt.Println("EVENT=" + string(b))
	}
}
