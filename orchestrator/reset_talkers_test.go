package orchestrator_test

// Reset of the two Runs that are not a task's phases: a session's agent and
// a task's conductor, each raised by its own request_machine first.

import (
	"strings"
	"testing"
	"time"
)

// A session's live agent raises its machine, and a member's Reset moves the
// same Run back down: the Run is found by its session and role, the layer
// cleared, and the agent told who moved it and why.
func TestResetMovesALiveSessionAgentBackDown(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	s.syncer.ConductorWarm = time.Hour
	s.join(s.session(), s.ana, "chat")
	id := s.str(`SELECT id FROM sessions WHERE organization_id = $1`, s.org)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "how big is the archive?"})
	run := s.started(id)
	s.until("the brainstorm live", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running' AND control = 'none'`, run) == 1
	})
	if m := s.machineOf(run); m["name"] != "Small" {
		t.Fatalf("the brainstorm started on %v", m)
	}
	live := s.previewLux(run)
	if status, out := s.tool(run, "request_machine", `{"size":"Standard","reason":"sqlite killed loading archive.db"}`); status != 200 {
		t.Fatalf("request: %d %v", status, out)
	}
	s.until("the raise settled", func() bool {
		return s.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND kind = 'raise' AND settled_at IS NOT NULL AND refused IS NULL`, run) == 1 &&
			s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running' AND control = 'none'`, run) == 1
	})
	if m := s.machineOf(run); m["name"] != "Standard" || m["from"] != "session" {
		t.Fatalf("raised to %v", m)
	}

	status, out := s.as(s.ana, "POST", "/internal/agent-machines/reset", map[string]any{"sessionId": id, "note": "the archive fits now"})
	if status != 200 || out["moved"] != true {
		t.Fatalf("reset: %d %v", status, out)
	}
	s.until("the reset settled", func() bool {
		return s.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND kind = 'reset' AND settled_at IS NOT NULL AND refused IS NULL`, run) == 1
	})
	if s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND agent_machine IS NULL`, id) != 1 {
		t.Fatal("the session's layer is still set")
	}
	if m := s.machineOf(run); m["name"] != "Small" {
		t.Fatalf("after the reset: %v", m)
	}
	inputs := s.lux.ResumeInputs(live)
	if len(inputs) != 2 || !strings.HasPrefix(inputs[1], "Moved back to Small by Ana Nunes: the archive fits now") ||
		!strings.HasPrefix(inputs[0], "You now run on Standard (2 CPUs · 8 GiB · 20 GiB).") {
		t.Fatalf("resume inputs %q", inputs)
	}
	if s.previewLux(run) != live || s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id) != 1 {
		t.Fatal("the session's agent was replaced, not moved")
	}
	if s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'machine.reset' AND payload->>'moveId' <> ''
		AND payload->'by'->>'id' = $2`, id, s.ana) != 1 {
		t.Fatal("machine.reset not recorded with its move")
	}
}

// A raise that replaces the session's layer while its Reset is under way
// (during lux's dry run) is kept: the Reset drops only the raise it read.
func TestASessionResetDropsOnlyTheRaiseItRead(t *testing.T) {
	s := newSessionWorld(t)
	s.syncer.ConductorWarm = time.Hour
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	s.until("the brainstorm live", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running' AND control = 'none'`, run) == 1
	})
	standard := s.str(`SELECT id FROM machine_sizes WHERE organization_id = $1 AND name = 'Standard'`, s.org)
	mustExec(t, s.owner, `UPDATE sessions SET agent_machine = jsonb_build_object('sizeId', $2::text, 'moveId', 'mmv_read', 'askedBy', $3::text,
		'reason', 'oom', 'at', '2026-10-13T00:00:00Z') WHERE id = $1`, id, standard, run)
	mustExec(t, s.owner, `UPDATE runs SET machine = machine || jsonb_build_object('sizeId', $2::text, 'name', 'Standard', 'cpus', 2,
		'memoryMiB', 8192, 'diskGiB', 20, 'from', 'session') WHERE id = $1`, run, standard)
	live := s.previewLux(run)
	s.lux.OnResume = func(lr string, validateOnly bool) {
		if lr == live && validateOnly {
			mustExec(t, s.owner, `UPDATE sessions SET agent_machine = agent_machine || '{"moveId":"mmv_newer"}' WHERE id = $1`, id)
		}
	}
	if status, out := s.as(s.marcio, "POST", "/internal/agent-machines/reset", map[string]any{"sessionId": id}); status != 200 {
		t.Fatalf("reset: %d %v", status, out)
	}
	if s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND agent_machine->>'moveId' = 'mmv_newer'`, id) != 1 {
		t.Fatal("the newer raise was dropped")
	}
}

// A task's live conductor raises its machine, and a person's Reset of the
// conductor's role moves that Run back down.
func TestResetMovesALiveConductorBackDown(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	w.until("the conductor live", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running' AND control = 'none'`, conductor) == 1
	})
	if m := w.machineOf(conductor); m["name"] != "Small" {
		t.Fatalf("the conductor started on %v", m)
	}
	live := w.luxRunOf(conductor)
	w.must(task, "request_machine", `{"size":"Standard","reason":"the backend suite was killed, exit 137"}`)
	w.until("the raise settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND kind = 'raise' AND settled_at IS NOT NULL AND refused IS NULL`, conductor) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running' AND control = 'none'`, conductor) == 1
	})
	if m := w.machineOf(conductor); m["name"] != "Standard" || m["from"] != "task" {
		t.Fatalf("raised to %v", m)
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND agent_machines->'conductor'->>'askedBy' = $2`, task, conductor) != 1 {
		t.Fatal("the conductor's raise is not on the task")
	}

	person := w.person("Resetter")
	status, out := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "conductor"})
	if status != 200 || out["moved"] != true {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.until("the reset settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND kind = 'reset' AND settled_at IS NOT NULL AND refused IS NULL`, conductor) == 1
	})
	if w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND agent_machines = '{}'`, task) != 1 {
		t.Fatal("the task's layer is still set")
	}
	if m := w.machineOf(conductor); m["name"] != "Small" {
		t.Fatalf("after the reset: %v", m)
	}
	inputs := w.lux.ResumeInputs(live)
	if len(inputs) != 2 || !strings.HasPrefix(inputs[1], "Moved back to Small by Resetter.") {
		t.Fatalf("resume inputs %q", inputs)
	}
	if id, _, _ := w.conductor(task); id != conductor || w.luxRunOf(conductor) != live {
		t.Fatal("the conductor was replaced, not moved")
	}
}
