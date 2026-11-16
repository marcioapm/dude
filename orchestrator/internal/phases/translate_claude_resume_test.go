package phases

import (
	"context"
	"math"
	"slices"
	"testing"
)

// claudeSession, claudeTurn: the shim's session record, and one Claude Code
// turn whose result line gives the session's running total.
func claudeSession(id string) map[string]any {
	return rec("lux.session", map[string]any{"sessionId": id})
}

func claudeTurn(total float64) []map[string]any {
	return []map[string]any{claudeEnd, luxIdle, claudeResultLine(map[string]any{"total_cost_usd": total})}
}

// turnCosts are the costUsd of the turns recorded since the last read.
func (w *harnessWorld) turnCosts() []float64 {
	w.t.Helper()
	var out []float64
	for _, e := range ofType(w.events(), evModelRequestDone) {
		c, _ := e.Payload["costUsd"].(float64)
		out = append(out, c)
	}
	return out
}

func (w *harnessWorld) runCost() float64 {
	w.t.Helper()
	var cost float64
	if err := w.owner.QueryRow(context.Background(), `SELECT agent_cost_usd::float8 FROM runs WHERE id = $1`, w.run.ID).Scan(&cost); err != nil {
		w.t.Fatal(err)
	}
	return cost
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func nearAll(got, want []float64) bool { return slices.EqualFunc(got, want, near) }

// Claude Code resumed into its session (the same session id) carries the
// session's running total on: each turn costs what that total grew by, and
// the Run costs the session's last total, not the total again per resume.
func TestAResumedClaudeSessionCountsItsCostOnce(t *testing.T) {
	w := newHarnessWorld(t)
	var first []map[string]any
	first = append(first, claudeSession("s1"))
	first = append(append(first, claudeTurn(0.5)...), claudeTurn(1.2)...)
	w.feedAt(1, first...)
	w.restart()
	resumed := []map[string]any{claudeSession("s1")}
	resumed = append(append(resumed, claudeTurn(1.9)...), claudeTurn(2.4)...)
	w.feedAt(2, resumed...)
	if got := w.turnCosts(); !nearAll(got, []float64{0.5, 0.7, 0.7, 0.5}) {
		t.Errorf("turn costs = %v, want [0.5 0.7 0.7 0.5]", got)
	}
	if got := w.runCost(); !near(got, 2.4) {
		t.Errorf("run cost = %v, want the session's 2.4", got)
	}
}

// A conductor parked and woken many times is one Claude session: its cost
// is that session's last running total, however many resumes it took.
func TestManyResumesOfOneClaudeSessionCostItsLastTotal(t *testing.T) {
	w := newHarnessWorld(t)
	total := 0.0
	for epoch := 1; epoch <= 8; epoch++ {
		records := []map[string]any{claudeSession("s1")}
		for range 2 {
			total += 0.25 * float64(epoch)
			records = append(records, claudeTurn(total)...)
		}
		w.feedAt(epoch, records...)
		if epoch%3 == 0 {
			w.restart()
		}
	}
	if got := w.runCost(); !near(got, total) {
		t.Errorf("run cost after 7 resumes = %v, want the last total %v", got, total)
	}
}

// A resume lux could not reload the session for starts a new one (another
// id): its running total starts from zero, and is counted in full on top of
// the old session's, even when its first total passes the old one's last.
func TestAFreshClaudeSessionInTheSameRunIsCountedInFull(t *testing.T) {
	w := newHarnessWorld(t)
	first := append([]map[string]any{claudeSession("s1")}, claudeTurn(0.5)...)
	w.feedAt(1, append(first, claudeTurn(1.2)...)...)
	w.restart()
	fresh := []map[string]any{rec("lux.warning", map[string]any{"message": "session/load failed, starting a new session"}), claudeSession("s2")}
	fresh = append(append(fresh, claudeTurn(1.5)...), claudeTurn(2.0)...)
	w.feedAt(2, fresh...)
	if got := w.turnCosts(); !nearAll(got, []float64{0.5, 0.7, 1.5, 0.5}) {
		t.Errorf("turn costs = %v, want [0.5 0.7 1.5 0.5]", got)
	}
	if got := w.runCost(); !near(got, 3.2) {
		t.Errorf("run cost = %v, want 1.2 + 2.0", got)
	}
	// Resumed back into that new session: its total carries on.
	w.feedAt(3, append([]map[string]any{claudeSession("s2")}, claudeTurn(2.6)...)...)
	if got := w.runCost(); !near(got, 3.8) {
		t.Errorf("run cost = %v, want 1.2 + 2.6", got)
	}
}

// A turn whose result never came, ended by the resume, cost nothing yet:
// the session's total is the one already counted, not a new session's.
func TestAHalfTurnEndedByAResumeCostsNothingNew(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, append(append([]map[string]any{claudeSession("s1")}, claudeTurn(0.5)...), claudeEnd)...)
	w.feedAt(2, append([]map[string]any{claudeSession("s1")}, claudeTurn(0.8)...)...)
	if got := w.turnCosts(); !nearAll(got, []float64{0.5, 0, 0.3}) {
		t.Errorf("turn costs = %v, want [0.5 0 0.3]", got)
	}
	if got := w.runCost(); !near(got, 0.8) {
		t.Errorf("run cost = %v, want the session's 0.8", got)
	}
}

// A new session's first turn, ended by the next resume with no result,
// is read against the new session's total, not the old one's.
func TestANewSessionsHalfTurnIsNotReadAgainstTheOldTotal(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, append([]map[string]any{claudeSession("s1")}, claudeTurn(1.2)...)...)
	w.feedAt(2, claudeSession("s2"), claudeEnd)
	w.feedAt(3, append([]map[string]any{claudeSession("s2")}, claudeTurn(0.4)...)...)
	if got := w.turnCosts(); !nearAll(got, []float64{1.2, 0, 0.4}) {
		t.Errorf("turn costs = %v, want [1.2 0 0.4]", got)
	}
	if got := w.runCost(); !near(got, 1.6) {
		t.Errorf("run cost = %v, want 1.2 + 0.4", got)
	}
}

// A Run from before the session was kept, its session named nowhere, is
// resumed as the same session: its total carries on.
func TestAResumeOfARunWithNoKnownSessionKeepsItsCost(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, append([]map[string]any{claudeSession("s1")}, claudeTurn(0.5)...)...)
	for _, q := range []string{`UPDATE runs SET harness_state = harness_state - 'sessionId' WHERE id = $1`,
		`DELETE FROM events WHERE run_id = $1 AND event_type = '` + evSessionStarted + `'`} {
		if _, err := w.owner.Exec(t.Context(), q, w.run.ID); err != nil {
			t.Fatal(err)
		}
	}
	w.restart()
	w.feedAt(2, append([]map[string]any{claudeSession("s1")}, claudeTurn(0.8)...)...)
	if got := w.runCost(); !near(got, 0.8) {
		t.Errorf("run cost = %v, want the session's 0.8", got)
	}
}
