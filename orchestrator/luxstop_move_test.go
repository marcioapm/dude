package orchestrator_test

// lux's own moves (phases/luxstop.go) beside a machine move dude asks for
// (#130's request_machine and Reset): the turn the move cuts off belongs
// to dude's move, which resumes the Run on its new size and tells the agent
// once; nothing is held, failed or taken up live twice.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// stopsThrough routes dude's calls to lux through a proxy that calls
// onStop for each stop dude asks for and, while hold is set, answers the
// stop as lux accepting it without passing it on, keeping it for release.
// onGet, once set, is called once, at dude's next read of a Run, before
// lux answers it. While refuseCarryOn is set, an input carrying lux's
// carry-on is answered 503 without reaching lux, and counted in refused.
// With showRequested, a stop held is lux's accepted stop of the placement
// it reached: lux's reads of the Run show that placement's stopRequestedAt
// while it stays live, as lux records the request before it is applied.
type stopsThrough struct {
	hold          atomic.Bool
	showRequested bool
	onStop        func()
	onGet         atomic.Pointer[func()]
	refuseCarryOn atomic.Bool
	refused       atomic.Int32
	mu            sync.Mutex
	held          []*http.Request
	// Stops held, by the epoch of the placement each reached.
	stopsOf map[int]int
	handler http.Handler
}

func (w *world) stopsThrough() *stopsThrough {
	w.t.Helper()
	p := &stopsThrough{handler: w.lux.Handler(), stopsOf: map[int]int{}}
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && strings.Count(req.URL.Path, "/") == 3 && strings.HasPrefix(req.URL.Path, "/v1/runs/") {
			if fn := p.onGet.Swap(nil); fn != nil {
				(*fn)()
			}
			if p.showRequested {
				rw.Header().Set("Content-Type", "application/json")
				_, _ = rw.Write(p.withRequested(p.read(req)))
				return
			}
		}
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/input") && p.refuseCarryOn.Load() {
			body, _ := io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(body))
			if strings.Contains(string(body), "Your host was replaced") {
				p.refused.Add(1)
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusServiceUnavailable)
				_, _ = rw.Write([]byte(`{"error":{"code":"unavailable","message":"lux is restarting"}}`))
				return
			}
		}
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop") {
			if p.onStop != nil {
				p.onStop()
			}
			if p.hold.Load() {
				epoch := 0
				if raw, ok := p.read(req)["epoch"].(float64); ok {
					epoch = int(raw)
				}
				p.mu.Lock()
				p.held = append(p.held, req.Clone(context.Background()))
				p.stopsOf[epoch]++
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

// read is lux's record of the Run req is about, as fake lux has it.
func (p *stopsThrough) read(req *http.Request) map[string]any {
	path := strings.TrimSuffix(req.URL.Path, "/stop")
	get, _ := http.NewRequest(http.MethodGet, path, nil)
	get.Header.Set("Authorization", req.Header.Get("Authorization"))
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, get)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// withRequested is run with stopRequestedAt on each placement a held stop
// reached.
func (p *stopsThrough) withRequested(run map[string]any) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	placements, _ := run["placements"].([]any)
	for _, raw := range placements {
		if pl, ok := raw.(map[string]any); ok {
			if epoch, _ := pl["epoch"].(float64); p.stopsOf[int(epoch)] > 0 && pl["stopRequestedAt"] == nil {
				pl["stopRequestedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
	}
	b, _ := json.Marshal(run)
	return b
}

// stops is how many held stops reached each epoch's placement.
func (p *stopsThrough) stops() map[int]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.stopsOf)
}

// release has lux apply the stop it accepted last.
func (p *stopsThrough) release() {
	p.hold.Store(false)
	p.mu.Lock()
	var last *http.Request
	if n := len(p.held); n > 0 {
		last = p.held[n-1]
	}
	p.mu.Unlock()
	if last != nil {
		p.handler.ServeHTTP(httptest.NewRecorder(), last)
	}
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
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2 AND sent_at IS NULL AND failed_at IS NULL`, runID, movedNudge); n != 0 {
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
	w.waitLuxRunning(luxID)
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
	task, runID, luxID := w.raisedAtWork()
	release := w.holdLuxStart(luxID)
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

// As above, but dude's first send of the carry-on fails before it reaches
// lux, leaving it claimed for a retry, when the Reset parks the Run. The
// claimed carry-on is withdrawn too: the agent hears the Reset's move once
// and no carry-on, before or after the move's resume.
func TestAResetAfterAFailedCarryOnSendTellsTheAgentOnce(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.raisedAtWork()
	p := w.stopsThrough()
	p.refuseCarryOn.Store(true)
	release := w.holdLuxStart(luxID)
	w.lux.Move(luxID, "preempt")
	w.until("the move's carry-on to be queued", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2 AND sent_at IS NULL`, runID, movedNudge) == 1
	})
	w.lux.BeforeStart(nil)
	release()
	w.until("dude's send of the carry-on to fail, claimed", func() bool {
		return p.refused.Load() > 0 && w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2
			AND claimed_at IS NOT NULL AND sent_at IS NULL`, runID, movedNudge) == 1
	})
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 200 || out["moved"] != true {
		t.Fatalf("reset: %d %v", status, out)
	}
	p.refuseCarryOn.Store(false)
	w.movedOnce(task, runID, luxID, "Standard", "Moved back to Standard by Resetter.")
}

// raisedAtWork is movedRun's implementer on Bigger, raised in its task, so
// a person's Reset moves it back to Standard.
func (w *world) raisedAtWork() (task, runID, luxID string) {
	w.t.Helper()
	task, runID, luxID = w.movedRun()
	mustExec(w.t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', jsonb_build_object('sizeId','msz_big','moveId','mmv_old','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, runID)
	mustExec(w.t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
	return task, runID, luxID
}

// pausedWhileLuxMovesIt has lux preempt the implementer mid-turn and a
// person pause it before lux has it running again; the pause's stop never
// reaches lux, so lux then runs its own placement, live, while dude has the
// Run paused.
func (w *world) pausedWhileLuxMovesIt(p *stopsThrough, runID, luxID string) {
	w.t.Helper()
	release := w.holdLuxStart(luxID)
	w.lux.Move(luxID, "preempt")
	w.until("the move's carry-on to be queued", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2 AND sent_at IS NULL`, runID, movedNudge) == 1
	})
	p.hold.Store(true)
	if status, body := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		w.t.Fatalf("pause: %d %v", status, body)
	}
	w.until("dude to record the pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND control = 'none'`, runID) == 1
	})
	p.hold.Store(false)
	w.lux.BeforeStart(nil)
	release()
	w.waitLuxRunning(luxID)
}

// reset is a person's Reset of the implementer's raised machine, through
// api (w.api when ""), as its answer's status and whether it moved a Run.
func (w *world) reset(api, task string) (int, any) {
	if api == "" {
		api = w.api
	}
	b, _ := json.Marshal(map[string]any{"taskId": task, "role": "implementer"})
	req, _ := http.NewRequest("POST", api+"/internal/agent-machines/reset", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	req.Header.Set("X-Dude-Actor", "key_resetter_"+w.org)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out["moved"]
}

// A person resumes the implementer lux moved and placed again itself, and
// a person Resets its machine while dude reads lux's placement to take it
// up live: the Reset is committed before the take-up's. The Run is not
// taken up on its old size: lux is asked to stop it, and the resume that
// follows moves it to Standard and tells the agent once.
func TestAResetCommittedDuringALiveTakeUpMovesTheRun(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.raisedAtWork()
	w.person("Resetter")
	p := w.stopsThrough()
	w.pausedWhileLuxMovesIt(p, runID, luxID)
	var resetStatus int
	var moved any
	reset := func() { resetStatus, moved = w.reset("", task) }
	p.onGet.Store(&reset)
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("dude to read lux's placement", func() bool { return p.onGet.Load() == nil })
	if resetStatus != 200 || moved != true {
		t.Fatalf("reset: %d moved %v", resetStatus, moved)
	}
	w.movedOnce(task, runID, luxID, "Standard", "Moved back to Standard by Resetter.")
}

// readGated is lux for an API whose reads of a Run wait for release, once
// entered says so.
type readGated struct {
	lux.Client
	entered, release chan struct{}
	once             sync.Once
}

func (g *readGated) Get(ctx context.Context, id string) (lux.Run, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	return g.Client.Get(ctx, id)
}

// As above, but the Reset read the Run paused before the take-up, and
// records its move after the take-up committed: the Reset finds the Run
// running and parks it for its move, as a Reset of a running Run does.
func TestAResetThatReadTheRunPausedBeforeALiveTakeUpParksIt(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.raisedAtWork()
	w.person("Resetter")
	p := w.stopsThrough()
	w.pausedWhileLuxMovesIt(p, runID, luxID)
	gated := &readGated{Client: w.syncer.Lux, entered: make(chan struct{}), release: make(chan struct{})}
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(gated.release) }) })
	srv := httptest.NewServer((&api.Server{DB: w.app, Lux: gated, Token: "svc", Log: quiet, Kick: func() {}}).Handler())
	t.Cleanup(srv.Close)
	type answer struct {
		status int
		moved  any
	}
	done := make(chan answer, 1)
	go func() {
		status, moved := w.reset(srv.URL, task)
		done <- answer{status, moved}
	}()
	<-gated.entered
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the Run taken up live", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
	released.Do(func() { close(gated.release) })
	if a := <-done; a.status != 200 || a.moved != true {
		t.Fatalf("reset: %d moved %v", a.status, a.moved)
	}
	w.movedOnce(task, runID, luxID, "Standard", "Moved back to Standard by Resetter.")
}

// lux preempts the implementer dude parked for its machine move, before
// lux applied dude's stop, and runs it again itself (epoch 2). dude asks
// lux to stop that placement once: lux records the request, and sweeps
// after see it on the placement and ask nothing more, the Run still
// paused. lux preempts it again before the stop lands (epoch 3, asked
// nothing): dude asks lux to stop that one, once. Once lux applies the
// stop, dude resumes the Run on Bigger, telling the agent once.
func TestALuxMoveDudeStopsIsAskedOncePerPlacement(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.movedRun()
	p := w.stopsThrough()
	p.hold.Store(true)
	p.showRequested = true
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"go test killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("dude to park the Run for its move", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'machine'`, runID) == 1
	})
	preempted := func(epoch int) {
		t.Helper()
		w.lux.Move(luxID, "preempt")
		w.waitLuxRunning(luxID)
		if r := w.lux.Runs()[0]; w.lux.State(luxID) != "running" || r.Epoch != epoch {
			t.Fatalf("lux's run is %s in epoch %d, want running in %d", w.lux.State(luxID), r.Epoch, epoch)
		}
		w.until(fmt.Sprintf("dude to ask lux to stop epoch %d", epoch), func() bool { return p.stops()[epoch] > 0 })
		for range 10 {
			w.pump()
		}
		if got := p.stops()[epoch]; got != 1 {
			t.Errorf("lux was asked to stop epoch %d %d times, want once", epoch, got)
		}
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID); n != 1 {
			t.Fatalf("the Run is not paused for its move at epoch %d", epoch)
		}
	}
	preempted(2)
	preempted(3)
	p.release()
	w.movedOnce(task, runID, luxID, "Bigger", toldBigger)
	r := w.lux.Runs()[0]
	if resources := w.lux.ResumeResourcesOf(luxID); r.Resumed != 1 || len(resources) != 1 || resources[0] == nil {
		t.Errorf("lux resumed the Run %d times with resources %s; want once, sized", r.Resumed, resources)
	}
}
