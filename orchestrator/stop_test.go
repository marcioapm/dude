package orchestrator_test

// Stop (api/stop.go): a person stops the whole task where it stands —
// phase Runs, the conductor and its children — so it can be picked back
// up, or starts it over at once.

import (
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// stop is the owner's Stop of the task, which must be accepted.
func (w *world) stop(as, task string, body map[string]any) map[string]any {
	w.t.Helper()
	status, out := w.callAs(as, "/internal/tasks/"+task+"/stop", body)
	if status != 200 {
		w.t.Fatalf("stop: %d %v", status, out)
	}
	return out
}

// keptRun says the Run is aborted and kept in lux to resume.
func (w *world) keptRun(runID string) bool {
	return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND keep
		AND lux_stop_reason = 'kept' AND kept_until > now()`, runID) == 1
}

// The prod case: a conductor task parked at a decision, the conductor
// between turns and nothing else live. Stop aborts the task, keeps the
// conductor for a resume, and the task offers every way back.
func TestStoppingAConductorTaskParkedAtADecisionKeepsTheConductor(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	out := w.stop(ana, task, map[string]any{"note": "nothing to build yet"})
	if out["status"] != "aborted" {
		t.Fatalf("stop answered %v", out)
	}
	if s := w.taskStatus(task); s != "aborted" {
		t.Fatalf("a stopped task is %s", s)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'aborted'`, task); n != 1 {
		t.Errorf("the delivery was not aborted")
	}
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	if got, _ := w.options(task); strings.Join(got, " ") != "resume retry restart" {
		t.Fatalf("a task stopped under the conductor offers %v", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.stopped'
		AND payload->>'note' = 'nothing to build yet' AND payload->>'decider' = 'conductor'
		AND payload->'runIds' = jsonb_build_array($2::text) AND actor_id = $3`, task, conductor, ana); n != 1 {
		t.Errorf("task.stopped was not recorded with the conductor")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.aborted'`, conductor); n != 1 {
		t.Errorf("%d run.aborted for the conductor", n)
	}
}

// A conductor with a reviewer it started and a child of its own: Stop
// aborts the three, keeps the conductor, and the reviewer (never on lux)
// is queued again by a resume. The child is let go, as AbortChildTx does.
func TestStoppingAConductorAndItsReviewerStopsBoth(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	w.implemented(task)
	w.must(task, "start_phase", `{"phase":"review"}`)
	w.advanced()
	reviewers := w.pendingPhaseRuns(task)
	if len(reviewers) == 0 {
		t.Fatalf("no reviewer pending\n%s", w.describeRuns())
	}
	child := w.taskChild(task, conductor)
	out := w.stop(ana, task, map[string]any{})
	ids, _ := out["status"].(string)
	if ids != "aborted" {
		t.Fatalf("stop: %v", out)
	}
	for _, id := range reviewers {
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, id); n != 1 {
			t.Errorf("reviewer %s was not aborted", id)
		}
	}
	// Asked to stop, never kept; the syncer's hand-over may name its parent's end as the reason.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND child_stop_reason IN ('aborted', 'parent-ended') AND control = 'abort' AND NOT keep`, child); n != 1 {
		t.Errorf("the conductor's child was not aborted: %s", w.str(`SELECT status || ' ' || COALESCE(child_stop_reason, '-') || ' ' || control || ' ' || keep FROM runs WHERE id = $1`, child))
	}
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	if got, _ := w.options(task); !slices.Contains(got, "resume") {
		t.Fatalf("a stopped conductor and reviewer offer %v", got)
	}
	stoppedIDs := w.str(`SELECT payload->>'runIds' FROM events WHERE task_id = $1 AND event_type = 'task.stopped'`, task)
	for _, id := range append(slices.Clone(reviewers), conductor, child) {
		if !strings.Contains(stoppedIDs, id) {
			t.Errorf("task.stopped does not name %s: %s", id, stoppedIDs)
		}
	}
}

// Under Deliver, Stop is what aborting a phase Run was: the Runs kept, the
// delivery aborted, every way back offered; a resume goes on with them.
func TestStoppingADeliverTaskIsAbortingItsPhaseRuns(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true, Reply: "Done after resume.", Commit: map[string]string{"A.md": "a\n"}}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	w.deliver(task)
	w.until("the implementer to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, task) == 1
	})
	implementer := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	w.stop(ana, task, map[string]any{"note": "wrong approach"})
	w.until("the implementer to be kept", func() bool { return w.keptRun(implementer) })
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'aborted'`, task); n != 1 {
		t.Errorf("the delivery was not aborted")
	}
	if got, _ := w.options(task); strings.Join(got, " ") != "resume retry restart" {
		t.Fatalf("a stopped Deliver task offers %v", got)
	}
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("review, after the same implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) > 0
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, task); n != 1 {
		t.Errorf("%d implementers: the resume made a new one", n)
	}
}

// Only the owner or an admin stops a task, refused in the menu's words;
// a task not in progress is refused 409, and nothing moves.
func TestStopIsTheOwnersOrAnAdminsAndOnlyInProgress(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	bo := w.person("Bo")
	idle := w.task()
	w.assignOwner(idle, ana)
	if status, body := w.callAs(ana, "/internal/tasks/"+idle+"/stop", map[string]any{}); status != 409 {
		t.Fatalf("stopping a task not started: %d %v", status, body)
	}
	if s := w.taskStatus(idle); s != "received" {
		t.Fatalf("a refused stop moved the task to %s", s)
	}
	w.talk(task)
	status, body := w.callAs(bo, "/internal/tasks/"+task+"/stop", map[string]any{})
	e, _ := body["error"].(map[string]any)
	if status != 403 || e["message"] != "Only Ana, the owner, or an admin can do this" {
		t.Fatalf("another member's stop: %d %v", status, body)
	}
	if s := w.taskStatus(task); s == "aborted" {
		t.Fatal("a refused stop stopped the task")
	}
	mustExec(t, w.owner, `UPDATE people SET role = 'admin' WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, bo)
	w.stop(bo, task, map[string]any{})
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/stop", map[string]any{}); status != 409 {
		t.Fatalf("stopping a stopped task: %d %v", status, body)
	}
}

// Stop is the whole task's, not its current attempt's: a phase Run left
// live on an older attempt (an anomaly: Start over ends them) is stopped
// and named with the current attempt's.
func TestStopStopsALiveRunLeftOnAnOlderAttempt(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	mustExec(t, w.owner, `UPDATE runs SET attempt = 2 WHERE id = $1`, conductor)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{attempt}', '2') WHERE task_id = $1`, task)
	stray := "run_stray_" + task
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, phase)
		SELECT $1, organization_id, project_id, task_id, 1, 'pending', 'agent', 'reviewer', 'review' FROM runs WHERE id = $2`, stray, conductor)
	out := w.stop(ana, task, map[string]any{})
	if out["status"] != "aborted" {
		t.Fatalf("stop: %v", out)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, stray); n != 1 {
		t.Errorf("attempt 1's live reviewer was not stopped: %s", w.str(`SELECT status::text FROM runs WHERE id = $1`, stray))
	}
	if stopped := w.str(`SELECT payload->>'runIds' FROM events WHERE task_id = $1 AND event_type = 'task.stopped'`, task); !strings.Contains(stopped, stray) {
		t.Errorf("task.stopped does not name attempt 1's reviewer: %s", stopped)
	}
}

// taskChild inserts a live child the conductor started, as start_child
// leaves one before lux has it.
func (w *world) taskChild(task, conductor string) string {
	w.t.Helper()
	id := "run_child_" + task
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, phase,
			parent_run_id, source_kind, child_profile, child_capabilities)
		SELECT $1, organization_id, project_id, task_id, attempt, 'pending', 'agent', 'reviewer', 'review', id, 'task_conductor', 'investigator', '{}'::jsonb
		FROM runs WHERE id = $2`, id, conductor)
	return id
}
