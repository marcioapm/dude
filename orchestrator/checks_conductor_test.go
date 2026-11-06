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

// A writer parked on the checks ask (its grace over before the conductor
// decided) resumes once on the verdict, and the gate reads that verdict for
// the very turn it asked about: the resume starts no new agent turn that
// would orphan it and ask again, and the work publishes verified.
func TestAParkedChecksAskResumesOnceOnTheVerdictAndPublishes(t *testing.T) {
	w := defaultsConducting(t)
	w.lux.ManualChecks = true // The test is the conductor deciding checks.
	w.syncer.ParkAfter = 300 * time.Millisecond
	w.onModel("implementer", fakeagent.ChecksHonestModel)
	task := w.task()
	w.deliverByAPI(task)
	w.until("the conductor asked about the report", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvRunChecksAsked) == 1
	})
	// The Run whose pushing turn the gate asked about.
	impl := w.str(`SELECT run_id FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvRunChecksAsked)
	turn := w.str(`SELECT payload->>'turn' FROM events WHERE run_id = $1 AND event_type = $2`, impl, delivery.EvRunChecksAsked)
	w.untilParked(impl)
	if resumed, _ := w.luxResumes(impl); resumed != 0 {
		t.Fatalf("resumed %d times before the verdict", resumed)
	}
	w.must(task, "decide_checks", `{"runId":"`+impl+`","turn":`+turn+`,"checks":[{"command":"`+fakeagent.CheckCommand+
		`","verdict":"backed","reason":"the recorded call exited 0"}],"missing":{"verdict":"none","reason":"nothing obvious"}}`)
	w.until("the asked turn judged", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, impl, delivery.EvRunChecksJudged) > 0
	})
	// Later writers' reports (the review's fix) are the test's to decide
	// too: each backed as it is asked, until the work publishes.
	w.until("the pull request", func() bool {
		w.backOpenAsks(task)
		return len(w.gh.Pulls()) == 1
	})
	if resumed, _ := w.luxResumes(impl); resumed != 1 {
		t.Errorf("resumed %d times, want once on the verdict", resumed)
	}
	if got := w.str(`SELECT COALESCE(max(cursor), 0)::text FROM events WHERE run_id = $1 AND event_type = 'agent.session.stopped'`, impl); got != turn {
		t.Errorf("the latest turn is %s after the resume, want the asked turn %s", got, turn)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, impl, delivery.EvRunChecksAsked); n != 1 {
		t.Errorf("asked %d times, want once", n)
	}
	if got := w.ids(`SELECT (payload->>'verified') FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`, impl, delivery.EvRunChecksJudged); strings.Join(got, ",") != "true" {
		t.Errorf("judged %v, want the asked turn verified once", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'turn' = $3
		AND (payload->>'verified')::bool AND (payload->'checks'->0->>'decisionCursor')::bigint = (SELECT cursor FROM events WHERE run_id = $1 AND event_type = $4)`,
		impl, delivery.EvRunChecksJudged, turn, delivery.EvRunChecksConducted); n != 1 {
		t.Errorf("%d judgements of turn %s citing the verdict", n, turn)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, impl); n != 1 {
		t.Error("the resumed implementer did not complete")
	}
}

// backOpenAsks decides, as the task's conductor, every checks ask not yet
// decided or judged: each reported pass backed, anything else rejected.
func (w *world) backOpenAsks(task string) {
	w.t.Helper()
	for _, id := range w.ids(`SELECT a.id FROM events a WHERE a.task_id = $1 AND a.event_type = $2 AND NOT EXISTS (SELECT 1 FROM events v
		WHERE v.run_id = a.run_id AND v.event_type IN ($3, $4) AND v.payload->>'turn' = a.payload->>'turn')`,
		task, delivery.EvRunChecksAsked, delivery.EvRunChecksConducted, delivery.EvRunChecksJudged) {
		args := w.str(`SELECT jsonb_build_object('runId', run_id, 'turn', (payload->>'turn')::bigint,
			'checks', (SELECT jsonb_agg(jsonb_build_object('command', c->>'command', 'reason', 'the test decides',
				'verdict', CASE WHEN c->>'result' = 'pass' THEN 'backed' ELSE 'reject' END)) FROM jsonb_array_elements(payload->'checks') c),
			'missing', jsonb_build_object('verdict', 'none', 'reason', 'nothing obvious'))::text FROM events WHERE id = $1`, id)
		w.as(task, "decide_checks", args)
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
