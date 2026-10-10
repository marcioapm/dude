package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"
)

// A Run goes to lux on its role's tier: the model the tier requests, under
// the provider its name goes through, and the tier's name as a label; the
// Run records them and the tier's effort, in the statement that records its
// lux Run id.
func TestAPhaseRunsOnItsRolesTierAndRecordsIt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.onModel("implementer", "gpt-5.6-sol")
	mustExec(t, w.owner, `UPDATE model_tiers SET name = 'Coder*', effort = 'low' WHERE organization_id = $1 AND model = 'gpt-5.6-sol'`, w.org)
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	spec := w.specOf("implement")
	if spec.Labels["dude.model"] != "gpt-5.6-sol" || spec.Labels["dude.model_tier"] != "Coder*" {
		t.Errorf("labels = %v, want the tier's model and name", spec.Labels)
	}
	var config map[string]any
	_ = json.Unmarshal([]byte(spec.Env["OPENCODE_CONFIG_CONTENT"]), &config)
	if config["model"] != "llm-openai/gpt-5.6-sol" {
		t.Errorf("config model = %v, want llm-openai/gpt-5.6-sol", config["model"])
	}
	var model, tier, effort, luxID string
	if err := w.owner.QueryRow(ctx, `SELECT model, model_tier, COALESCE(effort, '<null>'), lux_run_id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).
		Scan(&model, &tier, &effort, &luxID); err != nil {
		t.Fatal(err)
	}
	if model != "gpt-5.6-sol" || tier != "Coder*" || effort != "low" || luxID == "" {
		t.Errorf("run records model %q tier %q effort %q lux %q", model, tier, effort, luxID)
	}
}

// Editing the tier moves the next session, not the running one: a parked
// Run resumed after its tier changed — even to no model at all — goes on
// with the model and tier it started on.
func TestATiersNewModelReachesTheNextSessionNotTheRunningOne(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	tier := w.onModel("implementer", "claude-opus-5-5")
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)

	mustExec(t, w.owner, `UPDATE model_tiers SET model = NULL, name = 'Renamed' WHERE id = $1`, tier)
	r := w.lux.Runs()[0]
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, out)
	}
	w.until("the Run to resume", func() bool { w.pump(); return w.luxCalls(r.ID, "resume") == 1 })
	var model, name string
	if err := w.owner.QueryRow(ctx, `SELECT model, model_tier FROM runs WHERE id = $1`, runID).Scan(&model, &name); err != nil {
		t.Fatal(err)
	}
	// The Run keeps what it was submitted with: the model, and the tier's name then.
	if model != "claude-opus-5-5" || name != onModelTier("claude-opus-5-5") {
		t.Errorf("after the tier changed the running Run says %s on %s", model, name)
	}
	if spec := submitted(t, r); spec.Labels["dude.model"] != "claude-opus-5-5" {
		t.Errorf("lux's spec says %s", spec.Labels["dude.model"])
	}

	// A new session takes the tier as it is now.
	mustExec(t, w.owner, `UPDATE model_tiers SET model = 'claude-fable-5-1' WHERE id = $1`, tier)
	wi2 := w.task()
	w.deliver(wi2)
	w.until("the second implementer to reach lux", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND model = 'claude-fable-5-1' AND model_tier = 'Renamed'`, wi2) == 1
	})
}

// A role whose tier names no model, or that names no tier, fails its Run
// with the reason in words, and nothing reaches lux.
func TestARunWithNoModelFailsSayingWhy(t *testing.T) {
	for _, tc := range []struct {
		name, setup, want string
	}{
		{"a tier with no model", `UPDATE model_tiers SET model = NULL WHERE organization_id = $1`,
			"The Implementer runs on T fake/scripted, which names no model yet. An admin sets it in Models."},
		{"no tier", `UPDATE projects SET agent_models = agent_models - 'implementer' WHERE organization_id = $1`,
			"The Implementer runs on no model tier. An admin picks one in Agents."},
		{"a tier that is gone", `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer}', '{"tier":"mtr_gone"}') WHERE organization_id = $1`,
			"The Implementer's model tier no longer exists. An admin picks another in Agents."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			if tc.name == "no tier" {
				mustExec(t, w.owner, `UPDATE organizations SET default_agent_models = default_agent_models - 'implementer' WHERE id = $1`, w.org)
			}
			mustExec(t, w.owner, tc.setup, w.org)
			wi := w.task()
			w.deliver(wi)
			w.until("the implementer to fail", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'`, wi) == 1
			})
			var reason string
			_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&reason)
			if reason != tc.want {
				t.Errorf("reason = %q, want %q", reason, tc.want)
			}
			if n := len(w.lux.Runs()); n != 0 {
				t.Errorf("%d Runs reached lux", n)
			}
		})
	}
}
