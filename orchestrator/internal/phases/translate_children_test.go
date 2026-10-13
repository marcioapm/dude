package phases

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// types is each event's type, in order.
func types(events []harnessEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

// children is the Run's runs.children as stored.
func (w *harnessWorld) children() map[string]map[string]any {
	w.t.Helper()
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT children FROM runs WHERE id = $1`, w.run.ID).Scan(&raw); err != nil {
		w.t.Fatal(err)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		w.t.Fatal(err)
	}
	return out
}

// noChildCallAsTheParents fails when a call a child made was recorded as
// the Run's own.
func noChildCallAsTheParents(t *testing.T, events []harnessEvent) {
	t.Helper()
	childCalls := map[any]bool{}
	for _, e := range ofType(events, evChildToolCalled) {
		childCalls[e.Payload["callId"]] = true
	}
	for _, e := range append(ofType(events, evToolCalled), ofType(events, evToolCompleted)...) {
		if childCalls[e.Payload["callId"]] {
			t.Errorf("a child's call recorded as the Run's: %v", e)
		}
	}
}

// A real Claude Code turn whose Agent call ran an Explore subagent (Claude
// Code 2.1.207): the subagent's Read is its own, nested under the parent's
// call, and never the Run's; its end carries Claude Code's summary.
func TestAClaudeSubagentIsRecordedAsTheParentCallsChild(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(recorded(t, "claude-child.jsonl")...)
	events := w.events()
	want := []string{evToolCalled, evChildStarted, evChildToolCalled, evChildToolCompleted, evChildEnded, evToolCompleted,
		evAgentMessage, evModelRequestDone}
	if got := slices.DeleteFunc(types(events), func(s string) bool { return s == evAgentThought }); !slices.Equal(got, want) {
		t.Fatalf("events = %v\nwant %v", got, want)
	}
	noChildCallAsTheParents(t, events)
	const parent, child = "toolu_01GJFr24BVyX5H76UdYzZuQa", "a2bf1288fc17cd1d3"
	if p := ofType(events, evToolCalled)[0].Payload; p["tool"] != "task" || p["callId"] != parent {
		t.Errorf("parent call %v", p)
	}
	started := ofType(events, evChildStarted)[0].Payload
	if started["childId"] != child || started["callId"] != parent || started["kind"] != "task" ||
		started["title"] != "Read /etc/hosts first line" || started["agentType"] != "Explore" {
		t.Errorf("started %v", started)
	}
	called := ofType(events, evChildToolCalled)[0].Payload
	if called["tool"] != "read" || called["callId"] != "toolu_0127y2T6WAM5zKr2XbMTVHon" || called["childId"] != child ||
		called["parentCallId"] != parent || called["input"].(map[string]any)["file_path"] != "/etc/hosts" {
		t.Errorf("child call %v", called)
	}
	if done := ofType(events, evChildToolCompleted)[0].Payload; done["tool"] != "read" || done["status"] != "completed" ||
		!strings.Contains(headOf(done, "output"), "##") {
		t.Errorf("child call's end %v", done)
	}
	ended := ofType(events, evChildEnded)[0].Payload
	if ended["status"] != "completed" || !strings.Contains(ended["summary"].(string), "/etc/hosts") ||
		ended["durationSecs"] != float64(4) || ended["tokens"].(map[string]any)["total"] != float64(15136) {
		t.Errorf("ended %v", ended)
	}
	if len(w.children()) != 0 {
		t.Errorf("children left after the parent's call ended: %v", w.children())
	}
}

// While the subagent works, runs.children says what it did last under the
// parent's open call, across a restart of the orchestrator; the call's end
// drops it.
func TestAClaudeSubagentsLastEventIsKeptForItsOpenCall(t *testing.T) {
	recs := recorded(t, "claude-child.jsonl")
	at := slices.IndexFunc(recs, func(r map[string]any) bool { return strings.Contains(mustJSON(r), `"name":"Read"`) })
	if at < 0 {
		t.Fatal("no Read in the recording")
	}
	w := newHarnessWorld(t)
	w.feed(recs[:at+1]...)
	got := w.children()["toolu_01GJFr24BVyX5H76UdYzZuQa"]
	if got["childId"] != "a2bf1288fc17cd1d3" || got["kind"] != "task" || !strings.HasPrefix(got["lastEvent"].(string), "read /etc/hosts") ||
		got["lastEventAt"] == nil {
		t.Errorf("children entry %v", got)
	}
	w.restart()
	w.feed(recs[at+1:]...)
	if len(w.children()) != 0 {
		t.Errorf("children left: %v", w.children())
	}
	if n := len(ofType(w.events(), evChildStarted)); n != 1 {
		t.Errorf("%d child starts", n)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A real Codex turn (0.144.1) that delegated a read to a spawned agent: the
// agent's thread is a child of the call its subAgentActivity names, its
// command and reply are its own, its tokens are not the Run's, and its
// turn's end ends it. The Run's wait is an ordinary call.
func TestACodexSpawnedAgentIsRecordedAsTheParentCallsChild(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(recorded(t, "codex-child.jsonl")...)
	events := w.events()
	want := []string{evAgentMessage, evToolCalled, evToolCompleted, evChildStarted, evModelRequestDone, evToolCalled,
		evChildToolCalled, evChildToolCompleted, evChildMessage, evChildEnded, evToolCompleted, evModelRequestDone, evModelRequestDone}
	if got := types(events); !slices.Equal(got, want) {
		t.Fatalf("events = %v\nwant %v", got, want)
	}
	noChildCallAsTheParents(t, events)
	const spawn, thread = "call_0f65b4f136284e79835158f965bcb10a", "01a133b0-0aa5-7b93-b598-a603ff0d58a5"
	if p := ofType(events, evToolCalled)[0].Payload; p["tool"] != "task" || p["callId"] != spawn {
		t.Errorf("spawn call %v", p)
	}
	if p := ofType(events, evToolCalled)[1].Payload; p["tool"] != "wait" {
		t.Errorf("wait call %v", p)
	}
	if p := ofType(events, evChildStarted)[0].Payload; p["childId"] != thread || p["callId"] != spawn || p["kind"] != "spawn" ||
		p["title"] != "/root/read_hosts" {
		t.Errorf("started %v", p)
	}
	if p := ofType(events, evChildToolCalled)[0].Payload; p["tool"] != "bash" || p["parentCallId"] != spawn ||
		p["input"].(map[string]any)["command"] != "head -n 1 /etc/hosts" {
		t.Errorf("child call %v", p)
	}
	if p := ofType(events, evChildMessage)[0].Payload; p["text"] != "##" || p["childId"] != thread {
		t.Errorf("child message %v", p)
	}
	ended := ofType(events, evChildEnded)[0].Payload
	if tk := ended["tokens"].(map[string]any); ended["status"] != "completed" || ended["summary"] != "##" ||
		tk["input"] != float64(16788-6912) || tk["cacheRead"] != float64(6912) || tk["output"] != float64(45) {
		t.Errorf("ended %v", ended)
	}
	// The Run's turn is its own two requests: 8390 + 8476 input.
	end := ofType(events, evModelRequestDone)[2].Payload["tokens"].(map[string]any)
	if end["input"] != float64(16866) || end["output"] != float64(86) {
		t.Errorf("the Run's turn %v", end)
	}
}

// Two agents spawned at once, their threads' notifications interleaved,
// each bound to its own spawn call (the spawn item first, as the brief's
// shape has it): neither's calls are the other's or the Run's; the Run's
// wait stays open, kept busy by both, until it completes.
func TestTwoConcurrentCodexAgentsAreEachTheirOwnCallsChild(t *testing.T) {
	const run, a, b = "thr_run", "thr_a", "thr_b"
	n := func(method string, params map[string]any) map[string]any {
		return map[string]any{"type": "codex." + method, "data": params}
	}
	spawnItem := func(id, prompt string, receivers ...string) map[string]any {
		r := []any{}
		for _, x := range receivers {
			r = append(r, x)
		}
		return map[string]any{"type": "collabAgentToolCall", "id": id, "tool": "spawnAgent", "status": "inProgress",
			"senderThreadId": run, "receiverThreadIds": r, "prompt": prompt, "model": "gpt-6-sol"}
	}
	activity := func(id, thread string) map[string]any {
		return n("item/completed", map[string]any{"threadId": run, "item": map[string]any{"type": "subAgentActivity", "id": id,
			"kind": "started", "agentThreadId": thread, "agentPath": "/root/" + thread}})
	}
	cmd := func(thread, id, line string, done bool) map[string]any {
		item := map[string]any{"type": "commandExecution", "id": id, "command": line, "status": "inProgress"}
		method := "item/started"
		if done {
			item["status"], item["aggregatedOutput"], item["exitCode"], method = "completed", "ok", 0, "item/completed"
		}
		return n(method, map[string]any{"threadId": thread, "item": item})
	}
	wait := map[string]any{"type": "collabAgentToolCall", "id": "w1", "tool": "wait", "status": "inProgress", "senderThreadId": run,
		"receiverThreadIds": []any{a, b}}
	w := newHarnessWorld(t)
	w.feed(
		n("thread/started", map[string]any{"thread": map[string]any{"id": run, "parentThreadId": nil}}),
		n("item/started", map[string]any{"threadId": run, "item": spawnItem("s1", "Count the Go files\nin ./orchestrator")}),
		activity("s1", a),
		n("item/started", map[string]any{"threadId": run, "item": spawnItem("s2", "List the migrations")}),
		activity("s2", b),
		n("item/completed", map[string]any{"threadId": run, "item": spawnItem("s1", "Count the Go files\nin ./orchestrator", a)}),
		n("item/completed", map[string]any{"threadId": run, "item": spawnItem("s2", "List the migrations", b)}),
		n("item/started", map[string]any{"threadId": run, "item": wait}),
		cmd(a, "ca", "find . -name '*.go'", false),
		cmd(b, "cb", "ls migrations", false),
	)
	busy := w.children()
	if busy["w1"]["childId"] == nil || !strings.Contains(busy["w1"]["lastEvent"].(string), "bash") {
		t.Errorf("the wait call is not kept busy by its agents: %v", busy)
	}
	w.feed(
		cmd(b, "cb", "ls migrations", true),
		cmd(a, "ca", "find . -name '*.go'", true),
		n("item/completed", map[string]any{"threadId": a, "item": map[string]any{"type": "agentMessage", "id": "ma", "text": "412"}}),
		n("turn/completed", map[string]any{"threadId": b, "turn": map[string]any{"status": "completed"}}),
		n("turn/completed", map[string]any{"threadId": a, "turn": map[string]any{"status": "failed"}}),
		n("item/completed", map[string]any{"threadId": run, "item": map[string]any{"type": "collabAgentToolCall", "id": "w1",
			"tool": "wait", "status": "completed"}}),
	)
	events := w.events()
	noChildCallAsTheParents(t, events)
	started := map[any]any{}
	for _, e := range ofType(events, evChildStarted) {
		started[e.Payload["childId"]] = e.Payload["callId"]
		if e.Payload["childId"] == a && e.Payload["title"] != "Count the Go files" {
			t.Errorf("a's title %v", e.Payload["title"])
		}
	}
	if started[a] != "s1" || started[b] != "s2" || len(started) != 2 {
		t.Errorf("children by call %v", started)
	}
	for _, e := range ofType(events, evChildToolCompleted) {
		if owner := map[any]any{"ca": a, "cb": b}[e.Payload["callId"]]; e.Payload["childId"] != owner {
			t.Errorf("call %v recorded as %v's", e.Payload["callId"], e.Payload["childId"])
		}
	}
	ends := map[any]any{}
	for _, e := range ofType(events, evChildEnded) {
		ends[e.Payload["childId"]] = e.Payload["status"]
	}
	if ends[a] != "failed" || ends[b] != "completed" {
		t.Errorf("ends %v", ends)
	}
	if calls := ofType(events, evToolCalled); len(calls) != 3 {
		t.Errorf("the Run's own calls %v, want the two spawns and the wait", calls)
	}
	if len(w.children()) != 0 {
		t.Errorf("children left: %v", w.children())
	}
}
