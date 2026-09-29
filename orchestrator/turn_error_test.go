package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// What OpenCode answers a session/prompt for a model it has no provider for,
// as lux's ACP adapter relays it.
const cannotConnect = "session/prompt: Internal error: Cannot connect to API: Unable to connect. " +
	"Is the computer able to access the url? (-32603)"

// An agent whose turn fails fails its phase with the agent's error, and
// nothing is pushed: the branch would be the base commit, and the push's own
// error would hide why.
func TestAFailedTurnFailsThePhaseWithTheAgentsErrorAndPushesNothing(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5-5", "llm-anthropic/claude-sonnet-5-5"} {
		t.Run(model, func(t *testing.T) { testFailedTurn(t, model) })
	}
}

func testFailedTurn(t *testing.T, model string) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || jsonb_build_object('implementer', jsonb_build_object('model', $2::text))
		WHERE id = $1`, w.project, model)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{TurnError: cannotConnect} }
	wi := w.task()
	w.deliver(wi)
	w.until("the task to need a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })

	var status, errText string
	var pushAsked bool
	if err := w.owner.QueryRow(context.Background(), `SELECT status::text, COALESCE(error, ''), push_request_id IS NOT NULL
		FROM runs WHERE task_id = $1`, wi).Scan(&status, &errText, &pushAsked); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(errText, `model "`+model+`"`) || !strings.Contains(errText, "Cannot connect to API") {
		t.Errorf("run %s: %q; want failed, naming the model and the agent's error", status, errText)
	}
	if pushAsked || w.lux.Runs()[0].Pushed {
		t.Errorf("a turn that failed was pushed")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.failed'
		AND payload->>'error' LIKE '%Cannot connect to API%'`, wi); n != 1 {
		t.Errorf("%d run.failed events carrying the agent's error, want 1", n)
	}
	var escalated string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->>'reason', '') FROM events WHERE task_id = $1
		AND event_type = 'task.status_changed' ORDER BY cursor DESC LIMIT 1`, wi).Scan(&escalated)
	if escalated != "implement_failed" {
		t.Errorf("escalated for %q, want implement_failed", escalated)
	}
}

// A turn dude cancels itself — a nudge interrupting a quiet agent — ends with
// stopReason "cancelled" and no error: not a failure. The agent takes the
// nudge as its next turn, finishes, and its work is pushed.
func TestATurnANudgeCancelsIsNotAFailure(t *testing.T) {
	w := newWorld(t)
	w.syncer.IdleAfter = 300 * time.Millisecond
	// Quiet until told something; the nudge wakes it and it finishes.
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, WakeOnInput: true, Reply: "Done.", Commit: map[string]string{"A.md": "a\n"}, Message: "work"}
	}
	wi := w.task()
	w.deliver(wi)
	var runID string
	w.until("the implementer to finish after its nudge", func() bool {
		// Its nudge mark is cleared once it works again; the event stays.
		_ = w.owner.QueryRow(context.Background(), `SELECT r.id FROM runs r JOIN events e ON e.run_id = r.id
			WHERE r.task_id = $1 AND r.phase = 'implement' AND e.event_type = 'run.idle_nudged' AND r.status = 'completed'`, wi).Scan(&runID)
		return runID != ""
	})
	if r := w.lux.Runs()[0]; r.Interrupted != 1 || !r.Pushed {
		t.Errorf("interrupted=%d pushed=%v; want the nudge to cancel one turn and the work pushed", r.Interrupted, r.Pushed)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND error IS NOT NULL`, runID); n != 0 {
		t.Errorf("a cancelled turn left an error on the run")
	}
}
