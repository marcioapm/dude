package automations

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// postHarness is an agent automation whose runs' post_to_chat events are
// written as the tool writes them (run_id set, at given times).
type postHarness struct {
	*harness
	t  *testing.T
	at time.Time
	n  int
}

func newPostHarness(t *testing.T, runs ...string) postHarness {
	h := newHarness(t)
	h.automation("aut_digest", "agent", map[string]any{"kind": "schedule", "cron": "0 15 * * 5", "tz": "UTC"},
		map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.tier, "prompt": "Post the digest."}}})
	for _, id := range runs {
		exec(t, h.owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ($1, $2, 'aut_digest', 'running', 'schedule')`,
			id, h.org)
	}
	return postHarness{harness: h, t: t, at: time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)}
}

// event writes one of run's events, a second after the last, and answers its id.
func (h *postHarness) event(run, typ string, payload map[string]any) string {
	h.n++
	h.at = h.at.Add(time.Second)
	payload["automationRunId"] = run
	id := "evt_" + run + "_" + string(rune('a'+h.n))
	exec(h.t, h.owner, `INSERT INTO events (id, organization_id, project_id, event_type, actor_type, actor_id, source, run_id, occurred_at, payload)
		VALUES ($1, $2, $3, $4, 'agent', $5, 'orchestrator', $5, $6, $7)`, id, h.org, h.project, typ, run, h.at, payload)
	return id
}

func (h *postHarness) notConnected(run, org string) string {
	return h.event(run, agenttools.EvSlackPostRefused, map[string]any{"tool": "post_to_chat", "code": agenttools.PostNotConnected,
		"error": "Slack is not connected for " + org + ": an organisation admin adds dude's Slack app", "reason": agenttools.NotConnectedReason(org)})
}

func (h *postHarness) posted(run string) {
	h.event(run, agenttools.EvSlackPosted, map[string]any{"channel": "#engineering", "ts": "1.2"})
}

// finish ends every run as its agent's turn ending would: "succeeded".
func (h *postHarness) finish(runs ...string) map[string]runRow {
	return h.finishAs("succeeded", "", runs...)
}

// finishAs ends every run with status and reason, as the runner ends one.
func (h *postHarness) finishAs(status, reason string, runs ...string) map[string]runRow {
	ctx := context.Background()
	var a Automation
	if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) { a, err = Load(ctx, tx, "aut_digest"); return err }); err != nil {
		h.t.Fatal(err)
	}
	for _, id := range runs {
		if err := h.runner.finish(ctx, a, id, status, reason, nil); err != nil {
			h.t.Fatal(err)
		}
	}
	got := map[string]runRow{}
	for _, r := range h.runs("aut_digest") {
		got[r.ID] = r
	}
	return got
}

// failedOn is the event the runner recorded run as failed on; "" for none.
func (h *postHarness) failedOn(run string) string {
	var id string
	err := h.owner.QueryRow(context.Background(), `SELECT payload->>'eventId' FROM events WHERE run_id = $1 AND event_type = $2`,
		run, EvRunFailedOnPost).Scan(&id)
	if err != nil && !db.IsNotFound(err) {
		h.t.Fatal(err)
	}
	return id
}

// A run whose agent called post_to_chat with no Slack installation ends
// failed, saying so, and its owner is told; any other refused post (an
// unknown channel the agent could fix) leaves its outcome to the agent.
func TestAnAgentRunFailsOnAPostWithoutSlack(t *testing.T) {
	h := newPostHarness(t, "arn_ok", "arn_noslack")
	h.event("arn_ok", agenttools.EvSlackPostRefused, map[string]any{"tool": "post_to_chat",
		"error": "there is no channel #nowhere that dude's Slack bot can see: check the name"})
	// An organisation whose name holds ": " keeps all of it in the reason.
	refusal := h.notConnected("arn_noslack", "Acme: EU")
	got := h.finish("arn_ok", "arn_noslack")
	if r := got["arn_ok"]; r.Status != "succeeded" {
		t.Errorf("a run with a post the agent could fix: %+v", r)
	}
	if r := got["arn_noslack"]; r.Status != "failed" || r.Reason != "post_to_chat failed: Slack is not connected for Acme: EU" {
		t.Errorf("a run that posted with no Slack: %+v", r)
	}
	if on := h.failedOn("arn_noslack"); on != refusal {
		t.Errorf("the run failed on %q, want the refusal %q", on, refusal)
	}
	if on := h.failedOn("arn_ok"); on != "" {
		t.Errorf("a succeeded run names a call it failed on: %q", on)
	}
	var told int
	if err := h.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = $1 AND payload->>'runId' = 'arn_noslack'`,
		EvAutomationFailed).Scan(&told); err != nil || told != 1 {
		t.Errorf("its owner told: %d %v", told, err)
	}
}

// A run that failed for its own reason (timed out, crashed) keeps it, and
// names no post_to_chat call, though one was refused for want of Slack.
func TestARunThatFailedForAnotherReasonNamesNoPost(t *testing.T) {
	h := newPostHarness(t, "arn_to")
	h.notConnected("arn_to", "Acme")
	got := h.finishAs("failed", "timed out after 30 minutes", "arn_to")
	if r := got["arn_to"]; r.Status != "failed" || r.Reason != "timed out after 30 minutes" {
		t.Errorf("a run that timed out: %+v", r)
	}
	if on := h.failedOn("arn_to"); on != "" {
		t.Errorf("a timed-out run names the refusal %q", on)
	}
}

// Slack connected during the run: a post that went after the last
// not-connected refusal leaves the run succeeded; a refusal after the post
// still fails it.
func TestAPostAfterSlackIsConnectedClearsTheRunsFailure(t *testing.T) {
	h := newPostHarness(t, "arn_then_posted", "arn_posted_then_not")
	h.notConnected("arn_then_posted", "Acme")
	h.posted("arn_then_posted")
	h.posted("arn_posted_then_not")
	refusal := h.notConnected("arn_posted_then_not", "Acme")
	got := h.finish("arn_then_posted", "arn_posted_then_not")
	if r := got["arn_then_posted"]; r.Status != "succeeded" {
		t.Errorf("refused, then posted once Slack was connected: %+v", r)
	}
	if r := got["arn_posted_then_not"]; r.Status != "failed" || r.Reason != "post_to_chat failed: Slack is not connected for Acme" {
		t.Errorf("posted, then refused once Slack was gone: %+v", r)
	}
	if on := h.failedOn("arn_posted_then_not"); on != refusal {
		t.Errorf("posted then refused: failed on %q, want %q", on, refusal)
	}
	if on := h.failedOn("arn_then_posted"); on != "" {
		t.Errorf("refused then posted names %q", on)
	}
}
