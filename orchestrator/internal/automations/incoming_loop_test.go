package automations

import (
	"context"
	"encoding/json"
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
