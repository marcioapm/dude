package orchestrator_test

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// lux preempts an implementer that is idle, waiting on a person, and
// resumes it elsewhere itself: the agent writes nothing on its new host, so
// dude's servers.changed {luxState: running} is the one event that ends
// the chat's move (#262). Written once for the move, not on later frames.
func TestAnIdleRunLuxMovesIsAnnouncedBackToRunningOnce(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: `{"question":"Should the table be sorted?"}`, Reply: "Sorted.",
			Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	task := w.task()
	w.deliver(task)
	w.until("the implementer to wait on a person, idle", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'
			AND lux_state = 'running' AND waiting_since IS NOT NULL`, task) == 1
	})
	runID := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	luxID := w.luxRunOf(runID)
	const backToRunning = `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'servers.changed'
		AND payload->>'change' = 'state' AND payload->>'luxState' = 'running'`
	// fakelux places a fresh Run straight from submitted, a wait, so its
	// first running is announced already; the move's is counted after it.
	before := w.count(backToRunning, runID)

	// As lux does at assignment (prod: resuming, scheduled, the stages,
	// running), which the fake's own placement skips: the step into
	// running is then from scheduled, not from a wait.
	w.lux.BeforeAssign(func(id string) {
		if id == luxID {
			w.lux.Place(id)
		}
	})
	w.lux.Move(luxID, "preempt")
	w.waitLuxRunning(luxID)
	w.lux.BeforeAssign(nil)
	w.until("dude to follow the moved Run to running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running' AND left_running_at IS NULL`, runID) == 1 &&
			w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.stage'
				AND payload->>'stage' = 'running' AND (payload->>'epoch')::int = 2`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'servers.changed'
		AND payload->>'luxState' = 'scheduled'`, runID); n != 1 {
		t.Fatalf("%d servers.changed scheduled: the move did not go through scheduled", n)
	}
	// Later frames of the running placement, if any, have time to be read.
	time.Sleep(300 * time.Millisecond)
	if n := w.count(backToRunning+` AND cursor > (SELECT max(cursor) FROM events WHERE run_id = $1
		AND event_type = 'run.stage' AND payload->>'stage' = 'park')`, runID); n != 1 {
		t.Fatalf("back to running after the move announced %d times, want 1", n)
	}
	if n := w.count(backToRunning, runID); n != before+1 {
		t.Fatalf("%d servers.changed running since the Run first ran, want only the move's", n-before)
	}
	// The case under test: nothing of the agent's ends the move instead.
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND cursor > (SELECT max(cursor) FROM events
		WHERE run_id = $1 AND event_type = 'run.stage' AND payload->>'stage' = 'park')
		AND event_type IN ('agent.message', 'agent.thought', 'agent.tool.called', 'agent.session.started',
			'agent.prompt.delivered', 'run.directive.accepted', 'run.directive.delivered', 'run.resume.timed')`, runID); n != 0 {
		t.Fatalf("%d agent events after the move: the agent was not idle", n)
	}
}
