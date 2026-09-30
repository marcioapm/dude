package phases

import (
	"math"
	"testing"
)

// What a Run cost is lux's figure once lux has one — replacing the
// harness's, never added to it — and the harness's or dude's estimate
// until then; the task's total is its Runs' sum under the same rule.
func TestEffectiveCostPrefersLuxAndNeverAddsItToTheHarnesss(t *testing.T) {
	f := newCostFixture(t)
	// Each ran exactly an hour, on a $0.20/h machine by dude's estimate,
	// and its harness reported $0.30 of tokens.
	add := func(id string, luxAI, luxCompute any) {
		t.Helper()
		if _, err := f.owner.Exec(f.ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, lux_run_id,
				started_at, ended_at, agent_cost_usd, machine_usd_per_hour, lux_ai_usd, lux_compute_usd)
			VALUES ($1, $2, 'prj_'||$2, 'wi_'||$2, 1, 'lux_'||$1, now() - interval '1 hour', now(), 0.30, 0.20,
			        $3::numeric, $4::numeric)`, id, f.org, luxAI, luxCompute); err != nil {
			t.Fatal(err)
		}
	}
	add("run_lux", "1.810247", "0.007659225")
	add("run_harness", nil, nil)
	add("run_zero", "0", nil)

	metrics := func(id string) (model, machine float64) {
		t.Helper()
		if err := f.owner.QueryRow(f.ctx, `SELECT cost_usd, machine_usd FROM run_metrics($1)`, id).Scan(&model, &machine); err != nil {
			t.Fatal(err)
		}
		return
	}
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-6 }

	if model, machine := metrics("run_lux"); !near(model, 1.810247) || !near(machine, 0.007659225) {
		t.Errorf("lux-priced Run: model %v machine %v, want lux's 1.810247 and 0.007659225", model, machine)
	}
	if model, machine := metrics("run_harness"); !near(model, 0.30) || !near(machine, 0.20) {
		t.Errorf("Run lux has not priced: model %v machine %v, want the harness's 0.30 and the estimate 0.20", model, machine)
	}
	// lux saying zero is a price: it replaces the harness's figure too.
	if model, _ := metrics("run_zero"); !near(model, 0) {
		t.Errorf("lux's AI cost of 0: model %v, want 0", model)
	}

	var model, machine float64
	if err := f.owner.QueryRow(f.ctx, `SELECT cost_usd, machine_usd FROM task_metrics('wi_'||$1)`, f.org).Scan(&model, &machine); err != nil {
		t.Fatal(err)
	}
	if !near(model, 1.810247+0.30) || !near(machine, 0.007659225+0.20+0.20) {
		t.Errorf("task: model %v machine %v", model, machine)
	}
}
