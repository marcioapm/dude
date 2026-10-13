package orchestrator_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func (w *world) wakeTimeoutOf(id string) time.Duration {
	sv, _ := w.lux.TenantServer(id)
	return sv.WakeTimeoutDuration()
}

func TestPreviewWakeTimeoutStartsLongAndChangesAfterReady(t *testing.T) {
	w := newWorld(t)
	feed := w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	if got := w.wakeTimeoutOf(web); got != 15*time.Minute {
		t.Fatalf("old settings first timeout = %v", got)
	}
	w.open(web)
	w.until("later timeout after ready", func() bool { return w.wakeTimeoutOf(web) == 5*time.Minute })
	if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND preview_ready_at IS NOT NULL AND wake_timeouts_checked_at IS NOT NULL`, runID) != 1 {
		t.Fatal("ready or the timeout check not persisted")
	}
	readyAt := w.str(`SELECT preview_ready_at::text FROM runs WHERE id = $1`, runID)

	// A later ready of the preview leaves its first one.
	if err := feed.Apply(context.Background(), lux.FeedEvent{ID: 1 << 40, Type: "server.state", ServerID: web,
		Data: map[string]any{"state": lux.ServerReady}}); err != nil {
		t.Fatal(err)
	}
	if got := w.str(`SELECT preview_ready_at::text FROM runs WHERE id = $1`, runID); got != readyAt {
		t.Fatalf("preview_ready_at moved from %s to %s", readyAt, got)
	}

	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"firstWakeTimeoutMinutes":25,"wakeTimeoutMinutes":7}' WHERE id = $1`, w.project)

	// Checked under 30 seconds ago: not looked at again yet.
	mustExec(t, w.owner, `UPDATE runs SET wake_timeouts_checked_at = now() - interval '10 seconds' WHERE id = $1`, runID)
	calls := len(w.lux.TenantCalls())
	for range 5 {
		w.pump()
	}
	if got := w.wakeTimeoutOf(web); got != 5*time.Minute {
		t.Fatalf("rechecked within 30 seconds: timeout %v; lux saw %v", got, w.lux.TenantCalls()[calls:])
	}

	// Over 30 seconds ago: the asleep preview takes the new setting.
	mustExec(t, w.owner, `UPDATE runs SET wake_timeouts_checked_at = now() - interval '31 seconds' WHERE id = $1`, runID)
	w.until("changed later timeout while asleep", func() bool { return w.wakeTimeoutOf(web) == 7*time.Minute })
	if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wake_timeouts_checked_at > now() - interval '30 seconds'`, runID) != 1 {
		t.Fatal("the preview woke, or the check was not recorded")
	}

	_, next := w.declare()
	if got := w.wakeTimeoutOf(w.serverID(next, "web")); got != 25*time.Minute {
		t.Fatalf("new first timeout = %v", got)
	}
}

// A server recorded before dude knew its wake timeout (NULL) is set at the
// next sweep, whatever lux has.
func TestAServerWithNoRecordedWakeTimeoutIsSet(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	old := "5m0s"
	if _, err := w.previews.Lux.PatchServer(context.Background(), web, lux.PatchServer{WakeTimeout: &old}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.owner, `UPDATE preview_servers SET wake_timeout_s = NULL WHERE run_id = $1`, runID)
	mustExec(t, w.owner, `UPDATE runs SET wake_timeouts_checked_at = NULL WHERE id = $1`, runID)
	w.until("the first wake timeout set", func() bool { return w.wakeTimeoutOf(web) == 15*time.Minute })
	if w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND wake_timeout_s = 900`, runID) != 1 {
		t.Fatal("the timeout set was not recorded")
	}
}

// A wake makes a server's wake timeout what the settings say before lux
// starts it, as it does its recipe, when lux's differs from what dude
// recorded.
func TestAWakePatchesTheWakeTimeoutBeforeTheStart(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	other := "10m0s"
	if _, err := w.previews.Lux.PatchServer(context.Background(), web, lux.PatchServer{WakeTimeout: &other}); err != nil {
		t.Fatal(err)
	}
	from := len(w.lux.TenantCalls())
	w.open(web)
	// Later PATCHes (the later timeout, once it served) may follow the start.
	calls := w.tenantCallsOf(web, from)
	patch, start := slices.Index(calls, "patch "+web+" wakeTimeout"), slices.Index(calls, "start "+web)
	if patch < 0 || start < 0 || patch > start {
		t.Fatalf("lux saw %v; want a PATCH of wakeTimeout before the start", calls)
	}
}
