package orchestrator_test

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

func TestABrainstormSpecHasNoRunningTimeLimit(t *testing.T) {
	s := newSessionWorld(t)
	s.syncer.Agent.Timeout = "4h"
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	v := s.luxRun(run)
	if v == nil {
		t.Fatal("brainstorm was not submitted")
	}
	if v.spec.Timeout != "" {
		t.Fatalf("brainstorm timeout = %q, want none", v.spec.Timeout)
	}
}

func TestBrainstormsAreNotReportedStalled(t *testing.T) {
	for _, open := range []bool{true, false} {
		name := "silent"
		if open {
			name = "open call"
		}
		t.Run(name, func(t *testing.T) {
			s := newSessionWorld(t)
			s.withTools()
			scripted := s.lux.Decide
			s.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				labels, _ := spec["labels"].(map[string]any)
				if labels["dude.role"] != "brainstorm" {
					return scripted(spec)
				}
				b := fakelux.Behaviour{Hang: true}
				if open {
					b.OpenCalls = [][3]string{taskCall}
				}
				return b
			}
			id := s.session()
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
			var run string
			s.until("a running brainstorm", func() bool {
				run, _ = s.brainstorm(id)
				return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'
					AND lux_state = 'running' AND agent_busy_at IS NOT NULL AND (open_tool_calls_at <> '{}') = $2`, run, open) == 1
			})
			// A project-bearing brainstorm exercises the phase exclusion independently
			// of the task-less brainstorm's exclusion by the project join.
			linked := "run_linked_" + s.org
			linkedSession := s.session()
			mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, session_id, project_id, attempt, status, kind, role,
				started_at, agent_active_at, files_changed_at, open_tool_calls_at)
				SELECT $2, organization_id, $5, $3, attempt, status, kind, role,
					now() - interval '5 hours', now() - interval '5 hours', now() - interval '5 hours',
					CASE WHEN $4 THEN jsonb_build_object('open_0', now() - interval '5 hours') ELSE '{}'::jsonb END
				FROM runs WHERE id = $1`, run, linked, s.project, open, linkedSession)
			mustExec(t, s.owner, `UPDATE runs SET lux_state = 'running' WHERE id = $1`, linked)
			// The join must also exclude a task-less Run even if it carries a phase.
			phaseSession := s.session()
			phaseRun := "run_phase_" + s.org
			mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, phase,
				lux_state, started_at, agent_active_at, files_changed_at, open_tool_calls_at)
				SELECT $2, organization_id, $3, attempt, status, kind, role, 'review', 'running',
					started_at, now() - interval '5 hours', now() - interval '5 hours', open_tool_calls_at
				FROM runs WHERE id = $1`, linked, phaseRun, phaseSession)
			s.quickDiffs()
			s.hangingReviews(taskCall)
			task := s.task()
			s.conductedReview(task)
			controls := s.reviewersOpen(task, 2)
			s.diffsSettled(controls)
			s.openSince(controls[0], 5*time.Hour)
			mustExec(t, s.owner, `UPDATE runs SET started_at = now() - interval '5 hours',
				agent_active_at = now() - interval '5 hours', files_changed_at = now() - interval '5 hours',
				open_tool_calls_at = CASE WHEN $2 THEN jsonb_build_object('open_0', now() - interval '5 hours') ELSE '{}'::jsonb END
				WHERE id = $1`, run, open)
			s.sweep()
			if n := s.stalls(controls[0]); n != 1 {
				t.Fatalf("stalled task beside brainstorms got %d reports, want 1", n)
			}
			if n := s.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'stalled'`, task); n != 1 {
				t.Fatalf("stalled task got %d wakes, want 1", n)
			}
			if n := s.stalls(linked); n != 0 {
				t.Fatalf("project-bearing brainstorm got %d stall reports", n)
			}
			if n := s.stalls(phaseRun); n != 0 {
				t.Fatalf("task-less phase-bearing brainstorm got %d stall reports", n)
			}
			if n := s.stalls(run); n != 0 {
				t.Fatalf("brainstorm got %d stall reports", n)
			}
			if n := s.count(`SELECT count(*) FROM conductor_wakes WHERE organization_id = $1 AND kind = 'stalled' AND task_id <> $2`, s.org, task); n != 0 {
				t.Fatalf("brainstorm produced %d stall wakes", n)
			}
		})
	}
}
