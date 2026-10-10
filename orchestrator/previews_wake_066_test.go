package orchestrator_test

// Migration 066 applied to preview rows an upgrade can find: a failed or
// lost Run whose end was already applied, the same with its snapshot gone,
// a wake claimed by an orchestrator stopped for the upgrade, and a Run
// cancelled in lux.

import (
	"context"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lastEventID is the id of the Run's latest lifecycle event in lux.
func (w *world) lastEventID(id string) int64 {
	w.t.Helper()
	var last int64
	if err := w.previews.Lux.Output(context.Background(), id, "", 0, func(f lux.Frame) error {
		if f.Kind == "lux" {
			last = f.EventID
		}
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
	return last
}

// endedBeforeTheMigration leaves an asleep preview whose Run was resumed
// outside dude and ended as end says (failed, lost) before it ran, with
// every event applied by a release before 066 (its cursor at the Run's
// last event, lux_state the end); then 066 is applied. fails is how many
// starts of the preview's Runs fail or are lost, the first being that one.
func (w *world) endedBeforeTheMigration(end string, fails int) (runID, web string, r *fakelux.Run) {
	w.t.Helper()
	runID, web = w.asleepPreview()
	r = w.luxRuns()[0]
	if end == "lost" {
		w.lux.LoseStarts("dude.preview="+runID, fails)
	} else {
		w.lux.FailStarts("dude.preview="+runID, fails)
	}
	if _, err := w.previews.Lux.Resume(context.Background(), r.ID, lux.ResumeInput{
		Secrets: []lux.Secret{{Name: "GIT_TOKEN", Value: "fixture"}}}); err != nil {
		w.t.Fatal(err)
	}
	waitFor(w.t, "the resumed start ended", func() bool { return w.luxCalls(r.ID, "resume") == 1 && w.lux.State(r.ID) == end })
	mustExec(w.t, w.owner, `UPDATE runs SET lux_state = $2, lux_after_event = $3, status = 'paused' WHERE id = $1`,
		runID, end, w.lastEventID(r.ID))
	w.migrate066()
	return runID, web, r
}

// Taken as having run, a failed or lost Run is resumed once; that resume's
// own failure is counted, and the Run replaced by one that serves.
func TestAnEndAppliedBeforeTheMigrationIsResumedOnceThenCounted(t *testing.T) {
	for _, end := range []string{"failed", "lost"} {
		t.Run(end, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			runID, web, r := w.endedBeforeTheMigration(end, 2)
			w.lux.RequestServer(web, "/")
			w.untilPreview(runID, "a new Run running", func() bool {
				return len(w.luxRuns()) == 2 &&
					w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
			})
			if r.Resumed != 2 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
				t.Fatalf("old Run resumed %d, calls %v; want resumed once more by dude, then replaced", r.Resumed, w.lux.CallsOf(r.ID))
			}
			w.open(web)
		})
	}
}

// A migrated failed Run with its snapshot gone: lux refuses the resume
// (409 no_snapshot), which is not a start; the replacement's own failed
// start is counted, and the next replacement serves.
func TestAnEndAppliedBeforeTheMigrationWithoutASnapshotIsReplaced(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web, r := w.endedBeforeTheMigration("failed", 2)
	w.previews.Lux = &countingLux{Client: w.previews.Lux,
		refuseResume: &lux.Error{Status: 409, Code: "no_snapshot", Message: "run cannot be resumed: its snapshot is gone"}}
	w.lux.RequestServer(web, "/")
	w.untilPreview(runID, "a third Run running", func() bool {
		return len(w.luxRuns()) == 3 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	failed := w.luxRuns()[1]
	if r.Resumed != 1 || failed.Resumed != 0 || !slices.Contains(w.lux.CallsOf(failed.ID), "cancel") {
		t.Fatalf("refused Run resumed %d; failed replacement resumed %d, calls %v; want it replaced",
			r.Resumed, failed.Resumed, w.lux.CallsOf(failed.ID))
	}
	w.open(web)
}

// A wake claimed when 066 is applied, by an orchestrator stopped for the
// upgrade: left to it until its claim is past wakeClaimFor, then taken
// over; the stopped Run is resumed, and serves.
func TestAWakeClaimedAcrossTheMigrationIsTakenOver(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web := w.asleepPreview()
	r := w.luxRuns()[0]
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now(), wake_claimed_at = now() - interval '1 minute' WHERE id = $1`, runID)
	w.migrate066()
	for range 3 {
		w.pump()
	}
	if r.Resumed != 0 || len(w.luxRuns()) != 1 {
		t.Fatalf("a wake another orchestrator holds was acted on: resumed %d\n%s", r.Resumed, w.preview(runID))
	}
	mustExec(t, w.owner, `UPDATE runs SET wake_claimed_at = now() - interval '3 minutes' WHERE id = $1`, runID)
	w.untilPreview(runID, "running again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 1 || len(w.luxRuns()) != 1 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("resumed %d, %d lux runs, calls %v; want the stopped Run resumed", r.Resumed, len(w.luxRuns()), w.lux.CallsOf(r.ID))
	}
	w.open(web)
}

// A Run cancelled in lux, applied before 066: it never runs again, so a
// wake submits a new one, which serves; nothing is counted.
func TestARunCancelledBeforeTheMigrationIsReplaced(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web := w.asleepPreview()
	r := w.luxRuns()[0]
	if err := w.previews.Lux.Cancel(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'cancelled', lux_after_event = $2 WHERE id = $1`, runID, w.lastEventID(r.ID))
	w.migrate066()
	w.open(web)
	// lux serves the new Run before dude has applied its running: what
	// dude recorded is waited for, not assumed.
	w.untilPreview(runID, "the new Run recorded running", func() bool {
		runs := w.luxRuns()
		return len(runs) == 2 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id = $2 AND status = 'running'
			AND lux_state = 'running'`, runID, runs[1].ID) == 1
	})
	if n := len(w.luxRuns()); n != 2 || r.Resumed != 0 ||
		w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id = $2 AND status = 'running' AND start_failures = 0`,
			runID, w.luxRuns()[1].ID) != 1 {
		t.Fatalf("%d lux runs, cancelled one resumed %d; want a new one serving\n%s", n, r.Resumed, w.preview(runID))
	}
}
