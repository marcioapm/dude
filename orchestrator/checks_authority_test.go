package orchestrator_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A writer's checks report wakes the conductor (run.checks.asked), but that
// is not the conductor asking the person: no question of the conductor's
// is open, so the quiet decision's deadline is not suspended and Deliver's
// defaults take it once it passes.
func TestAChecksTurnDoesNotSuspendTheQuietDeadline(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	task := w.task()
	w.quietAfterImplement(task)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvRunChecksAsked); n == 0 {
		t.Fatalf("the implementer's report asked the conductor no checks")
	}
	if n := w.count(`SELECT count(*) FROM questions q JOIN runs r ON r.id = q.run_id AND r.role = 'conductor'
		WHERE q.task_id = $1`, task); n != 0 {
		t.Fatalf("the checks turn left %d conductor questions to the person", n)
	}
	w.lapse(task)
	w.until("the defaults at the deadline", func() bool {
		w.pump()
		return w.defaulted(task, delivery.DefaultedDeadline) == 1
	})
}

// A person's open answer outlives a checks turn: a writer's report asked
// of the conductor (AskChecksTx, as the gate asks) and decided through
// decide_checks after the answer neither voids the answer nor consumes the
// decision, and the answer still opens the pull request, cited, with no
// second question to the person.
func TestAnOpenAnswerOutlivesAChecksTurn(t *testing.T) {
	w := conducting(t)
	task := w.task()
	q := w.askedAndAnsweredOpen(task, "open")
	at := w.decisionAt(task)
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	var turn int64
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		if turn, err = delivery.LatestTurnTx(context.Background(), tx, impl); err != nil {
			return err
		}
		ref := delivery.RunRef{Org: w.org, ProjectID: w.str(`SELECT project_id FROM tasks WHERE id = $1`, task), TaskID: task, RunID: impl}
		return delivery.AskChecksTx(context.Background(), tx, ref, delivery.ChecksAsked{Turn: turn,
			Check: []delivery.Check{{Command: "make test", Result: delivery.CheckPass, Evidence: "ok"}}}, "checks to decide")
	}); err != nil {
		t.Fatal(err)
	}
	w.must(task, "decide_checks", fmt.Sprintf(`{"runId":%q,"turn":%d,"checks":[{"command":"make test","verdict":"backed","reason":"the recorded call exits 0"}],
		"missing":{"verdict":"none","reason":"nothing obvious"}}`, impl, turn))
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, impl, delivery.EvRunChecksConducted); n != 1 {
		t.Fatalf("%d checks verdicts recorded, want 1", n)
	}
	if v := w.str(`SELECT COALESCE(state->'openAuthority'->'voided'->>'why', '') FROM workflow_runs WHERE task_id = $1`, task); v != "" {
		t.Fatalf("the checks turn voided the answer: %s", v)
	}
	if got := w.decisionAt(task); got != at {
		t.Fatalf("the checks turn moved the decision from %q to %q", at, got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionTaken); n == 0 {
		t.Fatalf("no decision recorded before the checks turn")
	}
	w.carriedOut(task, "start_phase", `{"phase":"simplify"}`)
	w.agentsDone(task, "the simplifier's narrow review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.finishSimplifierReview(task)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	out := w.must(task, "decide", `{"action":"open_pull_request"}`)
	if by, _ := out["authorizedBy"].(map[string]any); by["questionId"] != q {
		t.Errorf("open_pull_request's result: %v", out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1`, task); n != 1 {
		t.Errorf("%d questions to the person, want the one", n)
	}
}
