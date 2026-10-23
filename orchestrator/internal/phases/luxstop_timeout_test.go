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
