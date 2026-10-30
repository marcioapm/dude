package phases

import (
	"strings"
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
	// Two more recoveries of the Run, each its own publisher, priced and
	// repriced in turn: the Run counts every recovery's cost, not the
	// latest one's, and is settled only once each is final.
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO run_recoveries (id, organization_id, task_id, run_id, branch, status,
			publisher_lux_run_id, lux_cost_next_at)
		VALUES ('rcv_a', $1, 'wi_'||$1, 'run_dead', 'dude/x/recovered-a', 'done', 'lux_pub_a', now()),
		       ('rcv_b', $1, 'wi_'||$1, 'run_dead', 'dude/x/recovered-b', 'done', 'lux_pub_b', now())`, f.org); err != nil {
		t.Fatal(err)
	}
	priced := func(ai, compute lux.Decimal, final bool) lux.RunCost {
		status := lux.CostPending
		if final {
			status = lux.CostFinal
		}
		return lux.RunCost{Status: status, Final: final, ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, ai), usd(lux.FamilyCompute, compute)}}
	}
	type kept struct {
		AI, Compute float64
		Unsettled   int
	}
	for _, step := range []struct {
		what           string
		a, b           lux.RunCost
		model, machine float64
		final          bool
	}{
		{"both pending", priced("0.1", "0.2", false), priced("0.01", "0.02", false),
			1.25 + 0.000123 + 0.1 + 0.01, 0.5 + 0.0125 + 0.2 + 0.02, false},
		{"A repriced", priced("0.3", "0.4", false), priced("0.01", "0.02", false),
			1.25 + 0.000123 + 0.3 + 0.01, 0.5 + 0.0125 + 0.4 + 0.02, false},
		{"A final", priced("0.5", "0.6", true), priced("0.01", "0.02", false),
			1.25 + 0.000123 + 0.5 + 0.01, 0.5 + 0.0125 + 0.6 + 0.02, false},
		{"B final", priced("0.5", "0.6", true), priced("0.05", "0.06", true),
			1.25 + 0.000123 + 0.5 + 0.05, 0.5 + 0.0125 + 0.6 + 0.06, true},
	} {
		f.lux.set("lux_pub_a", step.a)
		f.lux.set("lux_pub_b", step.b)
		f.sweep()
		if g := read(); g.Model != step.model || g.RunModel != step.model || g.RunMachine != step.machine ||
			g.TaskMachine != step.machine || g.Final != step.final {
			t.Errorf("%s: %+v, want model %v, machine %v, final %v", step.what, g, step.model, step.machine, step.final)
		}
		var onRun, summed kept
		if err := f.owner.QueryRow(f.ctx, `SELECT COALESCE(recovery_ai_usd, 0)::float8, COALESCE(recovery_compute_usd, 0)::float8,
				recoveries_unsettled FROM runs WHERE id = 'run_dead'`).Scan(&onRun.AI, &onRun.Compute, &onRun.Unsettled); err != nil {
			t.Fatal(err)
		}
		if err := f.owner.QueryRow(f.ctx, `SELECT COALESCE(sum(lux_ai_usd), 0)::float8, COALESCE(sum(lux_compute_usd), 0)::float8,
				count(*) FILTER (WHERE publisher_lux_run_id IS NOT NULL AND lux_cost_status IS DISTINCT FROM 'final')::int
			FROM run_recoveries WHERE run_id = 'run_dead'`).Scan(&summed.AI, &summed.Compute, &summed.Unsettled); err != nil {
			t.Fatal(err)
		}
		if onRun != summed {
			t.Errorf("%s: the Run keeps %+v, its recoveries sum to %+v", step.what, onRun, summed)
		}
	}
}

// run_model_usd and run_cost_origin stay functions of the row alone, as
// 061 made them: IMMUTABLE single expressions, so a list over every Run
// (the board's cost) inlines run_model_usd and never runs a query per row.
func TestTheRunCostFunctionsAreInlined(t *testing.T) {
	f := newCostFixture(t)
	for _, fn := range []string{"run_model_usd", "run_cost_origin"} {
		var volatility, src string
		if err := f.owner.QueryRow(f.ctx, `SELECT provolatile::text, prosrc FROM pg_proc WHERE proname = $1`, fn).
			Scan(&volatility, &src); err != nil {
			t.Fatal(err)
		}
		if volatility != "i" {
			t.Errorf("%s is provolatile %q, want IMMUTABLE (i)", fn, volatility)
		}
		if n := strings.Count(strings.ToUpper(src), "SELECT"); n != 1 {
			t.Errorf("%s reads %d SELECTs, want its one expression: %s", fn, n, src)
		}
	}
	// run_cost_origin returns a record, which Postgres never inlines (061's
	// was not either): its one row-only SELECT above is what keeps it cheap.
	for _, q := range []string{`SELECT sum(run_model_usd(r)) FROM runs r`} {
		rows, err := f.owner.Query(f.ctx, `EXPLAIN (VERBOSE, FORMAT TEXT) `+q)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, l)
		}
		rows.Close()
		whole := strings.Join(lines, "\n")
		if strings.Contains(whole, "run_model_usd") || strings.Contains(whole, "run_cost_origin") || strings.Contains(whole, "SubPlan") {
			t.Errorf("%s is not inlined:\n%s", q, whole)
		}
	}
}
