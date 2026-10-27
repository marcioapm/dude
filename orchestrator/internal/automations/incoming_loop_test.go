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

type deliveryRow struct{ Verdict, Reason, Group, Run string }

func (h *harness) delivery(id string) deliveryRow {
	h.t.Helper()
	var d deliveryRow
	if err := h.owner.QueryRow(context.Background(), `SELECT COALESCE(verdict, ''), COALESCE(reason, ''), COALESCE(group_key, ''),
		COALESCE(run_id, '') FROM incoming_deliveries WHERE id = $1`, id).Scan(&d.Verdict, &d.Reason, &d.Group, &d.Run); err != nil {
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
	h.until("the three are read", func() bool { return h.delivery("dlv_4").Verdict != "" })

	if d := h.delivery("dlv_2"); d.Verdict != VerdictSentToRun || d.Run != first.Run {
		t.Errorf("resolved of the live group: %+v, want sent to %s", d, first.Run)
	}
	if d := h.delivery("dlv_3"); d.Verdict != VerdictStarted || d.Run == first.Run || d.Run == "" {
		t.Errorf("another group: %+v, want a run of its own", d)
	}
	if d := h.delivery("dlv_4"); d.Verdict != VerdictSkipped || d.Reason != "commonLabels.severity is warning, not one of critical, high" || d.Run != "" {
		t.Errorf("a missed condition: %+v", d)
	}
	var payloads []Payload
	if err := h.owner.QueryRow(context.Background(), `SELECT payloads FROM automation_runs WHERE id = $1`, first.Run).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if got := []string{payloads[0].DeliveryID, payloads[1].DeliveryID}; len(payloads) != 2 || !slices.Equal(got, []string{"dlv_1", "dlv_2"}) {
		t.Errorf("the live run holds %+v", payloads)
	}
}

// Deliveries that cannot be decided hold up nothing: 50 whose runs the
// database refuses fill a whole batch, yet a delivery with a group key
// longer than the column takes, one whose missed condition quotes a long
// value, and another organisation's delivery are all decided; after its
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
		if read("dlv_long_key") != "" && read("dlv_long_value") != "" && read("dlv_other") != "" {
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
	exec(t, owner, `INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes, payload, received_at) VALUES
		('ref_8', $1, 'aut_ret', 'refused', 'bad_signature', 1, NULL, $2),
		('ref_6', $1, 'aut_ret', 'refused', 'rate_limited', 1, NULL, $3),
		('ok_31', $1, 'aut_ret', 'skipped', 'x', 2, '{}', $4),
		('ok_29', $1, 'aut_ret', 'started', NULL, 2, '{}', $5),
		('unread_31', $1, 'aut_ret', NULL, NULL, 2, '{}', $4)`,
		org, now.Add(-8*day), now.Add(-6*day), now.Add(-31*day), now.Add(-29*day))
	if _, err := Retain(ctx, app, now); err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID   string
		Body bool
	}
	rows, _ := owner.Query(ctx, `SELECT id, payload IS NOT NULL FROM incoming_deliveries WHERE organization_id = $1 ORDER BY id`, org)
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		t.Fatal(err)
	}
	want := []row{{"ok_29", true}, {"ok_31", false}, {"ref_6", false}, {"unread_31", true}}
	if !slices.Equal(got, want) {
		t.Errorf("after retention %+v, want %+v (an unread body waits for its reader)", got, want)
	}
}
