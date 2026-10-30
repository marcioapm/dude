package phases

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A recovery's publisher Run is priced by lux as any Run: the cost sweep
// reads it, keeps it on the recovery, and it is counted for the Run it
// recovered — run_model_usd, run_metrics, task_metrics, run_cost_origin —
// by exactly lux's amounts, settling from pending to final as a Run's
// does, and is never asked of lux again once final.
func TestARecoverysCostIsTheDeadRunsAndTheTasks(t *testing.T) {
	f := newCostFixture(t)
	f.run("run_dead", "1 hour")
	f.lux.set("lux_run_dead", lux.RunCost{Status: lux.CostFinal, Final: true,
		ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "1.25"), usd(lux.FamilyCompute, "0.5")}})
	f.sweep()
	type figures struct {
		Model, RunModel, RunMachine, TaskModel, TaskMachine float64
		Final                                               bool
	}
	read := func() figures {
		t.Helper()
		var g figures
		if err := f.owner.QueryRow(f.ctx, `SELECT run_model_usd(r)::float8, m.cost_usd, m.machine_usd, tm.cost_usd, tm.machine_usd, o.lux_final
			FROM runs r CROSS JOIN LATERAL run_metrics(r.id) m CROSS JOIN LATERAL run_cost_origin(r) o
			CROSS JOIN LATERAL task_metrics(r.task_id) tm WHERE r.id = 'run_dead'`).
			Scan(&g.Model, &g.RunModel, &g.RunMachine, &g.TaskModel, &g.TaskMachine, &g.Final); err != nil {
			t.Fatal(err)
		}
		return g
	}
	before := read()
	if before.Model != 1.25 || before.RunMachine != 0.5 || before.TaskModel != 1.25 || !before.Final {
		t.Fatalf("before any recovery: %+v", before)
	}
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO run_recoveries (id, organization_id, task_id, run_id, branch, status,
			publisher_lux_run_id, lux_cost_next_at)
		VALUES ('rcv_1', $1, 'wi_'||$1, 'run_dead', 'dude/x/recovered-run_dead', 'done', 'lux_pub', now())`, f.org); err != nil {
		t.Fatal(err)
	}
	// Asked, and nothing priced yet: no amount, and no longer settled.
	f.sweep()
	if g := read(); g.Model != 1.25 || g.RunMachine != 0.5 || g.Final {
		t.Errorf("the publisher not priced yet: %+v", g)
	}
	f.lux.set("lux_pub", lux.RunCost{Status: lux.CostIncomplete,
		ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0"), usd(lux.FamilyCompute, "0.010000001")}})
	f.sweep()
	if g := read(); g.Model != 1.25 || g.RunMachine != 0.5+0.010000001 || g.TaskMachine != 0.5+0.010000001 || g.Final {
		t.Errorf("priced, not final: %+v", g)
	}
	f.lux.set("lux_pub", lux.RunCost{Status: lux.CostFinal, Final: true,
		ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0.000123"), usd(lux.FamilyCompute, "0.0125")}})
	f.sweep()
	want := figures{Model: 1.25 + 0.000123, RunModel: 1.25 + 0.000123, RunMachine: 0.5 + 0.0125, TaskModel: 1.25 + 0.000123,
		TaskMachine: 0.5 + 0.0125, Final: true}
	if g := read(); g != want {
		t.Errorf("final: %+v, want %+v", g, want)
	}
	asked := f.lux.asked["lux_pub"]
	f.sweep()
	if f.lux.asked["lux_pub"] != asked {
		t.Error("a final recovery's cost was asked for again")
	}
	// Not the Run's own lux cost: nothing reported as if lux priced it anew.
	if n := len(f.reported("run_dead")); n != 1 {
		t.Errorf("%d run.cost.reported on the dead Run, want its own 1", n)
	}
}
