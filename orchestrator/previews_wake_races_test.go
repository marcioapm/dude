package orchestrator_test

// A wakeable preview's failed start, when what lux reports reaches dude in
// another order than the wake that caused it: a replacement that fails
// before its servers are attached, a second orchestrator replaying events
// already applied, a failure or a running seen before the wake's own
// acknowledgement commits. Each is held at a boundary, not slept through.

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// gateLux is lux as one orchestrator sees it, with boundaries a test holds:
// an attach sent only once the Run has ended (attachAfterEnd), a resume's
// answer held until afterResume returns, and an output stream held at the
// first lifecycle event of state holdAt until release is closed.
type gateLux struct {
	lux.Client
	fake           *fakelux.Server
	attachAfterEnd bool

	mu          sync.Mutex
	afterResume func(runID string)

	holdAt  string
	held    bool
	reached chan struct{} // closed once a stream is held
	release chan struct{} // closed by the test to let it go on
	done    chan struct{} // closed once the held stream has ended
}

func newGateLux(c lux.Client, fake *fakelux.Server) *gateLux {
	return &gateLux{Client: c, fake: fake, reached: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
}

func (g *gateLux) AttachServer(ctx context.Context, serverID, runID string) (lux.TenantServer, error) {
	if g.attachAfterEnd {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if st := g.fake.State(runID); st == "stopped" || st == "failed" || st == "lost" || st == "cancelled" || st == "succeeded" {
				break
			}
		}
	}
	return g.Client.AttachServer(ctx, serverID, runID)
}

func (g *gateLux) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	r, err := g.Client.Resume(ctx, id, in)
	g.mu.Lock()
	hook := g.afterResume
	g.afterResume = nil
	g.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return r, err
}

func (g *gateLux) Output(ctx context.Context, runID, cursor string, afterEvent int64, fn func(lux.Frame) error) error {
	mine := false
	err := g.Client.Output(ctx, runID, cursor, afterEvent, func(f lux.Frame) error {
		g.mu.Lock()
		hold := !g.held && g.holdAt != "" && f.Kind == "lux" && f.EventType == "state" && stateOf(f) == g.holdAt
		if hold {
			g.held, mine = true, true
		}
		g.mu.Unlock()
		if hold {
			close(g.reached)
			select {
			case <-g.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return fn(f)
	})
	if mine {
		close(g.done)
	}
	return err
}

func stateOf(f lux.Frame) string {
	var d struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(f.EventData, &d)
	return d.State
}

// wait waits for ch without sweeping.
func wait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// waitFor polls cond without sweeping.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// secondFollower is a second orchestrator's preview loop, on its own lux
// client, whose output stream is held at the first event of state holdAt.
// The preview is woken with both sweeping, so both follow its Run, and it
// returns once the second is held there and the first has seen it running.
func (w *world) secondFollower(runID, web, holdAt string) *gateLux {
	w.t.Helper()
	g := newGateLux(w.previews.Lux, w.lux)
	g.holdAt = holdAt
	other := &servers.Previews{Service: &servers.Service{DB: w.app, Lux: g, Log: quiet, PreviewDomain: previewDomain},
		Forges: w.previews.Forges, DefaultImage: "default:img"}
	w.t.Cleanup(other.Stop)
	w.lux.RequestServer(web, "/")
	w.heard(runID)
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, _ = w.previews.Sweep(context.Background())
		_, _ = other.Sweep(context.Background())
		running := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
		if running && (holdAt != "running" || isClosed(g.reached)) {
			w.open(web)
			return g
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("the preview never ran with two followers:\n%s", w.describeRuns())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// preview says what dude holds of a preview and what lux has of each Run,
// for a failure's message.
func (w *world) preview(runID string) string {
	var out string
	_ = w.owner.QueryRow(context.Background(), `SELECT format('status=%s lux=%s/%s failures=%s wake=%s claimed=%s next=%s error=%s',
		status, lux_run_id, lux_state, start_failures, wake_wanted_at IS NOT NULL, wake_claimed_at IS NOT NULL,
		next_attempt_at - now(), error) FROM runs WHERE id = $1`, runID).Scan(&out)
	for _, r := range w.luxRuns() {
		if r != nil {
			out += "\n  " + r.ID + " " + w.lux.State(r.ID)
		}
	}
	return out
}

// untilPreview is until, failing with the preview's own state.
func (w *world) untilPreview(runID, what string, cond func() bool) {
	w.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(30 * time.Millisecond) {
		w.pump()
		if cond() {
			return
		}
	}
	w.t.Fatalf("timed out waiting for %s\n%s", what, w.preview(runID))
}

// A replacement Run that fails before its servers are attached (attach
// answers 409 finished) is still one counted failed start: one request
// tries exactly previewStartAttempts Runs, and a later request one more.
func TestAReplacementThatFailsBeforeItsAttachIsCounted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	g := newGateLux(w.previews.Lux, w.lux)
	g.attachAfterEnd = true
	w.previews.Lux = g
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	web := w.serverID(runID, "web")
	w.lux.FailStarts("dude.preview="+runID, 100)

	w.lux.RequestServer(web, "/")
	w.untilPreview(runID, "dude to give up", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 3 AND wake_wanted_at IS NULL AND status = 'paused'`, runID) == 1
	})
	for range 10 {
		w.pump()
	}
	if n := len(w.luxRuns()); n != 3 {
		t.Fatalf("%d lux runs for one request; want 3\n%s", n, w.describeRuns())
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
		t.Fatalf("%d lux runs after one more request; want 4", n)
	}
	_ = task
}

// A second orchestrator replays the Run's first running event after the
// failed start of a later resume was recorded: the count survives, and the
// recovery wake replaces the Run rather than resuming it again.
func TestAReplayedRunningDoesNotEraseAFailedStart(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	g := w.secondFollower(runID, web, "running")
	old := w.luxRuns()[0]

	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.lux.FailStarts("dude.preview="+runID, 1)
	w.lux.RequestServer(web, "/")
	w.until("the failed start counted", func() bool {
		return w.lux.State(old.ID) == "failed" &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1`, runID) == 1
	})
	close(g.release)
	wait(t, g.done, "the replaying follower to finish")
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1`, runID); n != 1 {
		t.Fatalf("the replay erased the failed start:\n%s", w.describeRuns())
	}
	w.open(web)
	if n := len(w.luxRuns()); n != 2 || old.Resumed != 1 || !slices.Contains(w.lux.CallsOf(old.ID), "cancel") {
		t.Fatalf("%d lux runs, old resumed %d, calls %v; want the failed Run replaced", n, old.Resumed, w.lux.CallsOf(old.ID))
	}
}

// The resume is accepted and its start fails; another orchestrator's
// follower consumes that failure before the resume's acknowledgement
// commits (dude still says paused). It is counted, and a new Run follows.
func TestAFailedStartSeenBeforeTheWakeCommitsIsCounted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	a := newGateLux(w.previews.Lux, w.lux)
	w.previews.Lux = a
	g := w.secondFollower(runID, web, "stopping")
	old := w.luxRuns()[0]

	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	wait(t, g.reached, "the second follower held")
	w.lux.FailStarts("dude.preview="+runID, 1)
	a.afterResume = func(id string) {
		waitFor(t, "the resumed start to fail", func() bool { return w.lux.State(id) == "failed" })
		close(g.release)
		wait(t, g.done, "the other follower to consume the failure")
	}
	w.lux.RequestServer(web, "/")
	w.until("a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if !slices.Contains(w.lux.CallsOf(old.ID), "cancel") {
		t.Errorf("the failed Run was not cancelled: %v", w.lux.CallsOf(old.ID))
	}
}

// The resumed Run runs, and another orchestrator's follower consumes that
// running before the resume's acknowledgement commits; then it crashes.
// It ran: the next request resumes the same Run from its snapshot.
func TestARunningSeenBeforeTheWakeCommitsIsNotAFailedStart(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	a := newGateLux(w.previews.Lux, w.lux)
	w.previews.Lux = a
	g := w.secondFollower(runID, web, "stopping")
	r := w.luxRuns()[0]

	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	wait(t, g.reached, "the second follower held")
	a.afterResume = func(id string) {
		waitFor(t, "the resumed Run running", func() bool { return w.lux.State(id) == "running" })
		close(g.release)
		waitFor(t, "the other follower to consume running", func() bool {
			return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, runID) == 1
		})
	}
	w.lux.RequestServer(web, "/")
	w.until("woken", func() bool {
		return r.Resumed == 1 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NULL AND status <> 'paused'`, runID) == 1
	})
	w.lux.Crash(r.ID)
	w.until("asleep after the crash", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	w.open(web)
	if n := len(w.luxRuns()); n != 1 || r.Resumed != 2 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("%d lux runs, resumed %d, calls %v; want the crashed Run resumed, not replaced", n, r.Resumed, w.lux.CallsOf(r.ID))
	}
}
