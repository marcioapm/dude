package phases

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
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

// A task_progress Claude Code writes after the subagent's
// task_notification is no activity: while the parent's call is still open
// the child's line keeps its end as its last event and its clock, and the
// Run's last activity does not move.
func TestAClaudeProgressAfterTheSubagentEndedIsNotActivity(t *testing.T) {
	recs := recorded(t, "claude-child.jsonl")
	end := slices.IndexFunc(recs, func(r map[string]any) bool { return strings.Contains(mustJSON(r), `"task_notification"`) })
	progress := slices.IndexFunc(recs, func(r map[string]any) bool { return strings.Contains(mustJSON(r), `"task_progress"`) })
	if end < 0 || progress < 0 || progress > end {
		t.Fatal("the recording has no progress before its notification")
	}
	const parent = "toolu_01GJFr24BVyX5H76UdYzZuQa"
	w := newHarnessWorld(t)
	w.feed(recs[:end+1]...)
	if last := w.children()[parent]["lastEvent"]; last != "ended completed" {
		t.Fatalf("before: lastEvent %v", last)
	}
	mustExecH(t, w, `UPDATE runs SET agent_active_at = now() - interval '40 minutes',
		children = jsonb_set(children, ARRAY[(SELECT k FROM jsonb_object_keys(children) k LIMIT 1), 'lastEventAt'],
			to_jsonb(now() - interval '40 minutes')) WHERE id = $1`)
	before := w.children()[parent]["lastEventAt"]
	w.events()
	w.feed(recs[progress])
	if got := w.events(); len(got) != 0 {
		t.Errorf("a late progress wrote %v", types(got))
	}
	got := w.children()[parent]
	if got["lastEvent"] != "ended completed" || got["lastEventAt"] != before {
		t.Errorf("a late progress touched the ended child: %v (lastEventAt was %v)", got, before)
	}
	var since time.Duration
	if err := w.owner.QueryRow(context.Background(), `SELECT extract(epoch FROM now() - agent_active_at)::float8 * interval '1 second'
		FROM runs WHERE id = $1`, w.run.ID).Scan(&since); err != nil {
		t.Fatal(err)
	}
	if since < 39*time.Minute {
		t.Errorf("a late progress moved the Run's last activity to %v ago", since)
	}
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

const ocParent, ocChild, ocCall = "ses_ecc7cfcd9ffeUVgVM0hMDoyFbu", "ses_ecc7cf31effeGCzeSYD4SeLE8p", "toolu_01L91pCabVxisGJueHYPC6dV"

// opencodeRecords is the real OpenCode 1.18.21 turn: ACP for the Run's own
// session, and the child session's bus events as lux relays them.
func opencodeRecords(t *testing.T) []map[string]any {
	return append([]map[string]any{{"type": "lux.session", "data": map[string]any{"sessionId": ocParent}}},
		recorded(t, "opencode-child.jsonl")...)
}

// A real OpenCode turn whose task tool ran a general subagent: ACP reports
// the call as kind think, recorded as task; the child session's read and
// reply are its own, never the Run's; the task's completion ends it.
func TestAnOpenCodeSubagentIsRecordedAsTheParentCallsChild(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(opencodeRecords(t)...)
	events := w.events()
	want := []string{evSessionStarted, evToolCalled, evChildStarted, evChildToolCalled, evChildToolCompleted, evChildMessage,
		evChildEnded, evToolCompleted, evModelRequestDone, evAgentMessage, evModelRequestDone}
	if got := types(events); !slices.Equal(got, want) {
		t.Fatalf("events = %v\nwant %v", got, want)
	}
	noChildCallAsTheParents(t, events)
	if p := ofType(events, evToolCalled)[0].Payload; p["tool"] != "task" || p["callId"] != ocCall {
		t.Errorf("parent call %v", p)
	}
	if p := ofType(events, evChildStarted)[0].Payload; p["childId"] != ocChild || p["callId"] != ocCall || p["kind"] != "task" ||
		p["title"] != "Read /etc/hosts first line" || p["agentType"] != "general" || p["model"] != "llmproxy-anthropic/claude-haiku-4.5" {
		t.Errorf("started %v", p)
	}
	if p := ofType(events, evChildToolCompleted)[0].Payload; p["tool"] != "read" || p["parentCallId"] != ocCall ||
		!strings.Contains(headOf(p, "output"), "1: ##") {
		t.Errorf("child call's end %v", p)
	}
	msg := ofType(events, evChildMessage)[0].Payload
	if tk := msg["tokens"].(map[string]any); msg["text"] != "##" || tk["cacheRead"] != float64(7979) {
		t.Errorf("message %v", msg)
	}
	if p := ofType(events, evChildEnded)[0].Payload; p["status"] != "completed" || p["summary"] != "##" {
		t.Errorf("ended %v", p)
	}
}

// Kind think is OpenCode's task tool only when its input names a subagent
// type; any other think call keeps its name.
func TestOpenCodesThinkIsATaskOnlyWithASubagentType(t *testing.T) {
	for _, tc := range []struct {
		u    map[string]any
		want string
	}{
		{map[string]any{"kind": "think", "title": "Investigate UI artifact capture", "rawInput": map[string]any{"subagent_type": "explore",
			"description": "Investigate UI artifact capture"}}, "task"},
		{map[string]any{"kind": "think", "title": "Weigh the options", "rawInput": map[string]any{"thought": "x"}}, "think"},
		{map[string]any{"kind": "think", "title": "task", "rawInput": map[string]any{}}, "task"},
		{map[string]any{"kind": "think", "title": "Two words"}, "think"},
	} {
		if got := toolName(tc.u); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.u, got, tc.want)
		}
	}
}

// A child's permission ask nobody answers: agent.child.waiting, and the
// open call's runs.children entry says what it waits on until the child's
// next event. A child of that child nests under it; one lux answered is
// said and holds nothing.
func TestAnOpenCodeChildWaitingOnAPermissionIsSaid(t *testing.T) {
	oc := func(typ string, data map[string]any) map[string]any {
		return map[string]any{"type": "opencode." + typ, "data": data}
	}
	w := newHarnessWorld(t)
	w.feed(map[string]any{"type": "lux.session", "data": map[string]any{"sessionId": "ses_run"}},
		map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": "c1", "status": "in_progress", "kind": "think",
			"title": "Investigate UI artifact capture", "rawInput": map[string]any{"subagent_type": "explore", "description": "Investigate UI artifact capture"}}},
		oc("session", map[string]any{"id": "ses_kid", "parentID": "ses_run", "title": "Investigate (@explore subagent)"}),
		oc("task", map[string]any{"sessionID": "ses_run", "callID": "c1", "childSessionID": "ses_kid",
			"model": map[string]any{"modelID": "claude-haiku-4.5", "providerID": "llmproxy-anthropic"}, "status": "running"}),
		oc("part", map[string]any{"sessionID": "ses_kid", "callID": "k1", "tool": "grep", "status": "running", "title": "grep",
			"input": `{"pattern":"capture","path":"/opt/venv/lib/python3.12/site-packages"}`}),
		oc("permission", map[string]any{"sessionID": "ses_kid", "permissionID": "per_1", "permission": "external_directory",
			"patterns": []any{"/opt/venv/lib/python3.12/site-packages/*"}, "callID": "k1", "answered": "none"}),
	)
	got := w.children()["c1"]
	on, _ := got["waitingOn"].(map[string]any)
	if got["childId"] != "ses_kid" || on["permission"] != "external_directory" {
		t.Fatalf("children entry %v", got)
	}
	waiting := ofType(w.events(), evChildWaiting)
	if len(waiting) != 1 || waiting[0].Payload["on"] != "permission" || waiting[0].Payload["answered"] != "none" ||
		waiting[0].Payload["callId"] != "c1" {
		t.Errorf("waiting %v", waiting)
	}
	w.restart()
	w.feed(
		oc("permission", map[string]any{"sessionID": "ses_kid", "permission": "read", "patterns": []any{"/etc/*"}, "answered": "allow"}),
		oc("session", map[string]any{"id": "ses_grandkid", "parentID": "ses_kid", "title": "Deeper"}),
		oc("task", map[string]any{"sessionID": "ses_kid", "callID": "k2", "childSessionID": "ses_grandkid", "status": "running"}),
		oc("part", map[string]any{"sessionID": "ses_grandkid", "callID": "g1", "tool": "bash", "status": "completed",
			"input": `{"command":"ls"}`, "output": "a\nb"}),
	)
	got = w.children()["c1"]
	if got["waitingOn"] != nil || !strings.HasPrefix(got["lastEvent"].(string), "bash completed") {
		t.Errorf("after the child moved on: %v", got)
	}
	events := w.events()
	if p := ofType(events, evChildStarted); len(p) != 1 || p[0].Payload["parentChildId"] != "ses_kid" || p[0].Payload["callId"] != "k2" {
		t.Errorf("grandchild %v", p)
	}
	if p := ofType(events, evChildWaiting); len(p) != 1 || p[0].Payload["answered"] != "allow" {
		t.Errorf("an answered ask %v", p)
	}
	if p := ofType(events, evChildToolCalled); len(p) != 1 || p[0].Payload["childId"] != "ses_grandkid" {
		t.Errorf("grandchild's call %v", p)
	}
}

// The fake lux says a child as each harness does (fakelux.Child): fed to
// the translator, every harness's child is the same agent.child.* life,
// nested under the parent's call, with none of its calls the Run's own.
// This is what the E2E suites drive through the deployed processes.
func TestTheFakeLuxsChildIsTheSameOnEveryHarness(t *testing.T) {
	for _, adapter := range []string{"opencode", "claude-code", "codex"} {
		t.Run(adapter, func(t *testing.T) {
			fake := fakelux.New("", "k", func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} })
			srv := httptest.NewServer(fake.Handler())
			t.Cleanup(func() { srv.Close(); fake.Close() })
			client := lux.New(srv.URL, "k")
			lr, err := client.Submit(context.Background(), lux.Spec{Name: "t", Image: lux.Image{Ref: "img"},
				Workload: lux.Workload{Adapter: adapter, Prompt: "go"}}, "child-"+adapter)
			if err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(5 * time.Second); len(fake.Records(lr.ID)) == 0; time.Sleep(10 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("the fake Run never started")
				}
			}
			for _, st := range []fakelux.ChildStep{
				{Op: "start", Title: "Investigate UI artifact capture", AgentType: "explore"},
				{Op: "tool", ToolCallID: "k1", Tool: "bash", Input: map[string]any{"command": "ls /opt"}, Done: true, Output: "venv"},
				{Op: "message", Text: "Found the capture hook."},
				{Op: "end", Text: "Found the capture hook."},
			} {
				st.CallID, st.ChildID = "call_1", "kid_1"
				if err := fake.Child(lr.ID, st); err != nil {
					t.Fatal(err)
				}
			}
			recs := fake.Records(lr.ID)
			w := newHarnessWorld(t)
			w.feed(recs...)
			events := slices.DeleteFunc(w.events(), func(e harnessEvent) bool {
				return !strings.HasPrefix(e.Type, "agent.child.") && e.Type != evToolCalled && e.Type != evToolCompleted
			})
			noChildCallAsTheParents(t, events)
			var got []string
			for _, e := range events {
				if strings.HasPrefix(e.Type, "agent.child.") {
					got = append(got, e.Type)
					if e.Payload["childId"] != "kid_1" || (e.Payload["callId"] != "call_1" && e.Payload["parentCallId"] != "call_1") {
						t.Errorf("%s: %v", e.Type, e.Payload)
					}
				}
			}
			want := []string{evChildStarted, evChildToolCalled, evChildToolCompleted, evChildMessage, evChildEnded}
			if !slices.Equal(got, want) {
				t.Errorf("child events %v, want %v", got, want)
			}
			if p := ofType(events, evChildStarted); len(p) == 1 && p[0].Payload["title"] != "Investigate UI artifact capture" {
				t.Errorf("title %v", p[0].Payload)
			}
		})
	}
}

// A child working while its parent's call waits is the agent working: the
// Run's last activity is the child's.
func TestAChildsActivityIsTheAgents(t *testing.T) {
	oc := func(typ string, data map[string]any) map[string]any {
		return map[string]any{"type": "opencode." + typ, "data": data}
	}
	w := newHarnessWorld(t)
	w.feed(map[string]any{"type": "lux.session", "data": map[string]any{"sessionId": "ses_run"}},
		map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": "c1", "status": "in_progress", "kind": "think",
			"title": "Look", "rawInput": map[string]any{"subagent_type": "explore", "description": "Look"}}},
		oc("task", map[string]any{"sessionID": "ses_run", "callID": "c1", "childSessionID": "ses_kid", "status": "running"}))
	mustExecH(t, w, `UPDATE runs SET agent_active_at = now() - interval '40 minutes' WHERE id = $1`)
	w.feed(oc("part", map[string]any{"sessionID": "ses_kid", "callID": "k1", "tool": "read", "status": "running",
		"input": `{"filePath":"/w/a.go"}`}))
	var since time.Duration
	if err := w.owner.QueryRow(context.Background(), `SELECT extract(epoch FROM now() - agent_active_at)::float8 * interval '1 second'
		FROM runs WHERE id = $1`, w.run.ID).Scan(&since); err != nil {
		t.Fatal(err)
	}
	if since > time.Minute {
		t.Errorf("the Run's last activity is %v ago after its child's call", since)
	}
}

func mustExecH(t *testing.T, w *harnessWorld, sql string) {
	t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, w.run.ID); err != nil {
		t.Fatal(err)
	}
}

// lux relays OpenCode's ask with a lux.warning, after the child records
// that may already follow it: an ask for a call already completed is
// recorded and holds nothing; an ask for an open call holds the child
// until its next record; an input lux cut short (not JSON any more) is
// kept as the string it is.
func TestAnOpenCodeAskThatArrivesLateHoldsNothing(t *testing.T) {
	oc := func(typ string, data map[string]any) map[string]any {
		return map[string]any{"type": "opencode." + typ, "data": data}
	}
	ask := func(call string) []map[string]any {
		return []map[string]any{
			oc("permission", map[string]any{"sessionID": "ses_kid", "permissionID": "per_" + call, "permission": "bash",
				"patterns": []any{"cat /etc/hostname"}, "callID": call, "answered": "none"}),
			{"type": "lux.warning", "data": map[string]any{"message": "an OpenCode subagent asked for permission bash; nothing can answer it"}},
		}
	}
	w := newHarnessWorld(t)
	w.feed(append([]map[string]any{{"type": "lux.session", "data": map[string]any{"sessionId": "ses_run"}},
		{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": "c1", "status": "in_progress", "kind": "think",
			"title": "Look", "rawInput": map[string]any{"subagent_type": "general", "description": "Look"}}},
		oc("task", map[string]any{"sessionID": "ses_run", "callID": "c1", "childSessionID": "ses_kid", "status": "running"}),
		oc("part", map[string]any{"sessionID": "ses_kid", "callID": "b1", "tool": "bash", "status": "running", "input": `{"command":"cat /etc/hostname"}`}),
		oc("part", map[string]any{"sessionID": "ses_kid", "callID": "b1", "tool": "bash", "status": "completed",
			"input": `{"command":"cat /etc/hostname"}`, "output": "box"}),
	}, ask("b1")...)...)
	if got := w.children()["c1"]; got["waitingOn"] != nil || !strings.HasPrefix(got["lastEvent"].(string), "bash completed") {
		t.Errorf("a late ask re-opened the wait: %v", got)
	}
	waiting := ofType(w.events(), evChildWaiting)
	if len(waiting) != 1 || waiting[0].Payload["held"] != false || waiting[0].Payload["childCallId"] != "b1" {
		t.Fatalf("late ask %v", waiting)
	}
	long := `{"command":"` + strings.Repeat("x", 300)
	w.feed(append([]map[string]any{oc("part", map[string]any{"sessionID": "ses_kid", "callID": "b2", "tool": "bash", "status": "running",
		"input": long[:300], "truncated": true})}, ask("b2")...)...)
	if on, _ := w.children()["c1"]["waitingOn"].(map[string]any); on["permission"] != "bash" {
		t.Errorf("an ask for an open call does not hold it: %v", w.children())
	}
	events := w.events()
	if p := ofType(events, evChildToolCalled)[0].Payload; p["input"] != long[:300] || p["truncated"] != true {
		t.Errorf("a cut input %v", p)
	}
	if p := ofType(events, evChildWaiting)[0].Payload; p["held"] != true {
		t.Errorf("ask %v", p)
	}
	w.feed(oc("part", map[string]any{"sessionID": "ses_kid", "callID": "b2", "tool": "bash", "status": "error", "error": "denied"}))
	if got := w.children()["c1"]; got["waitingOn"] != nil {
		t.Errorf("the child's next record did not end the wait: %v", got)
	}
}

// The scripted subagent-ask implementer on the fake lux (lux-fake's
// `subagent ask`, as lux plays it on OpenCode): a child with lux's ids, its
// read done, its bash call held on an ask nothing answers.
func TestTheScriptedSubagentAskWaitsOnBash(t *testing.T) {
	fake := fakelux.New("", "k", func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Subagent: "subagent ask"}
	})
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(func() { srv.Close(); fake.Close() })
	lr, err := lux.New(srv.URL, "k").Submit(context.Background(), lux.Spec{Name: "t", Image: lux.Image{Ref: "img"},
		Workload: lux.Workload{Adapter: "opencode", Prompt: "go"}}, "subagent-ask")
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		recs = fake.Records(lr.ID)
		if slices.ContainsFunc(recs, func(r map[string]any) bool { return r["type"] == "opencode.permission" }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the subagent never asked")
		}
	}
	w := newHarnessWorld(t)
	w.feed(recs...)
	var session string
	for _, r := range recs {
		if r["type"] == "lux.session" {
			session = r["data"].(map[string]any)["sessionId"].(string)
		}
	}
	got := w.children()["toolu_task1"]
	on, _ := got["waitingOn"].(map[string]any)
	if got["childId"] != session+"_child1" || on["permission"] != "bash" || on["patterns"].([]any)[0] != "cat /etc/hostname" {
		t.Errorf("children %v", w.children())
	}
	events := w.events()
	if calls := ofType(events, evChildToolCompleted); len(calls) != 1 || calls[0].Payload["callId"] != "toolu_read1" {
		t.Errorf("child calls done %v", calls)
	}
	if ofType(events, evAgentWarning) == nil || len(ofType(events, evChildEnded)) != 0 {
		t.Errorf("events %v", types(events))
	}
}
