package automations

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
)

// A run whose agent called post_to_chat with no Slack installation ends
// failed, saying so, and its owner is told; any other refused post (an
// unknown channel the agent could fix) leaves its outcome to the agent.
func TestAnAgentRunFailsOnAPostWithoutSlack(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_digest", "agent", map[string]any{"kind": "schedule", "cron": "0 15 * * 5", "tz": "UTC"},
		map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.tier, "prompt": "Post the digest."}}})
	exec(t, h.owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES
		('arn_ok', $1, 'aut_digest', 'running', 'schedule'), ('arn_noslack', $1, 'aut_digest', 'running', 'schedule')`, h.org)
	refused := func(run, code, msg, reason string) {
		exec(t, h.owner, `INSERT INTO events (id, organization_id, project_id, event_type, actor_type, actor_id, source, run_id, payload)
			VALUES ($1, $2, $3, $4, 'agent', $5, 'orchestrator', $5, jsonb_build_object('automationRunId', $5::text, 'code', $6::text,
			'error', $7::text, 'reason', NULLIF($8, '')))`,
			"evt_"+run+code, h.org, h.project, agenttools.EvChatPostRefused, run, code, msg, reason)
	}
	refused("arn_ok", "", "there is no channel #nowhere that dude's Slack bot can see: check the name", "")
	// An organisation whose name holds ": " keeps all of it in the reason.
	refused("arn_noslack", agenttools.PostNotConnected, "Slack is not connected for Acme: EU: an organisation admin adds dude's Slack app",
		agenttools.NotConnectedReason("Acme: EU"))
	ctx := context.Background()
	var a Automation
	if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) { a, err = Load(ctx, tx, "aut_digest"); return err }); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"arn_ok", "arn_noslack"} {
		if err := h.runner.finish(ctx, a, id, "succeeded", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]runRow{}
	for _, r := range h.runs("aut_digest") {
		got[r.ID] = r
	}
	if r := got["arn_ok"]; r.Status != "succeeded" {
		t.Errorf("a run with a post the agent could fix: %+v", r)
	}
	if r := got["arn_noslack"]; r.Status != "failed" || r.Reason != "post_to_chat failed: Slack is not connected for Acme: EU" {
		t.Errorf("a run that posted with no Slack: %+v", r)
	}
	var told int
	if err := h.owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE event_type = $1 AND payload->>'runId' = 'arn_noslack'`,
		EvAutomationFailed).Scan(&told); err != nil || told != 1 {
		t.Errorf("its owner told: %d %v", told, err)
	}
}
