package orchestrator_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// tierOn adds a tier of the session world's organisation requesting model.
func (s *sessionWorld) tierOn(name, model string) string {
	s.t.Helper()
	id := "mtr_" + strings.ToLower(name) + "_" + s.org
	mustExec(s.t, s.owner, `INSERT INTO model_tiers (id, organization_id, name, model, effort, position) VALUES ($1, $2, $3, $4, 'high', 20)`,
		id, s.org, name, model)
	return id
}

// brainstormOrg sets the organisation's Brainstorm harness.
func (s *sessionWorld) brainstormOrg(harness string) {
	s.t.Helper()
	mustExec(s.t, s.owner, `UPDATE organizations SET default_agent_models = jsonb_set(default_agent_models, '{brainstorm}',
		COALESCE(default_agent_models->'brainstorm', '{}'::jsonb) || jsonb_build_object('harness', $2::text)) WHERE id = $1`, s.org, harness)
}

// submitted is the session's agent once lux has it: its model, tier and
// harness as runs records them.
func (s *sessionWorld) submitted(session string) (run, model, tier, harness string) {
	s.t.Helper()
	s.until("the session's agent submitted", func() bool {
		run, _ = s.brainstorm(session)
		return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id IS NOT NULL`, run) == 1
	})
	_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(model, ''), COALESCE(model_tier, ''), COALESCE(harness, '') FROM runs WHERE id = $1`,
		run).Scan(&model, &tier, &harness)
	return run, model, tier, harness
}

// endRun ends the session's agent as its container stopping would, so the
// next message starts another.
func (s *sessionWorld) endRun(run string) {
	s.t.Helper()
	mustExec(s.t, s.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = $1`, run)
}

// foreignTier is a tier of a second organisation's, requesting a model
// any harness here would take.
func (s *sessionWorld) foreignTier() string {
	s.t.Helper()
	org := s.otherOrg()
	id := "mtr_foreign_" + org
	mustExec(s.t, s.owner, `INSERT INTO model_tiers (id, organization_id, name, model, position) VALUES ($1, $2, 'Foreign', 'fake/scripted', 0)`, id, org)
	return id
}

// otherOrg is a second organisation, made once per world.
func (s *sessionWorld) otherOrg() string {
	s.t.Helper()
	id := "org_other_" + s.org
	mustExec(s.t, s.owner, `INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1) ON CONFLICT DO NOTHING`, id)
	return id
}

// A session made with its own tier and harness stores them, its agent is
// submitted with them, and its detail says so.
func TestASessionsOwnTierAndHarnessAreWhatItsAgentRunsOn(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	opus := s.tierOn("Opus", "claude-opus-5")
	out := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "tier": opus, "harness": "claude-code"})
	id := out["id"].(string)
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND tier = $2 AND harness = 'claude-code'`, id, opus); n != 1 {
		t.Fatalf("the session does not store its choice")
	}
	_, model, tier, harness := s.submitted(id)
	if model != "claude-opus-5" || tier != "Opus" || harness != "claude-code" {
		t.Errorf("submitted on %s (%s) · %s, want claude-opus-5 (Opus) · claude-code", model, tier, harness)
	}
	m := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any)
	eff := m["effective"].(map[string]any)
	if m["harness"] != "claude-code" || m["tier"].(map[string]any)["name"] != "Opus" || m["tier"].(map[string]any)["effort"] != "high" ||
		eff["tierName"] != "Opus" || eff["model"] != "claude-opus-5" || eff["harness"] != "claude-code" {
		t.Errorf("detail model = %v", m)
	}
}

// A session that sets only its harness runs on the organisation's tier,
// and one that sets neither on the organisation's both.
func TestASessionWithOnlyAHarnessTakesTheOrganisationsTier(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "harness": "codex"})["id"].(string)
	_, model, tier, harness := s.submitted(id)
	if model != "fake/scripted" || tier != "Thinker" || harness != "codex" {
		t.Errorf("submitted on %s (%s) · %s, want the organisation's Thinker on codex", model, tier, harness)
	}
	m := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any)
	if m["tier"] != nil || m["effective"].(map[string]any)["tierName"] != "Thinker" {
		t.Errorf("detail model = %v", m)
	}

	// One that sets only its tier keeps the organisation's harness.
	other := s.tierOn("Other", "fake/hang")
	tierOnly := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "tier": other})["id"].(string)
	if _, model, tier, harness := s.submitted(tierOnly); model != "fake/hang" || tier != "Other" || harness != "scripted" {
		t.Errorf("a session that chose only its tier ran on %s (%s) · %s, want fake/hang (Other) · scripted", model, tier, harness)
	}

	plain := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
	// The scripted agent on OpenCode is recorded as "scripted" (phases.harnessScripted).
	if _, _, tier, harness := s.submitted(plain); tier != "Thinker" || harness != "scripted" {
		t.Errorf("a session that chose nothing ran on %s · %s", tier, harness)
	}
	m = s.ok(s.marcio, "GET", "/internal/sessions/"+plain, nil)["model"].(map[string]any)
	if m["tier"] != nil || m["harness"] != nil || m["effective"].(map[string]any)["harness"] != "opencode" {
		t.Errorf("detail model = %v", m)
	}
}

// A tier or harness a session cannot name is refused with a 400, on
// create (no session is made) and on /model (nothing changes).
func TestATierOrHarnessASessionCannotNameIsRefused(t *testing.T) {
	s := newSessionWorld(t)
	// Another organisation's tier is not one this session can name.
	foreign := s.foreignTier()
	refused := []map[string]any{
		{"tier": "mtr_nope", "harness": nil},
		{"tier": nil, "harness": "aider"},
		{"tier": foreign, "harness": nil},
	}
	for _, body := range refused {
		withMessage := map[string]any{"message": "hello"}
		for k, v := range body {
			if v != nil {
				withMessage[k] = v
			}
		}
		status, out := s.as(s.marcio, "POST", "/internal/sessions", withMessage)
		if status != http.StatusBadRequest {
			t.Errorf("create %v: %d %v, want 400", withMessage, status, out)
		}
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, s.org); n != 0 {
		t.Errorf("%d sessions made", n)
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE tier = $1`, foreign); n != 0 {
		t.Errorf("a session names another organisation's tier")
	}

	// The same bodies through /model change nothing.
	refusedOn := s.session()
	for _, body := range refused {
		if status, out := s.as(s.marcio, "POST", "/internal/sessions/"+refusedOn+"/model", body); status != http.StatusBadRequest {
			t.Errorf("/model %v: %d %v, want 400", body, status, out)
		}
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND tier IS NULL AND harness IS NULL`, refusedOn); n != 1 {
		t.Errorf("a refused /model was stored")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.changed'`, refusedOn); n != 0 {
		t.Errorf("a refused /model was recorded")
	}
}

// Any harness takes any tier's model, the LLM proxy translating: Codex
// with a Claude tier and Claude Code with a GPT one are taken on create
// and on /model, the detail carries no misfit, and the agent is submitted
// on that pair.
func TestAnyHarnessWithAnyTierIsTaken(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	opus := s.tierOn("Opus", "claude-opus-5")
	sol := s.tierOn("Sol", "gpt-6-sol")
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "tier": opus, "harness": "codex"})["id"].(string)
	run, model, tier, harness := s.submitted(id)
	if model != "claude-opus-5" || tier != "Opus" || harness != "codex" {
		t.Errorf("submitted on %s (%s) · %s, want claude-opus-5 (Opus) · codex", model, tier, harness)
	}
	m := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any)
	if _, has := m["misfit"]; has {
		t.Errorf("the detail carries a misfit: %v", m)
	}

	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": sol, "harness": "claude-code"})
	if eff := out["model"].(map[string]any)["effective"].(map[string]any); eff["model"] != "gpt-6-sol" || eff["harness"] != "claude-code" {
		t.Errorf("answered %v", out)
	}
	if _, has := out["model"].(map[string]any)["misfit"]; has {
		t.Errorf("the answer carries a misfit: %v", out)
	}
	s.endRun(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "again"})
	s.until("a second agent", func() bool { r, _ := s.brainstorm(id); return r != run })
	if _, model, tier, harness := s.submitted(id); model != "gpt-6-sol" || tier != "Sol" || harness != "claude-code" {
		t.Errorf("next start on %s (%s) · %s, want gpt-6-sol (Sol) · claude-code", model, tier, harness)
	}
}

// Only the owner chooses the model: a member who can chat, a reader and
// someone outside the session are refused, and nothing changes.
func TestOnlyTheOwnerChoosesTheModel(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.join(id, s.joao, "read")
	// An admin who is not in the session has no say in it either.
	for who, want := range map[string]int{s.ana: 403, s.joao: 403, s.outsider: 404, s.admin: 404} {
		if status, out := s.as(who, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"}); status != want {
			t.Errorf("%s: %d %v, want %d", who, status, out, want)
		}
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND harness IS NULL`, id); n != 1 {
		t.Errorf("a non-owner changed the harness")
	}
	// Every member reads it.
	if m := s.ok(s.joao, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any); m["effective"].(map[string]any)["harness"] != "opencode" {
		t.Errorf("a reader sees %v", m)
	}
}

// The owner's change is recorded once, naming who, the tier and the
// harness; nulls clear both back to the organisation's, recorded as such;
// the same choice again records nothing.
func TestTheOwnersChangeIsRecordedAndNullsClearIt(t *testing.T) {
	s := newSessionWorld(t)
	opus := s.tierOn("Opus", "claude-opus-5")
	id := s.session()
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": opus, "harness": "claude-code"})
	if eff := out["model"].(map[string]any)["effective"].(map[string]any); eff["tierName"] != "Opus" || eff["harness"] != "claude-code" {
		t.Errorf("answered %v", out)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": opus, "harness": "claude-code"})
	var by, tierName, harness, actor string
	_ = s.owner.QueryRow(context.Background(), `SELECT payload->>'by', payload->'tier'->>'name', payload->>'harness', actor_id FROM events
		WHERE session_id = $1 AND event_type = 'session.model.changed'`, id).Scan(&by, &tierName, &harness, &actor)
	if by != s.marcio || tierName != "Opus" || harness != "claude-code" || actor != s.marcio {
		t.Errorf("recorded by %s (actor %s) tier %s harness %s", by, actor, tierName, harness)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.changed'`, id); n != 1 {
		t.Errorf("%d changes recorded, want 1 (the second was the same)", n)
	}

	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": nil})
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND tier IS NULL AND harness IS NULL`, id); n != 1 {
		t.Errorf("nulls did not clear the choice")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.changed'
		AND payload->'tier' = 'null'::jsonb AND payload->'harness' = 'null'::jsonb`, id); n != 1 {
		t.Errorf("the clearing was not recorded")
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	if _, _, tier, harness := s.submitted(id); tier != "Thinker" || harness != "scripted" {
		t.Errorf("after clearing it ran on %s · %s", tier, harness)
	}
}

// idleOn starts a session on Claude Code over the Thinker tier (a Claude
// model), its agent never ending a turn on its own, and ends its first
// turn. Returns the session and its agent, running between turns.
func (s *sessionWorld) idleOn() (session, run string) {
	s.t.Helper()
	s.syncer.ConductorWarm = time.Hour
	s.lux.Decide = hang
	mustExec(s.t, s.owner, `UPDATE model_tiers SET model = 'claude-sonnet-5' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	session = s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "harness": "claude-code"})["id"].(string)
	run, _, _, harness := s.submitted(session)
	if harness != "claude-code" {
		s.t.Fatalf("first start on %s", harness)
	}
	s.runningIdle(run)
	return session, run
}

// parkedOn is idleOn's agent parked as its warm period's end does.
func (s *sessionWorld) parkedOn() (session, run string) {
	s.t.Helper()
	session, run = s.idleOn()
	mustExec(s.t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours' WHERE id = $1`, run)
	s.until("the session parked", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, run) == 1
	})
	return session, run
}

// runningIdle waits for the agent to run, then ends its turn as the
// agent's idle would, long enough ago that nothing reports it stalled.
func (s *sessionWorld) runningIdle(run string) {
	s.t.Helper()
	s.until("the brainstorm running", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, run) == 1
	})
	mustExec(s.t, s.owner, `UPDATE runs SET turn_done_at = now(), agent_busy_at = now() - interval '3 hours',
		agent_active_at = now() - interval '3 hours', files_changed_at = now() - interval '3 hours',
		stall_reported_at = now() - interval '3 hours' WHERE id = $1`, run)
}

// prompt is a Run's briefing as dude stored it.
func (s *sessionWorld) prompt(run string) string {
	var p string
	_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(prompt, '') FROM runs WHERE id = $1`, run).Scan(&p)
	return p
}

// freshAgent waits for the session's agent after old, and checks old was
// ended for the model and a session.model.applied names both.
func (s *sessionWorld) freshAgent(session, old string) string {
	s.t.Helper()
	s.until("a fresh agent", func() bool { r, _ := s.brainstorm(session); return r != old })
	next, _ := s.brainstorm(session)
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND control_reason = 'the session''s model changed'`, old); n != 1 {
		s.t.Errorf("the old agent was not ended for the model")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.applied'
		AND payload->>'from' = $2 AND payload->>'to' = $3`, session, old, next); n != 1 {
		s.t.Errorf("no session.model.applied from %s to %s", old, next)
	}
	return next
}

// noSwap says the session recorded no session.model.applied.
func (s *sessionWorld) noSwap(session string) {
	s.t.Helper()
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.applied'`, session); n != 0 {
		s.t.Errorf("a swap was recorded")
	}
}

// queuedOn says a message's answer out is its queuing on the agent run.
func (s *sessionWorld) queuedOn(out map[string]any, run string) {
	s.t.Helper()
	if out["runId"] != run || out["created"] != false {
		s.t.Errorf("answered %v, want queued on %s", out, run)
	}
}

// asked starts a session's agent with tools, Ana a member if withAna, and
// has it ask ask_person's args. Returns the session, agent and question.
func (s *sessionWorld) asked(args string, withAna bool) (session, run, question string) {
	s.t.Helper()
	s.withTools()
	session = s.session()
	if withAna {
		s.join(session, s.ana, "chat")
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/chat", map[string]any{"text": "how long is the window?"})
	run = s.started(session)
	status, out := s.tool(run, "ask_person", args)
	if status != 200 {
		s.t.Fatalf("ask_person: %d %v", status, out)
	}
	return session, run, out["questionId"].(string)
}

// askAna is ask_person's args for a one-item question put to Ana.
const askAna = `{"question":"Grow the 24h window for every kind?","choices":["Every kind","Only runs"],"to":"Ana"}`

// conversationThenFirst says the briefing carries before in the
// conversation so far, and first as its first message.
func conversationThenFirst(t *testing.T, prompt, before, first string) {
	t.Helper()
	so, msg := strings.Index(prompt, "## The conversation so far"), strings.Index(prompt, "## The first message\n\n"+first)
	if so < 0 || msg < 0 || !strings.Contains(prompt[so:msg], before) {
		t.Errorf("the briefing lacks %q in the conversation or %q as its first message:\n%s", before, first, prompt)
	}
}

// A harness change to a parked agent: the next message ends it and a
// fresh agent on the new harness takes over, briefed with the
// conversation and the message. lux's Run of the old one is cancelled.
func TestAHarnessChangeHandsTheNextMessageToAFreshAgent(t *testing.T) {
	s := newSessionWorld(t)
	id, run := s.parkedOn()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "opencode"})
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	next := s.freshAgent(id, run)
	if out["runId"] != next {
		t.Errorf("answered %v, want the fresh agent %s", out, next)
	}
	if _, model, _, harness := s.submitted(id); model != "claude-sonnet-5" || harness != "opencode" {
		t.Errorf("the fresh agent runs on %s · %s, want claude-sonnet-5 · opencode", model, harness)
	}
	conversationThenFirst(t, s.prompt(next), "Márcio Martins: hello", "Márcio Martins: carry on")
	if v := s.luxRun(run); v == nil || v.resumed != 0 {
		t.Errorf("the old agent was resumed")
	}
	s.until("the old lux Run cancelled", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel'`, run) == 1
	})
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message' AND payload->>'text' = 'carry on'`, next); n != 1 {
		t.Errorf("the message is not shown on the fresh agent")
	}
}

// A tier change likewise, to an agent running between turns.
func TestATierChangeHandsTheNextMessageToAFreshAgent(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	gpt := s.tierOn("Sol", "gpt-6-sol")
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
	run, _, _, _ := s.submitted(id)
	s.runningIdle(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": gpt, "harness": nil})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "now on Sol"})
	next := s.freshAgent(id, run)
	if _, model, tier, _ := s.submitted(id); model != "gpt-6-sol" || tier != "Sol" {
		t.Errorf("the fresh agent runs on %s (%s), want gpt-6-sol (Sol)", model, tier)
	}
	conversationThenFirst(t, s.prompt(next), "Márcio Martins: hello", "Márcio Martins: now on Sol")
}

// The fresh agent a change hands over to takes the setting as it is at the
// swap: with brainstorm_anywhere turned on, a swap to Codex is told it can
// read the web and searches live; with it turned off again, the next swap
// gets neither.
func TestAFreshAgentAfterAChangeTakesTheSettingAsItIsNow(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	sol := s.tierOn("Sol", "gpt-6-sol")
	opus := s.tierOn("Opus", "claude-opus-5")
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
	run, _, _, _ := s.submitted(id)
	s.runningIdle(run)

	mustExec(t, s.owner, `UPDATE organizations SET brainstorm_anywhere = true WHERE id = $1`, s.org)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": sol, "harness": "codex"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "now on Codex"})
	onCodex := s.freshAgent(id, run)
	if _, model, _, harness := s.submitted(id); model != "gpt-6-sol" || harness != "codex" {
		t.Fatalf("the fresh agent runs on %s · %s, want gpt-6-sol · codex", model, harness)
	}
	v := s.luxRun(onCodex)
	if !strings.Contains(v.spec.Workload.Prompt, webNote) {
		t.Errorf("the fresh agent with the setting on is not told it can read the web:\n%s", v.spec.Workload.Prompt)
	}
	if !codexSearchesLive(t, v.spec.Secrets) {
		t.Error("the fresh agent with the setting on is not pinned to search live")
	}

	s.runningIdle(onCodex)
	mustExec(t, s.owner, `UPDATE organizations SET brainstorm_anywhere = false WHERE id = $1`, s.org)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": opus, "harness": "codex"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "now on Opus"})
	onOpus := s.freshAgent(id, onCodex)
	if _, model, _, harness := s.submitted(id); model != "claude-opus-5" || harness != "codex" {
		t.Fatalf("the next fresh agent runs on %s · %s, want claude-opus-5 · codex", model, harness)
	}
	v = s.luxRun(onOpus)
	if strings.Contains(v.spec.Workload.Prompt, webNote) {
		t.Errorf("the fresh agent with the setting off is told it can read the web:\n%s", v.spec.Workload.Prompt)
	}
	if codexSearchesLive(t, v.spec.Secrets) {
		t.Error("the fresh agent with the setting off is pinned to search live")
	}
}

// A tier on the same model at another effort is a change: the next
// message hands over to a fresh agent requesting the new effort.
func TestAnEffortChangeHandsTheNextMessageToAFreshAgent(t *testing.T) {
	s := newSessionWorld(t)
	id, run := s.parkedOn()
	quick := "mtr_quick_" + s.org
	mustExec(t, s.owner, `INSERT INTO model_tiers (id, organization_id, name, model, effort, position)
		VALUES ($1, $2, 'Quick', 'claude-sonnet-5', 'low', 21)`, quick, s.org)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": quick, "harness": "claude-code"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "quicker now"})
	next := s.freshAgent(id, run)
	if _, model, _, harness := s.submitted(id); model != "claude-sonnet-5" || harness != "claude-code" {
		t.Errorf("the fresh agent runs on %s · %s, want claude-sonnet-5 · claude-code", model, harness)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND effort = 'low'`, next); n != 1 {
		t.Errorf("the fresh agent does not request the low effort")
	}
	if v := s.luxRun(run); v == nil || v.resumed != 0 {
		t.Errorf("the old agent was resumed")
	}
}

// A change set back to what the agent runs on, no change at all, another
// tier requesting the same model and effort, and the agent's tier renamed
// swap nothing: the parked agent is resumed with the message, on what it
// was submitted with.
func TestAChangeSetBackOrNoneResumesTheSameAgent(t *testing.T) {
	for _, change := range []string{"set back", "no change", "same model and effort on another tier", "tier renamed",
		"the chosen tier's model removed"} {
		t.Run(change, func(t *testing.T) {
			s := newSessionWorld(t)
			id, run := s.parkedOn()
			switch change {
			case "set back":
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "claude-code"})
			case "same model and effort on another tier":
				// Thinker requests claude-sonnet-5 at high effort, as tierOn's tiers do.
				twin := s.tierOn("Twin", "claude-sonnet-5")
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": twin, "harness": "claude-code"})
			case "tier renamed":
				mustExec(t, s.owner, `UPDATE model_tiers SET name = 'Deep thinker' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
			case "the chosen tier's model removed":
				// A fresh agent on a tier naming no model would only fail.
				sol := s.tierOn("Sol", "gpt-6-sol")
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": sol, "harness": "claude-code"})
				mustExec(t, s.owner, `UPDATE model_tiers SET model = NULL WHERE id = $1`, sol)
			}
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
			s.until("the session resumed", func() bool { v := s.luxRun(run); return v != nil && v.resumed == 1 })
			var secrets []lux.Secret
			for _, r := range s.lux.Runs() {
				if r.Resumed == 1 {
					secrets = r.ResumeSecrets[0]
				}
			}
			if names := secretNames(secrets); !slices.Contains(names, "ANTHROPIC_API_KEY") {
				t.Errorf("resume secrets = %v, want Claude Code's key", names)
			}
			if now, _ := s.brainstorm(id); now != run {
				t.Errorf("a fresh agent %s, want %s resumed", now, run)
			}
			s.noSwap(id)
		})
	}
}

// A parked agent still holding a message it was never sent swaps nothing
// at a change: a fresh agent would read the newer message first. Both are
// queued on it, in the order written; the message after both are read
// swaps.
func TestAChangeWhileAnEarlierMessageIsUnsentQueuesTheNextOnTheSameAgent(t *testing.T) {
	s := newSessionWorld(t)
	id, run := s.parkedOn()
	// Queued and a resume asked for; the syncer has not run since.
	first := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "first this"})
	if first["runId"] != run {
		t.Fatalf("the first message went to %v, want %s", first["runId"], run)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "opencode"})
	s.queuedOn(s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "then this"}), run)
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 1 {
		t.Errorf("%d Runs, want the parked one", n)
	}
	s.noSwap(id)
	if texts, want := s.directiveTexts(run), []string{"Márcio Martins: first this", "Márcio Martins: then this"}; !slices.Equal(texts, want) {
		t.Errorf("queued %q, want %q", texts, want)
	}

	// The refusal is not lasting: once the agent has read both, the next
	// message swaps, the briefing carrying both in order.
	s.until("both read", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, run) == 2
	})
	s.runningIdle(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "C"})
	prompt := s.prompt(s.freshAgent(id, run))
	conversationThenFirst(t, prompt, "Márcio Martins: then this", "Márcio Martins: C")
	if a, b := strings.Index(prompt, "Márcio Martins: first this"), strings.Index(prompt, "Márcio Martins: then this"); a < 0 || a > b {
		t.Errorf("the briefing does not carry A before B:\n%s", prompt)
	}
}

// directiveTexts are a Run's live messages, in the order written.
func (s *sessionWorld) directiveTexts(run string) []string {
	s.t.Helper()
	rows, err := s.owner.Query(context.Background(), `SELECT text FROM directives WHERE run_id = $1 AND failed_at IS NULL
		ORDER BY created_at, id`, run)
	if err != nil {
		s.t.Fatal(err)
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.t.Fatal(err)
	}
	return texts
}

// A message lux was sent and the agent has not read yet holds the swap as
// an unsent one does: B is queued on the same agent after A. Once both are
// read, or once both were sent longer ago than delivery.UnreadGraceSecs
// with no receipt, the next message swaps.
func TestAChangeWhileAnEarlierMessageIsUnreadQueuesTheNextOnTheSameAgent(t *testing.T) {
	for _, ending := range []string{"both read", "no receipt within the cap"} {
		t.Run(ending, func(t *testing.T) {
			s := newSessionWorld(t)
			s.syncer.ConductorWarm = time.Hour
			id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
			run := s.started(id)
			s.runningIdle(run)
			release := s.gateInput()
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "first this"})
			s.until("the first message sent, not read", func() bool {
				return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND sent_at IS NOT NULL AND delivered_at IS NULL`, run) == 1
			})
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
			s.queuedOn(s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "then this"}), run)
			s.noSwap(id)
			if texts, want := s.directiveTexts(run), []string{"Márcio Martins: first this", "Márcio Martins: then this"}; !slices.Equal(texts, want) {
				t.Errorf("queued %q, want %q", texts, want)
			}

			if ending == "both read" {
				release()
				s.until("both read", func() bool {
					return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, run) == 2
				})
				s.runningIdle(run)
			} else {
				// Both stay gated, sent and never read: past the cap no
				// receipt is coming, and they no longer hold the swap.
				s.until("both sent, not read", func() bool {
					return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND sent_at IS NOT NULL AND delivered_at IS NULL`, run) == 2
				})
				mustExec(t, s.owner, `UPDATE directives SET sent_at = now() - interval '121 seconds' WHERE run_id = $1 AND delivered_at IS NULL`, run)
			}
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "and now"})
			next := s.freshAgent(id, run)
			conversationThenFirst(t, s.prompt(next), "Márcio Martins: hello", "Márcio Martins: and now")
			if ending == "both read" {
				conversationThenFirst(t, s.prompt(next), "Márcio Martins: first this", "Márcio Martins: and now")
				conversationThenFirst(t, s.prompt(next), "Márcio Martins: then this", "Márcio Martins: and now")
			}
		})
	}
}

// A change while the agent is mid-turn interrupts nothing: a message then
// is queued on it as a steer; the first message after its turn ends hands
// the conversation to a fresh agent.
func TestAChangeMidTurnQueuesTheMessageOnTheSameAgent(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
	run, _, _, _ := s.submitted(id)
	s.until("the brainstorm running", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, run) == 1
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
	s.queuedOn(s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "also this"}), run)
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = 'Márcio Martins: also this'`, run); n != 1 {
		t.Errorf("the message is not queued on the agent mid-turn")
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 1 {
		t.Errorf("%d Runs, want the one mid-turn", n)
	}

	// The agent ends its turn and reads the steer as its next one.
	var luxID string
	if err := s.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE id = $1`, run).Scan(&luxID); err != nil {
		t.Fatal(err)
	}
	s.lux.EndTurn(luxID)
	s.until("the steer read", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = 'Márcio Martins: also this' AND delivered_at IS NOT NULL`, run) == 1
	})
	s.runningIdle(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "and now"})
	s.freshAgent(id, run)
	if _, _, _, harness := s.submitted(id); harness != "codex" {
		t.Errorf("the fresh agent runs on %s, want codex", harness)
	}
}

// An answer to the agent's open question after a change, as a message or
// through the form, is the fresh agent's first message, its briefing
// carrying the question asked; the question is answered, not cancelled.
// An agent whose turn ended waiting on a person (waiting_since, no
// turn_done_at) is between turns too.
func TestAnAnswerAfterAChangeBriefsAFreshAgentWithTheQuestion(t *testing.T) {
	for _, way := range []string{"message", "form", "message while waiting only"} {
		t.Run(way, func(t *testing.T) {
			form := way == "form"
			s := newSessionWorld(t)
			id, run, qid := s.asked(`{"question":"Grow the 24h window for every kind?","choices":["Every kind","Only runs"]}`, false)
			if way == "message while waiting only" {
				mustExec(t, s.owner, `UPDATE runs SET turn_done_at = NULL, waiting_since = now() WHERE id = $1`, run)
			}
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
			if form {
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/questions/"+qid+"/answer", map[string]any{"text": "Only runs"})
			} else {
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "Only runs"})
			}
			next := s.freshAgent(id, run)
			conversationThenFirst(t, s.prompt(next), "You asked: Grow the 24h window for every kind?",
				"Márcio Martins answered your question \"Grow the 24h window for every kind?\":\n\nOnly runs")
			if n := s.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'answered' AND answer = 'Only runs'`, qid); n != 1 {
				t.Errorf("the question is not answered")
			}
			if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'question.answered' AND payload->>'questionId' = $2
				AND NOT (payload ? 'directiveId')`, run, qid); n != 1 {
				t.Errorf("the answer is not recorded on the question's Run, with no directive")
			}
			if _, _, _, harness := s.submitted(id); harness != "codex" {
				t.Errorf("the fresh agent runs on %s, want codex", harness)
			}

			// Another change: the agent after next reads the answer once, as
			// said in the conversation.
			s.runningIdle(next)
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "opencode"})
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "and then?"})
			third := s.freshAgent(id, next)
			conversationThenFirst(t, s.prompt(third), "Márcio Martins answered: Only runs", "Márcio Martins: and then?")
			// The scripted agent's reply quotes its first message; the
			// replayed answer is the line "Márcio Martins answered: …".
			if n := strings.Count(s.prompt(third), "Márcio Martins answered: Only runs"); n != 1 {
				t.Errorf("the answer is in the third agent's briefing %d times, want once:\n%s", n, s.prompt(third))
			}
		})
	}
}

// What the old agent never read goes to the fresh one after the answer:
// a message held for another member's answer is not lost when that
// answer, after a change, hands the conversation over.
func TestTheOldAgentsUnreadMessagesAreHandedOn(t *testing.T) {
	s := newSessionWorld(t)
	id, run, _ := s.asked(askAna, true)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
	// Held for Ana's answer: it swaps nothing.
	held := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "also cost estimates?"})
	if held["runId"] != run {
		t.Fatalf("the held message went to %v, want %s", held["runId"], run)
	}
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "Only runs"})
	next := s.freshAgent(id, run)
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = 'Márcio Martins: also cost estimates?'`, next); n != 1 {
		t.Errorf("the held message was not handed to the fresh agent")
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NOT NULL AND error LIKE '%' || $2 || '%'`,
		held["directiveId"], next); n != 1 {
		t.Errorf("the old copy does not say the fresh agent has it")
	}
	conversationThenFirst(t, s.prompt(next), "Grow the 24h window", `Ana Nunes answered your question`)
	s.until("the fresh agent reads it", func() bool {
		v := s.luxRun(next)
		return v != nil && slices.Contains(append(v.inputs, v.resumes...), "Márcio Martins: also cost estimates?")
	})
}

// While the agent's question is open, only its answer swaps the agent: an
// aside after a change, from another member (held for the answer) or from
// the one it is put to, is queued on the agent that asked, and the
// question stays open. The answer then hands the conversation over, the
// question and the answer in the briefing once, and the aside with it.
func TestAnAsideWhileAQuestionIsOpenWaitsForTheAnswerToSwap(t *testing.T) {
	for _, byAna := range []bool{false, true} {
		t.Run(map[bool]string{false: "another member's", true: "the asked member's"}[byAna], func(t *testing.T) {
			s := newSessionWorld(t)
			id, run, qid := s.asked(askAna, true)
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
			who, said := s.marcio, "Márcio Martins: also cost estimates?"
			if byAna {
				who, said = s.ana, "Ana Nunes: also cost estimates?"
			}
			aside := s.ok(who, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "also cost estimates?", "aside": true})
			if aside["runId"] != run || aside["created"] != false {
				t.Fatalf("the aside answered %v, want queued on %s", aside, run)
			}
			if n := s.count(`SELECT count(*) FROM directives WHERE id = $1 AND run_id = $2`, aside["directiveId"], run); n != 1 {
				t.Errorf("the aside is not queued on the agent that asked")
			}
			if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 1 {
				t.Errorf("%d Runs after the aside, want the one that asked", n)
			}
			if n := s.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'open'`, qid); n != 1 {
				t.Errorf("the question is no longer open")
			}
			if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type IN ('session.model.applied', 'question.closed')`, id); n != 0 {
				t.Errorf("the aside swapped the agent or closed the question")
			}
			if byAna {
				// Not held: the agent that asked reads it, and its turn ends.
				s.until("the aside read", func() bool {
					return s.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, aside["directiveId"]) == 1
				})
				s.runningIdle(run)
			}

			s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "Only runs"})
			next := s.freshAgent(id, run)
			prompt := s.prompt(next)
			conversationThenFirst(t, prompt, "You asked Ana Nunes: Grow the 24h window for every kind?",
				"Ana Nunes answered your question \"Grow the 24h window for every kind?\":\n\nOnly runs")
			if a, q := strings.Count(prompt, "Ana Nunes answered"), strings.Count(prompt, "You asked Ana Nunes: Grow the 24h window"); a != 1 || q != 1 {
				t.Errorf("the briefing has the answer %d times and the question %d, want once each:\n%s", a, q, prompt)
			}
			if n := s.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'answered'`, qid); n != 1 {
				t.Errorf("the question is not answered")
			}
			if byAna {
				conversationThenFirst(t, prompt, said, "Ana Nunes answered")
			} else if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2`, next, said); n != 1 {
				t.Errorf("the held aside was not handed to the fresh agent")
			}
		})
	}

	// A plain message from the one asked, beside a question of several
	// items, answers none of them (they go through the form): it is an
	// aside too, and swaps nothing.
	t.Run("beside a multi-item question", func(t *testing.T) {
		s := newSessionWorld(t)
		id, run, qid := s.asked(`{"questions":[{"header":"Window","question":"Grow the 24h window?"},`+
			`{"header":"Scope","question":"For every kind?"}],"to":"Márcio"}`, false)
		s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
		s.queuedOn(s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "x"}), run)
		if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 1 {
			t.Errorf("%d Runs, want the one that asked", n)
		}
		if n := s.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'open'`, qid); n != 1 {
			t.Errorf("the question is no longer open")
		}
		s.noSwap(id)
	})
}

// Two messages at once after a change: one swaps the agent, the other
// queues on the fresh one. Exactly one session.model.applied, two Runs.
func TestTwoMessagesAtOnceAfterAChangeSwapOnce(t *testing.T) {
	s := newSessionWorld(t)
	id, run := s.parkedOn()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "opencode"})
	statuses := make(chan int, 2)
	for _, text := range []string{"one", "two"} {
		go func() {
			status, _ := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": text})
			statuses <- status
		}()
	}
	for range 2 {
		if status := <-statuses; status >= 300 {
			t.Errorf("a message answered %d", status)
		}
	}
	next := s.freshAgent(id, run)
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.applied'`, id); n != 1 {
		t.Errorf("%d swaps recorded, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 2 {
		t.Errorf("%d Runs, want the old one and one fresh agent", n)
	}
	// Whichever came second is queued on the fresh agent.
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text IN ('Márcio Martins: one', 'Márcio Martins: two')`, next); n != 1 {
		t.Errorf("%d messages queued on the fresh agent, want the second", n)
	}
}

// A repository linked after a change, to an agent running between turns,
// pauses and resumes it as it was (sync.go betweenTurns, "repository"):
// no member's message, so no swap. A parked agent is not woken by a link
// at all; it gets the repository at its next resume.
func TestALinkAfterAChangeResumesTheSameAgent(t *testing.T) {
	s := newSessionWorld(t)
	id, run := s.idleOn()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "opencode"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	s.until("the session resumed", func() bool { v := s.luxRun(run); return v != nil && v.resumed == 1 })
	if now, _ := s.brainstorm(id); now != run {
		t.Errorf("a fresh agent %s, want %s resumed", now, run)
	}
	s.noSwap(id)
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND harness = 'claude-code' AND status IN ('running', 'paused')`, run); n != 1 {
		t.Errorf("the agent no longer runs on what it was submitted with")
	}
}

// A tier a session chose that is then removed: the session follows the
// organisation's again, its members are told on the session, and its next
// agent runs on the organisation's tier.
func TestADeletedTierFallsBackToTheOrganisations(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	opus := s.tierOn("Opus", "fake/scripted")
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": opus, "harness": "codex"})
	mustExec(t, s.owner, `DELETE FROM model_tiers WHERE id = $1`, opus)
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND tier IS NULL AND harness = 'codex'`, id); n != 1 {
		t.Errorf("the session's tier was not cleared, or its harness was")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.fallback'
		AND payload->'tier'->>'name' = 'Opus' AND payload->'tier'->>'id' = $2`, id, opus); n != 1 {
		t.Errorf("the fallback was not recorded on the session")
	}
	m := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any)
	if m["tier"] != nil || m["effective"].(map[string]any)["tierName"] != "Thinker" {
		t.Errorf("detail model = %v", m)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	if _, _, tier, harness := s.submitted(id); tier != "Thinker" || harness != "codex" {
		t.Errorf("ran on %s · %s, want Thinker · codex", tier, harness)
	}
}

// A tier that names no model yet is not one a session can choose.
func TestATierWithNoModelIsRefused(t *testing.T) {
	s := newSessionWorld(t)
	empty := "mtr_empty_" + s.org
	mustExec(t, s.owner, `INSERT INTO model_tiers (id, organization_id, name, model, position) VALUES ($1, $2, 'Blank', NULL, 30)`, empty, s.org)
	status, out := s.as(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "tier": empty})
	if status != http.StatusBadRequest || !strings.Contains(out["error"].(map[string]any)["message"].(string), "the tier Blank names no model yet") {
		t.Errorf("create: %d %v", status, out)
	}
	id := s.session()
	if status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": empty, "harness": nil}); status != http.StatusBadRequest {
		t.Errorf("/model: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE tier = $1`, empty); n != 0 {
		t.Errorf("a session chose a tier with no model")
	}
}

// A session following the organisation's Brainstorm, Codex on a Claude
// tier, is made and its agent submitted on that pair; and a session that
// sets only one half is taken whatever the organisation's other half is.
func TestASessionOnTheOrganisationsPairIsSubmitted(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-sonnet-5' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	s.brainstormOrg("codex")
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
	if _, model, tier, harness := s.submitted(id); model != "claude-sonnet-5" || tier != "Thinker" || harness != "codex" {
		t.Errorf("submitted on %s (%s) · %s, want claude-sonnet-5 (Thinker) · codex", model, tier, harness)
	}
	if run, status := s.brainstorm(id); status == "failed" {
		t.Errorf("the session's agent %s failed", run)
	}

	s.brainstormOrg("claude-code")
	sol := s.tierOn("Sol", "gpt-6-sol")
	other := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+other+"/model", map[string]any{"tier": sol, "harness": nil})
	s.ok(s.marcio, "POST", "/internal/sessions/"+other+"/model", map[string]any{"tier": nil, "harness": "codex"})
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND tier IS NULL AND harness = 'codex'`, other); n != 1 {
		t.Errorf("a one-half change was not stored")
	}
}

// A tier's removal reaches only the sessions on it: another organisation's
// session on its own tier is left alone and told nothing; and an
// organisation removed with a session on one of its tiers goes, recording
// no fallback.
func TestATiersRemovalReachesOnlyItsOwnSessions(t *testing.T) {
	s := newSessionWorld(t)
	opus := s.tierOn("Opus", "fake/scripted")
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": opus, "harness": nil})
	other := s.otherOrg()
	theirs := "mtr_theirs_" + other
	mustExec(t, s.owner, `INSERT INTO model_tiers (id, organization_id, name, model, position) VALUES ($1, $2, 'Theirs', 'fake/scripted', 0)`, theirs, other)
	mustExec(t, s.owner, `INSERT INTO sessions (id, organization_id, title, tier) VALUES ('ssn_theirs', $1, 'Theirs', $2)`, other, theirs)

	mustExec(t, s.owner, `DELETE FROM model_tiers WHERE id = $1`, opus)
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'session.model.fallback' AND organization_id = $1 AND session_id = $2`, s.org, id); n != 1 {
		t.Errorf("%d fallbacks recorded on the session, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'session.model.fallback' AND organization_id = $1`, other); n != 0 {
		t.Errorf("the other organisation was told of a tier it never had")
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = 'ssn_theirs' AND tier = $1`, theirs); n != 1 {
		t.Errorf("the other organisation's session lost its tier")
	}

	// The other organisation goes, its session on its tier with it. A
	// fallback written for it would name an organisation already gone, and
	// its foreign key would refuse the whole delete.
	if _, err := s.owner.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, other); err != nil {
		t.Fatalf("removing an organisation with a session on its tier: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'session.model.fallback' AND session_id = 'ssn_theirs'`); n != 0 {
		t.Errorf("a removed organisation's tier recorded a fallback")
	}
	if n := s.count(`SELECT count(*) FROM model_tiers WHERE organization_id = $1`, other); n != 0 {
		t.Errorf("the removed organisation's tiers are still there")
	}
}

// A session moved off a tier while that tier's removal waits on its row is
// not told the tier was removed: the removal reads the row as committed
// after the wait, finds it on its new tier, and records nothing for it.
func TestATierRemovedWhileTheSessionMovesOffItTellsItNothing(t *testing.T) {
	s := newSessionWorld(t)
	ctx := context.Background()
	opus := s.tierOn("Opus", "fake/scripted")
	other := s.tierOn("Other", "fake/scripted")
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": opus, "harness": nil})

	// The owner's change, not yet committed: the session's row locked on its new tier.
	holder, err := pgx.Connect(ctx, s.owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(ctx)
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE sessions SET tier = $2 WHERE id = $1`, id, other); err != nil {
		t.Fatal(err)
	}
	removed := make(chan error, 1)
	go func() {
		_, err := s.owner.Exec(ctx, `DELETE FROM model_tiers WHERE id = $1`, opus)
		removed <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; {
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND pg_backend_pid() = ANY(pg_blocking_pids(pid))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 1 {
			break
		}
		select {
		case err := <-removed:
			t.Fatalf("the removal finished (%v) without waiting on the session's row", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the removal never waited on the session's row")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND tier = $2`, id, other); n != 1 {
		t.Errorf("the session lost the tier it moved to")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.fallback'`, id); n != 0 {
		t.Errorf("the session was told a tier it had left was removed")
	}
}
