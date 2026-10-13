package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func TestMachineResetWithoutActiveRun(t *testing.T) {
	for _, note := range []string{"", "Use fewer workers"} {
		t.Run(note, func(t *testing.T) {
			w := newWorld(t)
			task := w.task()
			mustExec(t, w.owner, `UPDATE tasks SET agent_machines='{"implementer":{"sizeId":"msz_standard","moveId":"mmv_old","askedBy":"run_old","reason":"oom","at":"2026-10-13T00:00:00Z"}}' WHERE id=$1`, task)
			person := w.person("Resetter")
			status, out := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer", "note": note})
			if status != 200 {
				t.Fatalf("reset: %d %v", status, out)
			}
			if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines='{}'`, task) != 1 {
				t.Fatal("layer not cleared")
			}
			if w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='machine.reset' AND payload->>'note'=$2 AND run_id IS NULL`, task, note) != 1 {
				t.Fatal("missing reset note event, on the task alone")
			}
			if w.count(`SELECT count(*) FROM machine_moves WHERE task_id=$1`, task) != 0 {
				t.Fatal("inactive Reset created a move")
			}
			if status, _ := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 404 {
				t.Fatalf("empty reset=%d", status)
			}
		})
	}
}

func TestMachineResetActiveRunValidatesBeforeMove(t *testing.T) {
	for _, tc := range []struct {
		name, note string
		refuse     bool
	}{{"accepted without note", "", false}, {"accepted with note", "Use two workers", false}, {"refused", "x", true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.lux.Decide = hang
			task := w.task()
			w.deliver(task)
			w.until("live", func() bool { return len(w.lux.Runs()) == 1 && w.lux.State(w.lux.Runs()[0].ID) == "running" })
			runID := w.str(`SELECT id FROM runs WHERE task_id=$1 AND phase='implement'`, task)
			w.until("recorded running", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none'`, runID) == 1
			})
			mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
			mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', jsonb_build_object('sizeId','msz_big','moveId','mmv_old','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, runID)
			mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
			if tc.refuse {
				mustExec(t, w.owner, `UPDATE machine_sizes SET pool_id='pool_missing' WHERE organization_id=$1 AND is_default`, w.org)
			}
			live := w.lux.Runs()[0].ID
			person := w.person("Resetter")
			status, out := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer", "note": tc.note})
			if tc.refuse {
				e, _ := out["error"].(map[string]any)
				if status != 422 || e["code"] != "unknown_pool" || e["message"] != "Standard's pool has no host it fits" {
					t.Fatalf("refusal: %d %v", status, out)
				}
				if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer'->>'moveId'='mmv_old'`, task) != 1 ||
					w.count(`SELECT count(*) FROM runs WHERE id=$1 AND control='none'`, runID) != 1 || w.count(`SELECT count(*) FROM machine_moves`) != 0 {
					t.Fatal("refused Reset changed state")
				}
				return
			}
			if status != 200 {
				t.Fatalf("reset: %d %v", status, out)
			}
			w.until("reset settled", func() bool {
				return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND kind='reset' AND settled_at IS NOT NULL`, runID) == 1
			})
			want := "Moved back to Standard by Resetter."
			if tc.note != "" {
				want = "Moved back to Standard by Resetter: " + tc.note
			}
			if inputs := w.lux.ResumeInputs(live); len(inputs) == 0 || !strings.HasPrefix(inputs[len(inputs)-1], want+"\n\n") {
				t.Fatalf("resume inputs %q", inputs)
			}
			if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines='{}'`, task) != 1 || len(w.lux.Runs()) != 1 {
				t.Fatal("Reset did not clear layer on the same Run")
			}
			if got := w.machineOf(runID); got["name"] != "Standard" {
				t.Fatalf("machine=%v", got)
			}
			if status, _ := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 404 {
				t.Fatalf("second reset=%d", status)
			}
			if tc.note == "" {
				return
			}
			// Three raises already used: the Reset gives none back.
			for i := range 3 {
				mustExec(t, w.owner, `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, to_size_id, apply, settled_at) VALUES ($1, $2, $3, $4, 'implementer', 'raise', '{}', 'msz_big', 'resize', now())`, fmt.Sprintf("mmv_used%d", i), w.org, task, runID)
			}
			w.until("running again", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none'`, runID) == 1
			})
			srv := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux, ValidateMachine: func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error { return nil }}).Handler())
			defer srv.Close()
			token, hash := agenttools.NewToken()
			mustExec(t, w.owner, `UPDATE runs SET mcp_token_hash=$2 WHERE id=$1`, runID, hash)
			req, _ := http.NewRequest("POST", srv.URL+"/tools/request_machine", strings.NewReader(`{"size":"Bigger","reason":"oom again"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 422 || !strings.Contains(string(body), "a Reset does not give a move back") {
				t.Fatalf("quota after Reset: %d %s", res.StatusCode, body)
			}
		})
	}
}

// A raise that replaces the task's layer while a Reset is under way (lux's
// dry run is between the Reset's read of the layer and its write) is kept:
// the Reset drops only the raise it read, by its moveId.
func TestResetDropsOnlyTheRaiseItRead(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', jsonb_build_object('sizeId','msz_big','moveId','mmv_read','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, runID)
	mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
	newer := `{"sizeId":"msz_big","moveId":"mmv_newer","askedBy":"run_other","reason":"again","at":"2026-10-13T01:00:00Z"}`
	w.lux.OnResume = func(id string, validateOnly bool) {
		if id == live && validateOnly {
			mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', $2::jsonb) WHERE id=$1`, task, newer)
		}
	}
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 200 {
		t.Fatalf("reset: %d %v", status, out)
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer' = $2::jsonb`, task, newer) != 1 {
		t.Fatalf("the newer raise was dropped: %s", w.str(`SELECT agent_machines::text FROM tasks WHERE id=$1`, task))
	}
}

func TestMachineResetKeepsPersonPause(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = hang
	task := w.task()
	w.deliver(task)
	w.until("live", func() bool { return len(w.lux.Runs()) == 1 && w.lux.State(w.lux.Runs()[0].ID) == "running" })
	runID := w.str(`SELECT id FROM runs WHERE task_id=$1 AND phase='implement'`, task)
	w.until("recorded running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none'`, runID) == 1
	})
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', jsonb_build_object('sizeId','msz_big','moveId','mmv_old','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, runID)
	mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, out)
	}
	w.until("person paused", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, runID) == 1 })
	live := w.lux.Runs()[0].ID
	resumes := w.luxCalls(live, "resume")
	person := w.person("Resetter")
	if status, out := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer", "note": "smaller"}); status != 200 {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.sweep()
	time.Sleep(150 * time.Millisecond)
	w.sweep()
	if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND dude_pause IS NULL`, runID) != 1 || w.luxCalls(live, "resume") != resumes {
		t.Fatal("Reset resumed a person's pause")
	}
	if w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND kind='reset' AND settled_at IS NULL`, runID) != 1 {
		t.Fatal("pending Reset move missing")
	}
	if status, _ := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 404 {
		t.Fatalf("cleared layer reset=%d", status)
	}
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines='{"implementer":{"sizeId":"msz_big","moveId":"mmv_new","askedBy":"run_x","reason":"oom","at":"2026-10-13T00:00:00Z"}}' WHERE id=$1`, task)
	if status, out := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 409 || out["error"].(map[string]any)["code"] != "pending" {
		t.Fatalf("pending reset=%d %v", status, out)
	}
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines='{}' WHERE id=$1`, task)
	w.resumeAgent(runID, w.lux.Runs()[0])
	w.until("settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, runID) == 1
	})
	if inputs := w.lux.ResumeInputs(live); len(inputs) == 0 || !strings.HasPrefix(inputs[len(inputs)-1], "Moved back to Standard by Resetter: smaller") {
		t.Fatalf("inputs %q", inputs)
	}
}

func TestSessionMachineResetIsForMembers(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "read")
	mustExec(t, s.owner, `UPDATE sessions SET agent_machine='{"sizeId":"msz_standard","moveId":"mmv_s","askedBy":"run_s","reason":"oom","at":"2026-10-13T00:00:00Z"}' WHERE id=$1`, id)
	if status, _ := s.as(s.outsider, "POST", "/internal/agent-machines/reset", map[string]any{"sessionId": id}); status != 404 {
		t.Fatalf("outsider=%d", status)
	}
	if status, out := s.as(s.ana, "POST", "/internal/agent-machines/reset", map[string]any{"sessionId": id, "note": "fits"}); status != 200 {
		t.Fatalf("member: %d %v", status, out)
	}
	if s.count(`SELECT count(*) FROM sessions WHERE id=$1 AND agent_machine IS NULL`, id) != 1 ||
		s.count(`SELECT count(*) FROM events WHERE session_id=$1 AND event_type='machine.reset' AND payload->>'note'='fits' AND payload->'by'->>'id'=$2`, id, s.ana) != 1 {
		t.Fatal("session reset not recorded")
	}
	if status, _ := s.as(s.ana, "POST", "/internal/agent-machines/reset", map[string]any{"sessionId": id}); status != 404 {
		t.Fatalf("again=%d", status)
	}
	long := strings.Repeat("é", 2001)
	if status, _ := s.as(s.ana, "POST", "/internal/agent-machines/reset", map[string]any{"sessionId": id, "note": long}); status != 400 {
		t.Fatalf("long note=%d", status)
	}
}

// liveImplementer delivers a task and returns it with its implementer Run
// once lux and dude both have it running.
func (w *world) liveImplementer() (task, runID, live string) {
	w.t.Helper()
	w.lux.Decide = hang
	task = w.task()
	w.deliver(task)
	w.until("live agent", func() bool { return len(w.lux.Runs()) == 1 && w.lux.State(w.lux.Runs()[0].ID) == "running" })
	runID = w.str(`SELECT id FROM runs WHERE task_id=$1 AND phase='implement'`, task)
	w.until("recorded running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none'`, runID) == 1
	})
	return task, runID, w.lux.Runs()[0].ID
}

// requestMachine calls request_machine as the Run's agent, validating with
// lux's dry run as production does.
func (w *world) requestMachine(runID, body string) (int, string) {
	w.t.Helper()
	srv := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux, ValidateMachine: func(ctx context.Context, id string, from, to *delivery.Machine, before lux.Run) error {
		return phases.ValidateResize(ctx, w.syncer.Lux, id, phases.PlanResize(ctx, w.syncer.Lux, from, to, before))
	}}).Handler())
	defer srv.Close()
	token, hash := agenttools.NewToken()
	mustExec(w.t, w.owner, `UPDATE runs SET mcp_token_hash=$2 WHERE id=$1`, runID, hash)
	req, _ := http.NewRequest("POST", srv.URL+"/tools/request_machine", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

// A fixer's raise is fixer@task: with the organization naming a fixer size,
// the fix Run still resumes on the size it asked for, the implementer's
// layer is untouched, and only the fixer's Reset moves the fix Run.
func TestAFixersRaiseIsTheFixersAndOnlyItsResetMovesIt(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
			return fakelux.Behaviour{Hang: true}
		}
		return scripted(spec)
	}
	standard := w.str(`SELECT id FROM machine_sizes WHERE organization_id=$1 AND name='Standard'`, w.org)
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	mustExec(t, w.owner, `UPDATE organizations SET default_agent_models = default_agent_models || jsonb_build_object('fixer', jsonb_build_object('machineSize', $2::text)) WHERE id = $1`, w.org, standard)
	task := w.task()
	w.deliver(task)
	fix := ""
	w.until("a live fix Run", func() bool {
		fix = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id=$1 AND phase='fix' AND status='running' AND control='none' AND lux_state='running'), '')`, task)
		return fix != ""
	})
	if status, body := w.requestMachine(fix, `{"size":"Bigger","reason":"go test killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("the move settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, fix) == 1
	})
	if got := w.machineOf(fix); got["name"] != "Bigger" || got["from"] != "task" {
		t.Fatalf("fix Run's machine=%v", got)
	}
	if w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND role='fixer' AND refused IS NULL AND refused_message IS NULL`, fix) != 1 ||
		w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.refused'`, fix) != 0 {
		t.Fatal("the fixer's move was not settled as moved, under fixer")
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines ? 'fixer' AND NOT agent_machines ? 'implementer'`, task) != 1 {
		t.Fatalf("layer %s", w.str(`SELECT agent_machines::text FROM tasks WHERE id=$1`, task))
	}
	// An implementer raise of its own: its Reset must not reach the fix Run.
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines = agent_machines || '{"implementer":{"sizeId":"msz_big","moveId":"mmv_impl","askedBy":"run_x","reason":"oom","at":"2026-10-13T00:00:00Z"}}' WHERE id=$1`, task)
	person := w.person("Resetter")
	status, out := w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"})
	if status != 200 || out["moved"] != false {
		t.Fatalf("implementer reset: %d %v", status, out)
	}
	if w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1`, fix) != 1 || w.machineOf(fix)["name"] != "Bigger" {
		t.Fatal("the implementer's Reset moved the fix Run")
	}
	w.until("the fix Run running on lux again", func() bool {
		return w.lux.State(w.str(`SELECT lux_run_id FROM runs WHERE id=$1`, fix)) == "running"
	})
	status, out = w.callAs(person, "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "fixer"})
	if status != 200 || out["moved"] != true {
		t.Fatalf("fixer reset: %d %v", status, out)
	}
	w.until("the Reset settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND kind='reset' AND settled_at IS NOT NULL`, fix) == 1
	})
	if got := w.machineOf(fix); got["name"] != "Standard" {
		t.Fatalf("after the fixer's Reset machine=%v", got)
	}
}

// A fixer with no raise of its own runs on the implementer's: the
// implementer's Reset moves the live fix Run, to the size the fixer's own
// settings name when they name one.
func TestTheImplementersResetMovesAFixerOnItsRaise(t *testing.T) {
	for _, tc := range []struct{ name, fixerSize, want string }{
		{"no fixer setting", "", "Standard"},
		{"the fixer's setting", "msz_fixer", "Fixer size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			scripted := w.lux.Decide
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
					return fakelux.Behaviour{Hang: true}
				}
				return scripted(spec)
			}
			mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
			if tc.fixerSize != "" {
				mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ($2, $1, $3, 3, 12288, 30)`, w.org, tc.fixerSize, tc.want)
				mustExec(t, w.owner, `UPDATE organizations SET default_agent_models = default_agent_models || jsonb_build_object('fixer', jsonb_build_object('machineSize', $2::text)) WHERE id = $1`, w.org, tc.fixerSize)
			}
			task := w.task()
			w.deliver(task)
			fix := ""
			w.until("a live fix Run", func() bool {
				fix = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id=$1 AND phase='fix' AND status='running' AND control='none' AND lux_state='running'), '')`, task)
				return fix != ""
			})
			mustExec(t, w.owner, `UPDATE tasks SET agent_machines='{"implementer":{"sizeId":"msz_big","moveId":"mmv_impl","askedBy":"run_x","reason":"oom","at":"2026-10-13T00:00:00Z"}}' WHERE id=$1`, task)
			mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, fix)
			status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"})
			if status != 200 || out["moved"] != true {
				t.Fatalf("implementer reset: %d %v", status, out)
			}
			w.until("the Reset settled", func() bool {
				return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND kind='reset' AND settled_at IS NOT NULL`, fix) == 1
			})
			if n := w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1`, fix); n != 1 {
				t.Fatalf("moves on the fix Run: %d", n)
			}
			if got := w.machineOf(fix); got["name"] != tc.want {
				t.Fatalf("fix Run's machine=%v, want %s", got, tc.want)
			}
			if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.reset' AND payload->'to'->>'name'=$2`, fix, tc.want) != 1 {
				t.Fatalf("the Reset's notice does not name %s", tc.want)
			}
		})
	}
}

// raisedImplementer is a live implementer recorded on Bigger, raised for
// its task under moveId mmv_old.
func (w *world) raisedImplementer() (task, runID, live string) {
	w.t.Helper()
	task, runID, live = w.liveImplementer()
	mustExec(w.t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	mustExec(w.t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', jsonb_build_object('sizeId','msz_big','moveId','mmv_old','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, runID)
	mustExec(w.t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
	return task, runID, live
}

// Two Resets of a paused Run racing past the pending check: the second's
// move row meets the one-at-a-time index and is answered 409, not 500.
func TestASecondResetRacingTheFirstIsAConflict(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.raisedImplementer()
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, out)
	}
	w.until("paused", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, runID) == 1 })
	// The other Reset's row lands while this one's dry run is with lux.
	w.lux.OnResume = func(id string, validateOnly bool) {
		if id == live && validateOnly {
			mustExec(t, w.owner, `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, to_size_id, apply) VALUES ('mmv_other', $1, $2, $3, 'implementer', 'reset', '{}', 'msz_big', 'resize')`, w.org, task, runID)
		}
	}
	status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"})
	if e, _ := out["error"].(map[string]any); status != 409 || e["code"] != "pending" {
		t.Fatalf("racing reset: %d %v", status, out)
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer'->>'moveId'='mmv_old'`, task) != 1 {
		t.Fatal("the refused Reset cleared the layer")
	}
}

// A person's pause asked for and not yet taken (control pending, the Run
// still running): a Reset then cannot park the Run for its move, so it is
// a conflict, and the person's pause is not turned into dude's.
func TestAResetDuringAPersonsPauseInFlightIsAConflict(t *testing.T) {
	w := newWorld(t)
	task, runID, _ := w.raisedImplementer()
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, out)
	}
	if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control<>'none' AND dude_pause IS NULL`, runID) != 1 {
		t.Fatalf("the pause is not in flight:\n%s", w.describeRuns())
	}
	control := w.str(`SELECT control::text FROM runs WHERE id=$1`, runID)
	before := w.str(`SELECT agent_machines::text FROM tasks WHERE id=$1`, task)
	status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"})
	if e, _ := out["error"].(map[string]any); status != 409 || e["code"] != "pending" {
		t.Fatalf("reset: %d %v", status, out)
	}
	if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND dude_pause IS NULL AND control::text=$2`, runID, control) != 1 {
		t.Fatalf("the person's pause was changed:\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM machine_moves`); n != 0 {
		t.Fatalf("moves recorded: %d", n)
	}
	if after := w.str(`SELECT agent_machines::text FROM tasks WHERE id=$1`, task); after != before {
		t.Fatalf("layer %s, was %s", after, before)
	}
}

// A Run lux has not started yet (scheduled) is not "nothing running": its
// Reset is refused until it runs or pauses.
func TestAResetOfAScheduledRunIsAConflict(t *testing.T) {
	w := newWorld(t)
	task, runID, _ := w.raisedImplementer()
	mustExec(t, w.owner, `UPDATE runs SET status='scheduled' WHERE id=$1`, runID)
	status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"})
	if e, _ := out["error"].(map[string]any); status != 409 || e["code"] != "pending" {
		t.Fatalf("reset: %d %v", status, out)
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer'->>'moveId'='mmv_old'`, task) != 1 {
		t.Fatal("the layer was cleared")
	}
}

// A Reset whose settings size gives the Run what it has already (same
// numbers, same pool) parks nothing: the layer is cleared, no move is
// recorded, and no refusal is said.
func TestAResetToTheSameNumbersOnlyClearsTheLayer(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_same', $1, 'Same', 2, 8192, 20)`, w.org)
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines='{"implementer":{"sizeId":"msz_same","moveId":"mmv_old","askedBy":"run_x","reason":"oom","at":"2026-10-13T00:00:00Z"}}' WHERE id=$1`, task)
	mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_same","name":"Same","from":"task"}' WHERE id=$1`, runID)
	status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"})
	if status != 200 || out["moved"] != false {
		t.Fatalf("reset: %d %v", status, out)
	}
	for range 3 {
		w.pump()
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines='{}'`, task) != 1 || w.count(`SELECT count(*) FROM machine_moves`) != 0 ||
		w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none'`, runID) != 1 || w.luxCalls(live, "stop") != 0 ||
		w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='machine.refused'`, task) != 0 {
		t.Fatalf("a Reset to the same numbers moved the Run: lux calls %v", w.lux.CallsOf(live))
	}
}

// A process that dies after lux accepted the sized resume, before dude
// recorded it: the next sweep's resume finds lux resuming already, and the
// move is settled from what lux then reports the Run has.
func TestMachineMoveSettlesAfterRestartFollowingAcceptedResume(t *testing.T) {
	w := newWorld(t)
	_, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"jest killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("parked", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, runID) == 1 })
	// lux takes the sized resume; its answer never reaches the process.
	lost := &lostAnswer{Client: w.syncer.Lux}
	lost.left.Store(1)
	w.syncer.Lux = lost
	w.until("settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL AND refused IS NULL`, runID) == 1
	})
	if got := w.machineOf(runID); got["name"] != "Bigger" || got["from"] != "task" {
		t.Fatalf("machine=%v", got)
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.refused'`, runID) != 0 ||
		w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.raised'`, runID) != 1 {
		t.Fatal("wrong settlement events")
	}
	if len(w.lux.Runs()) != 1 || w.lux.Runs()[0].ID != live || lost.left.Load() != 0 {
		t.Fatal("the answer was not lost on the same Run")
	}
	if got := w.resumeResources(live); len(got) == 0 || got[0] == nil || got[0].CPUs != 4 {
		t.Fatalf("lux took %v, want the sized resume first", got)
	}
}

// lostAnswer is lux taking a real resume whose answer the process never
// gets, left times: what a process dying mid-call leaves.
type lostAnswer struct {
	lux.Client
	left atomic.Int32
}

func (l *lostAnswer) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	res, err := l.Client.Resume(ctx, id, in)
	if err == nil && !in.ValidateOnly && l.left.Add(-1) >= 0 {
		return lux.Run{}, errors.New("connection reset by peer")
	}
	return res, err
}

// lux refuses the real resume after the dry run passed (the pool went in
// between), and the process dies before the fallback resume reaches lux.
// The refusal was kept first, so the next sweep resumes without resources,
// tells the agent lux's exact reason once, and the prior layer is back.
func TestMachineMoveRefusalSurvivesRestartBeforeFallback(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40, $2)`, w.org, bigPool)
	standard := w.str(`SELECT id FROM machine_sizes WHERE organization_id=$1 AND name='Standard'`, w.org)
	prior := `{"sizeId":"` + standard + `","moveId":"mmv_prior","askedBy":"run_old","reason":"earlier","at":"2026-10-13T00:00:00Z"}`
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', $2::jsonb) WHERE id=$1`, task, prior)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"duckdb out of memory"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.lux.Pools = slices.DeleteFunc(fakelux.DefaultPools(), func(p lux.Pool) bool { return p.ID == bigPool })
	var real atomic.Int32
	w.lux.OnResume = func(id string, validateOnly bool) {
		if id == live && !validateOnly && real.Add(1) == 2 {
			// The fallback resume never reaches lux: the process is gone.
			panic(http.ErrAbortHandler)
		}
	}
	w.until("refusal kept", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND refused='unknown_pool' AND settled_at IS NULL`, runID) == 1
	})
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer'->>'moveId'='mmv_prior'`, task) != 1 {
		t.Fatal("prior layer not restored before the fallback")
	}
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("settled refused", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL AND refused='unknown_pool'`, runID) == 1
	})
	var message string
	if err := w.owner.QueryRow(context.Background(), `SELECT refused_message FROM machine_moves WHERE run_id=$1`, runID).Scan(&message); err != nil || message != "Bigger's pool has no host it fits" {
		t.Fatalf("message %q %v", message, err)
	}
	told := 0
	for _, in := range w.lux.ResumeInputs(live) {
		if strings.HasPrefix(in, "You asked for Bigger; lux kept you on Standard: Bigger's pool has no host it fits; ask for another size\n\n") {
			told++
		}
	}
	if told != 1 {
		t.Fatalf("exact reason delivered %d times: %q", told, w.lux.ResumeInputs(live))
	}
	if got := w.resumeResources(live); len(got) == 0 || got[len(got)-1] != nil {
		t.Fatalf("fallback carried resources: %v", got)
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer' = $2::jsonb`, task, prior) != 1 {
		t.Fatal("a refused raise kept its layer")
	}
	if got := w.machineOf(runID); got["name"] != "Standard" {
		t.Fatalf("machine=%v", got)
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.refused' AND payload->>'message'=$2 AND payload->'to'->>'name'='Bigger'`, runID, message) != 1 {
		t.Fatal("machine.refused not recorded once with the asked size")
	}
}

// lux refuses a Reset's real resume after its dry run passed (the pool went
// in between): the agent resumes where it was, told in the Reset's words
// that it was to move back and where lux kept it, and the notice says so.
func TestALuxRefusedResetIsToldAsAMoveBack(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"oom"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("raised on lux", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none'`, runID) == 1 && w.lux.State(live) == "running"
	})
	mustExec(t, w.owner, `UPDATE machine_sizes SET pool_id=$2 WHERE organization_id=$1 AND name='Standard'`, w.org, bigPool)
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"}); status != 200 || out["moved"] != true {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.lux.Pools = slices.DeleteFunc(fakelux.DefaultPools(), func(p lux.Pool) bool { return p.ID == bigPool })
	w.until("settled refused", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND kind='reset' AND settled_at IS NOT NULL AND refused='unknown_pool'`, runID) == 1
	})
	inputs := w.lux.ResumeInputs(live)
	if len(inputs) == 0 || !strings.HasPrefix(inputs[len(inputs)-1], "You were to move back to Standard; lux kept you on Bigger: Standard's pool has no host it fits.\n\n") {
		t.Fatalf("inputs %q", inputs)
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.refused' AND payload->>'kind'='reset'
		AND payload->>'message'='Standard''s pool has no host it fits' AND payload->'kept'->>'name'='Bigger'`, runID) != 1 {
		t.Fatal("machine.refused not recorded once for the Reset")
	}
}

// A refused raise puts back the layer it replaced only while the layer is
// still its own: another Run of the role (two reviewers share one) that
// raised it since keeps its raise.
func TestARefusedRaiseLeavesANewerRaiseAlone(t *testing.T) {
	w := newWorld(t)
	task, runID, _ := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40, $2)`, w.org, bigPool)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"oom"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	// The pool goes before the real resume, and another Run's raise
	// replaces this one's in the task's layer.
	w.lux.Pools = slices.DeleteFunc(fakelux.DefaultPools(), func(p lux.Pool) bool { return p.ID == bigPool })
	newer := `{"sizeId":"msz_big","moveId":"mmv_newer","askedBy":"run_other","reason":"again","at":"2026-10-13T01:00:00Z"}`
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', $2::jsonb) WHERE id=$1`, task, newer)
	w.until("settled refused", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL AND refused='unknown_pool'`, runID) == 1
	})
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer' = $2::jsonb`, task, newer) != 1 {
		t.Fatalf("the newer raise was replaced: %s", w.str(`SELECT agent_machines::text FROM tasks WHERE id=$1`, task))
	}
}

// A person pauses the Run while dude's machine park is under way: their
// pause stands (no resume on dude's account), the move stays pending, and
// their later resume carries it out.
func TestPersonPauseDuringMachineMoveIsKept(t *testing.T) {
	w := newWorld(t)
	_, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"oom"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, out)
	}
	resumes := w.luxCalls(live, "resume")
	w.until("paused", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, runID) == 1 })
	for range 3 {
		w.pump()
	}
	if w.luxCalls(live, "resume") != resumes || w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND dude_pause IS NULL`, runID) != 1 {
		t.Fatal("a person's pause was resumed on dude's account")
	}
	if w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NULL`, runID) != 1 {
		t.Fatal("move settled without a resume")
	}
	w.resumeAgent(runID, w.lux.Runs()[0])
	w.until("settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL AND refused IS NULL`, runID) == 1
	})
	if got := w.machineOf(runID); got["name"] != "Bigger" {
		t.Fatalf("machine=%v", got)
	}
}

func TestAgentMachineRequestResumesTheSameRun(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("live agent", func() bool { return len(w.lux.Runs()) == 1 && w.lux.State(w.lux.Runs()[0].ID) == "running" })
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	runID := w.str(`SELECT id FROM runs WHERE task_id=$1 AND phase='implement'`, wi)
	w.until("recorded running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none'`, runID) == 1
	})
	liveID := w.lux.Runs()[0].ID
	srv := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux, ValidateMachine: func(ctx context.Context, id string, from, to *delivery.Machine, before lux.Run) error {
		return phases.ValidateResize(ctx, w.syncer.Lux, id, phases.PlanResize(ctx, w.syncer.Lux, from, to, before))
	}}).Handler())
	defer srv.Close()
	token, hash := agenttools.NewToken()
	mustExec(t, w.owner, `UPDATE runs SET mcp_token_hash=$2 WHERE id=$1`, runID, hash)
	req, _ := http.NewRequest("POST", srv.URL+"/tools/request_machine", strings.NewReader(`{"size":"Bigger","reason":"suite killed at four workers"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("request: %d %s", res.StatusCode, body)
	}
	w.until("move settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, runID) == 1
	})
	if len(w.lux.Runs()) != 1 || w.lux.Runs()[0].ID != liveID {
		t.Fatal("machine move replaced Run")
	}
	if got := w.machineOf(runID); got["name"] != "Bigger" || got["from"] != "task" {
		t.Fatalf("machine=%v", got)
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.raised'`, runID) != 1 {
		t.Fatal("missing raised settlement")
	}
	want := "You now run on Bigger (4 CPUs · 16 GiB · 40 GiB). Start again any server or watcher you had running. Carry on.\n\n"
	if inputs := w.lux.ResumeInputs(liveID); len(inputs) != 1 || !strings.HasPrefix(inputs[0], want) {
		t.Fatalf("resume inputs %q", inputs)
	}
}

// The scripted implementer asks for a larger machine in its first turn.
// Accepted, that turn ends without the work being done or the phase
// finishing: the Run is parked, resumed on the size, and its work is the
// resumed turn's. Refused (nothing larger by that name), the turn is not
// held: it carries on and the phase finishes on its size.
func TestASuccessfulMachineRequestEndsTheTurnAndARefusedOneDoesNot(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			w := newWorld(t)
			w.withTools()
			w.onModel("implementer", fakeagent.MachineModel)
			if accepted {
				mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, $2, 4, 16384, 40)`,
					w.org, fakeagent.MachineSize)
			}
			task := w.task()
			w.deliver(task)
			runID := ""
			w.until("the implementer to end its first turn", func() bool {
				runID = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id=$1 AND phase='implement'), '')`, task)
				return runID != "" && len(w.lux.Runs()) > 0 && w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='agent.message'`, runID) > 0
			})
			live := w.lux.Runs()[0].ID
			if !accepted {
				w.until("the implementer to finish", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='completed'`, runID) == 1
				})
				if w.count(`SELECT count(*) FROM machine_moves`) != 0 || w.lux.Runs()[0].Resumed != 0 ||
					w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.requested' AND payload->>'refused' IS NULL`, runID) != 0 {
					t.Fatal("a refused request moved the Run")
				}
				if said := strings.Join(w.said(runID), "\n"); !strings.Contains(said, "Implemented it.") || strings.Contains(said, fakeagent.MachineEndedTurn) {
					t.Fatalf("a refused request ended the turn: %q", said)
				}
				return
			}
			w.until("the move settled and the implementer finished", func() bool {
				if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='completed'`, runID) == 1 &&
					w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1`, runID) == 0 {
					t.Fatalf("finished without moving; said %q, requested %s", w.said(runID),
						w.str(`SELECT COALESCE(string_agg(payload::text, ' | '), '') FROM events WHERE run_id=$1 AND event_type IN ('machine.requested', 'agent.tool.dude')`, runID))
				}
				return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL AND refused IS NULL`, runID) == 1 &&
					w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='completed'`, runID) == 1
			})
			// The turn the move was accepted in ended without the work: the
			// first agent message is the move's, and the work came after the
			// resume on the larger machine.
			said := w.said(runID)
			if len(said) < 2 || said[0] != fakeagent.MachineEndedTurn || !strings.Contains(strings.Join(said[1:], "\n"), "Implemented it.") {
				t.Fatalf("said %q", said)
			}
			if r := w.lux.Runs()[0]; r.ID != live || r.Resumed != 1 || len(w.lux.Runs()) != 1 {
				t.Fatalf("resumed %d times, %d lux Runs", r.Resumed, len(w.lux.Runs()))
			}
			if got := w.machineOf(runID); got["name"] != fakeagent.MachineSize {
				t.Fatalf("machine=%v", got)
			}
			if in := w.lux.ResumeInputs(live); len(in) != 1 || !strings.HasPrefix(in[0], "You now run on Bigger (4 CPUs · 16 GiB · 40 GiB).") {
				t.Fatalf("resume input %q", in)
			}
			// The turn that ended on the move was not taken as the work's end:
			// one turn_complete before the resume would have completed the
			// phase on Standard with nothing committed.
			if n := w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='agent.session.stopped'
				AND cursor < (SELECT min(cursor) FROM events WHERE run_id=$1 AND event_type='run.paused')`, runID); n != 0 {
				t.Fatalf("%d turn ends before the machine park", n)
			}
		})
	}
}

// lux's pool list failing at the move's resume: the Run is not resumed
// unsized (that would leave the move unsettled and the phase waiting for
// good); the resume is retried, and once the pools answer the Run resumes
// on the size and the phase finishes.
func TestAMoveWhosePoolsCannotBeReadAtTheResumeIsRetried(t *testing.T) {
	w := newWorld(t)
	pools := &poolsFailing{Client: w.syncer.Lux}
	w.syncer.Lux = pools
	w.withTools()
	w.onModel("implementer", fakeagent.MachineModel)
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, $2, 4, 16384, 40)`,
		w.org, fakeagent.MachineSize)
	task := w.task()
	w.deliver(task)
	runID := ""
	w.until("the move asked for", func() bool {
		runID = w.str(`SELECT COALESCE((SELECT run_id FROM machine_moves WHERE task_id=$1), '')`, task)
		return runID != ""
	})
	pools.fail.Store(true)
	live := w.str(`SELECT lux_run_id FROM runs WHERE id=$1`, runID)
	w.until("parked", func() bool { return w.lux.State(live) == "stopped" })
	for range 10 {
		mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
		w.sweep()
		// Each failed read spaces the next attempt, so pools that never
		// answer are not asked on every sweep.
		if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND next_attempt_at > now() + interval '3 seconds'`, runID) != 1 {
			t.Fatalf("the retry was not spaced:\n%s", w.describeRuns())
		}
	}
	reads := pools.calls.Load()
	w.sweep()
	if got := pools.calls.Load(); got != reads {
		t.Fatalf("a sweep inside the back-off read lux's pools: %d reads, was %d", got, reads)
	}
	if w.luxCalls(live, "resume") != 0 || w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND dude_pause='machine'`, runID) != 1 ||
		w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NULL`, runID) != 1 {
		t.Fatalf("resumed without lux's pools: resumes %d\n%s", w.luxCalls(live, "resume"), w.describeRuns())
	}
	pools.fail.Store(false)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the move settled and the implementer finished", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL AND refused IS NULL AND refused_message IS NULL`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='completed'`, runID) == 1
	})
	if got := w.machineOf(runID); got["name"] != fakeagent.MachineSize {
		t.Fatalf("machine=%v", got)
	}
}

// lux's pool list unreadable when a move is asked for: there is no dry run
// to make, so neither the agent's request nor a person's Reset parks the
// Run; the agent is told to ask again, and Reset answers 503.
func TestNoMoveIsParkedWhileLuxsPoolsCannotBeRead(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	pools := &poolsFailing{Client: w.syncer.Lux}
	pools.fail.Store(true)
	w.syncer.Lux = pools
	unchanged := func(what string) {
		t.Helper()
		if w.luxCalls(live, "stop") != 0 || w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none' AND dude_pause IS NULL`, runID) != 1 ||
			w.count(`SELECT count(*) FROM machine_moves`) != 0 {
			t.Fatalf("%s parked the Run: lux calls %v", what, w.lux.CallsOf(live))
		}
	}
	status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"oom"}`)
	var out struct{ Error string }
	_ = json.Unmarshal([]byte(body), &out)
	if status != 422 || out.Error != "lux's pools could not be read; ask again shortly" {
		t.Fatalf("request: %d %s", status, body)
	}
	unchanged("the request")
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines='{}'`, task) != 1 {
		t.Fatal("the request wrote the layer")
	}

	raise := `{"sizeId":"msz_big","moveId":"mmv_read","askedBy":"run_x","reason":"oom","at":"2026-10-13T00:00:00Z"}`
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', $2::jsonb) WHERE id=$1`, task, raise)
	mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
	down := httptest.NewServer((&api.Server{DB: w.app, Lux: pools, Token: "svc", Log: quiet, Kick: func() {}}).Handler())
	t.Cleanup(down.Close)
	w.api = down.URL
	code, res := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer"})
	if e, _ := res["error"].(map[string]any); code != 503 || e["code"] != "pools_unavailable" {
		t.Fatalf("reset: %d %v", code, res)
	}
	unchanged("the Reset")
	if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines->'implementer' = $2::jsonb`, task, raise) != 1 ||
		w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='machine.reset'`, task) != 0 {
		t.Fatal("the refused Reset changed the layer or recorded itself")
	}
}

// lux's dry run refuses the agent's move (its size's pool is gone): the
// agent gets lux's words and code at once, and nothing stops or changes.
func TestADryRunRefusalStopsNothing(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40, 'pool_missing')`, w.org)
	status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"oom"}`)
	var out struct{ Error, Code string }
	_ = json.Unmarshal([]byte(body), &out)
	if status != 422 || out.Code != "unknown_pool" || out.Error != "Bigger's pool has no host it fits; ask for another size" {
		t.Fatalf("request: %d %s", status, body)
	}
	for range 3 {
		w.pump()
	}
	if w.luxCalls(live, "stop") != 0 ||
		w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND control='none' AND dude_pause IS NULL`, runID) != 1 ||
		w.count(`SELECT count(*) FROM machine_moves`) != 0 || w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines='{}'`, task) != 1 {
		t.Fatalf("a refused dry run changed the Run: lux calls %v", w.lux.CallsOf(live))
	}
}

// A lux from before dry runs and pools (lux#93) answers 400 to validateOnly
// and to placement. The request is not refused for it: the Run is parked
// and resumed with the same body. In its pool the move applies (resources
// alone); to another pool lux refuses placement, and the Run resumes where
// it was, told so, with nothing recorded as moved and the layer put back.
func TestAnOlderLuxWithoutDryRunsOrPools(t *testing.T) {
	for _, tc := range []struct {
		name, size string
		pool       *string
	}{{"in its pool", "Bigger", nil}, {"to another pool", "Huge", ptr(bigPool)}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.lux.NoPlacement = true
			task, runID, live := w.liveImplementer()
			mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id) VALUES ('msz_new', $1, $2, 4, 16384, 40, $3)`,
				w.org, tc.size, tc.pool)
			status, body := w.requestMachine(runID, `{"size":"`+tc.size+`","reason":"jest killed, exit 137"}`)
			if status != 200 {
				t.Fatalf("an older lux's 400 to the dry run refused the request: %d %s", status, body)
			}
			w.until("the move settled", func() bool {
				return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, runID) == 1
			})
			inputs := w.lux.ResumeInputs(live)
			if tc.pool == nil {
				if got := w.machineOf(runID); got["name"] != tc.size || got["cpus"] != 4.0 {
					t.Fatalf("machine=%v", got)
				}
				if len(inputs) != 1 || !strings.HasPrefix(inputs[0], "You now run on Bigger (4 CPUs · 16 GiB · 40 GiB).") ||
					w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND refused IS NULL`, runID) != 1 {
					t.Fatalf("inputs %q", inputs)
				}
				return
			}
			// Refused for good: resumed where it was, without resources or
			// placement, and told the truth.
			want := "You asked for Huge; lux kept you on Standard: this lux cannot move a Run between pools yet. Ask for a size in your own pool.\n\n"
			if len(inputs) != 1 || !strings.HasPrefix(inputs[0], want) || strings.Contains(inputs[0], "You now run on") {
				t.Fatalf("inputs %q", inputs)
			}
			if got := w.resumeResources(live); len(got) != 1 || got[0] != nil {
				t.Fatalf("lux took %v", got)
			}
			if got := w.lux.ResumePlacementsOf(live); len(got) != 1 || got[0] != "" {
				t.Fatalf("placements %v", got)
			}
			got := w.machineOf(runID)
			if got["name"] != "Standard" || got["cpus"] != 2.0 || got["note"] != "Its settings now name Huge, but this resume kept Standard: this lux cannot move a stopped Run to another pool." {
				t.Fatalf("machine=%v", got)
			}
			if w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND refused='bad_request'`, runID) != 1 ||
				w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='machine.refused' AND payload->>'code'='bad_request'
					AND payload->'kept'->>'name'='Standard' AND payload->'to'->>'name'='Huge'`, runID) != 1 {
				t.Fatal("the refusal was not settled once")
			}
			if w.count(`SELECT count(*) FROM tasks WHERE id=$1 AND agent_machines='{}'`, task) != 1 {
				t.Fatal("the refused raise kept its layer")
			}
			if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.resized' AND payload->'machine'->>'name' <> 'Standard'`, runID) != 0 {
				t.Fatal("a resize to the refused size was recorded")
			}
		})
	}
}

// What a submitted agent is told of its machine: the sizes it may move to,
// with other pools named, in its prompt; a raised one says why it is on
// it; the conductor's prompt ends the same way.
func TestTheSubmittedPromptNamesTheSizesTheAgentMayMoveTo(t *testing.T) {
	// Real models, so each prompt is the one a real agent reads.
	w := conductorWorld(t, "llm-conductor")
	w.onModel("implementer", "llm-implementer")
	w.withTools()
	w.syncer.Agent.ToolsService = true
	w.lux.Decide = hang
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id) VALUES
		('msz_big', $1, 'Bigger', 4, 16384, 40, NULL), ('msz_huge', $1, 'Huge', 8, 32768, 80, $2), ('msz_vast', $1, 'Vast', 64, 262144, 80, $2)`, w.org, bigPool)
	task := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines='{"implementer":{"sizeId":"msz_big","moveId":"mmv_x","askedBy":"run_x","role":"implementer","reason":"jest killed, exit 137","at":"2026-10-13T00:00:00Z"}}' WHERE id=$1`, task)
	w.deliver(task)
	impl := ""
	w.until("the implementer on lux", func() bool {
		impl = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id=$1 AND phase='implement'), '')`, task)
		return impl != "" && w.promptOf(impl) != ""
	})
	prompt := w.promptOf(impl)
	want := "## Your machine\n\nYou run on Bigger (4 CPUs · 16 GiB · 40 GiB), raised for this task after the implementer asked: " +
		"\"jest killed, exit 137\". Larger sizes you may move to: Huge (8 · 32, big pool). "
	if !strings.Contains(prompt, want) || !strings.Contains(prompt, "dude machine request SIZE --reason") || strings.Contains(prompt, "Vast") {
		t.Fatalf("implementer's prompt:\n%s", prompt)
	}
	// The conductor of another task, on its Small: every larger size that
	// fits a host of its pool.
	other := w.task()
	status, out := w.call("/internal/tasks/"+other+"/talk", map[string]any{})
	conductor, _ := out["runId"].(string)
	if status != 201 || conductor == "" {
		t.Fatalf("talk: %d %v", status, out)
	}
	w.until("the conductor on lux", func() bool { return w.promptOf(conductor) != "" })
	if p := w.promptOf(conductor); !strings.HasSuffix(p, "\n\n## Your machine\n\nYou run on Small (0.5 CPUs · 1 GiB · 10 GiB). Larger sizes you may move to: "+
		"Standard (2 · 8), Bigger (4 · 16), Huge (8 · 32, big pool). If something you need to run is killed for memory (exit 137, "+
		"\"Killed\", an out-of-memory error) or is far too slow for its CPUs, do not call it flaky: run it again with fewer workers "+
		"if that is enough, and otherwise move with `dude machine request SIZE --reason \"…\"`, saying what ran out. A move keeps "+
		"your conversation and checkout; running processes and /tmp are not kept.") {
		t.Fatalf("conductor's prompt:\n%s", p)
	}
}

// Resizing a parked Run on resume (lux#51), through the fake lux's HTTP
// API: what dude sends, what lux's spec then holds, and what runs.machine
// records of lux's answer.

const gibB = int64(1) << 30

// Tiny is the size Jervasion's previews were moved to: half a CPU, 1 GiB
// of memory, 5 GiB of disk, in the default pool.
func (w *world) tiny() {
	mustExec(w.t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib)
		VALUES ('msz_tiny', $1, 'Tiny', 0.5, 1024, 5)`, w.org)
}

// resumeAgent asks for a paused agent Run to be resumed, as a person does
// (POST /internal/runs/{id}/resume), and sweeps until lux has the resume.
func (w *world) resumeAgent(runID string, r *fakelux.Run) {
	w.t.Helper()
	before := w.luxCalls(r.ID, "resume")
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		w.t.Fatalf("resume: %d %v", status, out)
	}
	w.until("lux to have the resume", func() bool { return w.luxCalls(r.ID, "resume") > before })
}

// resumeResources is the resources each resume lux accepted carried; nil
// for one that carried none.
func (w *world) resumeResources(luxRunID string) []*lux.Resources {
	var out []*lux.Resources
	for _, raw := range w.lux.ResumeResourcesOf(luxRunID) {
		var res *lux.Resources
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &res); err != nil {
				w.t.Fatal(err)
			}
		}
		out = append(out, res)
	}
	return out
}

// luxResources is what lux's stored spec of the Run holds now.
func (w *world) luxResources(luxRunID string) lux.Resources {
	var spec lux.Spec
	if err := json.Unmarshal(w.lux.SpecOf(luxRunID), &spec); err != nil || spec.Resources == nil {
		w.t.Fatalf("lux's spec: %v", err)
	}
	return *spec.Resources
}

func (w *world) machineOf(runID string) map[string]any {
	w.t.Helper()
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT machine FROM runs WHERE id = $1`, runID).Scan(&raw); err != nil {
		w.t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// previewLux is the lux Run of a preview.
func (w *world) previewLux(runID string) string {
	return w.str(`SELECT lux_run_id FROM runs WHERE id = $1`, runID)
}

// wakeAgain opens the asleep preview's server and waits for it to serve.
func (w *world) wakeAgain(runID, web string) {
	w.t.Helper()
	w.open(web)
	w.until("woken", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
}

// The JERV-2 case: a preview parked on Standard, its project's previews
// moved to Tiny, wakes on Tiny. The wake's resume sends Tiny's resources
// (the disk too: lux grows a disk, and shrinks it only when the saved
// state fits, as it does here), lux's spec has them, and runs.machine
// records what lux's answer says it applied.
func TestAWokenPreviewResumesOnTheSizeItsProjectNamesNow(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	if m := w.machineOf(runID); m["name"] != "Standard" || m["cpus"] != 2.0 {
		t.Fatalf("the preview started on %v, want Standard", m)
	}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tiny {
		t.Errorf("the wake sent %v, want Tiny's resources", got)
	}
	if got := w.luxResources(luxRun); got != tiny {
		t.Errorf("lux's spec = %+v, want Tiny's", got)
	}
	m := w.machineOf(runID)
	if m["name"] != "Tiny" || m["sizeId"] != "msz_tiny" || m["from"] != "project" || m["cpus"] != 0.5 || m["memoryMiB"] != 1024.0 ||
		m["diskGiB"] != 5.0 || m["note"] != nil || m["diskKept"] != nil {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}

	// Asleep and woken again on the same size: nothing is sent.
	w.lux.Idle(web)
	w.until("asleep again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 2 || got[1] != nil {
		t.Errorf("an unchanged size sent %v on the second wake, want nothing", got)
	}

	// Tiny's disk alone grows: that is a change, and lux grows the disk.
	mustExec(t, w.owner, `UPDATE machine_sizes SET disk_gib = 10 WHERE id = 'msz_tiny'`)
	w.lux.Idle(web)
	w.until("asleep a third time", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.wakeAgain(runID, web)
	tiny.Disk = 10 * gibB
	if got := w.resumeResources(luxRun); len(got) != 3 || got[2] == nil || *got[2] != tiny {
		t.Errorf("a disk-only change sent %v on the third wake, want Tiny with 10 GiB", got)
	}
	if m := w.machineOf(runID); m["diskGiB"] != 10.0 || m["cpus"] != 0.5 {
		t.Errorf("runs.machine = %v, want Tiny with 10 GiB", m)
	}
}

// A paused agent Run resumed by a person after its role's size changed
// resumes on the new size; resumed again with the size unchanged, it sends
// no resources.
func TestAnAgentRunResumesOnItsRolesSizeAndSendsNothingWhenUnchanged(t *testing.T) {
	w := newWorld(t)
	w.tiny()
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)
	r := w.lux.Runs()[0]

	// Unchanged: Standard, as it started.
	w.resumeAgent(runID, r)
	if got := w.resumeResources(r.ID); len(got) != 1 || got[0] != nil {
		t.Errorf("an unchanged size sent %v, want nothing", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 0 {
		t.Errorf("%d run.resized for an unchanged size", n)
	}

	w.until("running again", func() bool { return w.lux.State(r.ID) == "running" })
	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,machineSize}', '"msz_tiny"') WHERE id = $1`, w.project)
	w.pauseAgain(runID, r)
	w.resumeAgent(runID, r)
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(r.ID); len(got) != 2 || got[1] == nil || *got[1] != tiny {
		t.Errorf("the resume sent %v, want Tiny's", got)
	}
	if got := w.luxResources(r.ID); got != tiny {
		t.Errorf("lux's spec = %+v, want Tiny's", got)
	}
	if m := w.machineOf(runID); m["name"] != "Tiny" || m["cpus"] != 0.5 || m["diskGiB"] != 5.0 || m["from"] != "project" {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}
}

// pauseAgain pauses a running agent Run as a person would and waits for
// lux to stop it.
func (w *world) pauseAgain(runID string, r *fakelux.Run) {
	w.t.Helper()
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		w.t.Fatalf("pause: %d %v", status, out)
	}
	w.until("lux to stop it", func() bool {
		return w.lux.State(r.ID) == "stopped" && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
}

// A smaller disk lux will not apply (its saved state needs more): the Run
// resumes with Tiny's CPUs and memory and keeps its disk, and runs.machine
// says the disk lux kept, not the one asked for, with lux's reason. The
// next wake asks again (the state may fit by then); lux keeping the disk
// again changes nothing, so nothing is recorded: one run.resized, and the
// memory limit of the placement on Tiny stays.
func TestADiskShrinkLuxRefusesRecordsTheDiskItKept(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	w.lux.DiskUse = 8 * gibB // needs 10 GiB: more than Tiny's 5
	w.lux.MemoryShare = 0.95
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	if got := w.luxResources(luxRun); got != (lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 20 * gibB}) {
		t.Errorf("lux's spec = %+v, want Tiny's CPUs and memory on Standard's disk", got)
	}
	m := w.machineOf(runID)
	kept, _ := m["diskKept"].(map[string]any)
	if m["name"] != "Tiny" || m["cpus"] != 0.5 || m["memoryMiB"] != 1024.0 || m["diskGiB"] != 20.0 || kept == nil ||
		kept["requestedGiB"] != 5.0 || kept["reason"] != "its saved state used up to 8.0 GiB; a smaller disk must be at least 10.0 GiB (that plus max(25%, 1 GiB))" {
		t.Errorf("runs.machine = %v, want Tiny with the 20 GiB disk lux kept and why", m)
	}

	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(luxRun); len(got) != 2 || got[1] == nil || *got[1] != tiny {
		t.Errorf("the second wake sent %v, want Tiny's again", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 1 {
		t.Errorf("%d run.resized after lux kept the disk twice, want 1", n)
	}
	// 1 GiB, of which the container gets 95%, to a MiB: the placement on
	// Tiny's (epoch 2), recorded as the second wake read it.
	if m := w.machineOf(runID); m["sinceEpoch"] != 2.0 || m["memoryLimit"] != float64(int64(972)<<20) {
		t.Errorf("runs.machine = %v, want since epoch 2 with Tiny's memory limit", m)
	}
}

// sleepAgain lets the woken preview go idle and waits for it to be parked.
func (w *world) sleepAgain(runID, web string) {
	w.t.Helper()
	w.lux.Idle(web)
	w.until("asleep again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
}

// A settings-driven wake sends the other pool and records lux's binding.
func TestAWakeIntoASizeInAnotherPoolMovesToThatPool(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.lux.MemoryShare = 0.95
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
		VALUES ('msz_big', $1, 'Big', 8, 16384, 40, $2)`, w.org, bigPool)
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_big"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	want := lux.Resources{CPUs: 8, Memory: 16 * gibB, Disk: 40 * gibB}
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != want {
		t.Fatalf("resources: %v", got)
	}
	if got := w.lux.ResumePlacementsOf(luxRun); len(got) != 1 || got[0] != bigPool {
		t.Fatalf("placement: %v", got)
	}
	if got := w.luxResources(luxRun); got != want {
		t.Fatalf("lux's spec: %+v", got)
	}
	m := w.machineOf(runID)
	if m["name"] != "Big" || m["poolId"] != bigPool || m["note"] != nil {
		t.Fatalf("record: %v", m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 1 {
		t.Fatalf("run.resized: %d", n)
	}
}

// defaultPool is the fake lux's default pool's id (fakelux.DefaultPools).
const defaultPool = "pool_d4f7k2m9q1x8"

// A size naming lux's default pool by id is in the same pool as one naming
// none, either way round: a preview on Standard (no pool) resizes to Lean,
// which names the default by id (the Run's pool, as lux reports it); one
// on Lean resizes to Tiny (no pool), lux's pool list saying the Run's
// pool is its default.
func TestASizeNamingTheDefaultPoolByIdIsTheSamePoolAsNone(t *testing.T) {
	lean := lux.Resources{CPUs: 1, Memory: 2 << 30, Disk: 10 * gibB}
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	for name, tc := range map[string]struct {
		from, to string
		want     lux.Resources
		name     string
	}{
		"none to the default by id": {"", "msz_lean", lean, "Lean"},
		"the default by id to none": {"msz_lean", "msz_tiny", tiny, "Tiny"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.tiny()
			mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
				VALUES ('msz_lean', $1, 'Lean', 1, 2048, 10, $2)`, w.org, defaultPool)
			if tc.from != "" {
				mustExec(t, w.owner, `UPDATE projects SET preview_settings = jsonb_build_object('machineSize', $2::text) WHERE id = $1`, w.project, tc.from)
			}
			runID, web := w.asleepPreview()
			luxRun := w.previewLux(runID)

			mustExec(t, w.owner, `UPDATE projects SET preview_settings = jsonb_build_object('machineSize', $2::text) WHERE id = $1`, w.project, tc.to)
			w.wakeAgain(runID, web)
			if got := w.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tc.want {
				t.Errorf("the wake sent %v, want %s's", got, tc.name)
			}
			if m := w.machineOf(runID); m["name"] != tc.name || m["cpus"] != tc.want.CPUs || m["note"] != nil {
				t.Errorf("runs.machine = %v, want %s", m, tc.name)
			}
		})
	}
}

// poolsFailing is a lux whose pool list fails while fail is set; calls
// counts the lists asked for.
type poolsFailing struct {
	lux.Client
	fail  atomic.Bool
	calls atomic.Int32
}

func (c *poolsFailing) Pools(ctx context.Context) ([]lux.Pool, error) {
	c.calls.Add(1)
	if c.fail.Load() {
		return nil, &lux.Error{Status: 503, Code: "unavailable", Message: "down"}
	}
	return c.Client.Pools(ctx)
}

// An unavailable default pool list preserves the size; once readable,
// the next wake moves to that default and subsequent wakes send no resize.
func TestAWakeWhenLuxsPoolsCannotBeReadSendsAndRecordsNothing(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
		VALUES ('msz_big', $1, 'Big', 8, 16384, 40, $2)`, w.org, bigPool)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_big"}' WHERE id = $1`, w.project)
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	pools := &poolsFailing{Client: w.previews.Lux}
	pools.fail.Store(true)
	w.previews.Lux = pools
	before := w.machineOf(runID)

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] != nil {
		t.Errorf("the wake sent %v, want nothing", got)
	}
	if m := w.machineOf(runID); m["name"] != "Big" || m["note"] != nil || len(m) != len(before) {
		t.Errorf("runs.machine = %v, want it as it was: %v", m, before)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 0 {
		t.Errorf("%d run.resized while lux's pools could not be read", n)
	}
	if pools.calls.Load() == 0 {
		t.Fatal("lux's pools were never asked for")
	}

	pools.fail.Store(false)
	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	if m := w.machineOf(runID); m["name"] != "Tiny" || m["poolId"] != defaultPool || m["note"] != nil {
		t.Fatalf("record after pools recovered: %v", m)
	}
	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 3 || got[1] == nil || got[2] != nil {
		t.Errorf("the wakes sent %v, want unchanged, Tiny, unchanged", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 1 {
		t.Errorf("%d run.resized, want 1", n)
	}
}

// diskOnly is a lux that took a resume's disk and nothing else of its
// resources, saying nothing of it in its answer (with NoResize, a lux from
// before lux#51).
type diskOnly struct{ lux.Client }

func (c diskOnly) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	if in.Resources != nil {
		in.Resources = &lux.Resources{Disk: in.Resources.Disk}
	}
	return c.Client.Resume(ctx, id, in)
}

// A lux that applied only part of the size leaves the Run on neither size:
// runs.machine has the numbers lux's spec has, under the size it had, with
// a note naming the size its settings name. The next wake asks again and
// lux leaves those numbers as they are: the note stays, and nothing new is
// recorded.
func TestAPartlyAppliedSizeKeepsItsNameWithLuxsNumbers(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	w.lux.NoResize = true
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	sizeID := w.machineOf(runID)["sizeId"]
	w.previews.Lux = diskOnly{w.previews.Lux}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	partial := "Its settings name Tiny; lux applied only part of it, as shown."
	m := w.machineOf(runID)
	if m["name"] != "Standard" || m["sizeId"] != sizeID || m["from"] != "default" || m["cpus"] != 2.0 || m["memoryMiB"] != 8192.0 ||
		m["diskGiB"] != 5.0 || m["note"] != partial || m["sinceEpoch"] != 2.0 {
		t.Errorf("runs.machine = %v, want Standard's name with lux's 2 CPUs, 8 GiB and 5 GiB disk", m)
	}

	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 2 || got[1] == nil || got[1].Disk != 5*gibB {
		t.Fatalf("the second wake sent %v, want Tiny's disk again", got)
	}
	if m := w.machineOf(runID); m["note"] != partial || m["sinceEpoch"] != 2.0 || m["diskGiB"] != 5.0 {
		t.Errorf("after the second wake runs.machine = %v, want the partial note kept", m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 1 {
		t.Errorf("%d run.resized after the same partial apply twice, want 1", n)
	}
}

// answersNoSpec is a lux whose resume answer says nothing of the Run's
// resources.
type answersNoSpec struct{ lux.Client }

func (c answersNoSpec) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	res, err := c.Client.Resume(ctx, id, in)
	res.Resize, res.Spec.Resources = nil, nil
	return res, err
}

// An answer that says nothing of the Run's resources is not taken as the
// request: lux's Run is read again, and what it has is recorded.
func TestAResumeAnswerWithoutResourcesIsReadAgain(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	w.previews.Lux = answersNoSpec{w.previews.Lux}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)
	if m := w.machineOf(runID); m["name"] != "Tiny" || m["cpus"] != 0.5 || m["diskGiB"] != 5.0 {
		t.Errorf("runs.machine = %v, want Tiny as lux's Run says", m)
	}
}

// An eager (not wakeable) preview parked by the previews loop resumes on
// the size its project names now when a person starts one of its servers.
func TestAParkedEagerPreviewResumesOnTheSizeItsProjectNamesNow(t *testing.T) {
	w := newWorld(t)
	w.tiny()
	runID := w.parkedPreview()
	luxRun := w.previewLux(runID)
	if m := w.machineOf(runID); m["name"] != "Standard" {
		t.Fatalf("the preview started on %v, want Standard", m)
	}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = preview_settings || '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	mustExec(t, w.owner, `UPDATE runs SET pending_starts = '{docs}' WHERE id = $1`, runID)
	w.until("the preview to be resumed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})

	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tiny {
		t.Errorf("the resume sent %v, want Tiny's resources", got)
	}
	if got := w.luxResources(luxRun); got != tiny {
		t.Errorf("lux's spec = %+v, want Tiny's", got)
	}
	if m := w.machineOf(runID); m["name"] != "Tiny" || m["sizeId"] != "msz_tiny" || m["cpus"] != 0.5 || m["diskGiB"] != 5.0 {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}
}

// resumingAlready has the first resume of each Run reach lux twice: once
// without resources, as a resume whose answer was lost, then as asked. lux
// is then resuming the Run with no resources, and refuses others (409).
// asked is what dude sent, in order, and errs what lux answered each.
type resumingAlready struct {
	lux.Client
	mu    sync.Mutex
	seen  map[string]bool
	asked []*lux.Resources
	errs  []error
}

func (c *resumingAlready) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	c.mu.Lock()
	first := !c.seen[id]
	c.seen[id] = true
	c.mu.Unlock()
	if first {
		earlier := in
		earlier.Resources = nil
		if _, err := c.Client.Resume(ctx, id, earlier); err != nil {
			return lux.Run{}, err
		}
	}
	res, err := c.Client.Resume(ctx, id, in)
	c.mu.Lock()
	c.asked, c.errs = append(c.asked, in.Resources), append(c.errs, err)
	c.mu.Unlock()
	return res, err
}

// A wake whose resume finds the Run already resuming with no resources: lux
// refuses the new size (409 not_resumable), and the wake goes on as the
// same resume without them, as lux's docs say a retry is. The wake does not
// fail; runs.machine records the size lux's spec has, and says the new one
// was not applied, with lux's words.
func TestA409WhileResumingDoesNotFailTheWake(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	// The resume lux takes first stays resuming for the whole exchange:
	// its placement waits StartAfter/5 before it is assigned.
	w.lux.StartAfter = 2 * time.Second
	already := &resumingAlready{Client: w.previews.Lux, seen: map[string]bool{}}
	w.previews.Lux = already

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	already.mu.Lock()
	asked, errs := already.asked, already.errs
	already.mu.Unlock()
	if len(asked) != 2 || len(errs) != 2 {
		t.Fatalf("dude asked %v, lux answered %v; want two resumes", asked, errs)
	}
	if le, ok := lux.AsError(errs[0]); asked[0] == nil || *asked[0] != tiny || !ok || le.Status != 409 || asked[1] != nil || errs[1] != nil {
		t.Errorf("dude asked %v, lux answered %v; want Tiny refused 409, then the same resume without resources taken", asked, errs)
	}
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] != nil {
		t.Errorf("lux started resumes with %v, want one, without resources", got)
	}
	if got := w.luxResources(luxRun); got.CPUs != 2 {
		t.Errorf("lux's spec = %+v, want Standard's", got)
	}
	m := w.machineOf(runID)
	want := "Its settings now name Tiny, but this resume kept Standard (lux said: run is resuming already with other resources: they can only change before it is resumed). The next resume tries again."
	if m["name"] != "Standard" || m["cpus"] != 2.0 || m["note"] != want {
		t.Errorf("runs.machine = %v\nwant Standard with note %q", m, want)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND error IS NULL AND status = 'running'`, runID); n != 1 {
		t.Errorf("the wake failed:\n%s", w.describeRuns())
	}
}

// A lux from before lux#51 refuses cpus and memory on a resume (an unknown
// field, 400): the Run resumes without them, on the size lux's spec still
// has, and that is what is recorded, with the reason.
func TestALuxThatCannotResizeResumesTheRunAsItIs(t *testing.T) {
	w := newWorld(t)
	w.tiny()
	w.lux.NoResize = true
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)
	r := w.lux.Runs()[0]

	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,machineSize}', '"msz_tiny"') WHERE id = $1`, w.project)
	w.resumeAgent(runID, r)

	if got := w.luxResources(r.ID); got.CPUs != 2 || got.Disk != 20*gibB {
		t.Errorf("lux's spec = %+v, want Standard's", got)
	}
	m := w.machineOf(runID)
	note, _ := m["note"].(string)
	if m["name"] != "Standard" || m["cpus"] != 2.0 || note != "Its settings now name Tiny, but this resume kept Standard: this lux cannot change a stopped Run's CPUs or memory." {
		t.Errorf("runs.machine = %v", m)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID); n != 1 {
		t.Errorf("the Run did not resume:\n%s", w.describeRuns())
	}
	// No move was asked for: the agent gets its usual nudge, not lux's error.
	nudge := "You were paused and have been resumed. Continue the task where you left off."
	if in := w.lux.ResumeInputs(r.ID); len(in) != 1 || in[0] != nudge {
		t.Errorf("resume inputs %q, want the nudge", in)
	}
}

// ignoresResources is a lux that takes a resume and drops its resources
// unread: its answer carries no resize, and its spec is as it was.
type ignoresResources struct{ lux.Client }

func (c ignoresResources) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	in.Resources = nil
	return c.Client.Resume(ctx, id, in)
}

// What is recorded is what lux's Run says after the resume, never what was
// asked: a lux that ignored the resources leaves the Run on Standard, and
// runs.machine says Standard.
func TestALuxThatIgnoresResourcesIsRecordedAsItsSpecSays(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	w.previews.Lux = ignoresResources{w.previews.Lux}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	m := w.machineOf(runID)
	want := "Its settings now name Tiny, but this resume kept Standard (lux did not apply it)."
	if m["name"] != "Standard" || m["cpus"] != 2.0 || m["diskGiB"] != 20.0 || m["note"] != want {
		t.Errorf("runs.machine = %v, want Standard as lux's spec says", m)
	}
}

// A session's agent parked by the sweep and resumed by a chat message
// resumes on the size the organisation's brainstorm role names now. On a
// lux without sync modes, the session resume's retry without its sync
// carries that size too.
func TestAParkedSessionResumesOnTheSizeItsRoleNamesNow(t *testing.T) {
	for name, noModes := range map[string]bool{"with sync modes": false, "without sync modes": true} {
		t.Run(name, func(t *testing.T) {
			s := newSessionWorld(t)
			s.tiny()
			s.syncer.ConductorWarm = time.Hour
			brainstormOn := func(size string) {
				mustExec(t, s.owner, `UPDATE organizations SET default_agent_models = jsonb_set(default_agent_models, '{brainstorm}',
					COALESCE(default_agent_models->'brainstorm', '{}'::jsonb) || jsonb_build_object('machineSize', $2::text)) WHERE id = $1`, s.org, size)
			}
			// Migration 086 seeds brainstorm on Small; it starts on Standard here.
			brainstormOn(s.str(`SELECT id FROM machine_sizes WHERE organization_id = $1 AND name = 'Standard'`, s.org))
			id := s.session()
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
			run := s.started(id)
			s.until("the brainstorm running", func() bool {
				return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, run) == 1
			})
			if m := s.machineOf(run); m["name"] != "Standard" {
				t.Fatalf("the brainstorm started on %v, want Standard", m)
			}
			mustExec(t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours',
				agent_active_at = now() - interval '3 hours', files_changed_at = now() - interval '3 hours',
				stall_reported_at = now() - interval '3 hours' WHERE id = $1`, run)
			s.until("the session parked", func() bool {
				return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'session'
					AND lux_state = 'stopped'`, run) == 1
			})

			brainstormOn("msz_tiny")
			s.lux.NoSyncModes = noModes
			luxRun := s.previewLux(run)
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
			s.until("lux to have the resume", func() bool { return s.luxCalls(luxRun, "resume") > 0 })
			s.until("the resize recorded", func() bool {
				return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, run) > 0
			})

			tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
			if got := s.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tiny {
				t.Errorf("the resumes lux took carried %v, want one with Tiny's", got)
			}
			if m := s.machineOf(run); m["name"] != "Tiny" || m["sizeId"] != "msz_tiny" || m["cpus"] != 0.5 || m["sinceEpoch"] != 2.0 {
				t.Errorf("runs.machine = %v, want Tiny since epoch 2", m)
			}
			if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, run); n != 1 {
				t.Errorf("%d run.resized, want 1", n)
			}
		})
	}
}

// A conductor resumed on a lux without sync modes is resumed again without
// its sync (Syncer.resume's fallback): that retry still carries the size
// its role names now.
func TestAResumeRetriedWithoutSyncModesStillResizes(t *testing.T) {
	e := newEditing(t)
	e.tiny()
	e.wokenWith(e.task, "after implement")
	e.syncer.ConductorWarm = 1
	e.until("the conductor parked", func() bool { _, status, _ := e.conductor(e.task); return status == "paused" })
	e.syncer.ConductorWarm = 1 << 40
	mustExec(t, e.owner, `UPDATE projects SET agent_models = agent_models ||
		jsonb_build_object('conductor', COALESCE(agent_models->'conductor', '{}') || '{"machineSize":"msz_tiny"}') WHERE id = $1`, e.project)
	e.lux.NoSyncModes = true
	if status, _ := e.chat(e.task, "resume without modes"); status != 200 {
		t.Fatalf("chat %d", status)
	}
	e.heard("resume without modes")

	luxRun := e.luxRunOf(e.cond)
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	got := e.resumeResources(luxRun)
	if len(got) == 0 || got[len(got)-1] == nil || *got[len(got)-1] != tiny {
		t.Errorf("the resumes lux took carried %v, want the last Tiny's", got)
	}
	if m := e.machineOf(e.cond); m["name"] != "Tiny" || m["cpus"] != 0.5 {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}
}
