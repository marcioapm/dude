package orchestrator_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// submitted delivers a task with the implementer on a real model and
// answers the spec its Run was submitted with, and its rules as recorded on
// the Run (runs.network).
func (w *world) submittedNetwork() (lux.Network, lux.Network) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE projects SET agent_models = '{}'::jsonb WHERE id = $1`, w.project)
	w.onModel("implementer", "claude-impl")
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	var spec lux.Spec
	if err := json.Unmarshal(w.lux.Runs()[0].Spec, &spec); err != nil || spec.Network == nil {
		w.t.Fatalf("spec network: %v %+v", err, spec.Network)
	}
	var recorded lux.Network
	w.until("the Run's network to be recorded", func() bool {
		var raw []byte
		_ = w.owner.QueryRow(context.Background(), `SELECT network FROM runs WHERE task_id = $1 AND network IS NOT NULL`, wi).Scan(&raw)
		return raw != nil && json.Unmarshal(raw, &recorded) == nil
	})
	return *spec.Network, recorded
}

func rules(n lux.Network) (out []string) {
	for _, e := range n.Egress {
		out = append(out, e.Host+e.CIDR)
	}
	slices.Sort(out)
	return out
}

// A Run reaches its organisation's list and its project's additions, with
// the operator's floor and its model, and records what it was given.
func TestARunReachesItsOrganisationsAndProjectsLists(t *testing.T) {
	w := newWorld(t)
	w.syncer.Agent.Egress = []string{"mirror.internal"}
	mustExec(t, w.owner, `UPDATE organizations SET agent_egress = '{github.com,*.github.com,10.60.0.0/16}' WHERE id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE projects SET agent_egress = '{pypi.org,github.com}' WHERE id = $1`, w.project)
	sent, recorded := w.submittedNetwork()
	want := []string{"*.github.com", "10.60.0.0/16", "github.com", "llm.example", "mirror.internal", "pypi.org"}
	if got := rules(sent); sent.Unrestricted || !slices.Equal(got, want) {
		t.Errorf("egress = %v, want %v", got, want)
	}
	if got := rules(recorded); !slices.Equal(got, want) {
		t.Errorf("recorded = %v, want %v", got, want)
	}
}

// A project on its own list leaves its organisation's out, but not the
// operator's floor nor the model.
func TestAProjectOnItsOwnListLeavesItsOrganisationsOut(t *testing.T) {
	w := newWorld(t)
	w.syncer.Agent.Egress = []string{"mirror.internal"}
	mustExec(t, w.owner, `UPDATE organizations SET agent_egress = '{github.com}' WHERE id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE projects SET agent_egress = '{pypi.org}', agent_egress_mode = 'only' WHERE id = $1`, w.project)
	sent, _ := w.submittedNetwork()
	if got, want := rules(sent), []string{"llm.example", "mirror.internal", "pypi.org"}; !slices.Equal(got, want) {
		t.Errorf("egress = %v, want %v", got, want)
	}
}

// "*" in an organisation's list turns filtering off for its Runs.
func TestAnOrganisationsAnywhereIsUnrestricted(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE organizations SET agent_egress = '{github.com,*}' WHERE id = $1`, w.org)
	if sent, recorded := w.submittedNetwork(); !sent.Unrestricted || len(sent.Egress) != 0 || !recorded.Unrestricted {
		t.Errorf("network = %+v, recorded %+v, want unrestricted", sent, recorded)
	}
}

// A session's agent reaches its organisation's list and its model, and no
// project's list: a session spans projects.
func TestASessionReachesItsOrganisationsList(t *testing.T) {
	s := newSessionWorld(t)
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-think' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	s.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	mustExec(t, s.owner, `UPDATE organizations SET agent_egress = '{pypi.org}' WHERE id = $1`, s.org)
	mustExec(t, s.owner, `UPDATE projects SET agent_egress = '{registry.npmjs.org}' WHERE id = $1`, s.project)
	session := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/chat", map[string]any{"text": "which mirror?"})
	var v *luxRunView
	s.until("the session's agent to be submitted", func() bool {
		id, _ := s.brainstorm(session)
		v = s.luxRun(id)
		return v != nil
	})
	if v.spec.Network == nil {
		t.Fatal("no network")
	}
	if got, want := rules(*v.spec.Network), []string{"llm.example", "pypi.org"}; v.spec.Network.Unrestricted || !slices.Equal(got, want) {
		t.Errorf("egress = %v, want %v", got, want)
	}
	if _, exa := v.spec.Env["OPENCODE_ENABLE_EXA"]; exa {
		t.Error("OPENCODE_ENABLE_EXA set on a brainstorm on its organisation's list: mcp.exa.ai would be refused")
	}
}

// An organisation that lets brainstorms read the web: its session's agent
// reaches any host, whatever its list says, with OpenCode's websearch on,
// and records that; a task's Run of the same organisation keeps the list.
func TestABrainstormOfAnOrganisationThatLetsItReachesAnyHost(t *testing.T) {
	s := newSessionWorld(t)
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-think' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	mustExec(t, s.owner, `UPDATE organizations SET agent_egress = '{pypi.org}', brainstorm_anywhere = true WHERE id = $1`, s.org)

	sent, recorded := s.submittedNetwork()
	if got, want := rules(sent), []string{"llm.example", "pypi.org"}; sent.Unrestricted || !slices.Equal(got, want) || recorded.Unrestricted {
		t.Errorf("the implementer's network = %+v (recorded %+v), want %v", sent, recorded, want)
	}

	session := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/chat", map[string]any{"text": "what do competitors charge?"})
	var v *luxRunView
	var id string
	s.until("the session's agent to be submitted", func() bool {
		id, _ = s.brainstorm(session)
		v = s.luxRun(id)
		return v != nil
	})
	if n := v.spec.Network; n == nil || !n.Unrestricted || len(n.Egress) != 0 {
		t.Errorf("the brainstorm's network = %+v, want unrestricted", n)
	}
	if got := v.spec.Env["OPENCODE_ENABLE_EXA"]; got != "1" {
		t.Errorf("OPENCODE_ENABLE_EXA = %q, want 1", got)
	}
	if !strings.Contains(v.spec.Workload.Prompt, webNote) {
		t.Errorf("the brainstorm that may reach any host is not told it can read the web:\n%s", v.spec.Workload.Prompt)
	}
	var raw []byte
	s.until("the brainstorm's network to be recorded", func() bool {
		_ = s.owner.QueryRow(context.Background(), `SELECT network FROM runs WHERE id = $1`, id).Scan(&raw)
		return raw != nil
	})
	var kept lux.Network
	if err := json.Unmarshal(raw, &kept); err != nil || !kept.Unrestricted {
		t.Errorf("recorded = %s (%v), want unrestricted", raw, err)
	}

	// Turned off: the session's next agent keeps to the list, with no Exa.
	mustExec(t, s.owner, `UPDATE organizations SET brainstorm_anywhere = false WHERE id = $1`, s.org)
	mustExec(t, s.owner, `UPDATE runs SET status = 'failed', ended_at = now() WHERE id = $1`, id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/chat", map[string]any{"text": "and their free tiers?"})
	var next string
	s.until("the session's next agent to be submitted", func() bool {
		next, _ = s.brainstorm(session)
		v = s.luxRun(next)
		return next != id && v != nil
	})
	if got, want := rules(*v.spec.Network), []string{"llm.example", "pypi.org"}; v.spec.Network.Unrestricted || !slices.Equal(got, want) {
		t.Errorf("the next brainstorm's network = %+v, want %v", v.spec.Network, want)
	}
	if _, exa := v.spec.Env["OPENCODE_ENABLE_EXA"]; exa {
		t.Error("OPENCODE_ENABLE_EXA set on the brainstorm started after the setting was turned off")
	}
	if strings.Contains(v.spec.Workload.Prompt, webNote) {
		t.Errorf("the brainstorm started after the setting was turned off is told it can read the web:\n%s", v.spec.Workload.Prompt)
	}
}

// webNote opens what a brainstorm that may reach any host is told.
const webNote = "You can read the web: search for and fetch public pages"

// "*" in its organisation's list makes a brainstorm unrestricted with the
// setting off, and that counts as reaching any host from its first prompt:
// it is told it can read the web, and Codex is pinned to search live.
func TestABrainstormMadeUnrestrictedByAStarIsToldItCanReadTheWeb(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = hang
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'gpt-6-sol' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	mustExec(t, s.owner, `UPDATE organizations SET agent_egress = '{pypi.org,*}', brainstorm_anywhere = false WHERE id = $1`, s.org)
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "what do competitors charge?", "harness": "codex"})["id"].(string)
	run, _, _, harness := s.submitted(id)
	v := s.luxRun(run)
	if harness != "codex" || v.spec.Network == nil || !v.spec.Network.Unrestricted {
		t.Fatalf("submitted on %s with network %+v, want codex unrestricted", harness, v.spec.Network)
	}
	if !strings.Contains(v.spec.Workload.Prompt, webNote) {
		t.Errorf("not told it can read the web:\n%s", v.spec.Workload.Prompt)
	}
	if !codexSearchesLive(t, v.spec.Secrets) {
		t.Error("Codex not pinned to search live")
	}
}
