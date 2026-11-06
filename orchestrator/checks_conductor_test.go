package orchestrator_test

// The checks gate asks the conductor (conductor-first PR5a): a writer's
// reported failure and its check not run reach the pull request on the
// conductor's verdict, with no person asked; undecided past the wait, a
// failure is sent back and counts a round. Through the real API, workflow,
// syncer, tool server and fake lux.

import (
	"strings"
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
	w.lux.ManualChecks = true // Deliberately exercise an unanswered ask.
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

// A reported pass the recorded calls do not back (the command ran and
// failed; the report says pass) is decided unbacked by the conductor that
// reads them: sent back, each round counted, never published, and at the
// bound escalated, held by the conductor, then handed to a person at its
// deadline.
func TestAnUnbackedPassTheConductorReadsIsSentBackThenEscalates(t *testing.T) {
	w := defaultsConducting(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":1}' WHERE organization_id = $1`, w.org)
	w.onModel("implementer", fakeagent.ChecksUnbackedModel)
	task := w.task()
	w.deliverByAPI(task)
	w.until("the checks escalation", func() bool { return w.escalationReason(task) == delivery.ReasonChecksUnverified })
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' ORDER BY created_at LIMIT 1`, task)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND actor_type = 'agent'
		AND payload->'checks'->0->>'verdict' = 'unbacked'`, impl, delivery.EvRunChecksConducted); n != 2 {
		t.Fatalf("%d unbacked verdicts from the conductor, want one per turn (2)", n)
	}
	if got := w.judged(task); strings.Join(got, ",") != "unverified,unverified" {
		t.Errorf("judged %v, want each turn unverified", got)
	}
	if n := len(w.sentBack(task)); n != 1 {
		t.Errorf("sent back %d times, want the organisation's 1 round", n)
	}
	if n := w.implementerPushes(task); n != 0 || len(w.gh.Pulls()) != 0 {
		t.Fatalf("%d pushes, %d pull requests of an unbacked report", n, len(w.gh.Pulls()))
	}
	w.conductorHeld(task)
	// The conductor decides nothing past its wait: the escalation goes to
	// a person.
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now(), state = jsonb_set(state, '{escalation,raisedAt}', to_jsonb(now() - interval '1 hour'))
		WHERE task_id = $1 AND workflow_type = 'task.delivery' AND status = 'waiting'`, task)
	w.until("the escalation handed to a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
	if got := w.str(`SELECT state->'escalation'->>'holder' FROM workflow_runs WHERE task_id = $1 AND workflow_type = 'task.delivery'
		ORDER BY created_at DESC LIMIT 1`, task); got != delivery.HolderPerson {
		t.Errorf("held by %q after the deadline, want the person", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, task); n != 1 {
		t.Errorf("%d escalation questions asked of a person, want 1", n)
	}
}
