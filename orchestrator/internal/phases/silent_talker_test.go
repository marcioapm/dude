package phases

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func TestASilentTalkersBusyRetriesBackOff(t *testing.T) {
	w := newResumeWorld(t)
	w.silentConductor("11 minutes", "stuck_fingerprint = 'tool-call'")
	w.follow(running(1), busy(1))
	w.ageSilent("11 minutes")
	w.interruptSilent()
	w.follow(busy(1))
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND stuck_fingerprint = 'silent:1'`); n != 1 {
		t.Fatal("busy-only batch cleared the silent streak")
	}
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 1 {
		t.Fatalf("busy-only batch: interrupts = %d, want 1", n)
	}
	for i, step := range []struct {
		elapsed string
		want    int
		streak  string
	}{
		{"11 minutes", 1, "silent:1"},
		{"19 minutes", 1, "silent:1"},
		{"21 minutes", 2, "silent:2"},
		{"39 minutes", 2, "silent:2"},
		{"41 minutes", 3, "silent:3"},
		{"79 minutes", 3, "silent:3"},
		{"81 minutes", 4, "silent:4"},
	} {
		w.follow(record(1, "lux.activity", map[string]any{"activity": "idle"}), busy(1))
		w.exec(`UPDATE runs SET agent_active_at = now() - interval '11 minutes',
			stuck_interrupted_at = now() - $2::interval WHERE id = $1`, w.run.ID, step.elapsed)
		w.interruptSilent()
		if n := w.count(silentInterrupts); n != step.want {
			t.Fatalf("step %d (%s): interrupts = %d, want %d", i, step.elapsed, n, step.want)
		}
		var fingerprint string
		if err := w.owner.QueryRow(w.ctx, `SELECT stuck_fingerprint FROM runs WHERE id = $1`, w.run.ID).Scan(&fingerprint); err != nil || fingerprint != step.streak {
			t.Fatalf("step %d: fingerprint = %q, want %q: %v", i, fingerprint, step.streak, err)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.turn_stopped'
		AND (payload->>'streak')::int BETWEEN 1 AND 4`); n != 4 {
		t.Fatalf("streak notices = %d, want 4", n)
	}
}

func TestASilentTalkersBackoffIsCappedAtTwoHours(t *testing.T) {
	w := newResumeWorld(t)
	w.silentConductor("11 minutes", "")
	w.follow(running(1), busy(1))
	w.ageSilent("11 minutes")
	w.interruptSilent()
	for _, since := range []string{"21 minutes", "41 minutes", "81 minutes"} {
		w.ageSilent(since)
		w.interruptSilent()
	}
	w.ageSilent("119 minutes")
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 4 {
		t.Fatalf("before cap: interrupts = %d, want 4", n)
	}
	w.ageSilent("121 minutes")
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 5 {
		t.Fatalf("past cap: interrupts = %d, want 5", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND stuck_fingerprint = 'silent:5'`); n != 1 {
		t.Fatal("capped backoff did not advance the streak")
	}
}

func TestAMalformedSilentStreaksDoNotAbortTheSweep(t *testing.T) {
	w := newResumeWorld(t)
	for _, fingerprint := range []string{"silent:", "silent:abc", "silent:99999999999999999999", "silent:3"} {
		w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ($2, $1, 'Silent')`, w.run.Org, "session_"+fingerprint)
		w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status,
			lux_run_id, lux_state, control, session_id, agent_busy_at, agent_active_at, stuck_fingerprint)
			SELECT $2, organization_id, project_id, NULL, attempt, NULL, 'brainstorm', 'agent', 'running',
				'lux_' || $2, 'running', 'none', $3, now() - interval '1 hour', now() - interval '11 minutes', $4
			FROM runs WHERE id = $1`, w.run.ID, "run_"+fingerprint, "session_"+fingerprint, fingerprint)
		if fingerprint != "silent:3" {
			w.exec(`UPDATE runs SET stuck_interrupted_at = now() WHERE id = $2 AND organization_id = $1`, w.run.Org, "run_"+fingerprint)
		}
	}
	w.silentConductor("11 minutes", "stuck_fingerprint = 'tool-call'")
	w.interruptSilent()
	var interrupts int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE r.organization_id = $1 AND d.interrupt AND d.scope = 'turn'`, w.run.Org).Scan(&interrupts); err != nil {
		t.Fatal(err)
	}
	if interrupts != 5 {
		t.Fatalf("interrupts = %d, want 5", interrupts)
	}
	var streaks int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM runs WHERE organization_id = $1 AND stuck_fingerprint = 'silent:1'`, w.run.Org).Scan(&streaks); err != nil {
		t.Fatal(err)
	}
	if streaks != 5 {
		t.Fatalf("first-interrupt streaks = %d, want 5", streaks)
	}
}

func TestAPersonsSteerBypassesSilentBackoff(t *testing.T) {
	w := newResumeWorld(t)
	w.silentConductor("11 minutes", "")
	w.follow(running(1), busy(1))
	w.ageSilent("11 minutes")
	w.interruptSilent()
	for _, actor := range []string{ledger.ActorSystem, ledger.ActorAgent, ledger.ActorHuman} {
		if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
			_, err := delivery.Steer(w.ctx, tx, w.run.Org, delivery.SteerInput{
				RunID: w.run.ID, Text: "Try another approach.", Actor: delivery.Writer{ActorType: actor, ActorID: "per_test"},
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		w.follow(record(1, "lux.activity", map[string]any{"activity": "idle"}), busy(1))
		w.exec(`UPDATE runs SET agent_active_at = now() - interval '9 minutes' WHERE id = $1`, w.run.ID)
		w.interruptSilent()
		if n := w.count(silentInterrupts); n != 1 {
			t.Fatalf("recent activity: interrupts = %d, want 1", n)
		}
		w.exec(`UPDATE runs SET agent_active_at = now() - interval '11 minutes' WHERE id = $1`, w.run.ID)
		w.interruptSilent()
		want := 1
		if actor == ledger.ActorHuman {
			want = 2
		}
		if n := w.count(silentInterrupts); n != want {
			t.Fatalf("%s directive: interrupts = %d, want %d", actor, n, want)
		}
	}
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 2 {
		t.Fatalf("one human steer bypasses once: interrupts = %d, want 2", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND stuck_fingerprint = 'silent:2'`); n != 1 {
		t.Fatal("human steer reset rather than advanced the streak")
	}
}

func TestAPersonsOldOrSimultaneousSteerDoesNotBypassSilentBackoff(t *testing.T) {
	for _, boundary := range []string{"before", "equal"} {
		t.Run(boundary, func(t *testing.T) {
			w := newResumeWorld(t)
			w.silentConductor("11 minutes", "")
			if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
				_, err := delivery.Steer(w.ctx, tx, w.run.Org, delivery.SteerInput{
					RunID: w.run.ID, Text: "Try another approach.", Actor: delivery.Writer{ActorType: ledger.ActorHuman, ActorID: "per_test"},
				})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			w.interruptSilent()
			if boundary == "equal" {
				w.exec(`UPDATE runs SET stuck_interrupted_at = (SELECT min(created_at) FROM directives WHERE run_id = $1) WHERE id = $1`, w.run.ID)
			}
			w.interruptSilent()
			if n := w.count(silentInterrupts); n != 1 {
				t.Fatalf("%s directive: interrupts = %d, want 1", boundary, n)
			}
		})
	}
}

// silentConductor makes w's Run a conductor mid-turn, its last activity
// silentFor ago, with change applied after.
func (w *resumeWorld) silentConductor(silentFor string, change string) {
	w.t.Helper()
	w.exec(`UPDATE runs SET phase = NULL, role = 'conductor', kind = 'agent' WHERE id = $1`, w.run.ID)
	w.run.Phase, w.run.Role = "", "conductor"
	w.accepted(1, lux.Run{})
	w.follow(running(1), busy(1), spoke(1))
	w.exec(`UPDATE runs SET agent_active_at = now() - $2::interval WHERE id = $1`, w.run.ID, silentFor)
	if change != "" {
		w.exec(`UPDATE runs SET `+change+` WHERE id = $1`, w.run.ID)
	}
	w.run.Phase, w.run.Role, w.run.Status, w.run.LuxState, w.run.DudePause = "", "conductor", statusRunning, "running", ""
}

func (w *resumeWorld) interruptSilent() {
	w.t.Helper()
	if err := w.s.beforeDue(w.ctx); err != nil {
		w.t.Fatal(err)
	}
}

func (w *resumeWorld) ageSilent(since string) {
	w.t.Helper()
	w.exec(`UPDATE runs SET agent_active_at = now() - $2::interval,
		stuck_interrupted_at = CASE WHEN stuck_interrupted_at IS NOT NULL THEN now() - $2::interval END WHERE id = $1`, w.run.ID, since)
}

const (
	silentInterrupts = `SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt AND scope = 'turn'`
	silentNotices    = `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.turn_stopped'
		AND actor_type = 'system' AND task_id IS NOT NULL AND (payload->>'silentSecs')::float8 >= 600`
)

// A conductor busy with no tool open and no activity for the limit —
// OpenCode retrying a rate-limited model request — has its turn stopped,
// with backoff; the agent's next activity ends the streak.
func TestASilentConductorIsInterruptedOncePerSilence(t *testing.T) {
	w := newResumeWorld(t)
	w.silentConductor("11 minutes", "")
	w.follow(running(1), busy(1))
	w.ageSilent("11 minutes")
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 1 {
		t.Fatalf("interrupts = %d, want 1", n)
	}
	var directive string
	if err := w.owner.QueryRow(w.ctx, `SELECT text FROM directives WHERE run_id = $1 AND interrupt`, w.run.ID).Scan(&directive); err != nil || directive != silentTurnNudge {
		t.Fatalf("silent directive %q: %v", directive, err)
	}
	if n := w.count(silentNotices); n != 1 {
		t.Fatalf("run.turn_stopped events = %d, want 1", n)
	}
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 1 {
		t.Fatalf("second sweep: interrupts = %d, want 1", n)
	}
	// The agent speaks (its activity, as the stream records it), then goes
	// silent again for the limit.
	w.follow(spoke(1))
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND stuck_fingerprint IS NULL`); n != 1 {
		t.Fatal("model output did not end the silent streak")
	}
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 1 {
		t.Fatalf("right after new activity: interrupts = %d, want 1", n)
	}
	w.exec(`UPDATE runs SET agent_active_at = agent_active_at - interval '11 minutes' WHERE id = $1`, w.run.ID)
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 2 {
		t.Fatalf("a second silence: interrupts = %d, want 2", n)
	}
	if n := w.count(silentNotices); n != 2 {
		t.Fatalf("a second silence: run.turn_stopped events = %d, want 2", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.turn_stopped' AND payload->>'streak' = '1'`); n != 2 {
		t.Fatalf("fresh streak notices = %d, want 2", n)
	}
}

func TestOnlyASilentBusyConductorIsInterrupted(t *testing.T) {
	for _, tc := range []struct{ name, silentFor, change string }{
		{"recent activity", "9 minutes", ""},
		{"turn done", "11 minutes", "turn_done_at = now()"},
		{"idle", "11 minutes", "agent_busy_at = NULL, agent_active_at = NULL"},
		{"waiting on a person", "11 minutes", "waiting_since = now() - interval '11 minutes'"},
		{"open tool call", "11 minutes", "open_tool_calls = ARRAY['c1'], open_tool_calls_at = jsonb_build_object('c1', now() - interval '11 minutes')"},
		{"implementer", "11 minutes", "phase = 'implement', role = 'implementer'"},
		{"pause asked", "11 minutes", "control = 'pause_graceful'"},
		{"parked", "11 minutes", "status = 'paused', dude_pause = 'conductor'"},
		{"lux resuming", "11 minutes", "lux_state = 'resuming'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResumeWorld(t)
			w.silentConductor(tc.silentFor, tc.change)
			w.interruptSilent()
			if n := w.count(silentInterrupts); n != 0 {
				t.Fatalf("interrupts = %d, want 0", n)
			}
		})
	}
}

// A conductor parked far longer than the limit, its last activity from
// before the park, is taken back up by the resume and starts its next turn:
// its silence counts from that turn, not from before the park.
func TestAResumedConductorIsNotJudgedOnItsParkedTime(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	w.followingAs(func(st *streamLux) lux.Client { return &stoppedEpochLux{streamLux: st, base: base} })
	w.exec(`UPDATE runs SET agent_busy_at = now() - interval '2 hours', agent_active_at = now() - interval '2 hours',
		open_tool_calls_at = '{}', left_running_at = NULL WHERE id = $1`, w.run.ID)
	w.whilePaused()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_busy_at IS NULL AND agent_active_at IS NULL`); n != 1 {
		t.Fatal("resume retained the parked turn's activity clocks")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'resuming'`); n != 1 {
		t.Fatal("the resume was not accepted as resuming: the case is not the one under test")
	}
	// The same clocks as a conductor's: only the role differs.
	w.exec(`UPDATE runs SET phase = NULL, role = 'conductor', kind = 'agent' WHERE id = $1`, w.run.ID)
	w.run.Phase, w.run.Role = "", "conductor"
	w.follow(running(2), busy(2))
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'
		AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL`); n != 1 {
		t.Fatal("the resumed conductor is not running and busy: the case is not the one under test")
	}
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 0 {
		t.Fatalf("a resumed conductor got %d interrupts for its parked time, want none", n)
	}
}

func TestAHostMovePreservesTheSilentStreakAndInterruptTime(t *testing.T) {
	w := newResumeWorld(t)
	w.silentConductor("41 minutes", "")
	w.follow(running(1), busy(1))
	w.ageSilent("41 minutes")
	w.interruptSilent()
	var interrupted time.Time
	if err := w.owner.QueryRow(w.ctx, `SELECT stuck_interrupted_at FROM runs WHERE id = $1`, w.run.ID).Scan(&interrupted); err != nil {
		t.Fatal(err)
	}
	w.follow(luxState(1, "resuming"))
	w.exec(`UPDATE runs SET left_running_at = now() - interval '30 minutes' WHERE id = $1`, w.run.ID)
	w.follow(running(2))
	var moved time.Time
	if err := w.owner.QueryRow(w.ctx, `SELECT stuck_interrupted_at FROM runs WHERE id = $1`, w.run.ID).Scan(&moved); err != nil || !moved.Equal(interrupted) {
		t.Fatalf("host move changed interrupt time: %v -> %v: %v", interrupted, moved, err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND stuck_fingerprint = 'silent:1'`); n != 1 {
		t.Fatal("host move changed the silent streak")
	}
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 1 {
		t.Fatalf("host move interrupted the same silence again: %d", n)
	}
	w.exec(`UPDATE runs SET stuck_fingerprint = 'tool-call', lux_state = 'resuming', left_running_at = now() - interval '1 minute' WHERE id = $1`, w.run.ID)
	w.follow(running(3))
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND stuck_fingerprint = 'tool-call'`); n != 1 {
		t.Fatal("host move changed an open-tool fingerprint")
	}
}

// A conductor's wait for a host to resume it on is not its model's
// silence: back to running, its clock moves on by the wait.
func TestAConductorsHostMoveDoesNotCountAsSilence(t *testing.T) {
	w := newResumeWorld(t)
	w.silentConductor("39 minutes", "lux_state = 'resuming', left_running_at = now() - interval '30 minutes'")
	w.follow(running(2))
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 0 {
		t.Fatalf("a host move queued %d interrupts for nine running minutes, want none", n)
	}
	w.exec(`UPDATE runs SET agent_active_at = agent_active_at - interval '2 minutes' WHERE id = $1`, w.run.ID)
	w.interruptSilent()
	if n := w.count(silentInterrupts); n != 1 {
		t.Fatalf("eleven running minutes: interrupts = %d, want 1", n)
	}
}
