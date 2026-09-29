package orchestrator_test

import (
	"slices"
	"testing"
	"time"
)

// A preview stopped while parked, or before lux has it, still has its lux
// Run cancelled: nothing about a stopped preview is kept.
func TestAStoppedPreviewIsCancelledInLuxWhateverItsState(t *testing.T) {
	w := newWorld(t)
	w.actor = w.person("Ana")
	w.previews.Minute = time.Millisecond
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":1}' WHERE id = $1`, w.project)

	wi := w.task()
	if code, _ := w.do("POST", "/internal/tasks/"+wi+"/preview", nil); code != 201 {
		t.Fatal(code)
	}
	var luxID string
	w.until("the preview to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'paused' AND lux_state = 'stopped'`, wi) == 1
	})
	luxID = w.lux.Runs()[0].ID
	if code, _ := w.do("DELETE", "/internal/tasks/"+wi+"/preview", nil); code != 200 {
		t.Fatal(code)
	}
	w.until("lux to cancel the parked preview", func() bool { return slices.Contains(w.lux.CallsOf(luxID), "cancel") })

	// Stopped before the sweep ever submitted it: lux never hears of it.
	wi2 := w.task()
	if code, _ := w.do("POST", "/internal/tasks/"+wi2+"/preview", nil); code != 201 {
		t.Fatal(code)
	}
	if code, _ := w.do("DELETE", "/internal/tasks/"+wi2+"/preview", nil); code != 200 {
		t.Fatal(code)
	}
	w.pump()
	if n := len(w.lux.Runs()); n != 1 {
		t.Errorf("%d lux runs; a preview stopped before it was submitted was submitted", n)
	}
}
