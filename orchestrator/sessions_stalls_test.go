package orchestrator_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
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

type sessionClockLux struct {
	lux.Client
	frames  chan int
	next    chan struct{}
	resumed atomic.Bool
}

func (l *sessionClockLux) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	l.resumed.Store(true)
	return l.Client.Resume(ctx, id, in)
}

func (l *sessionClockLux) Output(ctx context.Context, id, cursor string, after int64, fn func(lux.Frame) error) error {
	return l.Client.Output(ctx, id, cursor, after, func(f lux.Frame) error {
		var state struct{ State string }
		_ = json.Unmarshal(f.EventData, &state)
		if f.Kind != "lux" || f.EventType != "state" || !l.resumed.Load() || state.State != "running" {
			return fn(f)
		}
		for step := 0; step < 3; step++ {
			select {
			case l.frames <- step:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-l.next:
			case <-ctx.Done():
				return ctx.Err()
			}
			if step < 2 {
				if step == 1 {
					f.EventID++
				}
				if err := fn(f); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func TestASessionParkAndMessageResumeResetItsRunningClocksOnce(t *testing.T) {
	s := newSessionWorld(t)
	s.syncer.ConductorWarm = time.Hour
	l := &sessionClockLux{Client: s.syncer.Lux, frames: make(chan int), next: make(chan struct{})}
	s.syncer.Lux = l
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	s.until("the brainstorm running", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, run) == 1
	})
	mustExec(t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours',
		agent_active_at = now() - interval '3 hours', files_changed_at = now() - interval '3 hours',
		stall_reported_at = now() - interval '3 hours' WHERE id = $1`, run)
	s.until("the session parked", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'session'
			AND lux_state = 'stopped'`, run) == 1
	})
	clock := func(col string) *time.Time {
		t.Helper()
		var at *time.Time
		if err := s.owner.QueryRow(context.Background(), `SELECT `+col+` FROM runs WHERE id = $1`, run).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if away := clock("left_running_at"); away == nil {
		t.Fatal("a parked brainstorm has no departure stamp")
	}
	before := time.Now().UTC()
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	s.until("the session resume accepted", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'resuming'
			AND dude_pause IS NULL`, run) == 1
	})
	s.sweep()
	wait := func(want int) {
		t.Helper()
		select {
		case got := <-l.frames:
			if got != want {
				t.Fatalf("frame step %d, want %d", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no running frame at step %d", want)
		}
	}
	wait(0)
	files, away := clock("files_changed_at"), clock("left_running_at")
	if files == nil || away == nil || files.Before(before) || !files.Equal(*away) || clock("agent_active_at") != nil {
		t.Fatalf("resume acceptance: files %v, departure %v, activity %v", files, away, clock("agent_active_at"))
	}
	report := clock("stall_reported_at")
	// Simulate placement wait without sleeping through a stall window.
	mustExec(t, s.owner, `UPDATE runs SET left_running_at = now() - interval '10 minutes' WHERE id = $1`, run)
	time.Sleep(50 * time.Millisecond)
	l.next <- struct{}{}
	wait(1)
	deadline := time.Now().Add(10 * time.Second)
	for clock("left_running_at") != nil {
		if time.Now().After(deadline) {
			t.Fatal("running frame was not committed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	movedFiles, movedReport := clock("files_changed_at"), clock("stall_reported_at")
	if clock("left_running_at") != nil || clock("agent_active_at") != nil || movedFiles == nil || !movedFiles.After(*files) {
		t.Fatalf("running entry: files %v (was %v), departure %v, activity %v", movedFiles, files, clock("left_running_at"), clock("agent_active_at"))
	}
	if movedReport == nil || report == nil || movedReport.Sub(*report) < 10*time.Minute || movedReport.Sub(*report) > 11*time.Minute {
		t.Fatalf("report clock moved from %v to %v, want placement wait only", report, movedReport)
	}
	var firstEvent int64
	if err := s.owner.QueryRow(context.Background(), `SELECT lux_after_event FROM runs WHERE id = $1`, run).Scan(&firstEvent); err != nil {
		t.Fatal(err)
	}
	l.next <- struct{}{}
	wait(2)
	for s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_after_event > $2`, run, firstEvent) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("repeated running frame was not committed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if again := clock("files_changed_at"); again == nil || !again.Equal(*movedFiles) {
		t.Fatalf("repeated running shifted files from %v to %v", movedFiles, again)
	}
	if again := clock("stall_reported_at"); again == nil || !again.Equal(*movedReport) {
		t.Fatalf("repeated running shifted report from %v to %v", movedReport, again)
	}
	if clock("agent_active_at") != nil || clock("left_running_at") != nil {
		t.Fatal("repeated running changed cleared activity or departure")
	}
	l.next <- struct{}{}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.unparked' AND payload->>'reason' = 'session'`, run); n != 1 {
		t.Fatalf("%d session resumes, want 1", n)
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
