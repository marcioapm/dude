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
