package orchestrator_test

// A wakeable preview whose Run fails to start after lux took the wake (a
// resume lux accepted, a submit): the preview recovers on a new Run, and a
// Run that fails on every start is not submitted forever.

import (
	"slices"
	"strings"
	"testing"
)

// The resume is accepted (2xx) and the Run then fails before it runs, as a
// container lux cannot start: the same wake, with no further request, gets
// a new Run that serves, and the old one is cancelled. The new Run is then
// the preview's own: parked and woken again, it is resumed.
func TestAnAcceptedResumeThatFailsToStartGetsANewRun(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web := w.asleepPreview()
	old := w.luxRuns()[0]
	w.lux.FailStarts("dude.preview="+runID, 1)

	w.lux.RequestServer(web, "/") // one request; the waking page waits
	w.until("a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if old.Resumed != 1 || !slices.Contains(w.lux.CallsOf(old.ID), "cancel") {
		t.Errorf("old Run resumed %d times, calls %v; want one resume, then cancelled", old.Resumed, w.lux.CallsOf(old.ID))
	}
	fresh := w.luxRuns()[1]
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id = $2 AND error IS NULL`,
		runID, fresh.ID); n != 1 {
		t.Fatalf("dude does not hold the new Run, recovered:\n%s", w.describeRuns())
	}
	w.open(web)

	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.open(web)
	if n := len(w.luxRuns()); n != 2 || fresh.Resumed != 1 {
		t.Fatalf("%d lux runs, the new one resumed %d times; want it resumed", n, fresh.Resumed)
	}
}

// A Run that fails on every start (a broken image, a host that cannot run
// it): dude tries previewStartAttempts Runs for one request, then stops,
// says why, and shows the preview asleep rather than scheduling. A person
// asking again (starting a server) gets one more Run, not another loop.
func TestAPreviewWhoseRunNeverStartsIsTriedABoundedNumberOfTimes(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	web := w.serverID(runID, "web")
	w.lux.FailStarts("dude.preview="+runID, 100)

	w.lux.RequestServer(web, "/")
	view := func() map[string]any {
		_, out := w.do("GET", "/internal/tasks/"+task+"/servers", nil)
		run, _ := out["run"].(map[string]any)
		return run
	}
	w.until("dude to give up and say why", func() bool {
		msg, _ := view()["error"].(string)
		return strings.Contains(msg, "failed to start (start-failed) 3 times") &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NULL AND status = 'paused'`, runID) == 1
	})
	for range 10 {
		w.pump()
	}
	if n := len(w.luxRuns()); n != 3 {
		t.Fatalf("%d lux runs for one request; want 3", n)
	}
	for _, r := range w.luxRuns()[:2] {
		if !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
			t.Errorf("replaced Run %s not cancelled: %v", r.ID, w.lux.CallsOf(r.ID))
		}
	}
	if run := view(); run["asleep"] != true || run["previewStage"] != nil {
		t.Fatalf("view = %v; want asleep, no stage", run)
	}

	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 200 {
		t.Fatalf("start = %d %v", code, out)
	}
	w.until("the person's wake tried", func() bool {
		return len(w.luxRuns()) == 4 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NULL AND status = 'paused'`, runID) == 1
	})
	for range 10 {
		w.pump()
	}
	if n := len(w.luxRuns()); n != 4 {
		t.Fatalf("%d lux runs after a person asked once more; want 4", n)
	}
}

// A Run that ran and then crashed is resumed, not replaced: its snapshot
// is the preview's state. Only a start that never ran counts.
func TestARunThatRanThenFailedIsResumedNotReplaced(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]
	w.lux.Crash(r.ID)
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	w.open(web)
	if n := len(w.luxRuns()); n != 1 || r.Resumed != 1 {
		t.Fatalf("%d lux runs, resumed %d; want the crashed one resumed", n, r.Resumed)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND error IS NULL`, runID); n != 1 {
		t.Errorf("a recovered preview kept its error:\n%s", w.describeRuns())
	}
}
