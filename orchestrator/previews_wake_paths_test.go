package orchestrator_test

// How a wake is acted on when lux's Run is not simply stopped: claimed
// by another orchestrator, lost, refused, stopping, crashed; and what an
// attach's 404 means.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// asleepPreview declares a preview of one server, wakes it, and lets it go idle:
// its Run is stopped and it is asleep. Returns the run and server ids.
func (w *world) asleepPreview() (runID, web string) {
	w.t.Helper()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID = w.declare()
	web = w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	return runID, web
}

// A wake another orchestrator claimed is left to it; once its claim is
// past wakeClaimFor (it died), this one takes the wake over.
func TestAnAbandonedWakeClaimIsTakenOver(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now(), wake_claimed_at = now() - interval '1 minute' WHERE id = $1`, runID)
	for range 3 {
		w.pump()
	}
	if n := len(w.luxRuns()); n != 0 {
		t.Fatal("a wake another orchestrator holds was acted on")
	}
	mustExec(t, w.owner, `UPDATE runs SET wake_claimed_at = now() - interval '3 minutes' WHERE id = $1`, runID)
	w.until("the abandoned wake taken over", func() bool { return len(w.luxRuns()) == 1 })
}

// lux lost the preview's Run (a restore): the next wake submits a new
// one and the preview serves.
func TestAWakeOfALostRunSubmitsANewOne(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	_, web := w.asleepPreview()
	w.lux.Forget()
	w.open(web)
	// Runs lists lux's runs by id, nil for those it forgot.
	if runs := w.luxRuns(); len(runs) != 2 || runs[0] != nil || runs[1] == nil {
		t.Fatalf("lux runs %v after lux forgot the old one; want one new one", runs)
	}
}

// A resume lux refuses (422 secrets_required, 409 no_snapshot, 409
// not_resumable): a new Run is submitted and the preview serves; it does
// not sit waking.
func TestARefusedResumeSubmitsANewRun(t *testing.T) {
	for name, refusal := range map[string]*lux.Error{
		"422 secrets_required": {Status: 422, Code: "secrets_required", Message: "secret values required: GIT_TOKEN"},
		"409 no_snapshot":      {Status: 409, Code: "no_snapshot", Message: "run cannot be resumed: its snapshot is gone"},
		"409 not_resumable":    {Status: 409, Code: "not_resumable", Message: "run is stopped: stop it first"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			runID, web := w.asleepPreview()
			calls := &countingLux{Client: w.previews.Lux, refuseResume: refusal}
			w.previews.Lux = calls
			old := w.luxRuns()[0]
			w.open(web)
			if n := len(w.luxRuns()); n != 2 {
				t.Fatalf("%d lux runs; want a second after the refusal", n)
			}
			if old.Resumed != 0 {
				t.Errorf("the refused Run was resumed %d times", old.Resumed)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id = $2`, runID, w.luxRuns()[1].ID); n != 1 {
				t.Errorf("dude still holds the old Run:\n%s", w.describeRuns())
			}
		})
	}
}

// A request while the idle stop is under way (lux: stopping) is resumed
// once the Run has stopped: one resume, and it serves.
func TestAWakeDuringTheIdleStopResumesOnceStopped(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	_, web := w.asleepPreview()
	calls := &countingLux{Client: w.previews.Lux, getAs: func(r *lux.Run) { r.State = "stopping" }}
	w.previews.Lux = calls
	r := w.luxRuns()[0]
	w.open(web)
	if _, resumes := calls.counts(); resumes != 1 || r.Resumed != 1 || len(w.luxRuns()) != 1 {
		t.Fatalf("%d resumes asked, %d taken, %d runs; want one resume of the one Run", resumes, r.Resumed, len(w.luxRuns()))
	}
}

// A Run that crashes while serving puts its preview to sleep, not to
// failed: the next request wakes it.
func TestACrashedPreviewRunSleepsAndWakes(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	w.lux.Crash(w.luxRuns()[0].ID)
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	w.open(web)
}

// attachNotFound answers the next attach with lux's 404 for a Run it does
// not have ("not found", no ids in it).
type attachNotFound struct {
	lux.Client
	mu   sync.Mutex
	once bool
}

func (a *attachNotFound) AttachServer(ctx context.Context, id, runID string) (lux.TenantServer, error) {
	a.mu.Lock()
	fail := !a.once
	a.once = true
	a.mu.Unlock()
	if fail {
		return lux.TenantServer{}, &lux.Error{Status: 404, Code: "not_found", Message: "not found"}
	}
	return a.Client.AttachServer(ctx, id, runID)
}

// An attach answered 404 for the Run is not the server gone: the server
// stays dude's, and the wake goes on.
func TestAnAttach404ForTheRunKeepsTheServer(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.previews.Lux = &attachNotFound{Client: w.previews.Lux}
	w.open(web)
	if n := w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND deleted_at IS NULL`, runID); n != 1 {
		t.Fatal("dude marked its live server deleted on a Run's 404")
	}
}

// A wake from dude (Start) counts for the reaper as a URL's does.
func TestAWakeFromDudeResetsTheReaperClock(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 200 {
		t.Fatalf("start = %d %v", code, out)
	}
	w.until("woken", func() bool {
		return w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND last_woken_at > now() - interval '1 minute'`, runID) == 1
	})
}

// A push while the preview is on its way up is kept until it runs, then
// synced: a resume's sync may have been of the older commit.
func TestAPushWhileWakingIsSyncedOnceRunning(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	_, web := w.asleepPreview()
	w.lux.StartAfter = 2 * time.Second // a window to push in while it wakes
	r := w.luxRuns()[0]
	runID := w.str(`SELECT id FROM runs WHERE kind = 'preview' AND organization_id = $1`, w.org)
	w.lux.RequestServer(web, "/")
	w.heard(runID)
	// Woken but not yet running; then the branch moves.
	w.until("waking", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('scheduled', 'starting')`, runID) == 1
	})
	mustExec(t, w.owner, `UPDATE runs SET sync_wanted_at = now() WHERE id = $1`, runID)
	w.until("the push synced once running", func() bool { return len(r.Syncs) == 1 })
}
