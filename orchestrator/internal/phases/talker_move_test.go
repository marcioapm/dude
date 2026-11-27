package phases

import (
	"context"
	"sync"
	"testing"

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
