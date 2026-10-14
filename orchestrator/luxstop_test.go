package orchestrator_test

// lux stopping a phase Run under its agent (phases/luxstop.go): a failure
// says lux's reason first, in either arrival order; a move fails nothing,
// and the agent is told to carry on; a person's resume of a Run lux already
// runs is delivered to it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// implementing delivers a task whose implementer hangs mid-turn until told
// something, then commits and finishes; it returns the task, the
// implementer's Run and its lux Run once the agent is at work.
func (w *world) implementing() (task, runID, luxID string) {
	w.t.Helper()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true, WakeOnInput: true, Reply: "Done.", Commit: map[string]string{"A.md": "a\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	task = w.task()
	w.deliver(task)
	w.until("the implementer to be working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs WHERE task_id = $1
			AND phase = 'implement' AND agent_busy_at IS NOT NULL AND lux_state = 'running'`, task).Scan(&runID, &luxID)
		return luxID != ""
	})
	return task, runID, luxID
}

// runFailure is a Run's status and error, and its run.failed events' errors.
func (w *world) runFailure(runID string) (status, errText string, failed []string) {
	w.t.Helper()
	ctx := context.Background()
	if err := w.owner.QueryRow(ctx, `SELECT status::text, COALESCE(error, '') FROM runs WHERE id = $1`, runID).Scan(&status, &errText); err != nil {
		w.t.Fatal(err)
	}
	rows, err := w.owner.Query(ctx, `SELECT payload->>'error' FROM events WHERE run_id = $1 AND event_type = 'run.failed' ORDER BY cursor`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			w.t.Fatal(err)
		}
		failed = append(failed, e)
	}
	if err := rows.Err(); err != nil {
		w.t.Fatal(err)
	}
	return status, errText, failed
}

// escalationError is the error the task's last escalation shows.
func (w *world) escalationError(task string) string {
	w.t.Helper()
	var e string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->'detail'->>'error', '') FROM events WHERE task_id = $1
		AND event_type = 'question.asked' AND payload->>'kind' = 'escalation' ORDER BY cursor DESC LIMIT 1`, task).Scan(&e); err != nil {
		w.t.Fatal(err)
	}
	return e
}

// lux stops the implementer over its disk limit while it builds: the Run
// fails saying so with lux's numbers, the agent's own error after it, and
// the escalation says the same — whether the agent's broken turn or lux's
// stop reaches dude first.
func TestALuxDiskStopIsNamedWithItsNumbersInEitherOrder(t *testing.T) {
	const words = "lux stopped the Run: disk limit exceeded (10.8 GB of 10 GiB)"
	for _, agentFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "lux first", true: "agent first"}[agentFirst], func(t *testing.T) {
			w := newWorld(t)
			task, runID, luxID := w.implementing()
			if agentFirst {
				w.lux.AgentExits(luxID)
				w.until("the broken turn held for lux's reason", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND harness_state ? 'brokenTurn'`, runID) == 1
				})
				w.lux.ExceedDisk(luxID, 10_790_000_000, 10<<30)
			} else {
				w.lux.ExceedDisk(luxID, 10_790_000_000, 10<<30)
				w.until("lux stopping the Run", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'stopping'`, runID) == 1
				})
				w.lux.AgentExits(luxID)
			}
			w.lux.EndStopping(luxID, "failed", "disk limit exceeded")
			w.until("the task to need a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
			status, errText, failed := w.runFailure(runID)
			want := words + "; the agent's turn failed: " + fakelux.ProcessExited
			if status != "failed" || errText != want {
				t.Errorf("run %s: %q; want failed with %q", status, errText, want)
			}
			if len(failed) != 1 || failed[0] != want {
				t.Errorf("run.failed errors %q; want one, %q", failed, want)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'
				AND payload->>'luxStop' = 'disk' AND (payload->>'usedBytes')::bigint = 10790000000
				AND (payload->>'limitBytes')::bigint = 10737418240 AND payload->>'agentError' = '`+fakelux.ProcessExited+`'`, runID); n != 1 {
				t.Errorf("run.failed does not carry lux's stop, its numbers and the agent's error")
			}
			if got := w.escalationReason(task); got != "implement_failed" {
				t.Errorf("escalated for %q, want implement_failed", got)
			}
			if got := w.escalationError(task); got != want {
				t.Errorf("the escalation says %q; want %q", got, want)
			}
		})
	}
}

// AWS reclaims the implementer's spot host: lux moves the Run and resumes
// it elsewhere. Its broken turn fails nothing; once lux runs it again the
// agent is told its host was replaced, carries on, and the phase completes.
func TestAPreemptedRunCarriesOnOnceLuxRunsItAgain(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.implementing()
	w.lux.Move(luxID, "preempt")
	w.until("the implementer to finish, and review to start", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) > 0
	})
	if _, _, failed := w.runFailure(runID); len(failed) != 0 {
		t.Errorf("a move failed the run: %q", failed)
	}
	if got := w.escalationReason(task); got != "" {
		t.Errorf("a move escalated: %s", got)
	}
	r := w.lux.Runs()[0]
	if got := countOf(r.Inputs, movedNudge); got != 1 {
		t.Errorf("the moved agent was told to carry on %d times: %q", got, r.Inputs)
	}
	if slices.Contains(w.lux.CallsOf(luxID), "resume") || !r.Pushed {
		t.Errorf("calls %v, pushed %v; want lux's own resume alone, and the work pushed", w.lux.CallsOf(luxID), r.Pushed)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.warning' AND payload->>'luxStop' = 'preempt'
		AND payload->>'message' = 'lux stopped the Run to move it: its spot host was reclaimed; its turn was cut off, and it is told to carry on once it runs again'`, runID); n != 1 {
		t.Errorf("%d warnings saying the move", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, task); n != 1 {
		t.Errorf("%d implementers", n)
	}
}

// lux moves the implementer and does not get it running again in time:
// the Run fails as any broken turn would, naming the move. lux then runs
// it after all, and a person's resume from the escalation is delivered to
// the Run lux has running — not held waiting for it to stop.
func TestAMoveLuxNeverEndsFailsNamingItAndAResumeReachesTheRunLuxRuns(t *testing.T) {
	w := newWorld(t)
	w.syncer.MoveWait = 500 * time.Millisecond
	task, runID, luxID := w.implementing()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	// lux's new placement waits for a host: with none assigned there is no
	// live placement, so the stop that keeps the failed Run finds nothing
	// to stop (lux requestStop), and lux places it once a host frees up.
	w.lux.BeforeAssign(func(id string) {
		if id == luxID {
			<-gate
		}
	})
	w.lux.Move(luxID, "preempt")
	w.until("the task to need a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
	status, errText, failed := w.runFailure(runID)
	const want = "lux stopped the Run to move it: its spot host was reclaimed, and it was not running again within 500ms; " +
		"the agent's turn failed: " + fakelux.ProcessExited
	if status != "failed" || errText != want || len(failed) != 1 || failed[0] != want {
		t.Errorf("run %s: %q, run.failed %q; want failed once with %q", status, errText, failed, want)
	}
	if got := w.escalationError(task); got != want {
		t.Errorf("the escalation says %q; want %q", got, want)
	}
	if got := w.escalationReason(task); got != "implement_failed" {
		t.Errorf("escalated for %q, want implement_failed", got)
	}
	w.until("the failed run to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, runID) == 1
	})
	// lux gets it running after all, its agent idle and waiting.
	w.lux.BeforeAssign(nil)
	release()
	deadline := time.Now().Add(10 * time.Second)
	for w.lux.State(luxID) != "running" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := w.lux.State(luxID); s != "running" {
		t.Fatalf("lux's run is %s", s)
	}
	if status, body := w.call("/internal/tasks/"+task+"/decide", map[string]any{"action": "resume", "note": "Carry on, please."}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the same implementer to finish, and review to start", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) > 0
	})
	r := w.lux.Runs()[0]
	if slices.Contains(w.lux.CallsOf(luxID), "resume") {
		t.Errorf("dude asked lux to resume a Run lux was running: calls %v", w.lux.CallsOf(luxID))
	}
	if got := countOf(r.Inputs, "Carry on, please."); got != 1 {
		t.Errorf("the note reached the agent %d times: %q", got, r.Inputs)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, task); n != 1 {
		t.Errorf("%d implementers", n)
	}
}

// As above, but lux's new placement starts running between the resume's
// read of the Run (still resuming) and anything after it: the running lux
// then reports is what dude follows, and the note reaches the agent.
func TestAResumeReachesAMovedRunThatStartsRunningWhileDudeReadsIt(t *testing.T) {
	w := newWorld(t)
	w.syncer.MoveWait = 500 * time.Millisecond
	task, runID, luxID := w.implementing()
	assign, start := make(chan struct{}), make(chan struct{})
	var onceA, onceS sync.Once
	t.Cleanup(func() { onceA.Do(func() { close(assign) }); onceS.Do(func() { close(start) }) })
	w.lux.BeforeAssign(func(id string) {
		if id == luxID {
			<-assign
		}
	})
	w.lux.BeforeStart(func(id string) {
		if id == luxID {
			<-start
		}
	})
	w.lux.Move(luxID, "preempt")
	w.until("the failed run to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, runID) == 1
	})
	// A host is assigned; the placement waits to start its workload.
	onceA.Do(func() { close(assign) })
	handler := w.lux.Handler()
	var armed atomic.Bool
	armed.Store(true)
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.Path == "/v1/runs/"+luxID && armed.CompareAndSwap(true, false) {
			// dude reads the Run as lux has it now; lux runs it before
			// dude has the answer.
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			onceS.Do(func() { close(start) })
			for deadline := time.Now().Add(10 * time.Second); w.lux.State(luxID) != "running" && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}
			for k, v := range rec.Header() {
				rw.Header()[k] = v
			}
			rw.WriteHeader(rec.Code)
			_, _ = rw.Write(rec.Body.Bytes())
			return
		}
		handler.ServeHTTP(rw, req)
	}))
	t.Cleanup(func() {
		w.syncer.Stop()
		proxy.CloseClientConnections()
		proxy.Close()
	})
	w.syncer.Lux = lux.New(proxy.URL, "lux-key")
	if status, body := w.call("/internal/tasks/"+task+"/decide", map[string]any{"action": "resume", "note": "Carry on, please."}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the same implementer to finish, and review to start", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) > 0
	})
	if armed.Load() {
		t.Fatal("dude never read the Run while lux placed it")
	}
	r := w.lux.Runs()[0]
	if got := countOf(r.Inputs, "Carry on, please."); got != 1 {
		t.Errorf("the note reached the agent %d times: %q", got, r.Inputs)
	}
	if slices.Contains(w.lux.CallsOf(luxID), "resume") {
		t.Errorf("dude asked lux to resume a Run lux was running: calls %v", w.lux.CallsOf(luxID))
	}
}

// dude stops with a moved implementer's hold saved; lux gets the Run
// running while dude is down, and dude comes back after the move's bound.
// What lux said meanwhile is read before the hold is judged: nothing
// fails, the agent is told to carry on once, and the phase completes.
func TestAMoveLuxEndedWhileDudeWasDownIsNotFailedOnItsReturn(t *testing.T) {
	w := newWorld(t)
	w.syncer.MoveWait = 500 * time.Millisecond
	task, runID, luxID := w.implementing()
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
	w.until("the move to be held", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND harness_state->'brokenTurn'->>'moved' = 'preempt'`, runID) == 1
	})
	w.syncer.Stop()
	release()
	for deadline := time.Now().Add(10 * time.Second); w.lux.State(luxID) != "running" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if s := w.lux.State(luxID); s != "running" {
		t.Fatalf("lux's run is %s", s)
	}
	time.Sleep(700 * time.Millisecond)
	w.syncer = &phases.Syncer{DB: w.syncer.DB, Lux: w.syncer.Lux, Forges: w.syncer.Forges, Log: quiet, Agent: w.syncer.Agent,
		KeepFor: w.syncer.KeepFor, MoveWait: 500 * time.Millisecond}
	t.Cleanup(w.syncer.Stop)
	w.until("the same implementer to finish, and review to start", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) > 0
	})
	if _, _, failed := w.runFailure(runID); len(failed) != 0 {
		t.Errorf("run.failed %q", failed)
	}
	if got := w.escalationReason(task); got != "" {
		t.Errorf("escalated: %s", got)
	}
	if got := countOf(w.lux.Runs()[0].Inputs, movedNudge); got != 1 {
		t.Errorf("told to carry on %d times: %q", got, w.lux.Runs()[0].Inputs)
	}
}

// movedNudge is what a moved agent is told (phases.movedNudge).
const movedNudge = "Your host was replaced: lux moved you to another one, and your turn was cut off. Your workspace " +
	"and conversation are as you left them; processes you had running are not. Carry on with your task where you left off."

// countOf is how many of ins are exactly in.
func countOf(ins []string, in string) int {
	n := 0
	for _, s := range ins {
		if s == in {
			n++
		}
	}
	return n
}

// An agent whose process exits with no stop of lux's behind it fails as
// any failed turn: the agent's error, as it was, once lux has had its time
// to say otherwise.
func TestAnAgentProcessExitingOnItsOwnFailsWithItsError(t *testing.T) {
	w := newWorld(t)
	w.syncer.HeldWait = 300 * time.Millisecond
	task, runID, luxID := w.implementing()
	w.lux.AgentExits(luxID)
	w.until("the task to need a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
	status, errText, failed := w.runFailure(runID)
	if want := "the agent's turn failed: " + fakelux.ProcessExited; status != "failed" || errText != want || len(failed) != 1 || failed[0] != want {
		t.Errorf("run %s: %q, run.failed %q; want failed with %q", status, errText, failed, want)
	}
	if got := w.escalationError(task); got != errText {
		t.Errorf("the escalation says %q", got)
	}
}

// A person pauses the implementer and resumes it before lux has applied
// the stop it accepted: lux still runs the same placement, which is not
// one lux moved. It is not taken up as live; once the stop lands, dude
// resumes it in lux and the same Run finishes, failing nothing.
func TestAResumeBeforeLuxAppliedAPauseStillResumesTheRunInLux(t *testing.T) {
	w := newWorld(t)
	task, runID, luxID := w.implementing()
	handler := w.lux.Handler()
	var holding atomic.Bool
	holding.Store(true)
	held := make(chan *http.Request, 4)
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		// lux accepts the stop; it is applied when the test says.
		if holding.Load() && req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop") {
			select {
			case held <- req.Clone(context.Background()):
			default:
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusAccepted)
			_, _ = rw.Write([]byte(`{}`))
			return
		}
		handler.ServeHTTP(rw, req)
	}))
	t.Cleanup(func() {
		// Followers' streams through the proxy end first, or Close waits on them.
		w.syncer.Stop()
		proxy.CloseClientConnections()
		proxy.Close()
	})
	w.syncer.Lux = lux.New(proxy.URL, "lux-key")
	if status, body := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	w.until("dude to record the pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND control = 'resume'`, runID); n != 1 {
		t.Fatalf("a Run lux had yet to stop was taken up as live")
	}
	// lux applies the stop it accepted.
	holding.Store(false)
	select {
	case req := <-held:
		handler.ServeHTTP(httptest.NewRecorder(), req)
	default:
		t.Fatal("dude never asked lux to stop the Run")
	}
	w.until("the same implementer to finish, and review to start", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) > 0
	})
	if !slices.Contains(w.lux.CallsOf(luxID), "resume") {
		t.Errorf("dude never resumed the stopped Run in lux: calls %v", w.lux.CallsOf(luxID))
	}
	if _, _, failed := w.runFailure(runID); len(failed) != 0 {
		t.Errorf("run.failed %q", failed)
	}
}
