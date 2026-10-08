package orchestrator_test

import (
	"testing"

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
			s.lux.Decide = func(map[string]any) fakelux.Behaviour {
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
			mustExec(t, s.owner, `UPDATE runs SET started_at = now() - interval '5 hours',
				agent_active_at = now() - interval '5 hours', files_changed_at = now() - interval '5 hours',
				open_tool_calls_at = CASE WHEN $2 THEN jsonb_build_object('open_0', now() - interval '5 hours') ELSE '{}'::jsonb END
				WHERE id = $1`, run, open)
			s.sweep()
			if n := s.stalls(run); n != 0 {
				t.Fatalf("brainstorm got %d stall reports", n)
			}
			if n := s.count(`SELECT count(*) FROM conductor_wakes WHERE organization_id = $1 AND kind = 'stalled'`, s.org); n != 0 {
				t.Fatalf("brainstorm produced %d stall wakes", n)
			}
		})
	}
}
