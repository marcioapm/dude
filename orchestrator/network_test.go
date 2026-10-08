package orchestrator_test

import (
	"context"
	"encoding/json"
	"slices"
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
}
