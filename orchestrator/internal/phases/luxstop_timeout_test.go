package phases

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// timeLimitWorld is a harnessWorld whose Run is a tester's next round that
// went on from run_p<org> (how: resume or replay), on a 4h limit, in a
// delivery waiting at awaitTest; pending says whether the delivery lists
// the Run among those it waits on.
func timeLimitWorld(t *testing.T, how string, pending bool) *harnessWorld {
	t.Helper()
	w := newHarnessWorld(t)
	w.s.Agent.Timeout = "4h"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, status, ended_at)
			VALUES ('run_p'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'test', 'qa_browser', 'completed', now())`, nil},
		{`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_p'||$1, $1, 'agent.message', 'run_p'||$1, 'wi_'||$1, 'agent', 'run_p'||$1, 'test', '{"text":"Tested it"}')`, nil},
		{`UPDATE runs SET phase = 'test', role = 'qa_browser', continues_run_id = 'run_p'||$1, continues_how = $2 WHERE id = 'run_'||$1`,
			[]any{how}},
		{`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id)
			VALUES ('wfr_'||$1, $1, 'task.delivery', 'delivery:'||$1, 'waiting', 'awaitTest',
				jsonb_build_object('taskId', 'wi_'||$1, 'attempt', 1,
					'pendingRunIds', CASE WHEN $2 THEN jsonb_build_array('run_'||$1) ELSE '[]'::jsonb END), 'wi_'||$1)`,
			[]any{pending}},
	} {
		if _, err := w.owner.Exec(context.Background(), q.sql, append([]any{w.run.Org}, q.args...)...); err != nil {
			t.Fatal(err)
		}
	}
	w.run.Phase, w.run.Role = "test", "qa_browser"
	w.restart()
	return w
}

// held is the Run's status, whether it is held for a replay, and how many
// run.failed it has.
func (w *harnessWorld) held() (status string, timedOut bool, failed int) {
	w.t.Helper()
	if err := w.owner.QueryRow(context.Background(), `SELECT status::text, harness_state ? 'timedOut',
			(SELECT count(*) FROM events WHERE run_id = r.id AND event_type = 'run.failed')
		FROM runs r WHERE id = $1`, w.run.ID).Scan(&status, &timedOut, &failed); err != nil {
		w.t.Fatal(err)
	}
	return status, timedOut, failed
}

// processExited is the shim's turn end when lux killed the agent's process.
var processExited = map[string]any{"type": "acp.turn_end", "data": map[string]any{"stopReason": "", "error": "session/prompt: process exited (137)"}}

// lux times out a resumed round while its agent is mid-turn: it begins
// stopping (timeout), the agent's process exits, and the Run fails
// (timeout), the turn's end arriving before or after lux's stopping. The
// broken turn is decided for lux's reason, and the round is held for the
// sweep to replay, not failed. A replay, which is no continuation, fails:
// at most one replay per round.
func TestAResumedRoundLuxTimesOutMidTurnIsHeldForAReplay(t *testing.T) {
	stopping := func(w *harnessWorld) {
		w.luxState("stopping")
	}
	exited := func(w *harnessWorld) { w.feed(processExited) }
	for _, tc := range []struct {
		name, how    string
		first, then  func(*harnessWorld)
		wantTimedOut bool
	}{
		{"stopping, then the turn's end", "resume", stopping, exited, true},
		{"the turn's end, then stopping", "resume", exited, stopping, true},
		{"a replay", "replay", stopping, exited, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := timeLimitWorld(t, tc.how, true)
			tc.first(w)
			tc.then(w)
			w.luxState("failed")
			status, timedOut, failed := w.held()
			if tc.wantTimedOut {
				if status != "running" || !timedOut || failed != 0 {
					t.Fatalf("status %s, held %v, %d run.failed; want running, held, none", status, timedOut, failed)
				}
				return
			}
			if status != "failed" || timedOut || failed != 1 {
				t.Fatalf("status %s, held %v, %d run.failed; want failed once, not held", status, timedOut, failed)
			}
		})
	}
}

// A held round the delivery no longer waits on (a person stopped the task
// meanwhile) cannot be replaced in its slot: the sweep fails it as lux's
// time limit fails a Run, not kept, and the hold is gone.
func TestAHeldRoundTheDeliveryNoLongerWaitsOnFails(t *testing.T) {
	w := timeLimitWorld(t, "resume", false)
	w.luxState("stopping")
	w.feed(processExited)
	w.luxState("failed")
	if _, err := w.s.advance(context.Background(), w.swept()); err != nil {
		t.Fatal(err)
	}
	status, timedOut, failed := w.held()
	var keep bool
	if err := w.owner.QueryRow(context.Background(), `SELECT keep FROM runs WHERE id = $1`, w.run.ID).Scan(&keep); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || keep || timedOut || failed != 1 {
		t.Fatalf("status %s, keep %v, held %v, %d run.failed; want failed once, not kept, not held", status, keep, timedOut, failed)
	}
	if p := w.failedWith(); p["luxStop"] != "timeout" {
		t.Errorf("run.failed %v, want luxStop timeout", p)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND continues_how = 'replay'`, w.run.TaskID); n != 0 {
		t.Errorf("%d replays made, want none", n)
	}
}

// A pause asked for just before lux timed out a resumed round (an idle or
// a person's park) does not keep it from its replay: pausing the spent lux
// Run would only have its resume stopped again at once.
func TestAHeldRoundWithAPausePendingIsReplayedNotPaused(t *testing.T) {
	w := timeLimitWorld(t, "resume", true)
	// What pausing would ask of lux.
	w.s.Lux = &eventsLux{}
	if _, err := w.owner.Exec(context.Background(), `UPDATE runs SET control = 'pause_graceful', control_requested_at = now() WHERE id = $1`,
		w.run.ID); err != nil {
		t.Fatal(err)
	}
	w.luxState("stopping")
	w.feed(processExited)
	w.luxState("failed")
	if _, err := w.s.advance(context.Background(), w.swept()); err != nil {
		t.Fatal(err)
	}
	var status, replacedBy, how, continues string
	if err := w.owner.QueryRow(context.Background(), `SELECT r.status::text, COALESCE(r.replaced_by, ''),
			COALESCE(n.continues_how, ''), COALESCE(n.continues_run_id, '')
		FROM runs r LEFT JOIN runs n ON n.id = r.replaced_by WHERE r.id = $1`, w.run.ID).Scan(&status, &replacedBy, &how, &continues); err != nil {
		t.Fatal(err)
	}
	if status != "aborted" || replacedBy == "" || how != "replay" || continues != "run_p"+w.run.Org {
		t.Fatalf("status %s, replaced by %q (%s of %q); want aborted, replaced by a replay of run_p%s", status, replacedBy, how, continues, w.run.Org)
	}
}

// luxState is lux's state event for the Run's first placement, with reason
// timeout, applied and saved in one batch as the follower does.
func (w *harnessWorld) luxState(state string) {
	w.t.Helper()
	ctx := context.Background()
	raw, _ := json.Marshal(map[string]any{"state": state, "reason": "timeout"})
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		if err := w.tr.apply(ctx, tx, w.s, lux.Frame{Kind: "lux", Epoch: 1, EventType: "state", EventData: raw}); err != nil {
			return err
		}
		return w.tr.save(ctx, tx, "", 0)
	}); err != nil {
		w.t.Fatal(err)
	}
}

// swept is the Run as the sweep takes it up.
func (w *harnessWorld) swept() phaseRun {
	w.t.Helper()
	runs, err := w.s.due(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == w.run.ID {
			return r
		}
	}
	w.t.Fatalf("the sweep does not take up %s", w.run.ID)
	return phaseRun{}
}

func (w *harnessWorld) count(sql string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}
