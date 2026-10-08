package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

func TestBrainstormStuckCallsInterruptOnce(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		age          time.Duration
		want         int
	}{
		{"overdue", "", 11 * time.Minute, 1},
		{"young", "", 9 * time.Minute, 0},
		{"parked", "status = 'paused', dude_pause = 'session',", 11 * time.Minute, 0},
		{"done", "turn_done_at = now(),", 11 * time.Minute, 0},
		{"phase", "phase = 'review',", 11 * time.Minute, 0},
		{"conductor", "role = 'conductor',", 11 * time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSessionWorld(t)
			s.syncer.ConductorWarm = time.Hour
			s.lux.Decide = func(map[string]any) fakelux.Behaviour {
				return fakelux.Behaviour{Hang: true, Conductor: true, OpenCalls: [][3]string{taskCall}}
			}
			id := s.session()
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
			var run string
			s.until("the open brainstorm call", func() bool {
				run, _ = s.brainstorm(id)
				return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'
					AND open_tool_calls_at <> '{}'`, run) == 1
			})
			mustExec(t, s.owner, `UPDATE runs SET `+tc.change+` open_tool_calls_at = jsonb_build_object('open_0',
				now() - make_interval(secs => $2)) WHERE id = $1`, run, tc.age.Seconds())
			s.sweep()
			if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt AND scope = 'turn'`, run); n != tc.want {
				t.Fatalf("interrupt directives = %d, want %d", n, tc.want)
			}
			if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND session_id = $2
				AND event_type = 'session.turn_stopped' AND actor_id = 'dude' AND payload->>'tool' = 'task'`, run, id); n != tc.want {
				t.Fatalf("session notices = %d, want %d", n, tc.want)
			}
			if tc.want == 1 {
				s.until("the interrupt processed", func() bool {
					return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NOT NULL`, run) == 1
				})
				// Keep the same stuck facts after lux processes the first interruption.
				mustExec(t, s.owner, `UPDATE runs SET turn_done_at = NULL, open_tool_calls_at = jsonb_build_object('open_0',
					now() - interval '11 minutes') WHERE id = $1`, run)
			}
			s.sweep()
			if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt AND scope = 'turn'`, run); n != tc.want {
				t.Fatalf("second sweep interrupts = %d, want %d", n, tc.want)
			}
			if tc.want == 1 {
				mustExec(t, s.owner, `UPDATE runs SET turn_done_at = NULL, open_tool_calls_at = jsonb_build_object('different',
					now() - interval '11 minutes') WHERE id = $1`, run)
				s.sweep()
				if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt AND scope = 'turn'`, run); n != 2 {
					t.Fatalf("different call interrupts = %d, want 2", n)
				}
			}
		})
	}
}

func TestConcurrentSweepsInterruptAStuckBrainstormOnce(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, OpenCalls: [][3]string{taskCall}}
	}
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	var run string
	s.until("the open brainstorm call", func() bool {
		run, _ = s.brainstorm(id)
		return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'
			AND open_tool_calls_at <> '{}'`, run) == 1
	})
	mustExec(t, s.owner, `UPDATE runs SET open_tool_calls_at = jsonb_build_object('open_0', now() - interval '11 minutes') WHERE id = $1`, run)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.syncer.Sweep(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt AND scope = 'turn'`, run); n != 1 {
		t.Fatalf("concurrent sweeps queued %d interrupts, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'session.turn_stopped'`, run); n != 1 {
		t.Fatalf("concurrent sweeps wrote %d notices, want 1", n)
	}
}
