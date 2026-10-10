package orchestrator_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

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

// failure waits for the session's latest agent to fail, and is why.
func (s *sessionWorld) failure(session string) string {
	s.t.Helper()
	var why string
	s.until("the session's agent failed", func() bool {
		run, status := s.brainstorm(session)
		if status != "failed" {
			return false
		}
		_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(error, '') FROM runs WHERE id = $1`, run).Scan(&why)
		return true
	})
	return why
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

// A pair the harness cannot run is refused with a 400 that says why, on
// create (no session is made) and on /model (nothing changes) — counting
// the organisation's value for the half the session leaves unset.
func TestAMisfitTierAndHarnessAreRefused(t *testing.T) {
	s := newSessionWorld(t)
	gpt := s.tierOn("Sol", "gpt-6-sol")
	opus := s.tierOn("Opus", "claude-opus-5")
	// Another organisation's tier is not one this session can name.
	foreign := s.foreignTier()
	refused := []map[string]any{
		{"tier": gpt, "harness": "claude-code"},
		{"tier": opus, "harness": "codex"},
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
	if status, out := s.as(s.marcio, "POST", "/internal/sessions", map[string]any{"tier": gpt, "harness": "claude-code"}); status != 400 ||
		!strings.Contains(out["error"].(map[string]any)["message"].(string), "Claude Code takes an Anthropic model") {
		t.Errorf("create: %d %v", status, out)
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

	// The organisation's Brainstorm on Claude Code: a session's OpenAI tier
	// alone does not fit it.
	s.brainstormOrg("claude-code")
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-sonnet-5' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	id := s.session()
	if status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": gpt, "harness": nil}); status != 400 {
		t.Errorf("/model with the organisation's harness: %d %v, want 400", status, out)
	}
	if status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"}); status != 400 {
		t.Errorf("/model with the organisation's tier: %d %v, want 400", status, out)
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND tier IS NULL AND harness IS NULL`, id); n != 1 {
		t.Errorf("a refused change was stored")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.model.changed'`, id); n != 0 {
		t.Errorf("a refused change was recorded")
	}
	// Both halves together fit.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": gpt, "harness": "codex"})
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

// A change reaches the agent's next start, not the one running: a parked
// agent resumed after the change keeps its harness, and once it has ended
// the next message's agent runs on the new choice.
func TestAChangeAppliesAtTheNextStartAndAResumeKeepsItsHarness(t *testing.T) {
	s := newSessionWorld(t)
	s.syncer.ConductorWarm = time.Hour
	s.lux.Decide = hang
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-sonnet-5' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	gpt := s.tierOn("Sol", "gpt-6-sol")
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "harness": "claude-code"})["id"].(string)
	run, _, _, harness := s.submitted(id)
	if harness != "claude-code" {
		t.Fatalf("first start on %s", harness)
	}
	s.until("the brainstorm running", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, run) == 1
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": gpt, "harness": "opencode"})
	mustExec(t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours', agent_busy_at = now() - interval '3 hours',
		agent_active_at = now() - interval '3 hours', files_changed_at = now() - interval '3 hours',
		stall_reported_at = now() - interval '3 hours' WHERE id = $1`, run)
	s.until("the session parked", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, run) == 1
	})
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
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND harness = 'claude-code' AND model = 'claude-sonnet-5'`, run); n != 1 {
		t.Errorf("the resumed Run's recorded model or harness changed")
	}

	s.endRun(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "a fresh start"})
	s.until("a second agent", func() bool { r, _ := s.brainstorm(id); return r != run })
	_, model, tier, harness := s.submitted(id)
	if model != "gpt-6-sol" || tier != "Sol" || harness != "opencode" {
		t.Errorf("the next start ran on %s (%s) · %s, want gpt-6-sol (Sol) · opencode", model, tier, harness)
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

// A session that chooses nothing follows the organisation and is never
// refused for it: an organisation whose Brainstorm pair does not fit still
// makes one, whose agent fails in the admin's words, and /model back to
// the organisation's is taken.
func TestASessionThatChoosesNothingIsNotRefused(t *testing.T) {
	s := newSessionWorld(t)
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-sonnet-5' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	s.brainstormOrg("codex")
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
	if why := s.failure(id); !strings.Contains(why, "The Brainstorm runs on Codex") || !strings.Contains(why, "An admin picks another harness or tier in Agents.") {
		t.Errorf("failed with %q, want the organisation's wording", why)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": nil})
	m := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any)
	if why, _ := m["misfit"].(string); !strings.Contains(why, "An admin picks") {
		t.Errorf("model.misfit = %v, want the organisation's wording", m["misfit"])
	}
}

// A pair that stops fitting after it was chosen: a session on Sol and
// Codex whose Sol is removed falls back to the organisation's Anthropic
// tier, keeping Codex. Its detail says so, and its next start fails
// telling the owner to choose again in the session's Model, not an admin.
func TestAPairThatNoLongerFitsSaysSoInTheSessionsWords(t *testing.T) {
	s := newSessionWorld(t)
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-sonnet-5' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	sol := s.tierOn("Sol", "gpt-6-sol")
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": sol, "harness": "codex"})
	m := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any)
	if m["misfit"] != nil {
		t.Errorf("a fitting pair says %v", m["misfit"])
	}
	mustExec(t, s.owner, `DELETE FROM model_tiers WHERE id = $1`, sol)
	m = s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["model"].(map[string]any)
	const want = "Codex takes an OpenAI model, but the tier Thinker requests claude-sonnet-5. Choose another harness or tier in the session's Model."
	if m["misfit"] != want {
		t.Errorf("model.misfit = %v, want %q", m["misfit"], want)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	if why := s.failure(id); why != want {
		t.Errorf("failed with %q, want %q", why, want)
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
