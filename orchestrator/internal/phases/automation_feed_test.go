package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// feedCases are harness records of one turn — a message, a thought, a
// tool call and its end — as each harness sends them. A task's Run and an
// automation run fed the same records write the same activity.
var feedCases = []struct {
	name    string
	records []map[string]any
}{
	{"acp", []map[string]any{
		{"type": "acp.agent_thought_chunk", "data": map[string]any{"content": map[string]any{"type": "text", "text": "Numbers first."}}},
		{"type": "acp.agent_message_chunk", "data": map[string]any{"content": map[string]any{"type": "text", "text": "Getting this week's numbers."}}},
		{"type": "acp.tool_call", "data": map[string]any{"toolCallId": "c1", "status": "in_progress", "title": "bash", "kind": "execute",
			"rawInput": map[string]any{"command": "pr-stats --org acme --since 7d"}}},
		{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": "c1", "status": "completed", "title": "bash", "kind": "execute",
			"rawOutput": map[string]any{"output": strings.Repeat("x", 9000), "metadata": map[string]any{"exit": 0.0}}}},
		{"type": "acp.agent_message_chunk", "data": map[string]any{"content": map[string]any{"type": "text", "text": "Posted."}}},
		{"type": "acp.turn_end", "data": map[string]any{}},
	}},
	{"claude", []map[string]any{
		{"type": "claude.assistant", "data": map[string]any{"message": map[string]any{"content": []any{
			map[string]any{"type": "thinking", "thinking": "Numbers first."},
			map[string]any{"type": "text", "text": "Getting this week's numbers."},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "kubectl logs api"}}}}}},
		{"type": "claude.user", "data": map[string]any{"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "error", "is_error": true}}},
			"tool_use_result": map[string]any{"stdout": "", "stderr": "error: You must be logged in"}}},
		{"type": "claude.assistant", "data": map[string]any{"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "toolu_2", "name": "mcp__dude__post_to_chat", "input": map[string]any{"channel": "#engineering", "text": "Digest"}}}}}},
		{"type": "claude.user", "data": map[string]any{"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": `{"channel":"#engineering"}`}}}}},
	}},
	{"codex", []map[string]any{
		{"type": "codex.item/completed", "data": map[string]any{"item": map[string]any{"type": "reasoning", "id": "r1", "summary": []any{"Numbers first."}}}},
		{"type": "codex.item/started", "data": map[string]any{"item": map[string]any{"type": "commandExecution", "id": "x1",
			"command": "/bin/zsh -lc 'govulncheck ./...'", "status": "inProgress"}}},
		{"type": "codex.item/completed", "data": map[string]any{"item": map[string]any{"type": "commandExecution", "id": "x1",
			"command": "/bin/zsh -lc 'govulncheck ./...'", "status": "failed", "aggregatedOutput": "3 vulnerabilities", "exitCode": 3}}},
		{"type": "codex.item/completed", "data": map[string]any{"item": map[string]any{"type": "agentMessage", "id": "m1", "text": "Three found."}}},
	}},
}

// feedTypes are the activity both callers write.
var feedTypes = map[string]bool{evAgentMessage: true, evAgentThought: true, evToolCalled: true, evToolCompleted: true}

func TestAnAutomationRunsActivityIsTheRunsTranslation(t *testing.T) {
	for _, tc := range feedCases {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			w.feed(tc.records...)
			var want []harnessEvent
			for _, e := range w.events() {
				if feedTypes[e.Type] {
					want = append(want, e)
				}
			}
			if len(want) < 3 {
				t.Fatalf("the Run's translation wrote too little to compare: %v", want)
			}

			a := newAutomationWorld(t, w)
			a.feed(tc.records...)
			got := a.events()
			if !reflect.DeepEqual(got, want) {
				g, _ := json.Marshal(got)
				wj, _ := json.Marshal(want)
				t.Fatalf("automation run's activity:\n%s\nthe Run's:\n%s", g, wj)
			}
		})
	}
}

// A call still open when its run is stopped ends aborted, with the reason.
func TestAnAutomationRunStoppedMidCallAbortsTheCall(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	a.feed(feedCases[0].records[2])
	ctx := context.Background()
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		f, err := LoadAutomationFeed(ctx, tx, a.ref)
		if err != nil {
			return err
		}
		return f.End(ctx, tx, "Stopped by the time limit")
	}); err != nil {
		t.Fatal(err)
	}
	got := a.events()
	last := got[len(got)-1]
	if last.Type != evToolCompleted || last.Payload["callId"] != "c1" || last.Payload["status"] != "aborted" ||
		last.Payload["note"] != "Stopped by the time limit" {
		t.Fatalf("events: %v", got)
	}
}

// post_to_chat's completion carries the tool server's record of it: the
// channel, ts and permalink it posted at.
func TestPostToChatsCompletionCarriesThePermalink(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	for _, q := range []string{
		`INSERT INTO events (id, organization_id, event_type, automation_run_id, actor_type, actor_id, source, payload) VALUES
			('evt_p_'||$1, $1, 'slack.message.posted', 'arn_'||$1, 'agent', 'a', 'control-plane',
			 '{"tool":"post_to_chat","channel":"#engineering","channelId":"C1","ts":"1.2","permalink":"https://acme.slack.com/archives/C1/p12"}')`,
		`INSERT INTO events (id, organization_id, event_type, automation_run_id, actor_type, actor_id, source, payload) VALUES
			('evt_d_'||$1, $1, 'agent.tool.dude', 'arn_'||$1, 'agent', 'a', 'control-plane', '{"tool":"post_to_chat","arguments":{"channel":"#engineering","text":"Digest"},"result":{"channel":"#engineering","permalink":"https://acme.slack.com/archives/C1/p12"}}')`,
	} {
		if _, err := w.owner.Exec(context.Background(), q, w.run.Org); err != nil {
			t.Fatal(err)
		}
	}
	a.feed(feedCases[1].records...)
	var done map[string]any
	for _, e := range a.events() {
		if e.Type == evToolCompleted && e.Payload["tool"] == "post_to_chat" {
			done = e.Payload
		}
		// The Bash call before it is no dude tool: nothing joined.
		if e.Payload["tool"] == "bash" && e.Payload["dude"] != nil {
			t.Fatalf("bash joined: %v", e.Payload)
		}
	}
	dude, _ := done["dude"].(map[string]any)
	posted, _ := dude["posted"].(map[string]any)
	if posted["permalink"] != "https://acme.slack.com/archives/C1/p12" || posted["channel"] != "#engineering" || dude["eventId"] != "evt_d_"+w.run.Org {
		t.Fatalf("completion: %v", done)
	}
}

func TestDudeRecordsJoinArgumentsNotCompletionOrder(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	ctx := context.Background()
	for _, v := range []struct{ id, text, failure string }{{"refused", "bad", "Name a channel"}, {"first", "one", ""}, {"second", "two", ""}} {
		p, _ := json.Marshal(map[string]any{"tool": "post_to_chat", "arguments": map[string]any{"text": v.text}, "error": v.failure, "posted": map[string]any{"permalink": "https://example.com/" + v.id}})
		if _, err := w.owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, automation_run_id, actor_type, actor_id, source, payload) VALUES ($1,$2,'agent.tool.dude',$3,'agent','a','agent',$4)`, "evt_"+v.id+w.run.Org, w.run.Org, a.ref.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	start := func(id, text string) map[string]any {
		return map[string]any{"type": "acp.tool_call", "data": map[string]any{"toolCallId": id, "status": "in_progress", "title": "post_to_chat", "rawInput": map[string]any{"text": text}}}
	}
	end := func(id string) map[string]any {
		return map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": id, "status": "completed", "title": "post_to_chat"}}
	}
	a.feed(start("aborted", "one"))
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		f, err := LoadAutomationFeed(ctx, tx, a.ref)
		if err != nil {
			return err
		}
		if err := f.End(ctx, tx, "Stopped: the run was cancelled."); err != nil {
			return err
		}
		return f.Save(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
	a.feed(start("missing", "no record"), end("missing"), start("r", "bad"), end("r"), start("a", "one"), start("b", "two"), end("b"), end("a"))
	got := map[string]map[string]any{}
	for _, e := range a.events() {
		if e.Type == evToolCompleted {
			got[e.Payload["callId"].(string)] = e.Payload
		}
	}
	if got["aborted"]["dude"] != nil || got["aborted"]["status"] != "aborted" {
		t.Fatalf("aborted call: %v", got["aborted"])
	}
	if got["missing"]["dude"] != nil {
		t.Fatalf("missing call consumed a record: %v", got["missing"])
	}
	for call, record := range map[string]string{"r": "refused", "a": "first", "b": "second"} {
		d, _ := got[call]["dude"].(map[string]any)
		if d["eventId"] != "evt_"+record+w.run.Org {
			t.Fatalf("%s: %v", call, got[call])
		}
		if call == "r" && (got[call]["status"] != "error" || d["error"] != "Name a channel") {
			t.Fatalf("refusal: %v", got[call])
		}
	}
}

func TestAutomationRunningOutputSurvivesAnAbortedEnd(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	a.feed(feedCases[0].records[2], map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"toolCallId": "c1", "status": "in_progress", "title": "bash", "rawOutput": map[string]any{"output": "partial result"}}})
	ctx := context.Background()
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		f, err := LoadAutomationFeed(ctx, tx, a.ref)
		if err != nil {
			return err
		}
		return f.End(ctx, tx, "Stopped")
	}); err != nil {
		t.Fatal(err)
	}
	got := a.events()
	last := got[len(got)-1]
	out, _ := last.Payload["output"].(map[string]any)
	if last.Payload["status"] != "aborted" || out["head"] != "partial result" {
		t.Fatalf("completion: %v", last)
	}
}

func TestAutomationCommandExitIsRecordedOnceBeforeTerminalState(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	a.ref.Lines = true
	ctx := context.Background()
	for range 2 {
		if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
			f, err := LoadAutomationFeed(ctx, tx, a.ref)
			if err != nil {
				return err
			}
			if err := f.Apply(ctx, tx, lux.Frame{Kind: "lux", EventType: "exited", EventData: json.RawMessage(`{"exitCode":3}`)}); err != nil {
				return err
			}
			return f.Save(ctx, tx)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var n, code int
	if err := w.owner.QueryRow(ctx, `SELECT count(*), COALESCE(max((payload->>'exitCode')::int),-1) FROM events WHERE automation_run_id=$1 AND event_type='automation.log.exited'`, a.ref.ID).Scan(&n, &code); err != nil {
		t.Fatal(err)
	}
	if n != 1 || code != 3 {
		t.Fatalf("exit events=%d code=%d", n, code)
	}
}

func TestAutomationFeedDoesNotRewriteUnchangedState(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	ctx := context.Background()
	a.feed(feedCases[0].records[2])
	var before, after string
	if err := w.owner.QueryRow(ctx, `SELECT xmin::text FROM automation_runs WHERE id=$1`, a.ref.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	a.feed()
	if err := w.owner.QueryRow(ctx, `SELECT xmin::text FROM automation_runs WHERE id=$1`, a.ref.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("unchanged state rewritten: %s -> %s", before, after)
	}
}

// serverPass records one read of a server's log in its own transaction.
func (a *automationWorld) serverPass(lines []ServerLine) {
	a.t.Helper()
	ctx := context.Background()
	if err := a.w.s.DB.InOrg(ctx, a.ref.Org, func(tx pgx.Tx) error {
		f, err := LoadAutomationFeed(ctx, tx, a.ref)
		if err != nil {
			return err
		}
		if err := f.ServerLines(ctx, tx, lines, 500); err != nil {
			return err
		}
		return f.Save(ctx, tx)
	}); err != nil {
		a.t.Fatal(err)
	}
}

// serverLog is the run's recorded server lines and omitted markers, in order.
func (a *automationWorld) serverLog() []string {
	a.t.Helper()
	rows, err := a.w.owner.Query(context.Background(), `SELECT event_type, COALESCE(payload->>'text', '') FROM events
		WHERE automation_run_id = $1 AND event_type IN ('automation.log.line', 'automation.log.cut') ORDER BY cursor`, a.ref.ID)
	if err != nil {
		a.t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var typ, text string
		if err := rows.Scan(&typ, &text); err != nil {
			a.t.Fatal(err)
		}
		if typ == "automation.log.cut" {
			text = "CUT " + text
		}
		out = append(out, text)
	}
	return out
}

func TestServerLogReadsAreDedupedAtTheWindowEdgeAndGapsAreMarked(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	a.ref.Lines = true
	at := func(t int64, text string) ServerLine { return ServerLine{T: t, Stream: "stdout", Text: text} }
	// Lines share a millisecond; the next read's window starts inside them,
	// and a line at that millisecond arrives after the first read.
	a.serverPass([]ServerLine{at(10, "a"), at(20, "b1"), at(20, "b2")})
	a.serverPass([]ServerLine{at(20, "b2"), at(20, "b3"), at(30, "c")})
	// The following read's oldest line is newer than any recorded and lux kept only its tail: a gap.
	var window []ServerLine
	for i := range 500 {
		window = append(window, at(int64(1000+i), fmt.Sprintf("late %d", i)))
	}
	a.serverPass(window)
	got := a.serverLog()
	want := []string{"a", "b1", "b2", "b3", "c", "CUT Earlier container log lines were not kept: more than 500 arrived between reads."}
	if len(got) != len(want)+500 || !reflect.DeepEqual(got[:len(want)], want) || got[len(got)-1] != "late 499" {
		t.Fatalf("server log: %d lines, head %q, last %q", len(got), got[:min(len(got), len(want))], got[len(got)-1])
	}
}

// A command's lines are capped per read (500, then one marker for that
// read) and per run (2000, then one marker for the rest of the run).
func TestAutomationCommandLogIsBoundedAcrossBatches(t *testing.T) {
	w := newHarnessWorld(t)
	a := newAutomationWorld(t, w)
	a.ref.Lines = true
	ctx := context.Background()
	for batch := range 6 {
		if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
			f, err := LoadAutomationFeed(ctx, tx, a.ref)
			if err != nil {
				return err
			}
			var data strings.Builder
			for i := range 600 {
				fmt.Fprintf(&data, "b%d-%d\n", batch, i)
			}
			if err := f.Apply(ctx, tx, lux.Frame{Kind: "record", Channel: "stdout", Data: data.String()}); err != nil {
				return err
			}
			return f.Save(ctx, tx)
		}); err != nil {
			t.Fatal(err)
		}
	}
	got := a.serverLog()
	readCut := "CUT Lines past 500 in one read are not kept here; the run's log keeps its end."
	runCut := "CUT Lines after the first 2000 are not kept here; the run's log keeps its end."
	// Three reads end with their read marker; the fourth reaches the run's cap at its 500th line.
	if len(got) != 2000+3+1 || got[0] != "b0-0" || got[499] != "b0-499" || got[500] != readCut ||
		got[501] != "b1-0" || got[1502] != readCut || got[2002] != "b3-499" || got[2003] != runCut {
		t.Fatalf("%d entries; around the edges: %q %q %q %q", len(got), got[499], got[500], got[501], got[len(got)-1])
	}
}

type automationWorld struct {
	t    *testing.T
	w    *harnessWorld
	ref  AutomationRunRef
	seen int64
}

// newAutomationWorld adds an automation run to w's organisation.
func newAutomationWorld(t *testing.T, w *harnessWorld) *automationWorld {
	ctx := context.Background()
	org := w.run.Org
	for _, q := range []string{
		`INSERT INTO automations (id, organization_id, project_id, name, kind, trigger, agent)
			VALUES ('aut_'||$1, $1, 'prj_'||$1, 'Digest', 'agent', '{"kind":"schedule","cron":"0 15 * * 5","tz":"Europe/Lisbon"}', '{"own":{"prompt":""}}')`,
		`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_'||$1, $1, 'aut_'||$1, 'running', 'schedule')`,
	} {
		if _, err := w.owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	return &automationWorld{t: t, w: w, ref: AutomationRunRef{ID: "arn_" + org, Org: org, ProjectID: "prj_" + org}}
}

// feed translates records in one batch, as the automations loop does: loaded, applied, saved.
func (a *automationWorld) feed(records ...map[string]any) {
	a.t.Helper()
	ctx := context.Background()
	if err := a.w.s.DB.InOrg(ctx, a.ref.Org, func(tx pgx.Tx) error {
		f, err := LoadAutomationFeed(ctx, tx, a.ref)
		if err != nil {
			return err
		}
		for _, r := range records {
			data, _ := json.Marshal(r["data"])
			if err := f.Apply(ctx, tx, lux.Frame{Kind: "record", Epoch: 1, Event: &lux.RecordEvent{Type: r["type"].(string), Data: data}}); err != nil {
				return err
			}
		}
		return f.Save(ctx, tx)
	}); err != nil {
		a.t.Fatal(err)
	}
}

// events are the automation run's activity written since the last call; none names a Run.
func (a *automationWorld) events() []harnessEvent {
	a.t.Helper()
	rows, err := a.w.owner.Query(context.Background(), `SELECT cursor, event_type, payload, run_id IS NULL AND task_id IS NULL
		FROM events WHERE automation_run_id = $1 AND cursor > $2 ORDER BY cursor`, a.ref.ID, a.seen)
	if err != nil {
		a.t.Fatal(err)
	}
	defer rows.Close()
	var out []harnessEvent
	for rows.Next() {
		var e harnessEvent
		var alone bool
		if err := rows.Scan(&a.seen, &e.Type, &e.Payload, &alone); err != nil {
			a.t.Fatal(err)
		}
		if !alone {
			a.t.Fatalf("%s names a Run or task", e.Type)
		}
		if feedTypes[e.Type] {
			out = append(out, e)
		}
	}
	return out
}
