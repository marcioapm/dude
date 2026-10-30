package orchestrator_test

// The conductor's stop_run: it stops a child agent it started, through the
// orchestrator's real code — the tool called with the conductor's token,
// the syncer ending the child on the fake lux, and the child's result
// coming back to the conductor as aborted.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// hangingChildren has every child investigator hang, so it is live to stop.
func (w *world) hangingChildren() {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "investigate" {
			return fakelux.Behaviour{Hang: true}
		}
		return scripted(spec)
	}
}

// liveChild is a conducted task (talk) and an investigator its conductor
// started, running on lux.
func (w *world) liveChild() (task, conductor, child string) {
	w.t.Helper()
	w.onModel("investigator", "fake/scripted")
	w.hangingChildren()
	task = w.task()
	conductor = w.talk(task)
	child, _ = w.must(task, "start_agent", `{"profile":"investigator","prompt":"Look at the worker.","key":"look"}`)["runId"].(string)
	w.until("the child running on lux", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_run_id IS NOT NULL`, child) == 1
	})
	return task, conductor, child
}

func stopArgs(run, reason string) string {
	b, _ := json.Marshal(map[string]any{"run": run, "reason": reason})
	return string(b)
}

// stopEvents is how many run.aborted events the conductor wrote on run.
func (w *world) stopEvents(run string) int {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.aborted' AND payload->>'by' = 'conductor'`, run)
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
