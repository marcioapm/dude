package phases

import "testing"

// acpSession, acpCost: lux's session record, and an ACP agent's running
// report of its session's cost: the session's total so far, not the turn's.
func acpSession(id string) map[string]any {
	return rec("lux.session", map[string]any{"sessionId": id})
}

func acpCost(amount float64) map[string]any {
	return rec("acp.usage_update", map[string]any{"sessionUpdate": "usage_update", "cost": map[string]any{"amount": amount, "currency": "USD"}})
}

func (w *harnessWorld) harnessStateHas(key string) bool {
	w.t.Helper()
	var has bool
	if err := w.owner.QueryRow(w.t.Context(), `SELECT harness_state ? $2 FROM runs WHERE id = $1`, w.run.ID, key).Scan(&has); err != nil {
		w.t.Fatal(err)
	}
	return has
}

// An ACP agent resumed into a new session (lux could not reload its own)
// reports that session's cost from zero: it is counted in full on top of
// what the old session cost, from its first report on, and kept so across
// a restart of the follower (#480).
func TestANewACPSessionsCostIsCountedOnTopOfTheOldOnes(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, acpSession("ses_a"), acpCost(1.2))
	w.feedAt(2, rec("lux.warning", map[string]any{"message": "session/load failed, starting a new session"}),
		acpSession("ses_b"), acpCost(0.8))
	w.restart()
	w.feedAt(3, acpSession("ses_b"), acpCost(1.5))
	if got := w.turnCosts(); !nearAll(got, []float64{1.2, 0.8, 0.7}) {
		t.Errorf("costs = %v, want [1.2 0.8 0.7]", got)
	}
	if got := w.runCost(); !near(got, 2.7) {
		t.Errorf("run cost = %v, want 1.2 + 1.5", got)
	}
	// A new session's report of 0 is an unknown cost: no refund.
	w.feedAt(4, acpSession("ses_c"), acpCost(0))
	if got := w.turnCosts(); len(got) != 0 {
		t.Errorf("a new session's unknown cost recorded %v", got)
	}
	if got := w.runCost(); !near(got, 2.7) {
		t.Errorf("run cost = %v after an unknown cost, want 2.7", got)
	}
	w.feedAt(4, acpCost(0.25))
	if got := w.turnCosts(); !nearAll(got, []float64{0.25}) {
		t.Errorf("costs = %v, want [0.25]", got)
	}
	if got := w.runCost(); !near(got, 2.95) {
		t.Errorf("run cost = %v, want 1.2 + 1.5 + 0.25", got)
	}
}

// Resumed into the same session, an ACP agent's running total carries on:
// counted once, not again on top of itself.
func TestAResumedACPSessionCountsItsCostOnce(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, acpSession("ses_a"), acpCost(1.2))
	w.restart()
	w.feedAt(2, acpSession("ses_a"), acpCost(1.5))
	if got := w.turnCosts(); !nearAll(got, []float64{1.2, 0.3}) {
		t.Errorf("costs = %v, want [1.2 0.3]", got)
	}
	if got := w.runCost(); !near(got, 1.5) {
		t.Errorf("run cost = %v, want the session's 1.5", got)
	}
}

// The next round's row takes the Run's lux Run over (delivery.handOverTx):
// its harness_state as it was, and the costs so far as its base. Its
// session's next report is counted only for what that session grew by.
func TestAHandedOverACPRunCountsOnlyItsSessionsGrowth(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, acpSession("ses_a"), acpCost(1.2))
	w.feedAt(2, acpSession("ses_b"), acpCost(0.5))
	if !w.harnessStateHas("acpCostBase") {
		t.Fatal("the replaced session's base was not kept in harness_state")
	}
	// What handOverTx sets on the new row, here on the same one: the
	// harness_state copied, the cost so far moved into agent_cost_base.
	if _, err := w.owner.Exec(t.Context(), `UPDATE runs SET harness_state = harness_state - 'brokenTurn',
		agent_cost_base = agent_cost_base + agent_cost_usd, agent_cost_usd = 0 WHERE id = $1`, w.run.ID); err != nil {
		t.Fatal(err)
	}
	w.events()
	w.restart()
	w.feedAt(3, acpSession("ses_b"), acpCost(0.9))
	if got := w.turnCosts(); !nearAll(got, []float64{0.4}) {
		t.Errorf("costs = %v, want the session's growth [0.4]", got)
	}
	if got := w.runCost(); !near(got, 0.4) {
		t.Errorf("the next round's cost = %v, want 0.4", got)
	}
	var base float64
	if err := w.owner.QueryRow(t.Context(), `SELECT agent_cost_base::float8 FROM runs WHERE id = $1`, w.run.ID).Scan(&base); err != nil || !near(base, 1.7) {
		t.Errorf("base = %v (%v), want 1.7", base, err)
	}
	// And a session replaced in the next round counts from there.
	w.feedAt(4, acpSession("ses_c"), acpCost(0.3))
	if got := w.turnCosts(); !nearAll(got, []float64{0.3}) {
		t.Errorf("costs = %v, want [0.3]", got)
	}
	if got := w.runCost(); !near(got, 0.7) {
		t.Errorf("the next round's cost = %v, want 0.4 + 0.3", got)
	}
}

// A Run saved before the base was kept has no acpCostBase: it reads as
// none, and its session's total is counted as it was.
func TestAnACPRunWithNoCostBaseKeptCountsAsBefore(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, acpSession("ses_a"), acpCost(1.2))
	if w.harnessStateHas("acpCostBase") {
		t.Error("a Run with no replaced session saved an acpCostBase")
	}
	if _, err := w.owner.Exec(t.Context(), `UPDATE runs SET harness_state = harness_state - 'acpCostBase' WHERE id = $1`, w.run.ID); err != nil {
		t.Fatal(err)
	}
	w.restart()
	w.feedAt(2, acpSession("ses_a"), acpCost(1.0), acpCost(1.5))
	if got := w.turnCosts(); !nearAll(got, []float64{1.2, 0.3}) {
		t.Errorf("costs = %v, want [1.2 0.3]", got)
	}
	if got := w.runCost(); !near(got, 1.5) {
		t.Errorf("run cost = %v, want 1.5", got)
	}
}
