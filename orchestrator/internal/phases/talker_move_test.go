package phases

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// sentLux is eventsLux that records what deliverDirectives sends.
type sentLux struct {
	*eventsLux
	sentMu sync.Mutex
	sent   []string
}

func (f *sentLux) Input(_ context.Context, _ string, in lux.InputRequest) error {
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	f.sent = append(f.sent, in.Text)
	return nil
}

func (f *sentLux) said() []string {
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	return append([]string(nil), f.sent...)
}

// talkerWorld is newHarnessWorld's Run as a task's conductor, or a
// session's agent, mid-turn on epoch 1.
func talkerWorld(t *testing.T, role string) *harnessWorld {
	w := newHarnessWorld(t)
	switch role {
	case "conductor":
		mustOwner(t, w, `UPDATE runs SET phase = NULL, role = 'conductor', kind = 'agent' WHERE id = $1`, w.run.ID)
	case "brainstorm":
		mustOwner(t, w, `INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_' || $1, $1, 'S')`, w.run.Org)
		mustOwner(t, w, `UPDATE runs SET phase = NULL, role = 'brainstorm', kind = 'agent', session_id = 'ssn_' || organization_id,
			project_id = NULL, task_id = NULL WHERE id = $1`, w.run.ID)
		w.run.SessionID, w.run.ProjectID, w.run.TaskID = "ssn_"+w.run.Org, "", ""
	}
	w.run.Phase, w.run.Role = "", role
	w.restart()
	return w
}

// turnCutOff is the agent's turn breaking with its container, on epoch 1.
func (w *harnessWorld) turnCutOff() {
	w.t.Helper()
	w.feed(processExited)
}

// A talker's turn that lux's move cuts off (a preemption, a drain, a
// migrate) is no failure: nothing is said failed or counted, the talker is
// not parked, and once lux runs the new placement the agent is told to
// carry on, through the sweep. An operator's stop, or an exit lux never
// explains, is the agent's failed turn, parked and counted as before.
func TestATalkersTurnLuxMovesIsCarriedOnNotParked(t *testing.T) {
	for _, role := range []string{"conductor", "brainstorm"} {
		for _, reason := range []string{"preempt", "drain", "migrate"} {
			t.Run(role+"/"+reason, func(t *testing.T) {
				w := talkerWorld(t, role)
				fake := &sentLux{eventsLux: &eventsLux{}}
				w.s.Lux = fake
				t.Cleanup(w.s.Stop)
				w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": reason})
				w.turnCutOff()
				w.luxFrame(1, "state", map[string]any{"state": "stopped", "reason": reason})
				w.luxFrame(2, "state", map[string]any{"state": "resuming", "reason": "auto-resume after " + reason})
				w.feed()
				if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type IN ('run.failed', 'run.parked')`, w.run.ID); n != 0 {
					t.Errorf("%d run.failed or run.parked for a move", n)
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND control = 'none'
					AND dude_pause IS NULL AND NOT harness_state ? 'failedTurns'`, w.run.ID); n != 1 {
					t.Errorf("the talker was parked or counted a failure for a move")
				}
				if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2`, w.run.ID, movedNudge); n != 1 {
					t.Fatalf("%d carry-ons queued, want 1", n)
				}
				w.luxFrame(2, "state", map[string]any{"state": "running"})
				w.feed()
				if _, err := w.s.advance(context.Background(), w.swept()); err != nil {
					t.Fatal(err)
				}
				if got := fake.said(); len(got) != 1 || got[0] != movedNudge {
					t.Errorf("sent %q once lux runs it again, want the carry-on", got)
				}
			})
		}
		for _, c := range []struct{ name, stop string }{{"an operator's stop", "stop"}, {"no stop lux said", ""}} {
			t.Run(role+"/"+c.name, func(t *testing.T) {
				w := talkerWorld(t, role)
				if c.stop != "" {
					w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": c.stop})
				}
				w.turnCutOff()
				if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'
					AND payload->'failedTurns' = '1' AND payload->'kept' = 'true'`, w.run.ID); n != 1 {
					t.Errorf("%d run.failed kept with failedTurns 1, want 1", n)
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control = 'pause_graceful' AND dude_pause = $2
					AND (harness_state->>'failedTurns')::int = 1`, w.run.ID, w.run.parkKind()); n != 1 {
					t.Errorf("not parked for its failed turn, counted once")
				}
				if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, w.run.ID); n != 0 {
					t.Errorf("%d carry-ons queued for a failure", n)
				}
			})
		}
	}
}

// parkedTalkerLuxMoved is a talker parked between turns (dude_pause
// conductor or session, its lux Run asked to stop) whose placement lux had
// already begun moving: lux placed it again itself, in epoch 2, and runs
// it. control is the Run's control: none, or a person's resume.
func parkedTalkerLuxMoved(t *testing.T, role, control string) (*harnessWorld, *sentLux) {
	w := talkerWorld(t, role)
	mustOwner(t, w, `UPDATE runs SET status = 'paused', control = $2, dude_pause = $3, lux_stop_reason = 'pause',
		lux_state = 'running', turn_done_at = NULL, agent_session_epoch = 1,
		harness_state = '{"luxStop": {"reason": "preempt", "epoch": 1}}'::jsonb WHERE id = $1`, w.run.ID, control, w.run.parkKind())
	fake := &sentLux{eventsLux: &eventsLux{run: &lux.Run{ID: "lrun_1", State: "running", Epoch: 2,
		Placements: []lux.Placement{{Epoch: 1, State: "exited"}, {Epoch: 2, State: "running"}}}}}
	w.s.Lux = fake
	t.Cleanup(w.s.Stop)
	return w, fake
}

// queue is a message for the talker, as Chat queues one.
func (w *harnessWorld) queue(text string) {
	w.t.Helper()
	mustOwner(w.t, w, `INSERT INTO directives (id, organization_id, task_id, run_id, text, created_at)
		VALUES ('dir_'||$1||'_'||md5($3), $1, NULLIF($2, ''), $4, $3, now() - interval '1 minute')`, w.run.Org, w.run.TaskID, text, w.run.ID)
}

// sweeps advances the Run as n sweeps would.
func (w *harnessWorld) sweeps(n int) {
	w.t.Helper()
	for range n {
		runs, err := w.s.due(context.Background())
		if err != nil {
			w.t.Fatal(err)
		}
		for _, r := range runs {
			if r.ID == w.run.ID {
				if _, err := w.s.advance(context.Background(), r); err != nil {
					w.t.Fatal(err)
				}
			}
		}
	}
}

// A parked talker lux placed again itself after a move is taken back up as
// lux runs it, and what was queued for it is delivered within two sweeps,
// in order, without lux being asked to stop or resume it: for a message
// (the talker's parked → resumed path) and for a person's Resume. With
// nothing queued and no Resume it stays parked, as any parked talker.
func TestAParkedTalkerLuxPlacedAgainHearsWhatIsQueued(t *testing.T) {
	for _, role := range []string{"conductor", "brainstorm"} {
		for _, control := range []string{"none", "resume"} {
			t.Run(role+"/control "+control, func(t *testing.T) {
				w, fake := parkedTalkerLuxMoved(t, role, control)
				w.queue("first")
				w.queue("second")
				w.sweeps(2)
				if got := fake.said(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
					t.Errorf("sent %q, want first and second", got)
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND control = 'none'
					AND dude_pause IS NULL AND lux_stop_reason IS NULL`, w.run.ID); n != 1 {
					t.Errorf("not taken back up running")
				}
				fake.mu.Lock()
				stops := fake.stops
				fake.mu.Unlock()
				if stops != 0 {
					t.Errorf("lux asked to stop %d times, want none", stops)
				}
			})
		}
		t.Run(role+"/nothing queued", func(t *testing.T) {
			w, fake := parkedTalkerLuxMoved(t, role, "none")
			w.sweeps(2)
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = $2`, w.run.ID, w.run.parkKind()); n != 1 {
				t.Errorf("a parked talker with nothing for it was taken up")
			}
			if got := fake.said(); len(got) != 0 {
				t.Errorf("sent %q", got)
			}
		})
	}
}

// A conductor parked in that state is woken as any parked conductor: the
// wake's note is queued (WakeConductorTx) and reaches the agent.
func TestAWakeReachesAParkedConductorLuxPlacedAgain(t *testing.T) {
	w, fake := parkedTalkerLuxMoved(t, "conductor", "none")
	w.s.WakeWindow = time.Second
	mustOwner(t, w, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_1', $1, $2, 'decision', 'k', 'the owner waived it', now() - interval '1 minute')`, w.run.Org, w.run.TaskID)
	if err := w.s.wakeConductors(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'the owner waived it') > 0`, w.run.ID); n != 1 {
		t.Fatalf("%d wake notes queued, want 1", n)
	}
	w.sweeps(2)
	if got := fake.said(); len(got) != 1 || !strings.Contains(got[0], "the owner waived it") {
		t.Errorf("sent %q, want the wake's note", got)
	}
}
