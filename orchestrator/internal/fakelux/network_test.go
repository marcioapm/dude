package fakelux

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// dnsEvents are the dns lifecycle events on a Run's stream, as lux writes
// them: one per distinct name it was asked, allowed or not.
func dnsEvents(t *testing.T, c lux.Client, runID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = c.Output(ctx, runID, "", 0, func(f lux.Frame) error {
		if f.Kind == "lux" && f.EventType == "dns" {
			var d map[string]any
			_ = json.Unmarshal(f.EventData, &d)
			out = append(out, d)
		}
		return nil
	})
	return out
}

// A Run's agent looks names up as its behaviour says; lux answers each
// distinct one once, with whether the Run may reach it, before the turn's
// tools run.
func TestAnAgentsLookupsAreDNSEventsAsLuxWritesThem(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour {
		return Behaviour{Lookups: []Lookup{{Name: "files.pythonhosted.org"}, {Name: "api.github.com", Allowed: true}, {Name: "files.pythonhosted.org"}}}
	})
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	c := lux.New(srv.URL, "k")
	run, err := c.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "agent:1"}, Workload: lux.Workload{Adapter: "acp"}}, "dns")
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "the turn never ended", func(r *Run) bool { return r.turnsEnded == 1 })
	got := dnsEvents(t, c, run.ID)
	if len(got) != 2 || got[0]["name"] != "files.pythonhosted.org" || got[0]["allowed"] != false ||
		got[1]["name"] != "api.github.com" || got[1]["allowed"] != true {
		t.Errorf("dns events = %v", got)
	}
}

// The scripted agent on fakeagent.LookupModel looks up the names its
// project's tests ask for, and lux decides from the Run's own network
// whether each is allowed, as a real resolver would.
func TestTheScriptedLookupsAreDecidedByTheRunsNetwork(t *testing.T) {
	fake := New("", "k", nil)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	c := lux.New(srv.URL, "k")
	spec := lux.Spec{Image: lux.Image{Ref: "agent:1"}, Workload: lux.Workload{Adapter: "acp", Command: []string{"lux-fake"}},
		Labels:  map[string]string{"dude.phase": "implement", "dude.model": fakeagent.LookupModel, "dude.run": "run_1"},
		Network: &lux.Network{Egress: []lux.EgressRule{{Host: "*.pythonhosted.org"}}}}
	run, err := c.Submit(context.Background(), spec, "scripted-dns")
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "the turn never ended", func(r *Run) bool { return r.turnsEnded == 1 })
	allowed := map[string]any{}
	for _, e := range dnsEvents(t, c, run.ID) {
		allowed[e["name"].(string)] = e["allowed"]
	}
	if len(allowed) != len(fakeagent.Lookups) || allowed["files.pythonhosted.org"] != true || allowed["registry.npmjs.org"] != false {
		t.Errorf("lookups = %v", allowed)
	}
}
