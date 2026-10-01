package orchestrator_test

// When a wakeable preview's Run is stopped: every server idle by lux's
// account, or never ready and unused for its idleAfter; a stop lux did not
// take is asked again.

import (
	"context"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// running wakes a declared preview and waits for dude to see it running.
func (w *world) running(runID, server string) {
	w.t.Helper()
	w.lux.RequestServer(w.serverID(runID, server), "/")
	w.until("running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
}

// A stop lux refused for now (a 503) on park is asked again: the Run does
// not keep running behind an asleep preview.
func TestAFailedStopOnParkIsAskedAgain(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	calls := &countingLux{Client: w.previews.Lux}
	w.previews.Lux = calls
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]
	calls.mu.Lock()
	calls.failStop = &lux.Error{Status: 503, Code: "unavailable", Message: "lux is restarting"}
	calls.mu.Unlock()
	w.lux.Idle(web)
	w.until("the Run stopped in lux", func() bool {
		mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
		return r.State == "stopped"
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID); n != 1 {
		t.Errorf("dude's row:\n%s", w.describeRuns())
	}
}

// A server lux never reports idle (its command never opens its port) does
// not keep the Run up for good: unused for its idleAfter, it counts as
// idle, and the preview is parked.
func TestAServerThatNeverBecomesReadyIsParked(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":0.005}' WHERE id = $1`, w.project)
	w.recipe("web", 3000, "fakelux-never-ready", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.lux.RequestServer(web, "/")
	w.until("dude to see it running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if sv, _ := w.lux.TenantServer(web); sv.State == lux.SrvReady {
		t.Fatal("the never-ready server is ready")
	}
	r := w.luxRuns()[0]
	w.until("the preview parked", func() bool {
		// Past parkSweepEvery since the last check.
		mustExec(t, w.owner, `UPDATE runs SET park_checked_at = now() - interval '1 hour' WHERE id = $1 AND park_checked_at IS NOT NULL`, runID)
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	w.until("its Run stopped", func() bool { return r.State == "stopped" })
}

// A server whose process exited counts as idle: with the other idle, the
// preview is parked. Each check asks lux for the preview's servers once,
// by label, not once per server.
func TestAnExitedServerCountsAsIdle(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("api", 4000, "go run . fakelux-exit=3", "api", nil, true)
	_, runID := w.declare()
	calls := &countingLux{Client: w.previews.Lux}
	w.previews.Lux = calls
	web, api := w.serverID(runID, "web"), w.serverID(runID, "api")
	w.open(web)
	w.until("api exited", func() bool { sv, _ := w.lux.TenantServer(api); return sv.State == lux.SrvExited })
	w.until("dude to see it running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
	if !w.lux.Idle(web) {
		t.Fatal("web not idle-able")
	}
	w.until("the preview parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.serverGets != 0 || calls.serverLists == 0 {
		t.Errorf("park checks asked %d server GETs and %d lists; want lists only", calls.serverGets, calls.serverLists)
	}
}

// A server that went idle before anyone requested it (lastRequestAt null
// in the event) and is then requested keeps the Run.
func TestARequestAfterAnIdleWithNoRequestKeepsTheRun(t *testing.T) {
	w := newWorld(t)
	feed := w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	// Woken from dude, so lux has no lastRequestAt for it.
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 200 {
		t.Fatalf("start = %d %v", code, out)
	}
	web := w.serverID(runID, "web")
	w.until("running", func() bool {
		sv, _ := w.lux.TenantServer(web)
		return sv.State == lux.SrvReady && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
	if sv, _ := w.lux.TenantServer(web); sv.LastRequestAt != nil {
		t.Fatalf("lastRequestAt %v before any request", sv.LastRequestAt)
	}
	r := w.luxRuns()[0]
	if err := feed.Apply(context.Background(), lux.FeedEvent{ID: 1 << 40, Type: "server.idle", ServerID: web,
		Data: map[string]any{"lastRequestAt": nil}}); err != nil {
		t.Fatal(err)
	}
	w.lux.RequestServer(web, "/")
	for range 3 {
		w.pump()
	}
	if slices.Contains(w.lux.CallsOf(r.ID), "stop") {
		t.Fatal("stopped although a request came after an idle that carried none")
	}
}
