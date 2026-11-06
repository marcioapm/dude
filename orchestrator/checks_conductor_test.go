package orchestrator_test

// The checks gate asks the conductor (conductor-first PR5a): a writer's
// reported failure and its check not run reach the pull request on the
// conductor's verdict, with no person asked; undecided past the wait, a
// failure is sent back and counts a round. Through the real API, workflow,
// syncer, tool server and fake lux.

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// The dude#224 shape, minus the ask: the implementer reports a failure
// that fails the same at the task's base and a browser check not run; the
// conductor decides them pre-existing and deferred (decide_checks), and the
// work publishes with no question asked and no wait on a person.
func TestAConductorVerdictPublishesAReportedFailureWithoutAPerson(t *testing.T) {
	w := defaultsConducting(t)
	w.onModel("implementer", fakeagent.ChecksDeferredModel)
	task := w.task()
	w.deliverByAPI(task)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' ORDER BY created_at LIMIT 1`, task)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND actor_type = 'agent'
		AND actor_id = payload->>'conductorRunId'
		AND payload->'checks'->0->>'verdict' = 'preexisting' AND payload->'checks'->1->>'verdict' = 'deferred'`,
		impl, delivery.EvRunChecksConducted); n != 1 {
		t.Fatalf("%d conductor verdicts on the implementer's report", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND (payload->>'verified')::bool
		AND payload->'checks'->0->>'result' = $3 AND payload->'checks'->1->>'result' = $4 AND payload->'checks'->0->>'by' = 'conductor'
		AND (payload->'checks'->0->>'decisionCursor')::bigint = (SELECT cursor FROM events WHERE run_id = $1 AND event_type = $5)`,
		impl, delivery.EvRunChecksJudged, delivery.CheckPreexisting, delivery.CheckDeferred, delivery.EvRunChecksConducted); n != 1 {
		t.Fatalf("%d judgements citing the conductor's verdict", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'`, task); n != 0 {
		t.Errorf("%d questions asked of a person", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed' AND payload->>'status' = 'awaiting_input'`, task); n != 0 {
		t.Errorf("the task waited on a person %d times", n)
	}
	if n := w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND status = 'awaiting_input'`, task); n != 0 {
		t.Error("the task waits on a person")
	}
	// The status ledger is read: the task did move (to running, in review).
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed' AND payload ? 'status'`, task); n == 0 {
		t.Error("no status change recorded: the awaiting_input check reads nothing")
	}
}

// Undecided past the wait, a reported failure is never passed: it is sent
// back and counts a round, as an untiered rejection does.
func TestAnUndecidedReportedFailureIsSentBackAtTheDeadline(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	w.onModel("implementer", fakeagent.ChecksPreexistingModel)
	w.syncer.ParkAfter = time.Hour
	task := w.task()
	w.deliverByAPI(task)
	w.until("the conductor asked about the report", func() bool {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeChecks) == 1
	})
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' ORDER BY created_at LIMIT 1`, task)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, impl, delivery.EvRunChecksJudged); n != 0 {
		t.Fatalf("judged %d times while the conductor holds it", n)
	}
	mustExec(t, w.owner, `UPDATE events SET payload = jsonb_set(payload, '{deadline}', to_jsonb(now() - interval '1 second'))
		WHERE run_id = $1 AND event_type = $2`, impl, delivery.EvRunChecksAsked)
	w.until("the report rejected at the deadline", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND NOT (payload->>'verified')::bool
			AND payload->>'directiveId' IS NOT NULL AND payload->'checks'->0->>'judgeReason' LIKE 'not verified by the conductor%'`,
			impl, delivery.EvRunChecksJudged) >= 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND (payload->>'verified')::bool`,
		impl, delivery.EvRunChecksJudged); n != 0 {
		t.Fatal("an undecided failure passed")
	}
	if len(w.gh.Pulls()) != 0 {
		t.Fatal("published an undecided failure")
	}
}
