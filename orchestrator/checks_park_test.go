package orchestrator_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
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

// A person's steer inside the grace starts a turn that is held again: the
// Run still waits on the person, and is parked once the grace is over.
func TestAHeldRunSteeredInsideTheGraceIsParkedAfterIt(t *testing.T) {
	w := newWorld(t)
	w.syncer.ParkAfter = time.Hour
	wi := w.unbackedUntilEscalated(0)
	runID := w.implementerOf(wi)
	if status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "try the check once more"}); status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	w.until("the steered turn to be held again", func() bool {
		return strings.Join(w.judged(wi), ",") == "unverified,unverified" &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NOT NULL`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND waiting_since IS NOT NULL`, runID); n != 1 {
		t.Fatalf("the steered held Run does not wait on a person")
	}
	w.syncer.ParkAfter = 50 * time.Millisecond
	w.until("the steered held Run to be parked", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked' AND payload->>'reason' = 'person'`, runID) == 1
	})
	if n := w.implementerPushes(wi); n != 0 {
		t.Errorf("%d pushes of a held Run", n)
	}
}

// A decision ends the Run's wait on the checks escalation, and only that
// one: a question the Run has open still waits on its person.
func TestADecisionKeepsTheWaitOfAnotherOpenAsk(t *testing.T) {
	for _, c := range []struct {
		name     string
		question bool
		waiting  int
	}{
		{"nothing else open", false, 0},
		{"a question open", true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.syncer.ParkAfter = time.Hour
			wi := w.unbackedUntilEscalated(0)
			runID := w.implementerOf(wi)
			if c.question {
				mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status)
					VALUES ('q_'||$1, $2, $3, $1, 'Which?', 'open')`, runID, w.org, wi)
			}
			owner, _, _ := w.people(wi)
			if code, body := w.decide(owner, wi, "retry", ""); code != 200 {
				t.Fatalf("retry: %d %v", code, body)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL`, runID); n != c.waiting {
				t.Errorf("%d Runs still waiting after the decision, want %d", n, c.waiting)
			}
		})
	}
}

// luxInputs are the inputs lux's Run for runID was given, the prompt aside.
func (w *world) luxInputs(runID string) []string {
	w.t.Helper()
	luxID := w.luxRunOf(runID)
	for _, r := range w.lux.Runs() {
		if r.ID == luxID {
			return r.Inputs
		}
	}
	return nil
}

// resumeRequests counts the resume requests lux is sent for runID's lux
// Run from now on, dry runs aside: a replayed one counts, unlike Resumed.
func (w *world) resumeRequests(runID string) *atomic.Int32 {
	w.t.Helper()
	luxID := w.luxRunOf(runID)
	var n atomic.Int32
	w.lux.OnResume = func(id string, validateOnly bool) {
		if id == luxID && !validateOnly {
			n.Add(1)
		}
	}
	return &n
}

// A retry resumes the parked Run once, and the send-back is its next turn:
// no carry-on as well.
func TestARetryResumesAParkedHeldRunWithTheSendBack(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.parkedHeld()
	owner, _, _ := w.people(wi)
	before := len(w.luxInputs(runID))
	requests := w.resumeRequests(runID)
	held := w.str(`SELECT payload->>'turn' FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor DESC LIMIT 1`,
		runID, delivery.EvRunChecksJudged)
	heldDone := w.str(`SELECT turn_done_at::text FROM runs WHERE id = $1`, runID)
	if code, body := w.decide(owner, wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	// The held turn is not kept: once resumed, the Run waits on the
	// send-back's new turn, never on the held one's done mark.
	kept := false
	w.until("the send-back to be delivered", func() bool {
		if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND turn_done_at::text = $2`, runID, heldDone) == 1 {
			kept = true
		}
		return w.count(`SELECT count(*) FROM directives d JOIN events e ON e.payload->>'directiveId' = d.id
			WHERE d.run_id = $1 AND d.delivered_at IS NOT NULL AND e.event_type = $2`, runID, delivery.EvRunChecksDecided) == 1
	})
	if kept {
		t.Errorf("the resumed Run kept its held turn done")
	}
	w.until("the send-back's turn to be judged", func() bool {
		return w.count(`SELECT count(*) FROM events j WHERE j.run_id = $1 AND j.event_type = $2 AND j.cursor >
			(SELECT max(cursor) FROM events WHERE run_id = $1 AND event_type = $3)`, runID, delivery.EvRunChecksJudged, delivery.EvRunChecksDecided) > 0
	})
	if n := w.count(`SELECT count(*) FROM events j WHERE j.run_id = $1 AND j.event_type = $2 AND j.cursor >
		(SELECT max(cursor) FROM events WHERE run_id = $1 AND event_type = $3) AND (j.payload->>'turn')::bigint <= $4::bigint`,
		runID, delivery.EvRunChecksJudged, delivery.EvRunChecksDecided, held); n != 0 {
		t.Errorf("%d judgements after the retry of a turn up to the held %s, want only later turns", n, held)
	}
	if resumed, _ := w.luxResumes(runID); resumed != 1 {
		t.Errorf("resumed %d times, want once", resumed)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("%d lux resume requests, want 1", n)
	}
	if got := w.luxInputs(runID)[before:]; len(got) != 1 || !strings.HasPrefix(got[0], "Your checks report was not verified") {
		t.Errorf("after the retry the agent was given %q, want the send-back alone", got)
	}
	if got := w.lux.ResumeInputs(w.luxRunOf(runID)); len(got) != 1 || got[0] != "" {
		t.Errorf("the resume carried %q, want no input of its own", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.unparked'`, runID); n != 1 {
		t.Errorf("%d run.unparked events", n)
	}
}

// waivedFromPark waives the parked held Run's report in scope (run or
// attempt) and returns once the pull request is open.
func (w *world) waivedFromPark(wi, runID, note, scope string) {
	w.t.Helper()
	owner, _, _ := w.people(wi)
	requests := w.resumeRequests(runID)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, note, scope); code != 200 {
		w.t.Fatalf("the owner's waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if resumed, _ := w.luxResumes(runID); resumed != 1 {
		w.t.Errorf("resumed %d times, want once, to push", resumed)
	}
	if n := requests.Load(); n != 1 {
		w.t.Errorf("%d lux resume requests, want 1", n)
	}
	if n := w.implementerPushes(wi); n != 1 {
		w.t.Errorf("%d pushes, want 1", n)
	}
	// The waived turn is the one pushed: no new turn, nothing told to the agent.
	if got := w.lux.ResumeInputs(w.luxRunOf(runID)); len(got) != 1 || got[0] != "" {
		w.t.Errorf("the resume carried %q, want no input", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID); n != 1 {
		w.t.Errorf("the waived implementer did not complete")
	}
}

// A waiver of the parked Run's report resumes it to push, without another
// agent turn, and publishes as a live one's waiver does.
func TestARunWaiverOfAParkedHeldRunPushesAndPublishes(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.parkedHeld()
	w.waivedFromPark(wi, runID, "The check needs hardware CI has; it passed there.", delivery.ScopeRun)
	if got := w.judged(wi); strings.Join(got, ",") != "unverified,waived" {
		t.Errorf("judged %v, want the waiver recorded", got)
	}
	if got := checksSection(w.gh.Pull(1).Body); !strings.HasPrefix(got, "Checks were waived by a person:\n"+
		"- Implementer: The check needs hardware CI has; it passed there.\n") {
		t.Errorf("the pull request's checks section is\n%s", got)
	}
}

// An attempt waiver from a park pushes the same way, and later Runs of the
// attempt still carry it.
func TestAnAttemptWaiverOfAParkedHeldRunPushesAndIsCarried(t *testing.T) {
	w := newWorld(t)
	w.syncer.ParkAfter = 300 * time.Millisecond
	j := &scriptedJudge{}
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, j)
	runID := w.implementerOf(wi)
	w.until("the held Run to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, runID) == 1
	})
	w.until("lux to stop it", func() bool { _, st := w.luxResumes(runID); return st == "stopped" })
	w.waivedFromPark(wi, runID, clockNote, delivery.ScopeAttempt)
	if got := strings.Join(w.gateJudged(wi), ","); got != "implement:unverified,implement:waived,fix:carried,simplify:carried" {
		t.Errorf("judged %s", got)
	}
}

// Stop ends the parked Run: never resumed, its work unpushed.
func TestStopEndsAParkedHeldRunWithoutAResume(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.parkedHeld()
	owner, _, _ := w.people(wi)
	if code, body := w.decide(owner, wi, "stop", ""); code != 200 {
		t.Fatalf("stop: %d %v", code, body)
	}
	w.until("the task to be aborted", func() bool { return w.taskStatus(wi) == "aborted" })
	w.until("lux to end the Run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND lux_stop_reason IN ('cancel', 'kept')`, runID) == 1
	})
	for range 5 {
		w.pump()
	}
	if resumed, _ := w.luxResumes(runID); resumed != 0 {
		t.Errorf("resumed %d times after a stop", resumed)
	}
	if n := w.implementerPushes(wi); n != 0 {
		t.Errorf("%d pushes after a stop", n)
	}
}

// Decided inside the grace, the Run is taken as it is: never parked
// afterwards, never resumed.
func TestADecisionInsideTheGraceNeitherResumesNorParks(t *testing.T) {
	for _, action := range []string{"retry", delivery.ActionWaive} {
		t.Run(action, func(t *testing.T) {
			w := newWorld(t)
			w.syncer.ParkAfter = 2 * time.Second
			// The retried agent backs its next report, so nothing is held again.
			mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":0}' WHERE organization_id = $1`, w.org)
			wi := w.checksDelivery(fakeagent.ChecksFixedModel, &scriptedJudge{})
			w.until("the checks escalation", func() bool { return w.escalationReason(wi) == delivery.ReasonChecksUnverified })
			runID := w.implementerOf(wi)
			owner, _, _ := w.people(wi)
			if code, body := w.decide(owner, wi, action, "Waived: CI runs it."); code != 200 {
				t.Fatalf("%s: %d %v", action, code, body)
			}
			w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			// Past the grace: nothing parks or resumes it.
			time.Sleep(2500 * time.Millisecond)
			for range 5 {
				w.pump()
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`, runID); n != 0 {
				t.Errorf("parked %d times after the decision", n)
			}
			if resumed, _ := w.luxResumes(runID); resumed != 0 {
				t.Errorf("resumed %d times", resumed)
			}
		})
	}
}

// An orchestrator restarted between the park and the decision keeps the
// hold: parked, not resumed, until the person decides; then it pushes.
func TestARestartKeepsAParkedHeldRunHeld(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.parkedHeld()
	w.syncer.Stop()
	w.syncer = &phases.Syncer{DB: w.syncer.DB, Lux: w.syncer.Lux, Forges: w.syncer.Forges, Log: quiet, Agent: w.syncer.Agent,
		KeepFor: w.syncer.KeepFor, Judge: w.syncer.Judge, ParkAfter: w.syncer.ParkAfter}
	t.Cleanup(w.syncer.Stop)
	for range 5 {
		w.pump()
	}
	if resumed, _ := w.luxResumes(runID); resumed != 0 {
		t.Fatalf("resumed %d times after a restart, undecided", resumed)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, runID); n != 1 {
		t.Fatalf("the restart lost the park")
	}
	w.waivedFromPark(wi, runID, "Waived: CI runs it.", delivery.ScopeRun)
}

// A person's Resume of the parked Run before deciding takes it back up
// still held, with no new turn: a later waiver pushes the held turn.
func TestAPersonsResumeOfAParkedHeldRunKeepsTheHeldTurn(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.parkedHeld()
	// Taken back up, it is not parked again while this test looks at it.
	w.syncer.ParkAfter = time.Hour
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the held Run to run again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running' AND waiting_since IS NOT NULL`, runID) == 1
	})
	if n := w.implementerPushes(wi); n != 0 {
		t.Fatalf("%d pushes of a Run still held", n)
	}
	owner, _, _ := w.people(wi)
	if code, body := w.decide(owner, wi, delivery.ActionWaive, "Waived: CI runs it."); code != 200 {
		t.Fatalf("waive: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if resumed, _ := w.luxResumes(runID); resumed != 1 {
		t.Errorf("resumed %d times, want the person's once", resumed)
	}
}
