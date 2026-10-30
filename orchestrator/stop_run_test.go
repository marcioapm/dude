package orchestrator_test

// The conductor's stop_run: it stops a child agent it started, through the
// orchestrator's real code — the tool called with the conductor's token,
// the syncer ending the child on the fake lux, and the child's result
// coming back to the conductor as aborted.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// hangingChildren has every child investigator hang, so it is live to stop,
// with calls open.
func (w *world) hangingChildren(calls ...[3]string) {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "investigate" {
			return fakelux.Behaviour{Hang: true, OpenCalls: calls}
		}
		return scripted(spec)
	}
}

// liveChild is a conducted task (talk) and an investigator its conductor
// started, running on lux, hanging with calls open.
func (w *world) liveChild(calls ...[3]string) (task, conductor, child string) {
	w.t.Helper()
	w.onModel("investigator", "fake/scripted")
	w.hangingChildren(calls...)
	task = w.task()
	conductor = w.talk(task)
	child, _ = w.must(task, "start_agent", `{"profile":"investigator","prompt":"Look at the worker.","key":"look"}`)["runId"].(string)
	w.until("the child running on lux", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_run_id IS NOT NULL`, child) == 1
	})
	return task, conductor, child
}

// stopRun calls stop_run as the conductor whose spec is given, without
// sweeping: what the test arranged in the database is what the tool sees.
func (w *world) stopRun(spec, run, reason string) (int, map[string]any) {
	w.t.Helper()
	status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "stop_run", stopArgs(run, reason))
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return status, out
}

// stopRefused is stopRun's refusal, saying want.
func (w *world) stopRefused(spec, run, reason, want string) {
	w.t.Helper()
	status, out := w.stopRun(spec, run, reason)
	if msg, _ := out["error"].(string); status != 422 || !strings.Contains(msg, want) {
		w.t.Errorf("stop_run %s %q: %d %v, want refused saying %q", run, reason, status, out, want)
	}
}

func stopArgs(run, reason string) string {
	b, _ := json.Marshal(map[string]any{"run": run, "reason": reason})
	return string(b)
}

// stopEvents is how many run.aborted events the conductor wrote on run.
func (w *world) stopEvents(run string) int {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.aborted' AND payload->>'by' = 'conductor'`, run)
}

// othersChild inserts run_otherschild, a running child of task's ended
// conductor run_oldconductor, not yet told to abort.
func (w *world) othersChild(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role)
		VALUES ('run_oldconductor', $1, $2, $3, 1, 'completed', 'conductor')`, w.org, w.project, task)
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, source_kind,
		parent_run_id, conductor_run_id, child_profile, child_capabilities)
		VALUES ('run_otherschild', $1, $2, $3, 1, 'running', 'investigate', 'investigator', 'task_conductor',
		'run_oldconductor', 'run_oldconductor', 'investigator', '{}')`, w.org, w.project, task)
}

// The conductor stops the investigator it started: the child is asked to
// abort with the conductor's reason, the syncer ends it and cancels its lux
// Run, the conductor is given its result as aborted, and the ledger says
// the conductor aborted it, why.
func TestTheConductorStopsItsOwnChild(t *testing.T) {
	w := conducting(t)
	task, conductor, child := w.liveChild()
	out := w.must(task, "stop_run", stopArgs(child, "It is reading the wrong service."))
	if out["stopped"] != child || out["status"] != "stopping" || out["next"] == nil || out["next"] == "" {
		t.Fatalf("stop_run answered %v", out)
	}
	var control, stopReason, why string
	if err := w.owner.QueryRow(context.Background(), `SELECT control::text, COALESCE(child_stop_reason, ''), COALESCE(control_reason, '')
		FROM runs WHERE id = $1`, child).Scan(&control, &stopReason, &why); err != nil {
		t.Fatal(err)
	}
	if control != "abort" || stopReason != "aborted" || why != "It is reading the wrong service." {
		t.Fatalf("the child: control %s, stop reason %q, control reason %q", control, stopReason, why)
	}
	var actorType, actorID string
	var payload map[string]any
	if err := w.owner.QueryRow(context.Background(), `SELECT actor_type, actor_id, payload FROM events
		WHERE run_id = $1 AND event_type = 'run.aborted'`, child).Scan(&actorType, &actorID, &payload); err != nil {
		t.Fatal(err)
	}
	if actorType != "agent" || actorID != conductor || payload["by"] != "conductor" || payload["conductorRunId"] != conductor ||
		payload["reason"] != "It is reading the wrong service." {
		t.Errorf("run.aborted by %s %s: %v", actorType, actorID, payload)
	}
	lr := w.luxRunOf(child)
	w.until("the child aborted and its lux Run cancelled", func() bool {
		if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, child) != 1 {
			return false
		}
		for _, r := range w.lux.Runs() {
			if r.ID == lr {
				return r.Cancelled
			}
		}
		return false
	})
	var text string
	w.until("the child's result for the conductor", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT d.text FROM child_result_outbox o JOIN directives d ON d.id = o.directive_id
			WHERE o.child_run_id = $1 AND d.run_id = $2`, child, conductor).Scan(&text)
		return text != ""
	})
	if !strings.Contains(text, `"status": "aborted"`) || !strings.Contains(text, child) {
		t.Errorf("the conductor's child result:\n%s", text)
	}
}

// A second stop of a child already stopping answers already_stopping and
// writes nothing more; once it has ended, stop_run is refused saying so.
func TestStopRunIsIdempotentAndRefusesTheEnded(t *testing.T) {
	w := conducting(t)
	task, _, child := w.liveChild()
	spec := w.conductorSpecOf(task)
	if status, out := w.stopRun(spec, child, "Not needed."); status != 200 || out["status"] != "stopping" {
		t.Fatalf("the first stop: %d %v", status, out)
	}
	if status, out := w.stopRun(spec, child, "Not needed, really."); status != 200 || out["status"] != "already_stopping" || out["stopped"] != child {
		t.Fatalf("the second stop: %d %v", status, out)
	}
	if n := w.stopEvents(child); n != 1 {
		t.Errorf("%d run.aborted events by the conductor, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control_reason = 'Not needed.'`, child); n != 1 {
		t.Error("the second stop changed the first one's reason")
	}
	w.until("the child aborted", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, child) == 1
	})
	w.stopRefused(spec, child, "Again.", "has ended (aborted)")
	if n := w.stopEvents(child); n != 1 {
		t.Errorf("%d run.aborted events by the conductor after the refusal, want 1", n)
	}
}

// stopWritten is how many rows a stop writes, task-wide: Runs asked to
// abort and run.aborted events.
func (w *world) stopWritten(task string) int {
	return w.count(`SELECT (SELECT count(*) FROM runs WHERE task_id = $1 AND (control = 'abort' OR child_stop_reason IS NOT NULL))
		+ (SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.aborted')`, task)
}

// What stop_run refuses, each saying why and writing nothing: a phase Run
// the delivery waits on (restart_run replaces it), the conductor itself,
// another task's Run, an unknown one, a child of another conductor of the
// task, a branch preview, an empty or too long reason.
func TestStopRunRefusesPhaseRunsConductorsAndOthersChildren(t *testing.T) {
	w := conducting(t)
	task, conductor, child := w.liveChild()
	spec := w.conductorSpecOf(task)
	other := task + "_other"
	mustExec(t, w.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ($1, $2, $3, 990, 'Other')`,
		other, w.org, w.project)
	// Rows nothing sweeps meanwhile: a reviewer of the delivery, another
	// task's Run, a conductor this one replaced and the child it started,
	// still not told to abort, and a branch preview.
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_reviewer', $1, $2, $3, 1, 'running', 'review', 'reviewer'),
		       ('run_otherstask', $1, $2, $4, 1, 'running', 'implement', 'implementer')`, w.org, w.project, task, other)
	w.othersChild(task)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind)
		VALUES ('run_preview', $1, $2, $3, 1, 'running', 'preview')`, w.org, w.project, task)
	before := w.stopWritten(task)
	for _, c := range []struct{ run, reason, want string }{
		{"run_reviewer", "stuck", "restart_run replaces it"},
		{conductor, "stuck", "is a conductor"},
		{"run_otherstask", "stuck", "not a Run of your task"},
		{"run_nope", "stuck", "not a Run of your task"},
		{"run_otherschild", "stuck", "is not your child"},
		{"run_preview", "stuck", "not a Run of your task"},
		{child, "   ", "reason is required"},
		{child, strings.Repeat("x", 501), "at most 500 characters"},
	} {
		w.stopRefused(spec, c.run, c.reason, c.want)
	}
	if n := w.stopWritten(task); n != before {
		t.Errorf("refusals wrote %d rows", n-before)
	}
	// The task done: read-only.
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, task)
	w.stopRefused(spec, child, "stuck", "read-only")
	if n := w.stopWritten(task); n != before {
		t.Errorf("a done task's stop wrote %d rows", n-before)
	}
}

// A conductor replaced after its call was authenticated, before the tool
// ran, stops nothing.
func TestASupersededConductorStopsNothing(t *testing.T) {
	w := conducting(t)
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: w.log, BeforeCall: func(tool string) {
		if tool == "stop_run" {
			once.Do(func() { close(paused); <-resume })
		}
	}}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	task, conductor, child := w.liveChild()
	spec := w.conductorSpecOf(task)
	done := make(chan string, 1)
	go func() {
		status, body := w.callTool(tools.URL, spec, "stop_run", stopArgs(child, "late"))
		if status != 422 {
			body = "status " + body
		}
		done <- body
	}()
	<-paused
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'terminated' WHERE id = $1`, conductor)
	if status, out := w.chat(task, "are you there?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	if next, _, _ := w.conductor(task); next == conductor {
		t.Fatal("no replacement conductor")
	}
	close(resume)
	if body := <-done; !strings.Contains(body, "no longer this task's conductor") {
		t.Fatalf("the replaced conductor's stop: %s", body)
	}
	if n := w.stopEvents(child); n != 0 {
		t.Errorf("the replaced conductor's stop wrote %d run.aborted events", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control_reason = 'late'`, child); n != 0 {
		t.Error("the replaced conductor's reason is on the child")
	}
}
