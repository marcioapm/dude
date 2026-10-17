package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A conductor whose turn stays busy with nothing said and no tool open —
// OpenCode retrying a rate-limited model request for days — is interrupted
// through lux once its silence passes 10 minutes, and the message a person
// wrote meanwhile, which lux took but the agent never read, is read in the
// turn that follows.
func TestASilentConductorsTurnIsInterruptedAndItReadsWhatWaited(t *testing.T) {
	w := conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	task, _ := w.delivered()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	_, out := w.chat(task, "what changed?")
	run, _ := out["runId"].(string)
	w.until("the conductor busy", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'
			AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL`, run) == 1
	})
	_, out = w.chat(task, "So?")
	steer, _ := out["directiveId"].(string)
	w.until("lux to take the steer", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL`, steer) == 1
	})
	if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, steer); n != 0 {
		t.Fatal("the hung conductor read the steer: the case is not the one under test")
	}
	w.sweep()
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt`, run); n != 0 {
		t.Fatalf("%d interrupts before the silence reached its limit", n)
	}

	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = now() - interval '11 minutes',
		agent_busy_at = now() - interval '11 minutes' WHERE id = $1`, run)
	w.sweep()
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND task_id = $2
		AND event_type = 'run.turn_stopped' AND actor_type = 'system'`, run, task); n != 1 {
		t.Fatalf("run.turn_stopped events = %d, want 1", n)
	}
	luxRun := w.luxRunOf(run)
	w.until("lux to interrupt the turn", func() bool {
		for _, r := range w.lux.Runs() {
			if r.ID == luxRun {
				return r.Interrupted == 1
			}
		}
		return false
	})
	w.until("the steer read", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, steer) == 1
	})
	w.sweep()
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt`, run); n != 1 {
		t.Fatalf("interrupts after another sweep = %d, want 1", n)
	}
}

// A session's agent silent the same way is stopped the same way, and the
// session says so.
func TestASilentBrainstormsTurnIsInterrupted(t *testing.T) {
	s := newSessionWorld(t)
	s.syncer.ConductorWarm = time.Hour
	s.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	var run string
	s.until("the session's agent busy", func() bool {
		run, _ = s.brainstorm(id)
		return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'
			AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL`, run) == 1
	})
	mustExec(t, s.owner, `UPDATE runs SET agent_active_at = now() - interval '11 minutes' WHERE id = $1`, run)
	s.sweep()
	s.sweep()
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt AND scope = 'turn'`, run); n != 1 {
		t.Fatalf("interrupt directives = %d, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND session_id = $2
		AND event_type = 'session.turn_stopped' AND actor_id = 'dude' AND payload ? 'silentSecs'`, run, id); n != 1 {
		t.Fatalf("session notices = %d, want 1", n)
	}
	var luxRun string
	if err := s.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE id = $1`, run).Scan(&luxRun); err != nil {
		t.Fatal(err)
	}
	s.until("lux to interrupt the turn", func() bool {
		for _, r := range s.lux.Runs() {
			if r.ID == luxRun {
				return r.Interrupted == 1
			}
		}
		return false
	})
}
