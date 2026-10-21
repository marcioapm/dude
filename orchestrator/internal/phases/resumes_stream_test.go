package phases

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// gatedLux is lux whose Gets each wait to be let through, by the order
// they were asked in; output streams as streamLux's. The first free Gets
// answer at once, unnumbered.
type gatedLux struct {
	*streamLux
	mu      sync.Mutex
	free    int
	gates   []chan struct{}
	entered chan int
}

func (f *gatedLux) Get(ctx context.Context, id string) (lux.Run, error) {
	f.mu.Lock()
	if f.free > 0 {
		f.free--
		f.mu.Unlock()
		return f.placementLux.Get(ctx, id)
	}
	n := len(f.gates)
	gate := make(chan struct{})
	f.gates = append(f.gates, gate)
	f.mu.Unlock()
	f.entered <- n
	select {
	case <-gate:
	case <-ctx.Done():
		return lux.Run{}, ctx.Err()
	}
	return f.placementLux.Get(ctx, id)
}

func (f *gatedLux) release(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.gates[n])
}

// gatedFollowing starts the real follower with lux's Gets gated.
func (w *resumeWorld) gatedFollowing() (*gatedLux, *streamLux) {
	w.t.Helper()
	var gated *gatedLux
	st := w.followingAs(func(st *streamLux) lux.Client {
		gated = &gatedLux{streamLux: st, entered: make(chan int, 4)}
		return gated
	})
	return gated, st
}

// followedUp has each resume follow-up signal the returned channel as it
// finishes.
func (w *resumeWorld) followedUp() chan struct{} {
	finished := make(chan struct{}, 4)
	w.s.followedUp = func() { finished <- struct{}{} }
	return finished
}

// While lux is slow to answer the Gets that read a resume's placements,
// the Run's stream goes on: its running state and the agent's first words
// are committed before either Get answers. One Get is let through and its
// follow-up seen to finish, then the other: in either order, the resume
// is timed once, for its epoch, with the placements lux reported.
func TestASlowLuxHoldsUpNoFramesAndTheResumeIsTimedOnce(t *testing.T) {
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		t.Run(map[int]string{0: "the first Get answers first", 1: "the second Get answers first"}[order[0]], func(t *testing.T) {
			w := newResumeWorld(t)
			base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
			w.sessionOn(1)
			w.resume(stoppedOnHost1(base))
			// lux has yet to report the image when the stream reaches the
			// first output, so the first output reads again.
			partial := runningAgain(base, "host-a")
			partial.Placements[1].ImageReadyAt = nil
			w.lux.set(partial, nil)
			finished := make(chan struct{}, 4)
			w.s.followedUp = func() { finished <- struct{}{} }
			var gated *gatedLux
			st := w.followingAs(func(st *streamLux) lux.Client {
				gated = &gatedLux{streamLux: st, entered: make(chan int, 4)}
				return gated
			})

			st.frames <- session(2)
			st.frames <- cursorFrame(running(2), "c1")
			first := waitEntered(t, gated)
			st.frames <- busy(2)
			st.frames <- cursorFrame(spoke(2), "c2")
			second := waitEntered(t, gated)
			w.committed("c2", time.Second)
			var buffered string
			_ = w.owner.QueryRow(w.ctx, `SELECT agent_message_buffer FROM runs WHERE id = $1`, w.run.ID).Scan(&buffered)
			if buffered != "On it." {
				t.Errorf("the agent's words were not committed while lux was slow: %q", buffered)
			}
			if n := len(w.timed()); n != 0 {
				t.Fatalf("timed before lux answered: %d", n)
			}

			// Both Gets answer with the placement complete.
			w.lux.set(runningAgain(base, "host-a"), nil)
			for _, n := range order {
				gated.release([]int{first, second}[n])
				waitGate(t, finished, "the follow-up whose Get answered finishing")
			}
			got := w.timed()
			if len(got) != 1 {
				t.Fatalf("%d run.resume.timed, want 1", len(got))
			}
			phases := got[0]["phases"].(map[string]any)
			if got[0]["epoch"] != 2.0 || len(phases) != 8 {
				t.Errorf("timed %v, want epoch 2 with every phase", got[0])
			}
			// lux's own phases, from its placement: assigned +100ms, image
			// +200ms, restored +400ms, workload +600ms.
			for name, want := range map[string]float64{"image": 100, "restore": 200, "start": 200} {
				if phases[name] != want {
					t.Errorf("%s = %v, want %v", name, phases[name], want)
				}
			}
		})
	}
	t.Run("recovery leaves a resume its follow-up is reading to it", testRecoveryLeavesAResumeItsFollowUpIsReadingToIt)
	t.Run("recovery right after the batch commits leaves the resume to its follow-up", testRecoveryRightAfterTheCommitLeavesTheResume)
}

// recoveryTimingGate pauses the first timing UPDATE before Postgres takes
// its statement snapshot; stream batches and placement reads remain
// independent. ended is closed once that UPDATE's transaction commits or
// rolls back.
type recoveryTimingGate struct {
	claimed atomic.Bool
	conn    atomic.Pointer[pgx.Conn]
	entered chan struct{}
	release chan struct{}
	ended   chan struct{}
}

type recoveryTimingKey struct{}

func (g *recoveryTimingGate) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	// The prefix also matches leaveOwed's revert (SET timed_at = NULL), which
	// comes after the gated UPDATE in its transaction: claimed lets it by.
	if strings.HasPrefix(q.SQL, "UPDATE run_resumes SET timed_at") && g.claimed.CompareAndSwap(false, true) {
		g.conn.Store(c)
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	if g.conn.Load() == c && (q.SQL == "commit" || q.SQL == "rollback") {
		return context.WithValue(ctx, recoveryTimingKey{}, true)
	}
	return ctx
}

func (g *recoveryTimingGate) TraceQueryEnd(ctx context.Context, c *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(recoveryTimingKey{}) != nil && g.conn.CompareAndSwap(c, nil) {
		close(g.ended)
	}
}

// batchCommitGate holds, once armed, the follower's next batch just after
// its commit returns: the batch is committed, and the follower has yet to
// start that batch's follow-up.
type batchCommitGate struct {
	armed   atomic.Bool
	conn    atomic.Pointer[pgx.Conn]
	claimed atomic.Bool
	entered chan struct{}
	release chan struct{}
}

type batchCommitKey struct{}

func (g *batchCommitGate) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	// applyBatch's first statement after the tenant is set.
	if g.armed.Load() && q.SQL == `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE` {
		g.conn.CompareAndSwap(nil, c)
	}
	if q.SQL == "commit" && g.conn.Load() == c {
		return context.WithValue(ctx, batchCommitKey{}, true)
	}
	return ctx
}

func (g *batchCommitGate) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(batchCommitKey{}) != nil && q.Err == nil && g.claimed.CompareAndSwap(false, true) {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
}

// traced has w's syncer use a pool of its own whose connections report
// their queries to tracer.
func (w *resumeWorld) traced(tracer pgx.QueryTracer) {
	w.t.Helper()
	cfg := w.s.DB.Pool.Config()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(w.ctx, cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(pool.Close)
	w.s.DB = &db.DB{Pool: pool}
}

// assertTimedComplete fails unless the Run has exactly one run.resume.timed
// for epoch 2, with every phase and lux's placement durations.
func (w *resumeWorld) assertTimedComplete() {
	w.t.Helper()
	var got []map[string]any
	for _, e := range w.timed() {
		if e["epoch"] == 2.0 {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		w.t.Fatalf("%d run.resume.timed for epoch 2, want 1: %v", len(got), w.timed())
	}
	phases := got[0]["phases"].(map[string]any)
	if len(phases) != 8 || got[0]["hostName"] != "host-a" {
		w.t.Errorf("timed %v, want epoch 2 on host-a with every phase", got[0])
	}
	for name, want := range map[string]float64{"image": 100, "restore": 200, "start": 200} {
		if phases[name] != want {
			w.t.Errorf("%s = %v, want %v", name, phases[name], want)
		}
	}
}

// failNext fails, once armed, the next statement match accepts, by
// running it under a cancelled context; failed is signalled when it does.
type failNext struct {
	armed  atomic.Bool
	match  func(pgx.TraceQueryStartData) bool
	failed chan struct{}
}

func newFailNext(armed bool, match func(pgx.TraceQueryStartData) bool) *failNext {
	f := &failNext{match: match, failed: make(chan struct{}, 4)}
	f.armed.Store(armed)
	return f
}

func (f *failNext) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if f.match(q) && f.armed.CompareAndSwap(true, false) {
		f.failed <- struct{}{}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled
	}
	return ctx
}

func (*failNext) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// timingOf matches a timing UPDATE of the one resume into epoch: a
// follow-up's, or the startup pass's; recovery at a follower's start and a
// Run's end asks for every epoch (0).
func timingOf(epoch int) func(pgx.TraceQueryStartData) bool {
	return func(q pgx.TraceQueryStartData) bool {
		return strings.HasPrefix(q.SQL, "UPDATE run_resumes SET timed_at = now()") && len(q.Args) > 1 && q.Args[1] == any(epoch)
	}
}

// holdCommit holds the commit of the transaction whose statement match
// accepts first, until release.
type holdCommit struct {
	match   func(pgx.TraceQueryStartData) bool
	conn    atomic.Pointer[pgx.Conn]
	claimed atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (g *holdCommit) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if g.match(q) {
		g.conn.CompareAndSwap(nil, c)
	}
	if q.SQL == "commit" && g.conn.Load() == c && g.claimed.CompareAndSwap(false, true) {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (*holdCommit) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// resumedOnHostA is a world whose Run is resumed from host 1 into epoch 2,
// lux reporting it running on host-a; without imageReady, lux has yet to
// report the image, so the first output reads the placements again.
func resumedOnHostA(t *testing.T, imageReady bool) (*resumeWorld, time.Time) {
	t.Helper()
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	w.sessionOn(1)
	w.resume(stoppedOnHost1(base))
	running := runningAgain(base, "host-a")
	if !imageReady {
		running.Placements[1].ImageReadyAt = nil
	}
	w.lux.set(running, nil)
	return w, base
}

// closeOnce returns a func closing ch at most once, also run at the test's
// end so nothing is left blocked on it.
func closeOnce(t *testing.T, ch chan struct{}) func() {
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return release
}

// Recovery's UPDATE runs once the first output is committed and while both
// follow-ups wait on lux: it leaves the resume untimed, and the follow-up
// that reads the placements first times it, complete.
func testRecoveryLeavesAResumeItsFollowUpIsReadingToIt(t *testing.T) {
	w, _ := resumedOnHostA(t, true)
	gate := &recoveryTimingGate{entered: make(chan struct{}), release: make(chan struct{}), ended: make(chan struct{})}
	w.traced(gate)
	release := closeOnce(t, gate.release)
	finished := w.followedUp()
	gated, st := w.gatedFollowing()
	// The follower's own recovery, at its start, is the first timing UPDATE.
	waitGate(t, gate.entered, "the follower's recovery reaching its timing UPDATE")
	st.frames <- session(2)
	st.frames <- cursorFrame(running(2), "c1")
	first := waitEntered(t, gated)
	st.frames <- busy(2)
	st.frames <- cursorFrame(spoke(2), "c2")
	second := waitEntered(t, gated)
	w.committed("c2", time.Second)
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("timed while recovery and both Gets were gated: %v", got)
	}

	release()
	waitGate(t, gate.ended, "recovery's transaction ending")
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("recovery timed a resume a follow-up was still reading the placements of: %v", got)
	}
	if row := w.row(2); row["timed_at"] != nil || row["first_output_at"] == nil {
		t.Fatalf("after recovery: timed_at %v, first_output_at %v; want untimed with its first output", row["timed_at"], row["first_output_at"])
	}
	for _, n := range []int{first, second} {
		gated.release(n)
		waitGate(t, finished, "the follow-up whose Get answered finishing")
	}
	w.assertTimedComplete()
}

// Recovery that runs between a batch's commit and the start of its
// follow-up finds the resume already owed, and leaves it; it times every
// other resume, of the Run and of another Run, that nobody owes.
func testRecoveryRightAfterTheCommitLeavesTheResume(t *testing.T) {
	w, base := resumedOnHostA(t, false)
	gate := &batchCommitGate{entered: make(chan struct{}), release: make(chan struct{})}
	w.traced(gate)
	release := closeOnce(t, gate.release)
	finished := w.followedUp()
	gated, st := w.gatedFollowing()
	st.frames <- session(2)
	st.frames <- running(2)
	st.frames <- cursorFrame(busy(2), "c1")
	w.committed("c1", time.Second)
	gated.release(waitEntered(t, gated))
	waitGate(t, finished, "the running state's follow-up finishing")

	gate.armed.Store(true)
	st.frames <- cursorFrame(spoke(2), "c2")
	waitGate(t, gate.entered, "the output batch's commit returning")
	// Untimed resumes nobody owes, in while epoch 2 is owed: an earlier one
	// of the Run, and one of another Run into the same epoch.
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at, first_output_at)
		VALUES ($1, $2, 1, 'person', $3, $3)`, w.run.ID, w.run.Org, base)
	other := "run_other_" + w.run.Org
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status)
		VALUES ($1, $2, $3, $4, 2, 'implement', 'running')`, other, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at, first_output_at)
		VALUES ($1, $2, 2, 'person', $3, $3)`, other, w.run.Org, base)

	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	w.s.TimeUntimedResumes(w.ctx)
	var epochs []any
	for _, e := range w.timed() {
		epochs = append(epochs, e["epoch"])
	}
	if len(epochs) != 1 || epochs[0] != 1.0 {
		t.Fatalf("recovery between the batch's commit and its follow-up timed epochs %v, want only the unowed epoch 1", epochs)
	}
	if row := w.row(2); row["timed_at"] != nil {
		t.Fatalf("the owed resume's timed_at is %v after recovery, want NULL", row["timed_at"])
	}
	var otherTimed int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`,
		other, evResumeTimed).Scan(&otherTimed); err != nil {
		t.Fatal(err)
	}
	if otherTimed != 1 {
		t.Fatalf("the other Run's untimed resume got %d run.resume.timed, want 1", otherTimed)
	}

	w.lux.set(runningAgain(base, "host-a"), nil)
	release()
	gated.release(waitEntered(t, gated))
	waitGate(t, finished, "the output batch's follow-up finishing")
	w.assertTimedComplete()
}

// Two follow-ups of one resume overlap: the running state's is held after
// its timing, before it is done, while the output batch's waits on lux.
// The first finishing leaves the resume owed to the second, which times
// it, complete.
func TestOverlappingFollowUpsOfOneResumeEachOweIt(t *testing.T) {
	w, base := resumedOnHostA(t, false)
	hold := &holdCommit{match: timingOf(2), entered: make(chan struct{}), release: make(chan struct{})}
	w.traced(hold)
	release := closeOnce(t, hold.release)
	finished := w.followedUp()
	gated, st := w.gatedFollowing()
	st.frames <- session(2)
	st.frames <- cursorFrame(running(2), "c1")
	gated.release(waitEntered(t, gated))
	waitGate(t, hold.entered, "the running state's follow-up reaching its timing's commit")

	st.frames <- busy(2)
	st.frames <- cursorFrame(spoke(2), "c2")
	second := waitEntered(t, gated)
	w.committed("c2", time.Second)
	release()
	waitGate(t, finished, "the running state's follow-up finishing")

	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("recovery timed a resume the output batch's follow-up still owed: %v", got)
	}
	w.lux.set(runningAgain(base, "host-a"), nil)
	gated.release(second)
	waitGate(t, finished, "the output batch's follow-up finishing")
	w.assertTimedComplete()
}

// The agent's session and its first words in one batch, lux yet to report
// the placement: the batch's one follow-up reads it, and its timing fails.
// Nothing is left owed: recovery times the resume.
func TestOneBatchWithTwoReadsOfAResumeOwesItOnce(t *testing.T) {
	w, _ := resumedOnHostA(t, true)
	locked := make(chan struct{}, 1)
	var lockSeen atomic.Bool
	fail := newFailNext(true, timingOf(2))
	w.traced(multitracer.New(fail, hookTracer(func(ctx context.Context, q pgx.TraceQueryStartData) context.Context {
		if q.SQL == `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE` && lockSeen.CompareAndSwap(false, true) {
			locked <- struct{}{}
		}
		return ctx
	})))
	finished := w.followedUp()
	unlock := w.holding(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID)
	st := w.following()
	// A batch with no resume in it, held on the Run's row while the rest
	// queue behind it.
	st.frames <- record(2, "acp.plan", map[string]any{"sessionUpdate": "plan", "entries": []any{}})
	waitGate(t, locked, "the first batch asking for the Run's row")
	// The session stamps running_at and the words first_output_at, lux's
	// placement unread: each asks for a read.
	st.frames <- session(2)
	st.frames <- cursorFrame(spoke(2), "c1")
	// Taken by the stream only once spoke(2) is queued.
	st.frames <- cursorFrame(record(2, "acp.plan", map[string]any{"sessionUpdate": "plan", "entries": []any{}}), "c2")
	unlock()
	w.committed("c2", 5*time.Second)
	waitGate(t, fail.failed, "the follow-up's timing failing")
	waitGate(t, finished, "the batch's follow-up finishing")
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("timed though the follow-up's timing failed: %v", got)
	}

	w.s.TimeUntimedResumes(w.ctx)
	if got := w.timed(); len(got) != 1 || got[0]["epoch"] != 2.0 || got[0]["hostName"] != "host-a" {
		t.Fatalf("after recovery: %v, want one run.resume.timed for epoch 2 on host-a", got)
	}
}

// A batch carrying a resume's first output fails, and the next follower
// records it again; that batch's follow-up reads the placements and its
// timing fails. The failed batch owes nothing: recovery times the resume.
func TestAFailedBatchOwesNothing(t *testing.T) {
	w, _ := resumedOnHostA(t, false)
	failSave := newFailNext(false, func(q pgx.TraceQueryStartData) bool {
		return strings.HasPrefix(q.SQL, "UPDATE runs SET agent_message_buffer")
	})
	failTiming := newFailNext(false, timingOf(2))
	w.traced(multitracer.New(failSave, failTiming))
	finished := w.followedUp()
	st := w.following()
	st.frames <- session(2)
	st.frames <- running(2)
	st.frames <- cursorFrame(busy(2), "c1")
	w.committed("c1", time.Second)
	waitGate(t, finished, "the running state's follow-up finishing")

	failSave.armed.Store(true)
	st.frames <- cursorFrame(spoke(2), "c2")
	waitGate(t, failSave.failed, "the output batch's save failing")
	waitGate(t, st.done, "the follower ending on its failed batch")
	if row := w.row(2); row["first_output_at"] != nil {
		t.Fatalf("the failed batch's first output was committed")
	}

	failTiming.armed.Store(true)
	st = w.following()
	st.frames <- cursorFrame(spoke(2), "c2")
	w.committed("c2", time.Second)
	waitGate(t, failTiming.failed, "the follow-up's timing failing")
	waitGate(t, finished, "the replayed batch's follow-up finishing")
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("timed though the follow-up's timing failed: %v", got)
	}

	w.s.TimeUntimedResumes(w.ctx)
	if got := w.timed(); len(got) != 1 || got[0]["epoch"] != 2.0 {
		t.Fatalf("after recovery: %v, want one run.resume.timed for epoch 2", got)
	}
}

// hookTracer hands each statement's start to the func.
type hookTracer func(context.Context, pgx.TraceQueryStartData) context.Context

func (h hookTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	return h(ctx, q)
}

func (hookTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// missedFramesLux resumes the Run into epoch 3: the first time streaming
// that placement's first frames, committed, before it answers (as
// earlyFramesLux). Gets wait as gatedLux's, but for the free ones.
type missedFramesLux struct {
	*gatedLux
	early   *earlyFramesLux
	resumed bool
}

func (f *missedFramesLux) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	if !f.resumed {
		f.resumed = true
		return f.early.Resume(ctx, id, in)
	}
	return lux.Run{ID: "lrun_1", State: "resuming", Epoch: 3}, nil
}

func (f *missedFramesLux) letGet(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.free = n
}

// missedFramesFollowing starts the follower on w's resumable Run, with lux
// a missedFramesLux.
func (w *resumeWorld) missedFramesFollowing() *missedFramesLux {
	w.t.Helper()
	w.sessionOn(1)
	w.resumable()
	var m *missedFramesLux
	w.followingAs(func(st *streamLux) lux.Client {
		m = &missedFramesLux{gatedLux: &gatedLux{streamLux: st, entered: make(chan int, 4)},
			early: &earlyFramesLux{streamLux: st, w: w, epoch: 3}}
		return m
	})
	return m
}

// runningOn3 is the Run as lux reports it running on epoch 3, on host-a.
func runningOn3(base time.Time) lux.Run {
	r := runningAgain(base, "host-a")
	r.Epoch, r.Placements[1].Epoch = 3, 3
	return r
}

// A resume whose first frames were missed is timed as it stands by the
// follow-up whilePaused starts: recovery while that follow-up waits on lux
// leaves it, and the follow-up times it with lux's placements.
func TestRecoveryLeavesAMissedFramesResumeToItsFollowUp(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	w.lux.set(stoppedOnHost1(base), nil)
	finished := w.followedUp()
	m := w.missedFramesFollowing()
	// The resume's own Get, before lux is asked.
	m.letGet(1)
	w.whilePaused()
	w.lux.set(runningOn3(base), nil)
	n := waitEntered(t, m.gatedLux)
	if row := w.row(3); row["frames_missed"] != true {
		t.Fatalf("frames_missed = %v, want true", row["frames_missed"])
	}

	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	w.s.TimeUntimedResumes(w.ctx)
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("recovery timed a missed-frames resume its follow-up was reading: %v", got)
	}
	if row := w.row(3); row["timed_at"] != nil {
		t.Fatalf("timed_at %v after recovery, want NULL", row["timed_at"])
	}
	m.release(n)
	waitGate(t, finished, "the missed-frames follow-up finishing")
	got := w.timed()
	if len(got) != 1 || got[0]["epoch"] != 3.0 || got[0]["hostName"] != "host-a" {
		t.Fatalf("timed %v, want one for epoch 3 on host-a", got)
	}
	if phases := got[0]["phases"].(map[string]any); phases["image"] != 100.0 {
		t.Errorf("image = %v, want lux's 100", phases["image"])
	}
}

// The transaction that takes a missed-frames resume out of paused fails:
// it owes nothing. The retry's follow-up reads the placements and its
// timing fails; recovery then times the resume.
func TestAFailedMissedFramesResumeOwesNothing(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	w.lux.set(stoppedOnHost1(base), nil)
	failResume := newFailNext(true, func(q pgx.TraceQueryStartData) bool {
		return strings.HasPrefix(q.SQL, "UPDATE runs SET status = 'running'") && strings.Contains(q.SQL, "lux_cost_next_at = now()")
	})
	failTiming := newFailNext(false, timingOf(3))
	w.traced(multitracer.New(failResume, failTiming))
	finished := w.followedUp()
	m := w.missedFramesFollowing()
	r := w.run
	r.Resumable = true
	m.letGet(1)
	if _, err := w.s.whilePaused(w.ctx, r); err == nil {
		t.Fatal("whilePaused succeeded, want its transaction to fail")
	}
	waitGate(t, failResume.failed, "the resume's transaction failing")

	failTiming.armed.Store(true)
	m.letGet(1)
	w.whilePaused()
	if row := w.row(3); row["frames_missed"] != true {
		t.Fatalf("frames_missed = %v after the retry, want true", row["frames_missed"])
	}
	w.lux.set(runningOn3(base), nil)
	m.release(waitEntered(t, m.gatedLux))
	waitGate(t, failTiming.failed, "the follow-up's timing failing")
	waitGate(t, finished, "the retry's follow-up finishing")
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("timed though the follow-up's timing failed: %v", got)
	}

	w.s.TimeUntimedResumes(w.ctx)
	if got := w.timed(); len(got) != 1 || got[0]["epoch"] != 3.0 || got[0]["hostName"] != "host-a" {
		t.Fatalf("after recovery: %v, want one run.resume.timed for epoch 3 on host-a", got)
	}
}

// A follow-up whose placement read runs out of time still times the
// resume, as it stands, on a budget of its own.
func TestAFollowUpWhoseReadTimesOutTimesTheResumeWithoutPlacements(t *testing.T) {
	w, _ := resumedOnHostA(t, true)
	w.s.followUpTimeout = 300 * time.Millisecond
	finished := w.followedUp()
	gated, st := w.gatedFollowing()
	// Each Get is never let through: each read ends at its deadline.
	st.frames <- cursorFrame(session(2), "c1")
	waitEntered(t, gated)
	waitGate(t, finished, "the running state's follow-up ending")
	st.frames <- cursorFrame(busy(2), "c2")
	st.frames <- cursorFrame(spoke(2), "c3")
	waitEntered(t, gated)
	waitGate(t, finished, "the output batch's follow-up ending")
	got := w.timed()
	if len(got) != 1 || got[0]["epoch"] != 2.0 || got[0]["hostName"] != nil {
		t.Fatalf("timed %v, want one run.resume.timed for epoch 2 without placements", got)
	}
	w.s.TimeUntimedResumes(w.ctx)
	if n := len(w.timed()); n != 1 {
		t.Fatalf("%d run.resume.timed after recovery, want still 1", n)
	}
}

// A follow-up whose placement read runs out of time, and whose timing
// fails, owes nothing after: recovery times the resume as it stands.
func TestAFollowUpThatTimesOutLeavesTheResumeToRecovery(t *testing.T) {
	w, _ := resumedOnHostA(t, true)
	w.s.followUpTimeout = 300 * time.Millisecond
	fail := newFailNext(false, timingOf(2))
	w.traced(fail)
	finished := w.followedUp()
	gated, st := w.gatedFollowing()
	// Each Get is never let through: each read ends at its deadline.
	st.frames <- cursorFrame(session(2), "c1")
	waitEntered(t, gated)
	waitGate(t, finished, "the running state's follow-up ending")
	fail.armed.Store(true)
	st.frames <- cursorFrame(busy(2), "c2")
	st.frames <- cursorFrame(spoke(2), "c3")
	waitEntered(t, gated)
	waitGate(t, fail.failed, "the output batch's follow-up's timing failing")
	waitGate(t, finished, "the output batch's follow-up ending")
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("timed though the follow-up's timing failed: %v", got)
	}

	if n := w.s.TimeUntimedResumes(w.ctx); n != 1 {
		t.Errorf("the startup pass went through %d resumes, want 1", n)
	}
	got := w.timed()
	if len(got) != 1 || got[0]["epoch"] != 2.0 || got[0]["hostName"] != nil {
		t.Fatalf("after recovery: %v, want one run.resume.timed for epoch 2 without placements", got)
	}
}

// waitGate waits for ch, failing with what did not happen.
func waitGate(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("waited 5s: %s", what)
	}
}

func waitEntered(t *testing.T, g *gatedLux) int {
	t.Helper()
	select {
	case n := <-g.entered:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("lux was never asked for the resume's placements")
		return 0
	}
}

// A stream replayed from the start carries every earlier placement's
// session, busy and words: none of them stamps the resume into the
// current epoch, and its own frames do.
func TestEarlierEpochsReplayedNeverStampTheCurrentResume(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Hour).UTC()
	w.lux.set(runningAgain(base, "host-a"), nil)
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at,
		running_at, busy_at, first_output_at, timed_at) VALUES ($1, $2, 2, 'person', $3, $3, $3, $3, $3, $3)`,
		w.run.ID, w.run.Org, base)
	w.resume(lux.Run{ID: "lrun_1", State: "stopped", Epoch: 2, Placements: []lux.Placement{{Epoch: 2, HostName: "host-a",
		State: "exited", ExitedAt: &base}}})
	if w.row(3)["requested_at"] == nil {
		t.Fatal("no resume into epoch 3")
	}
	before2 := w.row(2)
	var replay []lux.Frame
	for _, e := range []int{1, 2} {
		replay = append(replay, session(e), busy(e), spoke(e),
			record(e, "acp.tool_call", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t", "title": "ls"}), running(e))
	}
	w.follow(replay...)
	row := w.row(3)
	for _, col := range []string{"running_at", "busy_at", "first_output_at"} {
		if row[col] != nil {
			t.Errorf("an earlier epoch's frame stamped the current resume's %s", col)
		}
	}
	if after2 := w.row(2); after2["first_output_at"] != before2["first_output_at"] || after2["busy_at"] != before2["busy_at"] {
		t.Errorf("the replay moved epoch 2's stamps")
	}
	w.follow(session(3), busy(3), spoke(3))
	row = w.row(3)
	for _, col := range []string{"running_at", "busy_at", "first_output_at"} {
		if row[col] == nil {
			t.Errorf("epoch 3's own frame did not stamp its %s", col)
		}
	}
}

// The first thing the agent says or does — a message, a thought, a tool
// call or a tool call's update — ends a resume's timing; its plan, a usage
// report or its commands list does not.
func TestWhatEndsAResumesTimingIsTheAgentSayingOrDoingSomething(t *testing.T) {
	update := func(kind string, extra map[string]any) map[string]any {
		m := map[string]any{"sessionUpdate": kind}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	notOutput := []lux.Frame{
		record(2, "acp.plan", update("plan", map[string]any{"entries": []any{map[string]any{"content": "look", "status": "pending"}}})),
		record(2, "acp.usage_update", update("usage_update", map[string]any{"used": 10, "size": 1000})),
		record(2, "acp.available_commands_update", update("available_commands_update", map[string]any{"availableCommands": []any{}})),
	}
	for kind, data := range map[string]map[string]any{
		"agent_message_chunk": {"content": map[string]any{"type": "text", "text": "On it."}},
		"agent_thought_chunk": {"content": map[string]any{"type": "text", "text": "Hmm."}},
		"tool_call":           {"toolCallId": "t1", "title": "ls", "status": "pending"},
		"tool_call_update":    {"toolCallId": "t1", "status": "completed"},
	} {
		t.Run(kind, func(t *testing.T) {
			w := newResumeWorld(t)
			w.resume(stoppedOnHost1(time.Now()))
			w.lux.set(runningAgain(time.Now(), "host-a"), nil)
			// Each frame on its own, so one that ends the timing and a later
			// one that hides it are both caught; nothing is published yet.
			for _, f := range append([]lux.Frame{running(2), busy(2)}, notOutput...) {
				w.follow(f)
				if row := w.row(2); row["first_output_at"] != nil {
					t.Fatalf("a plan, usage or commands list ended the timing")
				}
				if n := len(w.timed()); n != 0 {
					t.Fatalf("%d run.resume.timed before the agent said or did anything, want 0", n)
				}
			}
			w.follow(record(2, "acp."+kind, update(kind, data)))
			if row := w.row(2); row["first_output_at"] == nil {
				t.Errorf("%s did not end the timing", kind)
			}
			timed := w.timed()
			if len(timed) != 1 {
				t.Fatalf("%d run.resume.timed, want 1", len(timed))
			}
			phases, _ := timed[0]["phases"].(map[string]any)
			if timed[0]["totalMs"] == nil || phases == nil || phases["firstOutput"] == nil {
				t.Errorf("the timing has no total or first output: %v", timed[0])
			}
		})
	}
}
