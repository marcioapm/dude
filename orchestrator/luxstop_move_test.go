package orchestrator_test

// lux's own moves (phases/luxstop.go) beside a machine move dude asks for
// (#130's request_machine and Reset): the turn the move cuts off belongs
// to dude's move, which resumes the Run on its new size and tells the agent
// once; nothing is held, failed or taken up live twice.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// stopsThrough routes dude's calls to lux through a proxy that calls
// onStop for each stop dude asks for and, while hold is set, answers the
// stop as lux accepting it without passing it on, keeping it for release.
type stopsThrough struct {
	hold    atomic.Bool
	onStop  func()
	mu      sync.Mutex
	held    []*http.Request
	handler http.Handler
}

func (w *world) stopsThrough() *stopsThrough {
	w.t.Helper()
	p := &stopsThrough{handler: w.lux.Handler()}
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop") {
			if p.onStop != nil {
				p.onStop()
			}
			if p.hold.Load() {
				p.mu.Lock()
				p.held = append(p.held, req.Clone(context.Background()))
				p.mu.Unlock()
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusAccepted)
				_, _ = rw.Write([]byte(`{}`))
				return
			}
		}
		p.handler.ServeHTTP(rw, req)
	}))
	w.t.Cleanup(func() {
		w.syncer.Stop()
		proxy.CloseClientConnections()
		proxy.Close()
	})
	w.syncer.Lux = lux.New(proxy.URL, "lux-key")
	return p
}

// heldStops is how many stops the proxy kept.
func (p *stopsThrough) heldStops() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.held)
}

// movedRun is a phase implementer at work in its first turn, with a
// larger size, Bigger, to move to.
func (w *world) movedRun() (task, runID, luxID string) {
	w.t.Helper()
	task, runID, luxID = w.implementing()
	mustExec(w.t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	return task, runID, luxID
}

// movedOnce checks the implementer ended its phase on size with one
// resume, the move's, telling it told: the agent heard of the move once,
// with no carry-on of lux's; nothing failed, nothing is held.
func (w *world) movedOnce(task, runID, luxID, size, told string) {
	w.t.Helper()
	w.until("the move settled, the implementer finished and review started", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND settled_at IS NOT NULL AND refused IS NULL`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) > 0
	})
	if _, _, failed := w.runFailure(runID); len(failed) != 0 {
		w.t.Errorf("run.failed %q", failed)
	}
	if got := w.escalationReason(task); got != "" {
		w.t.Errorf("escalated: %s", got)
	}
	if got := w.machineOf(runID); got["name"] != size {
		w.t.Errorf("machine %v, want %s", got, size)
	}
	r := w.lux.Runs()[0]
	want := told + "\n\n" + resumeNudgeText
	if in := w.lux.ResumeInputs(luxID); len(in) != 1 || in[0] != want {
		w.t.Errorf("resume inputs %q; want the move's one, %q", in, want)
	}
	if countOf(r.Inputs, want) != 1 || countOf(r.Inputs, movedNudge) != 0 {
		w.t.Errorf("the agent's inputs %q; want the move told once and no carry-on of lux's", r.Inputs)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2`, runID, movedNudge); n != 0 {
		w.t.Errorf("%d carry-ons of lux's left queued", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND harness_state ? 'brokenTurn'`, runID); n != 0 {
		w.t.Errorf("a broken turn is still held")
	}
}

// resumeNudgeText is phases.resumeNudge.
const resumeNudgeText = "You were paused and have been resumed. Continue the task where you left off."

// toldBigger is what a raise to Bigger tells the agent.
const toldBigger = "You now run on Bigger (4 CPUs · 16 GiB · 40 GiB). Start again any server or watcher you had running. Carry on."

// The implementer asks for a larger machine mid-turn; dude parks it, and
// the stop kills its harness, failing the turn in progress with lux's
// "process exited". That is the move's stop: the turn is neither held nor
// failed, and the agent hears of the move once, from the move's resume.
func TestADudeMachineMoveMidTurnIsNotHeldAndToldOnce(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.movedRun()
	p := w.stopsThrough()
	p.onStop = func() { w.lux.AgentExits(luxID) }
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"go test killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.movedOnce(task, runID, luxID, "Bigger", toldBigger)
	if r := w.lux.Runs()[0]; r.Resumed != 1 {
		t.Errorf("lux resumed the Run %d times, want once", r.Resumed)
	}
}

// lux preempts the implementer after dude parked it for its machine move
// and before lux applied dude's stop: lux resumes it itself, live, on the
// old size. That placement is not taken up: dude stops it again, then
// resumes it on Bigger, and the agent is told of the move once.
func TestALuxPreemptDuringADudeMachineMoveStillMovesIt(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.movedRun()
	p := w.stopsThrough()
	p.hold.Store(true)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"go test killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("dude to park the Run for its move", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'machine'`, runID) == 1
	})
	if p.heldStops() == 0 {
		t.Fatal("dude never asked lux to stop the Run")
	}
	w.lux.Move(luxID, "preempt")
	for deadline := time.Now().Add(10 * time.Second); w.lux.State(luxID) != "running" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if s := w.lux.State(luxID); s != "running" {
		t.Fatalf("lux's run is %s", s)
	}
	p.hold.Store(false)
	w.movedOnce(task, runID, luxID, "Bigger", toldBigger)
	if r := w.lux.Runs()[0]; r.Resumed != 1 || r.Stopped < 2 {
		t.Errorf("lux resumed the Run %d times and was asked to stop it %d times; want one resume, after a second stop", r.Resumed, r.Stopped)
	}
}

// lux preempts the implementer mid-turn and runs it again; before dude
// sends the carry-on its move queued, a person Resets the implementer's
// raised machine, parking the Run for that move. The carry-on is withdrawn:
// the Reset's resume tells the agent where it runs, once.
func TestAResetAfterALuxPreemptTellsTheAgentOnce(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.movedRun()
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', jsonb_build_object('sizeId','msz_big','moveId','mmv_old','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, runID)
	mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	w.lux.BeforeStart(func(id string) {
		if id == luxID {
			<-gate
		}
	})
	w.lux.Move(luxID, "preempt")
	w.until("the move's carry-on to be queued", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2 AND sent_at IS NULL`, runID, movedNudge) == 1
	})
	// lux runs it again; with no sweep, the carry-on is not sent yet.
	w.lux.BeforeStart(nil)
	release()
	deadline := time.Now().Add(10 * time.Second)
	for w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running' AND status = 'running'`, runID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if w.lux.State(luxID) != "running" {
		t.Fatalf("lux's run is %s", w.lux.State(luxID))
	}
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 200 || out["moved"] != true {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.movedOnce(task, runID, luxID, "Standard", "Moved back to Standard by Resetter.")
}
