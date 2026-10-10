package orchestrator_test

// A wakeable preview across migration 066 (rows whose earlier events were
// applied without the start markers), a wake's acknowledgement racing an
// earlier start's end that reaches dude after the wake was claimed, and a
// event drain whose page stops short of the Run's last event.

import (
	"context"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// migrate066 applies migration 066 to the world's database as an upgrade
// finds it: its columns are dropped (the schema before it, with every row
// as it is), then the migration file runs as the migrator runs it.
func (w *world) migrate066() {
	w.t.Helper()
	sql, err := os.ReadFile("../migrations/066_preview_start_failures.sql")
	if err != nil {
		w.t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := w.owner.Begin(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `ALTER TABLE runs DROP COLUMN start_failures, DROP COLUMN lux_start_event, DROP COLUMN lux_ran_event`); err != nil {
		w.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		w.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		w.t.Fatal(err)
	}
}

// stopsAfterResume is how many stops lux was asked for since the Run's
// latest resume.
func stopsAfterResume(calls []string) int {
	n := 0
	for _, c := range calls {
		switch c {
		case "resume":
			n = 0
		case "stop":
			n++
		}
	}
	return n
}

// holdEndAndResume holds one follower before end and all followers before
// resuming. The wake's Events requests remain unblocked.
func (w *world) holdEndAndResume(runID, end string) (endHeld, endRelease, resumingRelease chan struct{}) {
	endHeld, endRelease = make(chan struct{}), make(chan struct{})
	resumingRelease = make(chan struct{})
	var once sync.Once
	w.lux.BeforeEvent(func(id string, eventID int64, typ string) {
		if id != runID || typ != "state" {
			return
		}
		switch w.lux.EventState(id, eventID) {
		case end:
			mine := false
			once.Do(func() { mine = true; close(endHeld) })
			if mine {
				<-endRelease
			}
		case "resuming":
			<-resumingRelease
		}
	})
	w.t.Cleanup(func() {
		w.lux.BeforeEvent(nil)
		for _, ch := range []chan struct{}{endRelease, resumingRelease} {
			if !isClosed(ch) {
				close(ch)
			}
		}
	})
	return endHeld, endRelease, resumingRelease
}

// A preview running when 066 is applied, which then crashes, is resumed
// from its snapshot: its crash is not a failed start.
func TestAPreviewRunningAcrossTheMigrationIsResumedAfterACrash(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]
	w.migrate066()

	w.lux.Crash(r.ID)
	w.untilPreview(runID, "asleep after the crash", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'failed'`, runID) == 1
	})
	w.open(web)
	if n := len(w.luxRuns()); n != 1 || r.Resumed != 1 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("migrated running preview lost its snapshot: %d runs, original resumed %d, calls %v\n%s",
			n, r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 0`, runID); n != 1 {
		t.Errorf("the crash was counted as a failed start:\n%s", w.preview(runID))
	}
}

// A preview asleep (stopped) when 066 is applied, whose Run lux then loses,
// is resumed from its snapshot.
func TestAStoppedPreviewLostAfterTheMigrationIsResumed(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web := w.asleepPreview()
	r := w.luxRuns()[0]
	w.migrate066()

	// Nothing follows an asleep preview's stopped Run: the wake's drain
	// applies the loss.
	w.lux.Lose(r.ID)
	w.open(web)
	if n := len(w.luxRuns()); n != 1 || r.Resumed != 1 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("migrated stopped preview lost its snapshot: %d runs, original resumed %d, calls %v\n%s",
			n, r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 0`, runID); n != 1 {
		t.Errorf("the loss was counted as a failed start:\n%s", w.preview(runID))
	}
}

// 066's policy for a Run caught mid-start (its resuming applied before the
// migration), whose start then fails: taken as having run, it is resumed
// once more; that resume's own failure is counted, and the Run replaced.
func TestARunMidStartAcrossTheMigrationIsResumedOnceMoreThenCounted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web := w.asleepPreview()
	r := w.luxRuns()[0]
	w.lux.FailStarts("dude.preview="+runID, 2)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.lux.BeforeStart(func(id string) {
		mine := false
		once.Do(func() { mine = true })
		if mine {
			close(held)
			<-release
		}
	})
	t.Cleanup(func() {
		w.lux.BeforeStart(nil)
		if !isClosed(release) {
			close(release)
		}
	})
	w.lux.RequestServer(web, "/")
	w.untilPreview(runID, "the resumed start held", func() bool { return isClosed(held) })
	w.untilPreview(runID, "resuming applied", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'resuming' AND wake_wanted_at IS NULL`, runID) == 1
	})
	w.migrate066()
	close(release)

	w.untilPreview(runID, "the start's failure applied", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'failed'`, runID) == 1
	})
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 200 {
		t.Fatalf("start = %d %v", code, out)
	}
	w.untilPreview(runID, "a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 2 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("old Run resumed %d, calls %v; want resumed once more, then replaced", r.Resumed, w.lux.CallsOf(r.ID))
	}
}

// The parked Run's own stopped reaches dude only after the wake was
// claimed, while lux's answer to the resume is on its way; the resume's
// events come after its acknowledgement. The old stopped is not newer than
// the resume: the preview serves, and lux is asked for no stop.
func TestAnOldStoppedAppliedAfterTheClaimDoesNotUndoTheResume(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	a := newGateLux(w.previews.Lux, w.lux)
	w.previews.Lux = a
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]

	stoppedHeld, stoppedRelease, resumingRelease := w.holdEndAndResume(r.ID, "stopped")
	w.lux.Idle(web)
	w.untilPreview(runID, "the follower held before the old stopped", func() bool { return isClosed(stoppedHeld) })
	w.untilPreview(runID, "parked, lux stopped", func() bool {
		return w.lux.State(r.ID) == "stopped" &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopping'`, runID) == 1
	})
	a.afterResume = func(string) {
		close(stoppedRelease)
		waitFor(t, "the old stopped applied", func() bool {
			return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'stopped'`, runID) == 1
		})
	}
	w.lux.RequestServer(web, "/")
	w.heard(runID)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Resumed != 1 || !isClosed(stoppedRelease) {
		t.Fatalf("the sweep did not resume through the gate: resumed %d\n%s", r.Resumed, w.preview(runID))
	}
	close(resumingRelease)
	w.untilPreview(runID, "running again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	w.open(web)
	for range 5 {
		w.pump()
	}
	if n := stopsAfterResume(w.lux.CallsOf(r.ID)); n != 0 || w.lux.State(r.ID) != "running" || !w.lux.RequestServer(web, "/") {
		t.Fatalf("the resumed Run was stopped: lux %s, calls %v\n%s", w.lux.State(r.ID), w.lux.CallsOf(r.ID), w.preview(runID))
	}
}

// The Run crashed after it ran, and its follower has not applied the crash
// when the wake is claimed; the wake's drain applies it, the Run is
// resumed, and the resume's events come after its acknowledgement. The old
// crash is not newer than the resume: the preview serves, with no stop.
func TestAnOldCrashDrainedAfterTheClaimDoesNotUndoTheResume(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]

	failedHeld, failedRelease, resumingRelease := w.holdEndAndResume(r.ID, "failed")
	w.lux.Crash(r.ID)
	wait(t, failedHeld, "the follower held before the crash")
	w.lux.RequestServer(web, "/")
	w.heard(runID)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID); n != 1 {
		t.Fatalf("the crash was applied before the claim:\n%s", w.preview(runID))
	}
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Resumed != 1 {
		t.Fatalf("the wake did not resume the crashed Run: resumed %d\n%s", r.Resumed, w.preview(runID))
	}
	close(failedRelease)
	close(resumingRelease)
	w.untilPreview(runID, "running again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	w.open(web)
	for range 5 {
		w.pump()
	}
	if n := stopsAfterResume(w.lux.CallsOf(r.ID)); n != 0 || w.lux.State(r.ID) != "running" || !w.lux.RequestServer(web, "/") {
		t.Fatalf("the resumed Run was stopped: lux %s, calls %v\n%s", w.lux.State(r.ID), w.lux.CallsOf(r.ID), w.preview(runID))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 0`, runID); n != 1 {
		t.Errorf("a crash after running was counted:\n%s", w.preview(runID))
	}
}

// failedStartUnapplied leaves a preview whose resumed start failed in lux,
// with its follower held before that failure: dude has applied the start's
// resuming and not its end. A new wake is then wanted, as lux's next
// server.wake_requested asks for one.
func (w *world) failedStartUnapplied() (runID string, r *fakelux.Run) {
	w.t.Helper()
	runID, web := w.asleepPreview()
	r = w.luxRuns()[0]
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.lux.BeforeEvent(func(id string, eventID int64, typ string) {
		if id != r.ID || typ != "state" || w.lux.EventState(id, eventID) != "failed" {
			return
		}
		mine := false
		once.Do(func() { mine = true; close(held) })
		if mine {
			<-release
		}
	})
	w.t.Cleanup(func() {
		w.lux.BeforeEvent(nil)
		w.lux.PageEvents(nil)
		if !isClosed(release) {
			close(release)
		}
	})
	w.lux.FailStarts("dude.preview="+runID, 1)
	w.lux.RequestServer(web, "/")
	w.untilPreview(runID, "the follower held before the failure", func() bool {
		return w.luxCalls(r.ID, "resume") == 1 && isClosed(held)
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'resuming' AND start_failures = 0`, runID); n != 1 {
		w.t.Fatalf("not resuming in dude:\n%s", w.preview(runID))
	}
	if _, err := w.owner.Exec(context.Background(), `UPDATE runs SET wake_wanted_at = now() WHERE id = $1`, runID); err != nil {
		w.t.Fatal(err)
	}
	return runID, r
}

// pageEventsAt has the first page of GET events whose events include a
// state event of this state answered as how says; every later page is
// whole. pageShort carries only the events before it: none if it is the
// page's first, lux having recorded it after the page's query. pageFails
// fails the request (503). cut is closed once that page is answered.
func (w *world) pageEventsAt(state string, how pageCut) (cut <-chan struct{}) {
	var once sync.Once
	done := make(chan struct{})
	w.lux.PageEvents(func(id string, _ int64, ids []int64) int {
		for i, eventID := range ids {
			if w.lux.EventState(id, eventID) != state {
				continue
			}
			n := len(ids)
			once.Do(func() {
				if n = i; how == pageFails {
					n = -1
				}
				close(done)
			})
			return n
		}
		return len(ids)
	})
	return done
}

type pageCut int

const (
	pageShort pageCut = iota
	pageFails
)

// An event page ending before the Run's failure is not its full history.
// The drain keeps paging until an empty page and counts the failed start
// before deciding.
func TestADrainEndedShortOfTheFailureDrainsAgain(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, r := w.failedStartUnapplied()
	cut := w.pageEventsAt("failed", pageShort)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !isClosed(cut) {
		t.Fatal("the drain read no page short of the failure")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1 AND lux_state = 'failed' AND lux_run_id = $2`,
		runID, r.ID); n != 1 || len(w.luxRuns()) != 1 {
		t.Fatalf("the failed start was not counted before the wake decided: %d lux runs\n%s", len(w.luxRuns()), w.preview(runID))
	}
}

// A drain whose page request fails at the Run's failure is an error: the
// wake stays wanted and unclaimed, nothing is replaced, and the next try
// counts the failed start.
func TestADrainCutWithoutItsEndIsTriedAgain(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, r := w.failedStartUnapplied()
	cut := w.pageEventsAt("failed", pageFails)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !isClosed(cut) {
		t.Fatal("the drain asked for no page at the failure")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 0 AND wake_wanted_at IS NOT NULL
		AND wake_claimed_at IS NULL AND lux_run_id = $2`, runID, r.ID); n != 1 || len(w.luxRuns()) != 1 {
		t.Fatalf("a cut drain was decided on: %d lux runs\n%s", len(w.luxRuns()), w.preview(runID))
	}
	w.sweepAgain(runID)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1`, runID); n != 1 {
		t.Fatalf("the next try did not count the failed start:\n%s", w.preview(runID))
	}
}

// eventsDown is lux with its events endpoint unavailable while down is set.
type eventsDown struct {
	lux.Client
	down    atomic.Bool
	refused atomic.Int32
}

func (e *eventsDown) Events(ctx context.Context, runID string, after int64) ([]lux.Frame, error) {
	if e.down.Load() {
		e.refused.Add(1)
		return nil, &lux.Error{Status: 503, Code: "unavailable", Message: "events unavailable"}
	}
	return e.Client.Events(ctx, runID, after)
}

// sweepAgain makes the preview's backed-off wake due and sweeps once.
func (w *world) sweepAgain(runID string) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// The wake waits on a drain that cannot finish: the page request reaching
// the ended Run's failure is not answered within the drain's bound, or lux's
// events endpoint is unavailable. The wake stays wanted and unclaimed, with
// nothing replaced or resumed and nothing counted; once lux answers, the
// next try counts the failed start and replaces the Run.
func TestADrainThatCannotFinishLeavesTheWakeForLater(t *testing.T) {
	recovers := func(t *testing.T, w *world, runID string, r *fakelux.Run) {
		t.Helper()
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 0 AND wake_wanted_at IS NOT NULL
			AND wake_claimed_at IS NULL AND next_attempt_at > now() AND lux_run_id = $2`, runID, r.ID); n != 1 ||
			len(w.luxRuns()) != 1 || r.Resumed != 1 {
			t.Fatalf("a wake whose drain did not finish was decided on: %d lux runs, resumed %d\n%s",
				len(w.luxRuns()), r.Resumed, w.preview(runID))
		}
		w.sweepAgain(runID)
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1`, runID); n != 1 {
			t.Fatalf("the next try did not count the failed start:\n%s", w.preview(runID))
		}
		w.untilPreview(runID, "a new Run running", func() bool {
			return len(w.luxRuns()) == 2 &&
				w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
		})
		if !slices.Contains(w.lux.CallsOf(r.ID), "cancel") || r.Resumed != 1 {
			t.Fatalf("the failed Run: resumed %d, calls %v; want replaced", r.Resumed, w.lux.CallsOf(r.ID))
		}
	}
	t.Run("page request stays open", func(t *testing.T) {
		w := newWorld(t)
		w.wakeable()
		runID, r := w.failedStartUnapplied()
		w.previews.DrainFor = 200 * time.Millisecond
		open := make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(open) }) }
		t.Cleanup(release)
		var held atomic.Bool
		w.lux.PageEvents(func(id string, _ int64, ids []int64) int {
			for _, eventID := range ids {
				if w.lux.EventState(id, eventID) == "failed" {
					held.Store(true)
					<-open
				}
			}
			return len(ids)
		})
		if _, err := w.previews.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.lux.PageEvents(nil)
		release()
		if !held.Load() {
			t.Fatal("the drain asked for no page at the failure")
		}
		recovers(t, w, runID, r)
	})
	t.Run("events unavailable", func(t *testing.T) {
		w := newWorld(t)
		w.wakeable()
		runID, r := w.failedStartUnapplied()
		down := &eventsDown{Client: w.previews.Lux}
		down.down.Store(true)
		w.previews.Lux = down
		if _, err := w.previews.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if down.refused.Load() == 0 {
			t.Fatal("the drain asked lux for no events")
		}
		down.down.Store(false)
		recovers(t, w, runID, r)
	})
}
