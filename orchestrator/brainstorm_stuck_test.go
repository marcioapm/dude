package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

func TestBrainstormUpgradeDatesExistingOpenCalls(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "090")
	org := dbtest.Org(t, owner)
	mustExec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_upgrade', $1, 'Owner')`, org)
	mustExec(t, owner, `INSERT INTO sessions (id, organization_id, title, created_by)
		VALUES ('ssn_upgrade', $1, 'Existing brainstorm', 'per_upgrade')`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, session_id, attempt, role, kind, status, lux_state,
		started_at, open_tool_calls) VALUES ('run_upgrade', $1, 'ssn_upgrade', 1, 'brainstorm', 'agent', 'running', 'running',
		now() - interval '30 minutes', ARRAY['call_upgrade', 'call_missing'])`, org)
	mustExec(t, owner, `INSERT INTO events (id, organization_id, run_id, session_id, event_type, actor_type, actor_id, source, payload, occurred_at)
		VALUES ('evt_upgrade', $1, 'run_upgrade', 'ssn_upgrade', 'agent.tool.called', 'agent', 'run_upgrade',
		'orchestrator', '{"callId":"call_upgrade","tool":"bash"}', now() - interval '20 minutes')`, org)
	apply()
	var dated, fallback bool
	if err := owner.QueryRow(context.Background(), `SELECT
		COALESCE((open_tool_calls_at->>'call_upgrade')::timestamptz = (SELECT occurred_at FROM events WHERE id = 'evt_upgrade'), false),
		COALESCE((open_tool_calls_at->>'call_missing')::timestamptz = started_at, false) FROM runs WHERE id = 'run_upgrade'`).Scan(&dated, &fallback); err != nil {
		t.Fatal(err)
	}
	if !dated || !fallback {
		t.Fatalf("existing calls dated from ledger = %v, missing-event fallback = %v", dated, fallback)
	}
	app, err := db.Open(context.Background(), owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fake := fakelux.New("", "lux-key", nil)
	defer fake.Close()
	luxServer := httptest.NewServer(fake.Handler())
	defer luxServer.Close()
	syncer := &phases.Syncer{DB: app, Lux: lux.New(luxServer.URL, "lux-key"), Log: quiet}
	if _, err := syncer.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	var interrupts int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM directives WHERE run_id = 'run_upgrade'
		AND interrupt AND scope = 'turn'`).Scan(&interrupts); err != nil {
		t.Fatal(err)
	}
	if interrupts != 1 {
		t.Fatalf("upgrade sweep interrupts = %d, want 1", interrupts)
	}
}

func openBrainstormCall(s *sessionWorld, conductor bool) (id, run string) {
	s.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Conductor: conductor, OpenCalls: [][3]string{taskCall}}
	}
	id = s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	s.until("the open brainstorm call", func() bool {
		run, _ = s.brainstorm(id)
		return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'
			AND open_tool_calls_at <> '{}'`, run) == 1
	})
	return id, run
}

func TestBrainstormStuckCallsInterruptOnce(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		age          time.Duration
		want         int
	}{
		{"overdue", "", 11 * time.Minute, 1},
		{"young", "", 9 * time.Minute, 0},
		{"parked", "status = 'paused', dude_pause = 'session',", 11 * time.Minute, 0},
		{"pause_requested", "control = 'pause_graceful',", 11 * time.Minute, 0},
		{"done", "turn_done_at = now(),", 11 * time.Minute, 0},
		{"phase", "phase = 'review',", 11 * time.Minute, 0},
		{"conductor", "role = 'conductor',", 11 * time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSessionWorld(t)
			s.syncer.ConductorWarm = time.Hour
			id, run := openBrainstormCall(s, true)
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

func TestBrainstormOldestOpenCallDeterminesTheInterrupt(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, OpenCalls: [][3]string{taskCall, {"bash", "echo young", `{}`}}}
	}
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	var run string
	s.until("both open brainstorm calls", func() bool {
		run, _ = s.brainstorm(id)
		return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'
			AND (SELECT count(*) FROM jsonb_object_keys(open_tool_calls_at)) = 2`, run) == 1
	})
	mustExec(t, s.owner, `UPDATE runs SET open_tool_calls_at = jsonb_build_object(
		'open_0', now() - interval '11 minutes', 'open_1', now() - interval '1 minute') WHERE id = $1`, run)
	s.sweep()
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt AND scope = 'turn'`, run); n != 1 {
		t.Fatalf("mixed-age interrupts = %d, want 1", n)
	}
	var tool string
	var openSecs float64
	if err := s.owner.QueryRow(context.Background(), `SELECT payload->>'tool', (payload->>'openSecs')::float8
		FROM events WHERE run_id = $1 AND event_type = 'session.turn_stopped'`, run).Scan(&tool, &openSecs); err != nil {
		t.Fatal(err)
	}
	if tool != "task" || openSecs < 660 || openSecs > 665 {
		t.Fatalf("oldest call notice: tool = %q, openSecs = %v, want task and about 660", tool, openSecs)
	}
}

func TestConcurrentSweepsInterruptAStuckBrainstormOnce(t *testing.T) {
	s := newSessionWorld(t)
	_, run := openBrainstormCall(s, false)
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
