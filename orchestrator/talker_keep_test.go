package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// talking is one agent people talk to — a session's brainstorm or a task's
// conductor — and how a person writes to it.
type talking struct {
	*world
	kind string
	// write sends a message, returning the HTTP status and the Run it reached.
	write func(text string) (int, string)
	// latest is the newest Run of its session or task.
	latest func() string
	// park is the dude_pause its warm period's end parks it with.
	park string
}

// talkers are a brainstorm and a conductor, each having answered a first
// message, warm for an hour so only a stop can park them.
func talkers(t *testing.T) map[string]func(t *testing.T) (*talking, string) {
	return map[string]func(t *testing.T) (*talking, string){
		"brainstorm": func(t *testing.T) (*talking, string) {
			s := newSessionWorld(t)
			s.syncer.ConductorWarm = time.Hour
			id := s.session()
			tk := &talking{world: s.world, kind: "brainstorm", park: "session"}
			tk.write = func(text string) (int, string) {
				status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": text})
				run, _ := out["runId"].(string)
				return status, run
			}
			tk.latest = func() string { run, _ := s.brainstorm(id); return run }
			tk.write("where would metering live?")
			return tk, s.started(id)
		},
		"conductor": func(t *testing.T) (*talking, string) {
			w := conductorWorld(t)
			w.syncer.ConductorWarm = time.Hour
			task := w.task()
			tk := &talking{world: w, kind: "conductor", park: "conductor"}
			tk.write = func(text string) (int, string) {
				status, out := w.chat(task, text)
				run, _ := out["runId"].(string)
				return status, run
			}
			tk.latest = func() string { run, _, _ := w.conductor(task); return run }
			_, run := tk.write("what changed?")
			w.until("the conductor's answer", func() bool { return len(w.said(run)) == 1 })
			return tk, run
		},
	}
}

// stopOnItsOwn has lux report the Run's container gone, as state says,
// with no reason and without dude asking.
func (tk *talking) stopOnItsOwn(run, state string) {
	tk.t.Helper()
	id := tk.luxRunOf(run)
	switch state {
	case "stopped":
		tk.lux.StopOnItsOwn(id)
	case "succeeded":
		tk.lux.Succeed(id)
	case "failed":
		tk.lux.Crash(id)
	case "lost":
		tk.lux.Lose(id)
	case "terminated":
		tk.lux.CancelInLux(id)
	default:
		tk.t.Fatalf("no way to stop a Run as %s", state)
	}
}

// recorded waits, without sweeping, for the Run's follower to record lux's state.
func (tk *talking) recorded(run, state string) {
	tk.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = $2`, run, state) == 0 {
		if time.Now().After(deadline) {
			tk.t.Fatalf("lux's %s of %s was never recorded:\n%s", state, run, tk.describeRuns())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sessionID is the harness session the agent's session events name, and
// how many it named.
func (tk *talking) sessionIDs(run string) []string {
	tk.t.Helper()
	rows, err := tk.owner.Query(context.Background(), `SELECT payload->>'externalSessionId' FROM events
		WHERE run_id = $1 AND event_type = 'agent.session.started' ORDER BY cursor`, run)
	if err != nil {
		tk.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// resumedAnswering waits for the same Run, resumed in the same lux Run, to
// answer text.
func (tk *talking) resumedAnswering(run, text string) {
	tk.t.Helper()
	tk.until("the same Run to answer after its resume", func() bool {
		said := tk.said(run)
		return len(said) >= 2 && strings.Contains(said[len(said)-1], text)
	})
	luxRuns := 0
	for _, r := range tk.lux.Runs() {
		if strings.Contains(string(r.Spec), `"dude.run":"`+run+`"`) {
			luxRuns++
			if r.Resumed < 1 {
				tk.t.Errorf("lux Run %s was never resumed", r.ID)
			}
		}
	}
	if luxRuns != 1 {
		tk.t.Errorf("%d lux Runs for %s, want the one", luxRuns, run)
	}
	if latest := tk.latest(); latest != run {
		tk.t.Errorf("a new Run %s, want %s kept", latest, run)
	}
}

// A brainstorm or conductor whose container stops on its own, in any state
// lux can resume (stopped, succeeded, failed, lost), is parked as its warm
// period's end parks it, not ended: the next message resumes the same lux
// Run, whose harness reloads the same session.
func TestATalkerWhoseContainerStopsIsParkedAndResumed(t *testing.T) {
	for kind, start := range talkers(t) {
		for _, state := range []string{"stopped", "succeeded", "failed", "lost"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				tk, run := start(t)
				before := tk.sessionIDs(run)
				tk.stopOnItsOwn(run, state)
				tk.until("the Run parked", func() bool {
					return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = $2`, run, tk.park) == 1
				})
				if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'
					AND payload->>'reason' = $2 AND payload->>'stopped' = $3`, run, tk.park, state); n != 1 {
					t.Errorf("%d run.parked saying its container stopped (%s), want 1", n, state)
				}
				if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type IN ('run.completed', 'run.failed')`, run); n != 0 {
					t.Errorf("the stopped Run was ended:\n%s", tk.describeRuns())
				}
				status, reached := tk.write("and the tests?")
				if status != 200 || reached != run {
					t.Fatalf("the message after the stop: %d reached %q, want %s", status, reached, run)
				}
				tk.resumedAnswering(run, "and the tests?")
				if after := tk.sessionIDs(run); len(before) != 1 || len(after) != 1 || after[0] != before[0] {
					t.Errorf("harness sessions %v, then %v: want the one", before, after)
				}
			})
		}
	}
}

// A message that reaches a talker whose stop lux reported and the sweep has
// not parked yet is queued on that Run, which resumes for it.
func TestAMessageBeforeTheStopIsSweptQueuesOnTheSameRun(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			tk.stopOnItsOwn(run, "failed")
			tk.recorded(run, "failed")
			status, reached := tk.write("are you there?")
			if status != 200 || reached != run {
				t.Fatalf("the message: %d reached %q, want it queued for %s", status, reached, run)
			}
			tk.resumedAnswering(run, "are you there?")
		})
	}
}

// A message lux accepted and the agent never read is gone with a crashed
// harness: once the Run is parked it is sent again, and the Run resumes on
// its own to answer it.
func TestAMessageUnreadWhenTheContainerStopsIsAnsweredAfterTheResume(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			tk.lux.InputGate = make(chan struct{})
			status, reached := tk.write("did the build pass?")
			if status != 200 || reached != run {
				t.Fatalf("the message: %d reached %q, want %s", status, reached, run)
			}
			tk.until("the message sent to lux", func() bool {
				return tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND sent_at IS NOT NULL
					AND delivered_at IS NULL`, run) == 1
			})
			tk.stopOnItsOwn(run, "failed")
			close(tk.lux.InputGate)
			tk.lux.InputGate = nil
			tk.resumedAnswering(run, "did the build pass?")
		})
	}
}

// One lux ended for good (terminated), or no longer has, is ended as
// before: the next message starts a new Run, which answers it.
func TestATalkerLuxCannotResumeIsEndedAndReplaced(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind+"/terminated", func(t *testing.T) {
			tk, run := start(t)
			tk.stopOnItsOwn(run, "terminated")
			tk.until("the Run ended", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, run) == 1
			})
			status, next := tk.write("and the tests?")
			if status != 201 || next == run {
				t.Fatalf("the message after the end: %d reached %q, want a new Run", status, next)
			}
			tk.until("the new Run's answer", func() bool {
				said := tk.said(next)
				return len(said) == 1 && strings.Contains(said[0], "and the tests?")
			})
		})
		t.Run(kind+"/404", func(t *testing.T) {
			tk, run := start(t)
			tk.stopOnItsOwn(run, "stopped")
			tk.until("the Run parked", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, run) == 1
			})
			tk.lux.Forget()
			if status, reached := tk.write("and the tests?"); status != 200 || reached != run {
				t.Fatalf("the message to the parked Run: %d reached %q", status, reached)
			}
			tk.until("a new Run to answer the message", func() bool {
				next := tk.latest()
				if next == run {
					return false
				}
				said := tk.said(next)
				return len(said) == 1 && strings.Contains(said[0], "and the tests?")
			})
			if n := tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run); n != 1 {
				t.Errorf("the forgotten Run:\n%s", tk.describeRuns())
			}
		})
	}
}
