package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// incoming adds an enabled incoming automation of the project's (as h.automation does).
func (h *harness) incoming(id string, conditions []Condition, groupBy string, extra map[string]any) {
	h.t.Helper()
	hook := "hk_" + strings.ReplaceAll(strings.ToLower(id), "_", "") + "000000000000"
	h.automation(id, "agent", map[string]any{"kind": "incoming", "hookId": hook, "conditions": conditions,
		"groupBy": groupBy, "ratePerHour": 30, "verify": map[string]any{"mode": "header", "header": "Authorization"}}, extra)
}

// receive stores an accepted request to an automation's URL, as the control plane does.
func (h *harness) receive(automation, id string, body map[string]any) {
	h.t.Helper()
	b, _ := json.Marshal(body)
	exec(h.t, h.owner, `INSERT INTO incoming_deliveries (id, organization_id, automation_id, bytes, payload) VALUES ($1, $2, $3, $4, $5)`,
		id, h.org, automation, len(b), string(b))
}

type deliveryRow struct {
	Verdict, Reason, Group, Run string
	Index                       int // reason_index; -1 for none
}

func (h *harness) delivery(id string) deliveryRow {
	h.t.Helper()
	var d deliveryRow
	if err := h.owner.QueryRow(context.Background(), `SELECT COALESCE(verdict, ''), COALESCE(reason, ''), COALESCE(group_key, ''),
		COALESCE(run_id, ''), COALESCE(reason_index, -1) FROM incoming_deliveries WHERE id = $1`, id).Scan(&d.Verdict, &d.Reason, &d.Group, &d.Run, &d.Index); err != nil {
		h.t.Fatal(err)
	}
	return d
}

func alert(status, group, severity string) map[string]any {
	return map[string]any{"status": status, "groupKey": group, "commonLabels": map[string]any{"alertname": "HighErrorRate", "severity": severity}}
}

// A delivery whose group has a live run goes to that run whatever the
// concurrency (Queue here) and is not checked against the conditions
// (resolved fails "status is firing"); another group starts a run of its
// own; a delivery missing a condition is skipped, saying which.
func TestIncomingGroupFollowUpsReachTheLiveRun(t *testing.T) {
	h := newHarness(t)
	h.incoming("aut_alert", []Condition{{Path: "status", Op: "is", Values: []string{"firing"}},
		{Path: "commonLabels.severity", Op: "one_of", Values: []string{"critical", "high"}}}, "groupKey",
		map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Alert {{payload.commonLabels.alertname}}"}}})

	h.receive("aut_alert", "dlv_1", alert("firing", "g-api", "critical"))
	h.until("the first delivery starts a run", func() bool { return h.delivery("dlv_1").Verdict == VerdictStarted })
	first := h.delivery("dlv_1")
	if first.Group != "g-api" || first.Run == "" {
		t.Fatalf("first: %+v", first)
	}

	h.receive("aut_alert", "dlv_2", alert("resolved", "g-api", "critical"))
	h.receive("aut_alert", "dlv_3", alert("firing", "g-db", "high"))
	h.receive("aut_alert", "dlv_4", alert("firing", "g-web", "warning"))
	h.receive("aut_alert", "dlv_5", alert("resolved", "g-new", "critical"))
	h.until("the four are read", func() bool { return h.delivery("dlv_5").Verdict != "" })

	if d := h.delivery("dlv_2"); d.Verdict != VerdictSentToRun || d.Run != first.Run {
		t.Errorf("resolved of the live group: %+v, want sent to %s", d, first.Run)
	}
	if d := h.delivery("dlv_3"); d.Verdict != VerdictStarted || d.Run == first.Run || d.Run == "" {
		t.Errorf("another group: %+v, want a run of its own", d)
	}
	if d := h.delivery("dlv_4"); d.Verdict != VerdictSkipped || d.Reason != "commonLabels.severity is warning, not one of critical, high" ||
		d.Index != 1 || d.Run != "" {
		t.Errorf("a missed second condition: %+v", d)
	}
	// A group with no live run that misses the first condition.
	if d := h.delivery("dlv_5"); d.Verdict != VerdictSkipped || d.Reason != "status is resolved, not firing" || d.Index != 0 || d.Run != "" {
		t.Errorf("a missed first condition: %+v", d)
	}
	var payloads []Payload
	if err := h.owner.QueryRow(context.Background(), `SELECT payloads FROM automation_runs WHERE id = $1`, first.Run).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 || payloads[0].DeliveryID != "dlv_1" || payloads[1].DeliveryID != "dlv_2" {
		t.Errorf("the live run holds %+v", payloads)
	}
	// The follow-up is in the live run's activity where it arrived; the
	// delivery that started the run is not.
	rows, err := h.owner.Query(context.Background(), `SELECT payload->>'deliveryId', payload->>'source', payload->>'summary',
		occurred_at <= now() FROM events WHERE automation_run_id = $1 AND event_type = $2 ORDER BY cursor`, first.Run, EvFollowUp)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
		Delivery, Source, Summary string
		Past                      bool
	}])
	if err != nil || len(sent) != 1 || sent[0].Delivery != "dlv_2" || sent[0].Source != "incoming" || sent[0].Summary != "resolved · HighErrorRate" {
		t.Errorf("follow-ups in the feed: %+v %v", sent, err)
	}
}

// Deliveries that cannot be decided hold up nothing: 50 whose runs the
// database refuses fill a whole batch, yet a delivery with a group key
// longer than the column takes, one whose missed condition quotes a long
// value, one whose condition lists values past the reason's 2000 characters,
// and another organisation's delivery are all decided; after its
// last try each refused one is skipped, saying why.
func TestIncomingQueueGoesOnPastDeliveriesItCannotDecide(t *testing.T) {
	h := newHarness(t)
	cmd := map[string]any{"command": "true"}
	incoming := func(id string, conditions []Condition, groupBy string) map[string]any {
		return map[string]any{"kind": "incoming", "hookId": "hk_" + strings.ReplaceAll(id, "_", "") + "000000000000",
			"conditions": conditions, "groupBy": groupBy, "ratePerHour": 600, "verify": map[string]any{"mode": "header", "header": "Authorization"}}
	}
	h.automation("aut_bad", "command", incoming("aut_bad", nil, ""), cmd)
	h.automation("aut_good", "command", incoming("aut_good", []Condition{{Path: "status", Op: "is", Values: []string{"firing"}}}, "k"), cmd)
	exec(t, h.owner, `CREATE FUNCTION refuse_bad() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'refused for the test'; END $$`)
	exec(t, h.owner, `CREATE TRIGGER refuse_bad BEFORE INSERT ON automation_runs FOR EACH ROW
		WHEN (NEW.payloads->0->>'deliveryId' LIKE 'bad\_%') EXECUTE FUNCTION refuse_bad()`)
	for i := range 50 {
		h.receive("aut_bad", fmt.Sprintf("bad_%02d", i), map[string]any{"n": i})
	}
	long := strings.Repeat("k", 2100)
	h.receive("aut_good", "dlv_long_key", map[string]any{"status": "firing", "k": long})
	h.receive("aut_good", "dlv_long_value", map[string]any{"status": strings.Repeat("w", 2500), "k": "other"})
	var many []string
	for i := range 400 {
		many = append(many, fmt.Sprintf("v%04d", i))
	}
	h.automation("aut_list", "command", incoming("aut_list", []Condition{{Path: "status", Op: "one_of", Values: many}}, ""), cmd)
	h.receive("aut_list", "dlv_long_list", map[string]any{"status": "resolved"})

	other := dbtest.Org(t, h.owner)
	exec(t, h.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1 || '_p', $1, 'api', 'api', 'API')`, other)
	tr, _ := json.Marshal(incoming("aut_other", nil, ""))
	exec(t, h.owner, `INSERT INTO automations (id, organization_id, project_id, name, kind, enabled, trigger, command, repos)
		VALUES ('aut_other', $1, $1 || '_p', 'Other', 'command', true, $2, 'true', '"none"')`, other, string(tr))
	exec(t, h.owner, `INSERT INTO incoming_deliveries (id, organization_id, automation_id, bytes, payload) VALUES ('dlv_other', $1, 'aut_other', 2, '{}')`, other)

	read := func(id string) string {
		var v string
		if err := h.owner.QueryRow(context.Background(), `SELECT COALESCE(verdict, '') FROM incoming_deliveries WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, _ = h.runner.Sweep(context.Background())
		if read("dlv_long_key") != "" && read("dlv_long_value") != "" && read("dlv_long_list") != "" && read("dlv_other") != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deliveries behind the 50 undecidable ones were never decided")
		}
	}
	if d := h.delivery("dlv_long_key"); d.Verdict != VerdictStarted || d.Group != long[:2000] || d.Run == "" {
		t.Errorf("long group key: %s %d chars run %q", d.Verdict, len(d.Group), d.Run)
	}
	if d := h.delivery("dlv_long_value"); d.Verdict != VerdictSkipped || d.Reason != "status is "+strings.Repeat("w", 200)+"…, not firing" {
		t.Errorf("long value: %s %q", d.Verdict, d.Reason)
	}
	// A condition's own words past 2000 characters: the reason is cut, and the delivery decided at once.
	if d := h.delivery("dlv_long_list"); d.Verdict != VerdictSkipped || len(d.Reason) != 2000 || !strings.HasPrefix(d.Reason, "status is resolved, not one of v0000") {
		t.Errorf("long values list: %s %d %.60q", d.Verdict, len(d.Reason), d.Reason)
	}
	if v := read("dlv_other"); v != VerdictStarted {
		t.Errorf("the other organisation's delivery: %q", v)
	}
	if d := h.delivery("bad_00"); d.Verdict != "" {
		t.Fatalf("an undecidable delivery after one try: %+v", d)
	}
	for range MatchAttempts {
		h.advance(11 * time.Minute)
		_, _ = h.runner.Sweep(context.Background())
	}
	for _, id := range []string{"bad_00", "bad_49"} {
		if d := h.delivery(id); d.Verdict != VerdictSkipped || !strings.HasPrefix(d.Reason, "could not be matched: ") ||
			!strings.Contains(d.Reason, "refused for the test") || d.Run != "" {
			t.Errorf("%s after its last try: %+v", id, d)
		}
	}
}

// A follow-up is kept in the live run by reference ({deliveryId, groupKey}):
// its body is read from incoming_deliveries when it is sent to the agent.
// A run already holding FollowUpCap deliveries takes no more: the next is
// skipped, saying so.
func TestIncomingFollowUpsByReferenceUpToTheCap(t *testing.T) {
	h := newHarness(t)
	h.incoming("aut_ref", nil, "groupKey",
		map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Now {{payload.status}}"}}})
	h.receive("aut_ref", "dlv_r1", alert("firing", "g", "high"))
	h.until("the first runs", func() bool {
		r := h.runs("aut_ref")
		return len(r) == 1 && r[0].Status == "running" && r[0].LuxRunID != ""
	})
	run := h.runs("aut_ref")[0]
	h.receive("aut_ref", "dlv_r2", alert("resolved", "g", "high"))
	requestID := "automation-" + run.ID + "-1"
	h.until("the follow-up reaches the agent", func() bool { return len(h.fake.InputBodies(run.LuxRunID, requestID)) > 0 })
	var input struct{ Text string }
	_ = json.Unmarshal([]byte(h.fake.InputBodies(run.LuxRunID, requestID)[0]), &input)
	if !strings.Contains(input.Text, "⟧resolved⟦/external ") {
		t.Errorf("the follow-up's input: %s", input.Text)
	}
	var second string
	if err := h.owner.QueryRow(context.Background(), `SELECT payloads->1 FROM automation_runs WHERE id = $1`, run.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	_ = json.Unmarshal([]byte(second), &ref)
	if _, hasBody := ref["body"]; hasBody || ref["deliveryId"] != "dlv_r2" || ref["groupKey"] != "g" {
		t.Errorf("the run keeps the follow-up as %s, want a reference", second)
	}

	// Fill the run up to the cap, then one more.
	exec(t, h.owner, `UPDATE automation_runs SET payloads = payloads || (SELECT jsonb_agg(jsonb_build_object('deliveryId', 'x' || n,
		'event', 'incoming', 'groupKey', 'g')) FROM generate_series(1, $2 - 2) n), inputs_sent = $2 WHERE id = $1`, run.ID, FollowUpCap)
	h.receive("aut_ref", "dlv_over", alert("firing", "g", "high"))
	h.until("the one over the cap is read", func() bool { return h.delivery("dlv_over").Verdict != "" })
	if d := h.delivery("dlv_over"); d.Verdict != VerdictSkipped || d.Reason != fmt.Sprintf("the live run already holds %d deliveries", FollowUpCap) || d.Run != "" {
		t.Errorf("over the cap: %+v", d)
	}
	if r := h.runs("aut_ref"); len(r) != 1 || r[0].Payloads != FollowUpCap {
		t.Errorf("runs %+v", r)
	}
}

// Deliveries Fire sends into a live run (Concurrency send, each of another
// group) are held by reference too, and no more than FollowUpCap: the rest
// are skipped, saying so.
func TestIncomingSentToARunByReferenceUpToTheCap(t *testing.T) {
	h := newHarness(t)
	h.incoming("aut_flood", nil, "g", map[string]any{"concurrency": "send", "session_mode": "resumable", "expiry_days": 7,
		"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "x"}}})
	h.receive("aut_flood", "s_00", map[string]any{"g": "0"})
	h.until("the first runs", func() bool { r := h.runs("aut_flood"); return len(r) == 1 && r[0].Status == "running" })
	for i := 1; i <= 60; i++ {
		h.receive("aut_flood", fmt.Sprintf("s_%02d", i), map[string]any{"g": fmt.Sprint(i)})
	}
	h.until("the 60 are read", func() bool {
		var n int
		_ = h.owner.QueryRow(context.Background(), `SELECT count(*) FROM incoming_deliveries WHERE automation_id = 'aut_flood' AND verdict IS NULL`).Scan(&n)
		return n == 0
	})
	run := h.runs("aut_flood")
	var byValue int
	if err := h.owner.QueryRow(context.Background(), `SELECT count(*) FROM automation_runs r, jsonb_array_elements(r.payloads) WITH ORDINALITY p(e, i)
		WHERE r.id = $1 AND i > 1 AND e ? 'body'`, run[0].ID).Scan(&byValue); err != nil {
		t.Fatal(err)
	}
	if len(run) != 1 || run[0].Payloads != FollowUpCap || byValue != 0 {
		t.Errorf("the run holds %d payloads, %d follow-ups by value (runs %+v)", run[0].Payloads, byValue, run)
	}
	if d := h.delivery("s_49"); d.Verdict != VerdictSentToRun || d.Run != run[0].ID {
		t.Errorf("the 49th follow-up: %+v", d)
	}
	if d := h.delivery("s_60"); d.Verdict != VerdictSkipped || d.Reason != fmt.Sprintf("the live run already holds %d deliveries", FollowUpCap) || d.Run != "" {
		t.Errorf("over the cap: %+v", d)
	}
}

// A follow-up that arrives while its group's run is still queued does not
// replace the delivery that started it: the run's prompt is built from the
// first delivery, and the follow-up reaches the agent as its first input.
func TestIncomingRunStartsFromItsFirstDelivery(t *testing.T) {
	h := newHarness(t)
	// A tool line: the scripted agent's spec keeps it, so the prompt's substituted value shows there.
	h.incoming("aut_first", nil, "groupKey",
		map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "tool: note {{payload.status}}"}}})
	// Hold the run queued: its automation has a running run of its own.
	exec(t, h.owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source, payloads, session, started_at)
		VALUES ('arn_busy', $1, 'aut_first', 'running', 'event', '[]', 'ephemeral', now())`, h.org)
	h.receive("aut_first", "dlv_f1", alert("firing", "g", "high"))
	h.until("the first is read", func() bool { return h.delivery("dlv_f1").Verdict == VerdictStarted })
	h.receive("aut_first", "dlv_f2", alert("resolved", "g", "high"))
	h.until("the follow-up is read", func() bool { return h.delivery("dlv_f2").Verdict != "" })
	run := h.delivery("dlv_f1").Run
	if d := h.delivery("dlv_f2"); d.Verdict != VerdictSentToRun || d.Run != run {
		t.Fatalf("follow-up of a queued run: %+v", d)
	}
	exec(t, h.owner, `UPDATE automation_runs SET status = 'succeeded', ended_at = now() WHERE id = 'arn_busy'`)
	var lr string
	h.until("the run starts", func() bool {
		for _, r := range h.runs("aut_first") {
			if r.ID == run && r.LuxRunID != "" {
				lr = r.LuxRunID
			}
		}
		return lr != ""
	})
	requestID := "automation-" + run + "-1"
	h.until("the follow-up reaches the agent", func() bool { return len(h.fake.InputBodies(lr, requestID)) > 0 })
	spec := string(h.fake.SpecOf(lr))
	var input struct{ Text string }
	_ = json.Unmarshal([]byte(h.fake.InputBodies(lr, requestID)[0]), &input)
	if !strings.Contains(spec, "⟧firing⟦/external ") || strings.Contains(spec, "resolved") {
		t.Errorf("the run's spec: %s", spec)
	}
	if !strings.Contains(input.Text, "⟧resolved⟦/external ") {
		t.Errorf("the first input: %q", input.Text)
	}
}

// A command automation with Concurrency merge: the deliveries merged into
// its waiting run are all in that run's event file, bodies included.
func TestIncomingMergedDeliveriesReachACommandRun(t *testing.T) {
	h := newHarness(t)
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	h.automation("aut_merge", "command", map[string]any{"kind": "incoming", "hookId": "hk_merge000000000000", "ratePerHour": 30,
		"verify": map[string]any{"mode": "header", "header": "Authorization"}},
		map[string]any{"command": g.hold(`cat "$DUDE_EVENT_PATH"`), "concurrency": "merge"})
	h.receive("aut_merge", "m_1", map[string]any{"n": 1})
	h.until("m_1 runs", func() bool { r := h.runs("aut_merge"); return len(r) == 1 && r[0].Status == "running" })
	h.receive("aut_merge", "m_2", map[string]any{"n": 2})
	h.until("m_2 is read", func() bool { return h.delivery("m_2").Verdict != "" })
	h.receive("aut_merge", "m_3", map[string]any{"n": 3})
	h.until("m_3 is read", func() bool { return h.delivery("m_3").Verdict != "" })
	m2, m3 := h.delivery("m_2"), h.delivery("m_3")
	if m2.Verdict != VerdictStarted || m3.Verdict != VerdictSentToRun || m3.Run != m2.Run {
		t.Fatalf("m_2 %+v, m_3 %+v", m2, m3)
	}
	g.release(t)
	h.until("the merged run ends", func() bool { return h.run("aut_merge", m2.Run).Status == "succeeded" })
	var list []struct {
		DeliveryID string `json:"deliveryId"`
		Body       struct{ N int }
	}
	log := h.run("aut_merge", m2.Run).Log
	if err := json.Unmarshal([]byte(strings.TrimSpace(log)), &list); err != nil || len(list) != 2 ||
		list[0].DeliveryID != "m_2" || list[0].Body.N != 2 || list[1].DeliveryID != "m_3" || list[1].Body.N != 3 {
		t.Errorf("the merged run's event file %q: %v", log, err)
	}
}

// A delivery accepted while its automation was on is skipped if it is off
// by the time the loop reads it, and an organisation incoming automation
// naming no project skips every delivery, saying so.
func TestIncomingSkippedWhenOffOrWithNoProject(t *testing.T) {
	h := newHarness(t)
	cmd := map[string]any{"command": "true"}
	h.incoming("aut_off", nil, "", cmd)
	exec(t, h.owner, `UPDATE automations SET kind = 'command', agent = NULL, enabled = false WHERE id = 'aut_off'`)
	h.incoming("aut_noproj", nil, "", map[string]any{"command": "true", "project_id": nil})
	exec(t, h.owner, `UPDATE automations SET kind = 'command', agent = NULL WHERE id = 'aut_noproj'`)
	h.receive("aut_off", "dlv_off", alert("firing", "g", "high"))
	h.receive("aut_noproj", "dlv_noproj", alert("firing", "g", "high"))
	h.until("both read", func() bool { return h.delivery("dlv_off").Verdict != "" && h.delivery("dlv_noproj").Verdict != "" })
	if d := h.delivery("dlv_off"); d.Verdict != VerdictSkipped || d.Reason != "the automation was turned off" || d.Run != "" {
		t.Errorf("turned off: %+v", d)
	}
	if d := h.delivery("dlv_noproj"); d.Verdict != VerdictSkipped || d.Reason != IncomingNeedsProject || d.Run != "" {
		t.Errorf("no project: %+v", d)
	}
	if r := append(h.runs("aut_off"), h.runs("aut_noproj")...); len(r) != 0 {
		t.Errorf("runs %+v", r)
	}
}

// A test run is not a live run for real deliveries: a real delivery of a
// test run's group starts a run of its own, and the test run is sent nothing.
func TestIncomingRealDeliveryIsNotSentToATestRun(t *testing.T) {
	h := newHarness(t)
	h.incoming("aut_tst", nil, "groupKey",
		map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "x"}}})
	test, _, err := h.runner.RunNow(context.Background(), h.org, "aut_tst", h.person, "", &Payload{Body: json.RawMessage(`{"groupKey":"g"}`)})
	if err != nil {
		t.Fatal(err)
	}
	h.until("the test run runs", func() bool { return h.run("aut_tst", test).Status == "running" })
	h.receive("aut_tst", "dlv_real", alert("firing", "g", "high"))
	h.until("read", func() bool { return h.delivery("dlv_real").Verdict != "" })
	if d := h.delivery("dlv_real"); d.Verdict != VerdictStarted || d.Run == test || d.Run == "" {
		t.Errorf("a real delivery of a test run's group: %+v (test run %s)", d, test)
	}
	if r := h.run("aut_tst", test); r.Payloads != 1 {
		t.Errorf("the test run was sent the delivery: %+v", r)
	}
}

// What a firing did is the delivery's verdict: skipped by the concurrency
// (Skip) with the run that says so, and sent to another group's run (Send),
// which is not "started".
func TestIncomingVerdictSaysWhatTheConcurrencyDid(t *testing.T) {
	h := newHarness(t)
	agent := map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "x"}}
	h.incoming("aut_skip", nil, "groupKey", map[string]any{"agent": agent, "concurrency": "skip"})
	h.incoming("aut_sendc", nil, "groupKey", map[string]any{"agent": agent, "concurrency": "send", "session_mode": "resumable", "expiry_days": 7})
	for _, a := range []string{"aut_skip", "aut_sendc"} {
		h.receive(a, a+"_A", alert("firing", "A", "high"))
		h.until(a+" A starts", func() bool { return h.delivery(a+"_A").Verdict == VerdictStarted })
		h.until(a+" A runs", func() bool { r := h.runs(a); return len(r) == 1 && r[0].Status == "running" })
		h.receive(a, a+"_B", alert("firing", "B", "high"))
		h.until(a+" B read", func() bool { return h.delivery(a+"_B").Verdict != "" })
	}
	skipped := h.delivery("aut_skip_B")
	runs := h.runs("aut_skip")
	if skipped.Verdict != VerdictSkipped || skipped.Reason == "" || len(runs) != 2 || skipped.Run != runs[1].ID || runs[1].Status != "skipped" ||
		skipped.Reason != runs[1].Reason {
		t.Errorf("concurrency skip: %+v, runs %+v", skipped, runs)
	}
	if d := h.delivery("aut_sendc_B"); d.Verdict != VerdictSentToRun || d.Run != h.delivery("aut_sendc_A").Run {
		t.Errorf("concurrency send to another group's run: %+v", d)
	}
}

// Matching a GitHub delivery keeps, per automation on its event, what it
// came to for that automation's Deliveries tab: skipped with the filter in
// words, or the run it started.
func TestGitHubDeliveryVerdictsAreKeptPerAutomation(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_gh_skip", "command", map[string]any{"kind": "github", "event": "pull_request",
		"filters": map[string]any{"author": []string{"!dependabot[bot]"}}}, map[string]any{"command": "true"})
	h.automation("aut_gh_fire", "command", map[string]any{"kind": "github", "event": "pull_request"}, map[string]any{"command": "true"})
	h.deliver("d-gh", "pull_request", prBody("opened", "acme/web", "dependabot[bot]"))
	type verdict struct{ Automation, Verdict, Reason, Run string }
	read := func() []verdict {
		rows, err := h.owner.Query(context.Background(), `SELECT automation_id, verdict, COALESCE(reason, ''), COALESCE(run_id, '')
			FROM automation_github_verdicts WHERE delivery_id = 'd-gh' ORDER BY automation_id`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[verdict])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	h.until("both verdicts", func() bool { return len(read()) == 2 })
	runs := h.runs("aut_gh_fire")
	want := []verdict{{"aut_gh_fire", VerdictStarted, "", runs[0].ID}, {"aut_gh_skip", VerdictSkipped, "Author: it is by dependabot[bot], who is excluded", ""}}
	if got := read(); len(runs) != 1 || !slices.Equal(got, want) {
		t.Errorf("verdicts %+v, want %+v (runs %+v)", got, want, runs)
	}
}

// An organisation incoming automation runs in its projects only: it checks
// out their repositories (all, or the ids its repos names), its tools may
// reach only theirs, and create_task files in its first project.
func TestOrgIncomingRunIsScopedToItsProjects(t *testing.T) {
	h := newHarness(t)
	p2 := h.org + "_p2"
	exec(t, h.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'api', 'api', 'API')`, p2, h.org)
	exec(t, h.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		($1 || '_r', $2, $1, 'api', 'https://github.com/acme/api.git'),
		($3 || '_docs', $2, $3, 'docs', 'https://github.com/acme/docs.git')`, p2, h.org, h.project)
	h.incoming("aut_org", nil, "", map[string]any{"command": "true", "project_id": nil})
	exec(t, h.owner, `UPDATE automations SET kind = 'command', agent = NULL, project_ids = ARRAY[$1], repos = '"all"' WHERE id = 'aut_org'`, h.project)
	exec(t, h.owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_org', $1, 'aut_org', 'queued', 'event')`, h.org)
	ctx := context.Background()
	names := func() []string {
		var a Automation
		var got []string
		if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) error {
			var err error
			if a, err = Load(ctx, tx, "aut_org"); err != nil {
				return err
			}
			repos, err := automationRepos(ctx, tx, a, nil)
			for _, r := range repos {
				got = append(got, r.Name)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := names(); !slices.Equal(got, []string{"docs", "web"}) {
		t.Errorf("checked out with repos all: %v, want P1's docs and web", got)
	}
	exec(t, h.owner, `UPDATE automations SET repos = to_jsonb(ARRAY[$1 || '_docs']) WHERE id = 'aut_org'`, h.project)
	if got := names(); !slices.Equal(got, []string{"docs"}) {
		t.Errorf("checked out with repos [docs]: %v", got)
	}
	var a Automation
	if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) { a, err = Load(ctx, tx, "aut_org"); return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runner.toolsToken(ctx, a, "arn_org", nil); err != nil {
		t.Fatal(err)
	}
	var scope struct {
		ProjectID string   `json:"projectId"`
		Repos     []string `json:"repos"`
	}
	if err := h.owner.QueryRow(ctx, `SELECT tools_scope FROM automation_runs WHERE id = 'arn_org'`).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	slices.Sort(scope.Repos)
	if scope.ProjectID != h.project || !slices.Equal(scope.Repos, []string{"acme/docs", "acme/web"}) {
		t.Errorf("tools scope %+v, want project %s and its repositories only", scope, h.project)
	}
}

// Once the group's run has ended, the next delivery of that group is
// checked and fires afresh.
func TestIncomingAfterTheRunEndedStartsAgain(t *testing.T) {
	h := newHarness(t)
	h.incoming("aut_cmd", nil, "groupKey", map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "x"}}})
	h.receive("aut_cmd", "dlv_a", alert("firing", "g", "high"))
	h.until("started", func() bool { return h.delivery("dlv_a").Verdict == VerdictStarted })
	exec(t, h.owner, `UPDATE automation_runs SET status = 'succeeded', ended_at = now() WHERE id = $1`, h.delivery("dlv_a").Run)
	h.receive("aut_cmd", "dlv_b", alert("resolved", "g", "high"))
	h.until("read", func() bool { return h.delivery("dlv_b").Verdict != "" })
	if d := h.delivery("dlv_b"); d.Verdict != VerdictStarted || d.Run == h.delivery("dlv_a").Run {
		t.Errorf("after the run ended: %+v", d)
	}
}

// Run now on an incoming webhook: a past delivery's body or a written one
// is a test run of its own, its conditions unchecked; a refused delivery
// keeps no body to run on.
func TestIncomingRunNowWithADeliveryOrABody(t *testing.T) {
	h := newHarness(t)
	h.incoming("aut_now", []Condition{{Path: "status", Op: "is", Values: []string{"firing"}}}, "groupKey",
		map[string]any{"command": "true"})
	exec(t, h.owner, `UPDATE automations SET kind = 'command', agent = NULL WHERE id = 'aut_now'`)
	h.receive("aut_now", "dlv_past", alert("resolved", "g", "high"))
	exec(t, h.owner, `INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes) VALUES ('dlv_ref', $1, 'aut_now', 'refused', 'bad_signature', 3)`, h.org)
	ctx := context.Background()
	run, v, err := h.runner.RunNow(ctx, h.org, "aut_now", h.person, "dlv_past", nil)
	if err != nil || !v.Matches || run == "" {
		t.Fatalf("past delivery: %q %+v %v", run, v, err)
	}
	written, _, err := h.runner.RunNow(ctx, h.org, "aut_now", h.person, "", &Payload{Body: json.RawMessage(`{"status":"firing","groupKey":"g2"}`)})
	if err != nil || written == "" || written == run {
		t.Fatalf("written body: %q %v", written, err)
	}
	if _, _, err := h.runner.RunNow(ctx, h.org, "aut_now", h.person, "dlv_ref", nil); !errors.Is(err, ErrBadRequest) {
		t.Errorf("a refused delivery: %v, want a bad request", err)
	}
	var sources []string
	rows, _ := h.owner.Query(ctx, `SELECT source || ':' || (payloads->0->>'groupKey') FROM automation_runs WHERE automation_id = 'aut_now' ORDER BY created_at, id`)
	sources, _ = pgx.CollectRows(rows, pgx.RowTo[string])
	if !slices.Equal(sources, []string{"test:g", "test:g2"}) {
		t.Errorf("runs %v", sources)
	}
}

// Refused rows go after 7 days; the other deliveries keep their row and
// lose their body after 30.
func TestRetainIncomingRefusedSevenDaysBodiesThirty(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	now := time.Now()
	exec(t, owner, `INSERT INTO automations (id, organization_id, name, kind, trigger, agent)
		VALUES ('aut_ret', $1, 'R', 'agent', '{"kind":"incoming","hookId":"hk_ret000000000000"}', '{"own":{"prompt":""}}')`, org)
	day := 24 * time.Hour
	exec(t, owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_ret', $1, 'aut_ret', 'succeeded', 'event')`, org)
	hdr := `{"User-Agent":"Grafana"}`
	exec(t, owner, `INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes, payload, received_at, headers, run_id) VALUES
		('ref_8', $1, 'aut_ret', 'refused', 'bad_signature', 1, NULL, $2, '{}', NULL),
		('ref_6', $1, 'aut_ret', 'refused', 'rate_limited', 1, NULL, $3, '{}', NULL),
		('skip_31', $1, 'aut_ret', 'skipped', 'x', 2, '{}', $4, $6, NULL),
		('skip_29', $1, 'aut_ret', 'skipped', 'x', 2, '{}', $5, $6, NULL),
		('ran_31', $1, 'aut_ret', 'started', NULL, 2, '{}', $4, $6, 'arn_ret'),
		('ok_29', $1, 'aut_ret', 'started', NULL, 2, '{}', $5, $6, 'arn_ret'),
		('unread_31', $1, 'aut_ret', NULL, NULL, 2, '{}', $4, $6, NULL)`,
		org, now.Add(-8*day), now.Add(-6*day), now.Add(-31*day), now.Add(-29*day), hdr)
	exec(t, owner, `INSERT INTO incoming_refusals (organization_id, automation_id, hour, code, n) VALUES
		($1, 'aut_ret', date_trunc('hour', $2::timestamptz), 'bad_signature', 400), ($1, 'aut_ret', date_trunc('hour', $3::timestamptz), 'bad_signature', 9)`,
		org, now.Add(-8*day), now.Add(-6*day))
	if _, err := Retain(ctx, app, now); err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID            string
		Body, Headers bool
	}
	rows, _ := owner.Query(ctx, `SELECT id, payload IS NOT NULL, headers <> '{}' FROM incoming_deliveries WHERE organization_id = $1 ORDER BY id`, org)
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		t.Fatal(err)
	}
	// Past 30 days: a delivery no run holds goes; one a run holds keeps its row, without body or headers.
	want := []row{{"ok_29", true, true}, {"ran_31", false, false}, {"ref_6", false, false}, {"skip_29", true, true}, {"unread_31", true, true}}
	if !slices.Equal(got, want) {
		t.Errorf("after retention %+v, want %+v (an unread body waits for its reader)", got, want)
	}
	var counted []int
	rows, _ = owner.Query(ctx, `SELECT n FROM incoming_refusals WHERE organization_id = $1 ORDER BY n`, org)
	if counted, err = pgx.CollectRows(rows, pgx.RowTo[int]); err != nil || !slices.Equal(counted, []int{9}) {
		t.Errorf("refusal counts after retention %v %v, want the 6-day-old one only", counted, err)
	}
}
