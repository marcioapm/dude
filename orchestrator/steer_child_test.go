package orchestrator_test

// The conductor's steer reaching a child agent it started (start_agent,
// start_agents, verify), and only its own.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The conductor interrupts the investigator it started: the directive is
// the conductor's and interrupts, lux stops the child's turn to deliver it,
// and the child reading it wakes the conductor with steer_read.
func TestTheConductorInterruptsItsOwnChild(t *testing.T) {
	w := conducting(t)
	task, conductor, child := w.liveChild()
	spec := w.conductorSpecOf(task)
	args, _ := json.Marshal(map[string]any{"run": child, "text": "Stop reading the worker; read the scheduler.", "interrupt": true})
	status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "steer", string(args))
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	if status != 200 || out["directiveId"] == nil || out["run"] != child {
		t.Fatalf("steer: %d %s", status, body)
	}
	id := out["directiveId"].(string)
	var by string
	var interrupt bool
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(conductor_run_id, ''), interrupt FROM directives
		WHERE id = $1 AND run_id = $2`, id, child).Scan(&by, &interrupt); err != nil {
		t.Fatal(err)
	}
	if by != conductor || !interrupt {
		t.Errorf("the directive: by %q, interrupt %v", by, interrupt)
	}
	lr := w.luxRunOf(child)
	w.until("lux to interrupt the child", func() bool {
		for _, r := range w.lux.Runs() {
			if r.ID == lr {
				return r.Interrupted == 1
			}
		}
		return false
	})
	if note := w.wokenWith(task, "read your steer "+id); !strings.Contains(note, child) {
		t.Errorf("the note: %q", note)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'steer_read' AND key = $2`,
		task, "steer_read:"+id); n != 1 {
		t.Errorf("%d steer_read reasons for the steer, want 1", n)
	}
}

// A child whose parent is another conductor of the task — one that ended,
// its child not yet told to abort — is not the conductor's to steer: it is
// refused saying so, and nothing is written.
func TestSteeringAnotherConductorsChildIsRefused(t *testing.T) {
	w := conducting(t)
	task, _, _ := w.liveChild()
	spec := w.conductorSpecOf(task)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role)
		VALUES ('run_oldconductor', $1, $2, $3, 1, 'completed', 'conductor')`, w.org, w.project, task)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, source_kind,
		parent_run_id, conductor_run_id, child_profile, child_capabilities)
		VALUES ('run_otherschild', $1, $2, $3, 1, 'running', 'investigate', 'investigator', 'task_conductor',
		'run_oldconductor', 'run_oldconductor', 'investigator', '{}')`, w.org, w.project, task)
	written := func() int {
		return w.count(`SELECT (SELECT count(*) FROM directives WHERE task_id = $1)
			+ (SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.steered')`, task)
	}
	before := written()
	status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "steer", steerArgs("run_otherschild", "hello"))
	if status != 422 || !strings.Contains(body, "not your child") {
		t.Errorf("steer another conductor's child: %d %s", status, body)
	}
	if n := written(); n != before {
		t.Errorf("the refusal wrote %d rows", n-before)
	}
}
