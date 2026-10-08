package orchestrator_test

import (
	"slices"
	"testing"
)

// eachLux runs body in a fresh world on the current lux (newName), then on
// one from before terminated, whose succeeded is final (oldName).
func eachLux(t *testing.T, newName, oldName string, body func(t *testing.T, w *world, old bool)) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: newName, true: oldName}[old], func(t *testing.T) {
			w := newWorld(t)
			w.lux.CancelledState = old
			body(t, w, old)
		})
	}
}

// An asleep preview whose lux Run ended succeeded is woken by resuming that
// Run on a lux that resumes succeeded Runs, as a stopped one is; on a lux
// from before, which refuses, a new Run is submitted. Either then serves.
func TestASucceededPreviewRunIsResumedWhereLuxCan(t *testing.T) {
	eachLux(t, "resumable", "final", func(t *testing.T, w *world, old bool) {
		w.wakeable()
		w.recipe("web", 3000, "npm run dev", "", nil, true)
		_, runID := w.declare()
		web := w.serverID(runID, "web")
		w.open(web)
		w.running(runID, "web")
		r := w.luxRuns()[0]
		before := r.Resumed
		// Its workload exited 0 on its own: the preview goes to sleep.
		w.lux.Succeed(r.ID)
		w.untilPreview(runID, "asleep on a succeeded Run", func() bool {
			return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'succeeded'`, runID) == 1
		})
		w.open(web)
		w.untilPreview(runID, "serving", func() bool {
			return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
		})
		if old {
			// Replaced, and the refused Run terminated, not left resting in lux.
			if calls := w.lux.CallsOf(r.ID); r.Resumed != before || len(w.luxRuns()) != 2 || !slices.Contains(calls, "cancel") {
				t.Fatalf("a final succeeded Run: resumed %d more, %d lux runs, calls %v; want it terminated and replaced\n%s",
					r.Resumed-before, len(w.luxRuns()), calls, w.preview(runID))
			}
			return
		}
		if r.Resumed != before+1 || len(w.luxRuns()) != 1 {
			t.Fatalf("a resumable succeeded Run: resumed %d more, %d lux runs; want it resumed once (calls %v)\n%s", r.Resumed-before, len(w.luxRuns()), w.lux.CallsOf(r.ID), w.preview(runID))
		}
	})
}
