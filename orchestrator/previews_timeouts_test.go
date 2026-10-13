package orchestrator_test

import (
	"testing"
	"time"
)

func TestPreviewWakeTimeoutStartsLongAndChangesAfterReady(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	sv, _ := w.lux.TenantServer(web)
	if got := sv.WakeTimeoutDuration(); got != 15*time.Minute {
		t.Fatalf("old settings first timeout = %v", got)
	}
	w.open(web)
	w.until("later timeout after ready", func() bool {
		sv, _ := w.lux.TenantServer(web)
		return sv.WakeTimeoutDuration() == 5*time.Minute
	})
	if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND preview_ready_at IS NOT NULL`, runID) != 1 {
		t.Fatal("ready not persisted")
	}
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"firstWakeTimeoutMinutes":25,"wakeTimeoutMinutes":7}' WHERE id = $1`, w.project)
	mustExec(t, w.owner, `UPDATE runs SET wake_timeouts_checked_at = NULL WHERE id = $1`, runID)
	w.until("changed later timeout", func() bool {
		sv, _ := w.lux.TenantServer(web)
		return sv.WakeTimeoutDuration() == 7*time.Minute
	})
	_, next := w.declare()
	fresh, _ := w.lux.TenantServer(w.serverID(next, "web"))
	if fresh.WakeTimeoutDuration() != 25*time.Minute {
		t.Fatalf("new first timeout = %v", fresh.WakeTimeoutDuration())
	}
}
