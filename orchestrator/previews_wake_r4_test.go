package orchestrator_test

// A wake's boundaries with what lux reports meanwhile: a crash applied
// after an attach to a running Run and before its acknowledgement, a drain
// whose pages end at a state the Run is in again later, and a sweep whose
// selected wake is replaced before it claims it.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// afterAttach is lux whose first successful AttachServer answer is held
// until then returns.
type afterAttach struct {
	lux.Client
	once sync.Once
	then func()
}

func (a *afterAttach) AttachServer(ctx context.Context, serverID, runID string) (lux.TenantServer, error) {
	sv, err := a.Client.AttachServer(ctx, serverID, runID)
	if err == nil {
		a.once.Do(a.then)
	}
	return sv, err
}

// A wake for a Run already running attaches its servers; the Run crashes
// and the follower applies the crash before the attach's answer reaches
// the acknowledgement. The crash stands: the preview is not shown serving,
// the wake stays wanted, and the next sweep resumes the Run (it ran), which
// then serves.
func TestACrashAppliedAfterAnAttachIsKept(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]
	w.previews.Lux = &afterAttach{Client: w.previews.Lux, then: func() {
		w.lux.Crash(r.ID)
		waitFor(t, "the follower to apply the crash", func() bool {
			return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'failed'`, runID) == 1
		})
	}}
	// A wake outstanding for a Run that is up: its acknowledgement was lost
	// (a restart), or lux asked while the Run was coming up.
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now() WHERE id = $1`, runID)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'failed'
		AND wake_wanted_at IS NOT NULL AND wake_claimed_at IS NULL`, runID); n != 1 {
		t.Fatalf("the attach's answer was written over the crash applied after it:\n%s", w.preview(runID))
	}
	w.sweepAgain(runID)
	if r.Resumed != 1 {
		t.Fatalf("the crashed Run was not resumed: resumed %d, calls %v\n%s", r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
	w.open(web)
	if n := len(w.luxRuns()); n != 1 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") ||
		w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND start_failures = 0`, runID) != 1 {
		t.Fatalf("%d lux runs, calls %v; want the Run that ran resumed\n%s", n, w.lux.CallsOf(r.ID), w.preview(runID))
	}
}

// slowReplay is lux whose output stream from the start of a Run's records
// takes replay before its first frame, as lux reading and decompressing
// every archived output blob of the Run does, while replaying is set.
type slowReplay struct {
	lux.Client
	replay    time.Duration
	replaying atomic.Bool
}

func (s *slowReplay) Output(ctx context.Context, runID, cursor string, afterEvent int64, fn func(lux.Frame) error) error {
	if s.replaying.Load() && cursor == "" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.replay):
		}
	}
	return s.Client.Output(ctx, runID, cursor, afterEvent, fn)
}

// A preview's Run ran and crashed, its crash applied; reading the Run's
// archived output takes longer than the drain's bound. The wake does not
// wait on that output: the next sweep resumes the Run once, and it serves.
func TestACrashedPreviewWakesWhileItsOutputIsSlowToReplay(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]
	w.lux.Crash(r.ID)
	waitFor(t, "the follower to apply the crash", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'failed'`, runID) == 1
	})
	slow := &slowReplay{Client: w.previews.Lux, replay: 400 * time.Millisecond}
	slow.replaying.Store(true)
	w.previews.Lux = slow
	w.previews.DrainFor = 200 * time.Millisecond
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now(), next_attempt_at = NULL WHERE id = $1`, runID)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Resumed != 1 {
		t.Fatalf("the crashed Run was not resumed while its output was slow: resumed %d, calls %v\n%s",
			r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
	w.open(web)
	if n := len(w.luxRuns()); n != 1 || r.Resumed != 1 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") ||
		w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND start_failures = 0`, runID) != 1 {
		t.Fatalf("%d lux runs, resumed %d, calls %v; want the Run resumed once\n%s", n, r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
}

// crashedThenFailedResumes leaves a preview's Run that ran and crashed,
// then was resumed n times outside this wake (an accepted resume whose
// acknowledgement was lost), each start failing before it ran. The
// preview's follower is held before the crash, so dude has applied none of
// it; a wake is then wanted.
func (w *world) crashedThenFailedResumes(n int) (runID, web string, r *fakelux.Run) {
	w.t.Helper()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID = w.declare()
	web = w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r = w.luxRuns()[0]
	held, _ := w.holdOneStream("failed")
	w.t.Cleanup(func() { w.lux.PageEvents(nil) })
	w.lux.Crash(r.ID)
	wait(w.t, held, "the follower held before the crash")
	w.lux.FailStarts("dude.preview="+runID, n)
	for i := 1; i <= n; i++ {
		if _, err := w.previews.Lux.Resume(context.Background(), r.ID, lux.ResumeInput{
			Secrets: []lux.Secret{{Name: "GIT_TOKEN", Value: "fixture"}}}); err != nil {
			w.t.Fatal(err)
		}
		waitFor(w.t, "the resumed start failed", func() bool { return r.Resumed == i && w.lux.State(r.ID) == "failed" })
	}
	mustExec(w.t, w.owner, `UPDATE runs SET wake_wanted_at = now(), next_attempt_at = NULL WHERE id = $1`, runID)
	return runID, web, r
}

// The drain's first page ends after the crash, before the later resume's
// events (lux recorded them after the page's query): lux's state (failed) is the state applied, but the later start's
// failure is unread. It is counted before the wake decides: the Run is not
// resumed again, it is cancelled and replaced, and the new Run serves.
func TestARepeatedStateAtAPageEndIsNotTheRunsHistory(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web, r := w.crashedThenFailedResumes(1)
	w.pageEventsAt("resuming", pageShort)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Resumed != 1 || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1`, runID) != 1 {
		t.Fatalf("decided before the later failed start was counted: resumed %d, calls %v\n%s",
			r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
	w.untilPreview(runID, "a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 1 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("the failed Run: resumed %d, calls %v; want replaced", r.Resumed, w.lux.CallsOf(r.ID))
	}
	w.open(web)
}

// Every page of the drain carries one event, so no page finds lux with
// nothing past dude's cursor: the wake decides nothing (no resume, no
// cancel, no new Run) and is released, backed off. Once lux's pages are
// whole, the failed starts read are the count, and the Run is replaced.
func TestADrainThatNeverReachesAnEmptyPageDecidesNothing(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web, r := w.crashedThenFailedResumes(2)
	// Every page carries one event: the next page starts after it.
	var pages atomic.Int32
	w.lux.PageEvents(func(id string, _ int64, ids []int64) int {
		if id != r.ID {
			return len(ids)
		}
		pages.Add(1)
		return min(len(ids), 1)
	})
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Resumed != 2 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") || len(w.luxRuns()) != 1 || pages.Load() < 2 {
		t.Fatalf("decided on a partial history: resumed %d, calls %v, %d lux runs, %d pages\n%s",
			r.Resumed, w.lux.CallsOf(r.ID), len(w.luxRuns()), pages.Load(), w.preview(runID))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NOT NULL AND wake_claimed_at IS NULL
		AND next_attempt_at > now() AND lux_run_id = $2`, runID, r.ID); n != 1 {
		t.Fatalf("the wake was not released and backed off:\n%s", w.preview(runID))
	}
	w.lux.PageEvents(nil)
	w.sweepAgain(runID)
	w.untilPreview(runID, "a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 2 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("the failed Run: resumed %d, calls %v; want replaced", r.Resumed, w.lux.CallsOf(r.ID))
	}
	w.open(web)
}

// claimGate holds the first wake claim a connection sends until release is
// closed: the boundary between a sweep's selection and its claim. The claim
// is recognised by its statement, the only one that sets wake_claimed_at to
// now().
type claimGate struct {
	once             sync.Once
	reached, release chan struct{}
}

func (g *claimGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SET wake_claimed_at = now()") {
		g.once.Do(func() {
			close(g.reached)
			<-g.release
		})
	}
	return ctx
}

func (g *claimGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A sweep selects a wake; before it claims it, the follower applies the
// resumed start's failure and startFailed replaces that wake with a new one
// after its backoff. The stale sweep claims nothing, so the new wake is
// acted on once its backoff is due, not after wakeClaimFor.
func TestAStaleSweepDoesNotHoldTheNextWake(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web := w.asleepPreview()
	r := w.luxRuns()[0]
	held, release := w.holdOneStream("failed")
	w.lux.FailStarts("dude.preview="+runID, 1)
	w.lux.RequestServer(web, "/")
	w.untilPreview(runID, "the follower held before the failed start", func() bool {
		return r.Resumed == 1 && isClosed(held)
	})
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now(), next_attempt_at = NULL WHERE id = $1`, runID)
	var selected time.Time
	if err := w.owner.QueryRow(context.Background(), `SELECT wake_wanted_at FROM runs WHERE id = $1`, runID).Scan(&selected); err != nil {
		t.Fatal(err)
	}

	gate := &claimGate{reached: make(chan struct{}), release: make(chan struct{})}
	cfg := w.app.Pool.Config()
	cfg.ConnConfig.Tracer = gate
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	other := &servers.Previews{Service: &servers.Service{DB: &db.DB{Pool: pool}, Lux: w.previews.Lux, Log: quiet,
		PreviewDomain: previewDomain}, Forges: w.previews.Forges, DefaultImage: "default:img"}
	swept, returned := make(chan error, 1), make(chan struct{})
	// In this order on any exit: pool.Close waits on the connection the
	// gate holds, so the gate is released and the sweep has returned first.
	t.Cleanup(func() {
		if !isClosed(gate.release) {
			close(gate.release)
		}
		select {
		case <-returned:
		case <-time.After(30 * time.Second):
			t.Error("the stale sweep did not return")
		}
		other.Stop()
		pool.Close()
	})
	go func() {
		defer close(returned)
		_, err := other.Sweep(context.Background())
		swept <- err
	}()
	wait(t, gate.reached, "the stale sweep at its claim")

	close(release)
	waitFor(t, "startFailed to replace the wake", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1 AND wake_wanted_at > $2`, runID, selected) == 1
	})
	close(gate.release)
	select {
	case err := <-swept:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the stale sweep did not return")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at > $2 AND wake_claimed_at IS NULL`, runID, selected); n != 1 {
		t.Fatalf("the stale sweep holds the new wake:\n%s", w.preview(runID))
	}
	w.untilPreview(runID, "a new Run running within the backoff", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 1 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("the failed Run: resumed %d, calls %v; want replaced", r.Resumed, w.lux.CallsOf(r.ID))
	}
	w.open(web)
}
