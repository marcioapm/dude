package phases

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// gatedLux is lux whose Gets each wait to be let through, by the order
// they were asked in; output streams as streamLux's.
type gatedLux struct {
	*streamLux
	mu      sync.Mutex
	gates   []chan struct{}
	entered chan int
}

func (f *gatedLux) Get(ctx context.Context, id string) (lux.Run, error) {
	f.mu.Lock()
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
				waitFinished(t, finished)
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

// assertTimedComplete fails unless the Run has exactly one run.resume.timed,
// for epoch 2, with every phase and lux's placement durations.
func (w *resumeWorld) assertTimedComplete() {
	w.t.Helper()
	got := w.timed()
	if len(got) != 1 {
		w.t.Fatalf("%d run.resume.timed, want 1: %v", len(got), got)
	}
	phases := got[0]["phases"].(map[string]any)
	if got[0]["epoch"] != 2.0 || len(phases) != 8 || got[0]["hostName"] != "host-a" {
		w.t.Errorf("timed %v, want epoch 2 on host-a with every phase", got[0])
	}
	for name, want := range map[string]float64{"image": 100, "restore": 200, "start": 200} {
		if phases[name] != want {
			w.t.Errorf("%s = %v, want %v", name, phases[name], want)
		}
	}
}

// Recovery's UPDATE runs once the first output is committed and while both
// follow-ups wait on lux: it leaves the resume untimed, and the follow-up
// that reads the placements first times it, complete.
func testRecoveryLeavesAResumeItsFollowUpIsReadingToIt(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	w.sessionOn(1)
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-a"), nil)

	gate := &recoveryTimingGate{entered: make(chan struct{}), release: make(chan struct{}), ended: make(chan struct{})}
	w.traced(gate)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
	finished := make(chan struct{}, 4)
	w.s.followedUp = func() { finished <- struct{}{} }
	var gated *gatedLux
	st := w.followingAs(func(st *streamLux) lux.Client {
		gated = &gatedLux{streamLux: st, entered: make(chan int, 4)}
		return gated
	})
	// The follower's own recovery, at its start, is the first timing UPDATE.
	waitFinished(t, gate.entered)
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

	release.Do(func() { close(gate.release) })
	waitFinished(t, gate.ended)
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("recovery timed a resume a follow-up was still reading the placements of: %v", got)
	}
	if row := w.row(2); row["timed_at"] != nil || row["first_output_at"] == nil {
		t.Fatalf("after recovery: timed_at %v, first_output_at %v; want untimed with its first output", row["timed_at"], row["first_output_at"])
	}
	for _, n := range []int{first, second} {
		gated.release(n)
		waitFinished(t, finished)
	}
	w.assertTimedComplete()
}

// Recovery that runs between a batch's commit and the start of its
// follow-up finds the resume already owed, and leaves it.
func testRecoveryRightAfterTheCommitLeavesTheResume(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	w.sessionOn(1)
	w.resume(stoppedOnHost1(base))
	// The first read finds the image not reported yet, so the first output
	// reads again.
	partial := runningAgain(base, "host-a")
	partial.Placements[1].ImageReadyAt = nil
	w.lux.set(partial, nil)

	gate := &batchCommitGate{entered: make(chan struct{}), release: make(chan struct{})}
	w.traced(gate)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
	finished := make(chan struct{}, 4)
	w.s.followedUp = func() { finished <- struct{}{} }
	var gated *gatedLux
	st := w.followingAs(func(st *streamLux) lux.Client {
		gated = &gatedLux{streamLux: st, entered: make(chan int, 4)}
		return gated
	})
	st.frames <- session(2)
	st.frames <- running(2)
	st.frames <- cursorFrame(busy(2), "c1")
	w.committed("c1", time.Second)
	gated.release(waitEntered(t, gated))
	waitFinished(t, finished)

	gate.armed.Store(true)
	st.frames <- cursorFrame(spoke(2), "c2")
	waitFinished(t, gate.entered)
	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	w.s.TimeUntimedResumes(w.ctx)
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("recovery between the batch's commit and its follow-up timed the resume: %v", got)
	}

	w.lux.set(runningAgain(base, "host-a"), nil)
	release.Do(func() { close(gate.release) })
	gated.release(waitEntered(t, gated))
	waitFinished(t, finished)
	w.assertTimedComplete()
}

// A follow-up whose placement read and timing both run out of time owes
// nothing after: recovery times the resume as it stands.
func TestAFollowUpThatTimesOutLeavesTheResumeToRecovery(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	w.sessionOn(1)
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-a"), nil)
	w.s.followUpTimeout = 300 * time.Millisecond
	finished := make(chan struct{}, 4)
	w.s.followedUp = func() { finished <- struct{}{} }
	var gated *gatedLux
	st := w.followingAs(func(st *streamLux) lux.Client {
		gated = &gatedLux{streamLux: st, entered: make(chan int, 4)}
		return gated
	})
	// Each Get is never let through: each follow-up ends at its deadline.
	st.frames <- cursorFrame(session(2), "c1")
	waitEntered(t, gated)
	waitFinished(t, finished)
	st.frames <- cursorFrame(busy(2), "c2")
	w.committed("c2", time.Second)
	st.frames <- cursorFrame(spoke(2), "c3")
	waitEntered(t, gated)
	waitFinished(t, finished)
	if got := w.timed(); len(got) != 0 {
		t.Fatalf("a follow-up past its deadline timed the resume: %v", got)
	}

	if n := w.s.TimeUntimedResumes(w.ctx); n != 1 {
		t.Errorf("the startup pass went through %d resumes, want 1", n)
	}
	got := w.timed()
	if len(got) != 1 || got[0]["epoch"] != 2.0 || got[0]["hostName"] != nil {
		t.Fatalf("after recovery: %v, want one run.resume.timed for epoch 2 without placements", got)
	}
}

func waitFinished(t *testing.T, finished chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the follow-up whose Get answered did not finish")
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
