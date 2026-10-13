package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func (w *world) wakeTimeoutOf(id string) time.Duration {
	sv, _ := w.lux.TenantServer(id)
	return sv.WakeTimeoutDuration()
}

// setWakeTimeout changes a server's wake timeout in lux behind dude's back.
func (w *world) setWakeTimeout(id, timeout string) {
	w.t.Helper()
	if _, err := w.previews.Lux.PatchServer(context.Background(), id, lux.PatchServer{WakeTimeout: &timeout}); err != nil {
		w.t.Fatal(err)
	}
}

// patchesOf is the PATCHes lux saw to one server, from call index from on.
func (w *world) patchesOf(id string, from int) []string {
	return slices.DeleteFunc(w.tenantCallsOf(id, from), func(c string) bool { return !strings.HasPrefix(c, "patch ") })
}

type timeoutLists struct {
	lux.Client
	lists atomic.Int64
}

func (c *timeoutLists) ListServers(ctx context.Context, hostname string, labels ...string) ([]lux.TenantServer, error) {
	c.lists.Add(1)
	return c.Client.ListServers(ctx, hostname, labels...)
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
	if patches := w.patchesOf(web, calls); len(patches) > 0 || w.wakeTimeoutOf(web) != 5*time.Minute {
		t.Fatalf("rechecked within 30 seconds: timeout %v; lux saw %v", w.wakeTimeoutOf(web), patches)
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
	w.setWakeTimeout(web, "5m0s")
	mustExec(t, w.owner, `UPDATE preview_servers SET wake_timeout_s = NULL WHERE run_id = $1`, runID)
	mustExec(t, w.owner, `UPDATE runs SET wake_timeouts_checked_at = NULL WHERE id = $1`, runID)
	w.until("the first wake timeout set", func() bool { return w.wakeTimeoutOf(web) == 15*time.Minute })
	if w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND wake_timeout_s = 900`, runID) != 1 {
		t.Fatal("the timeout set was not recorded")
	}
}

// One server off its wake timeout is patched although another of the same
// preview is on it; the other is left alone.
func TestOnlyTheServerOffItsWakeTimeoutIsPatched(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("api", 3001, "npm run api", "", nil, true)
	_, runID := w.declare()
	web, api := w.serverID(runID, "web"), w.serverID(runID, "api")
	w.setWakeTimeout(api, "10m0s")
	mustExec(t, w.owner, `UPDATE preview_servers SET wake_timeout_s = 600 WHERE lux_server_id = $1`, api)
	mustExec(t, w.owner, `UPDATE runs SET wake_timeouts_checked_at = NULL WHERE id = $1`, runID)
	from := len(w.lux.TenantCalls())
	w.until("api's first wake timeout set", func() bool { return w.wakeTimeoutOf(api) == 15*time.Minute })
	if patches := w.patchesOf(web, from); len(patches) > 0 || w.wakeTimeoutOf(web) != 15*time.Minute {
		t.Fatalf("web: timeout %v; lux saw %v", w.wakeTimeoutOf(web), patches)
	}
	if w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND wake_timeout_s = 900`, runID) != 2 {
		t.Fatal("api's new timeout was not recorded")
	}
}

// A server marked gone keeps the wake timeout dude last recorded for it;
// lux no longer lists it, so the sweep could never set it, and it does not
// make the preview's timeouts due.
func TestADeletedServersWakeTimeoutDoesNotKeepThePreviewDue(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("api", 3001, "npm run api", "", nil, true)
	_, runID := w.declare()
	api := w.serverID(runID, "api")
	// Marked first, as attachAll and endInLux do, so the feed's
	// server.deleted finds it gone already and leaves the preview live.
	mustExec(t, w.owner, `UPDATE preview_servers SET deleted_at = now(), wake_timeout_s = 600 WHERE lux_server_id = $1`, api)
	if err := w.previews.Lux.DeleteServer(context.Background(), api); err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.owner, `UPDATE runs SET wake_timeouts_checked_at = NULL WHERE id = $1`, runID)
	client := &timeoutLists{Client: w.previews.Lux}
	w.previews.Lux = client
	from := len(w.lux.TenantCalls())
	for range 5 {
		w.pump()
	}
	if n := client.lists.Load(); n != 0 {
		t.Fatalf("the deleted server caused %d ListServers calls", n)
	}
	if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wake_timeouts_checked_at IS NULL`, runID) != 1 {
		t.Fatal("the deleted server made the preview's wake timeouts due, or the preview ended")
	}
	if calls := w.lux.TenantCalls()[from:]; len(calls) > 0 {
		t.Fatalf("lux saw %v", calls)
	}
	if w.count(`SELECT count(*) FROM preview_servers WHERE lux_server_id = $1 AND deleted_at IS NOT NULL AND wake_timeout_s = 600`, api) != 1 {
		t.Fatal("the deleted server's row changed")
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
	w.setWakeTimeout(web, "10m0s")
	from := len(w.lux.TenantCalls())
	w.open(web)
	// Later PATCHes (the later timeout, once it served) may follow the start.
	calls := w.tenantCallsOf(web, from)
	patch, start := slices.Index(calls, "patch "+web+" wakeTimeout"), slices.Index(calls, "start "+web)
	if patch < 0 || start < 0 || patch > start {
		t.Fatalf("lux saw %v; want a PATCH of wakeTimeout before the start", calls)
	}
}
