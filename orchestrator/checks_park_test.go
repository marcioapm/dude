package orchestrator_test

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// implementerOf is the task's implementer Run.
func (w *world) implementerOf(wi string) string {
	w.t.Helper()
	return w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi)
}

// luxResumes is how many times lux resumed the Run, and the state it has it in.
func (w *world) luxResumes(runID string) (resumed int, state string) {
	w.t.Helper()
	luxID := w.luxRunOf(runID)
	for _, r := range w.lux.Runs() {
		if r.ID == luxID {
			return r.Resumed, w.lux.State(luxID)
		}
	}
	w.t.Fatalf("lux has no Run %s", luxID)
	return 0, ""
}

// parkedHeld is a delivery whose implementer is held at the checks gate
// and parked once the grace is over: paused for a person, run.parked
// {reason: person}, lux's Run stopped and kept.
func (w *world) parkedHeld() (wi, runID string) {
	w.t.Helper()
	w.syncer.ParkAfter = 300 * time.Millisecond
	wi = w.unbackedUntilEscalated(0)
	runID = w.implementerOf(wi)
	w.until("the held Run to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, runID) == 1
	})
	w.until("lux to stop it", func() bool { _, st := w.luxResumes(runID); return st == "stopped" })
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked' AND payload->>'reason' = 'person'`, runID); n != 1 {
		w.t.Fatalf("%d run.parked {reason: person} events", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id IS NOT NULL AND status NOT IN ('failed', 'aborted')`, runID); n != 1 {
		w.t.Fatalf("the parked Run lost its lux Run")
	}
	return wi, runID
}

// Inside the project's grace the held Run stays running: it is not parked.
func TestAHeldRunIsNotParkedInsideTheGrace(t *testing.T) {
	w := newWorld(t)
	w.syncer.ParkAfter = time.Hour
	wi := w.unbackedUntilEscalated(0)
	runID := w.implementerOf(wi)
	for range 10 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND waiting_since IS NOT NULL`, runID); n != 1 {
		t.Fatalf("the held Run is not running and waiting on a person")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`, runID); n != 0 {
		t.Errorf("parked %d times inside the grace", n)
	}
}

// Past the grace the held Run is parked as one waiting on a person, and
// stays parked, held, until the person decides.
func TestAHeldRunIsParkedAfterTheGrace(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.parkedHeld()
	for range 5 {
		w.pump()
	}
	if resumed, _ := w.luxResumes(runID); resumed != 0 {
		t.Fatalf("resumed %d times with the escalation undecided", resumed)
	}
	if w.escalationReason(wi) != delivery.ReasonChecksUnverified || w.taskStatus(wi) != "awaiting_input" {
		t.Errorf("parking changed the hold: escalation %q, task %s", w.escalationReason(wi), w.taskStatus(wi))
	}
	if n := w.implementerPushes(wi); n != 0 {
		t.Errorf("%d pushes of a parked held Run", n)
	}
}
