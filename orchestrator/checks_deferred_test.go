package orchestrator_test

// A deferred check is a promise (the two live trials of 2026-11-12/13):
// the conductor passed a check not run because a later phase runs it, and
// the task went ready_to_merge with that check never run. Now the ready
// gate's deferred item holds the pull request in review until the
// conductor, asked again once the pull request is open and its CI has
// reported, keeps it (backed) or a person settles its reject. Through the
// real API, workflow, syncer, tool server and fake lux.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// deferredWorld delivers a task whose implementer reports a check not run
// that a later phase runs (fakeagent.ChecksDeferredModel's browser check,
// in the trials the bench workflow), which the defaults-taking conductor
// defers; the pull request opens and its CI goes green. It returns the
// task once the delivery waits on the pull request with CI reported.
func deferredWorld(t *testing.T) (*world, string) {
	w := defaultsConducting(t)
	w.onModel("implementer", fakeagent.ChecksDeferredModel)
	w.gh.SetChecks("success")
	task := w.task()
	w.deliverByAPI(task)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("the delivery waits on its pull request", func() bool {
		w.sync()
		w.pump()
		return w.waitingOnPR(task)
	})
	return w, task
}

// deferredWakes are the task's second checks asks' wakes, oldest first.
func (w *world) deferredWakes(task string) []string {
	return w.ids(`SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = $2 AND key LIKE 'checks:%:d%' ORDER BY created_at`,
		task, delivery.WakeChecks)
}

// storedDeferred is the deferred item of the task's pull request's stored
// gate; "" before one is stored.
func (w *world) storedDeferred(task string) (ok bool, detail string) {
	w.t.Helper()
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT ready_gate FROM pull_requests WHERE task_id = $1 AND ready_gate IS NOT NULL`,
		task).Scan(&raw); err == pgx.ErrNoRows {
		return false, ""
	} else if err != nil {
		w.t.Fatal(err)
	}
	var g delivery.StoredGate
	_ = json.Unmarshal(raw, &g)
	for _, it := range g.Items {
		if it.Key == delivery.ReadyDeferred {
			return it.OK, it.Detail
		}
	}
	return false, ""
}

// secondAsk is the task's newest second checks ask: its Run, turn and key.
func (w *world) secondAsk(task string) (run, turn, key string) {
	w.t.Helper()
	if err := w.owner.QueryRow(context.Background(), `SELECT run_id, payload->>'turn', payload->'discharge'->>'key' FROM events
		WHERE task_id = $1 AND event_type = $2 AND payload ? 'discharge' ORDER BY cursor DESC LIMIT 1`, task, delivery.EvRunChecksAsked).
		Scan(&run, &turn, &key); err != nil {
		w.t.Fatalf("the second ask: %v", err)
	}
	return run, turn, key
}

// answerDeferred answers the task's newest second ask as the conductor.
func (w *world) answerDeferred(task, verdict, reason string) (int, map[string]any) {
	w.t.Helper()
	_, _, key := w.secondAsk(task)
	return w.answerAsk(task, key, verdict, reason)
}

// answerAsk answers the second ask key names as the conductor.
func (w *world) answerAsk(task, key, verdict, reason string) (int, map[string]any) {
	w.t.Helper()
	row := w.rows(`SELECT run_id, payload->>'turn' FROM events WHERE task_id = $1 AND event_type = $2 AND payload->'discharge'->>'key' = $3`,
		task, delivery.EvRunChecksAsked, key)
	if len(row) != 1 {
		w.t.Fatalf("%d second asks keyed %s", len(row), key)
	}
	return w.as(task, "decide_checks", fmt.Sprintf(`{"runId":%q,"turn":%s,"ask":%q,"checks":[{"command":%q,"verdict":%q,"reason":%q}],"missing":{"verdict":"none","reason":"not asked"}}`,
		row[0][0], row[0][1], key, fakeagent.DeferredCommand, verdict, reason))
}

// deferredWake is the second ask's wake line for the ask key, the pull
// request target#1 at head, word for word.
func deferredWake(run, turn, key, head string) string {
	return fmt.Sprintf("The pull request is open at target at %s, with CI reported there: keep the checks you deferred to this later phase, "+
		"then answer with decide_checks (runId %s, turn %s, ask %s), one verdict per check in this order. Deferred is no verdict now: "+
		"backed, with a reason citing the evidence at these heads (a dispatch id and its outcome, a verify result, or the pull request's CI at this head), "+
		"or reject; missing is not asked again (give none). The ask names these heads: if they move, a new wake asks again, and an answer to this one is refused. "+
		"The pull request is not ready until you answer, and unanswered by the deadline it goes to a person. %q deferred (Deliver's defaults: not-run): kept (backed) or reject?",
		head[:7], run, turn, key, fakeagent.DeferredCommand)
}

// deferredDetail is the gate's deferred item's detail for the one
// deferred check, where it stands.
func deferredDetail(stands string) string {
	return fmt.Sprintf("1 deferred check(s) not kept: %q (deferred: Deliver's defaults: not-run) — %s", fakeagent.DeferredCommand, stands)
}

// readyEvents counts the task's task.ready_to_merge events.
func (w *world) readyEvents(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, task)
}

// weighedTwice has the delivery weigh its gate at least twice more.
func (w *world) weighedTwice(task string) {
	w.t.Helper()
	for range 2 {
		w.sync()
		w.reweigh(task)
		w.pump()
	}
}

// dischargeRaised counts the task's discharge escalations raised (held
// by the conductor first, escalation.raised, or by a person at once), and
// asked of a person (question.asked).
func (w *world) dischargeRaised(task string) (raised, asked int) {
	n := func(typ string) int {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND (payload->'detail'->>'discharge')::boolean`, task, typ)
	}
	return n("escalation.raised"), n("question.asked")
}

// The trial: the deferred check holds the pull request in review, CI green
// and every other item holding, and the conductor is woken once to keep
// it. Its answer at the second ask is backed or reject: deferred again is
// refused, and so is an answer that names no ask. Backed, citing the run
// it kept it by (here the pull request's CI at the head), the task is
// ready to merge.
func TestADeferredCheckHoldsReadyUntilTheConductorKeepsIt(t *testing.T) {
	w, task := deferredWorld(t)
	w.until("the conductor asked to keep the deferred check", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(task)) == 1
	})
	w.weighedTwice(task)
	if s := w.taskStatus(task); s == "ready_to_merge" {
		t.Fatal("ready to merge with a deferred check never run")
	}
	if n := w.readyEvents(task); n != 0 {
		t.Fatalf("%d ready_to_merge events", n)
	}
	if ok, detail := w.storedDeferred(task); ok || detail != deferredDetail("the conductor is asked to keep it at this head") {
		t.Fatalf("the deferred item: %v %q", ok, detail)
	}
	run, turn, key := w.secondAsk(task)
	head := w.gh.SHA(w.gh.Pull(1).Head)
	if wakes := w.deferredWakes(task); len(wakes) != 1 || wakes[0] != deferredWake(run, turn, key, head) {
		t.Fatalf("second asks' wakes:\n got %q\nwant %q", wakes, deferredWake(run, turn, key, head))
	}
	// An answer must name the ask it answers.
	if status, out := w.as(task, "decide_checks", fmt.Sprintf(`{"runId":%q,"turn":%s,"checks":[{"command":%q,"verdict":"backed","reason":"kept"}],"missing":{"verdict":"none","reason":"not asked"}}`,
		run, turn, fakeagent.DeferredCommand)); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "answer it with the ask that wake names") {
		t.Fatalf("no ask named: %d %v", status, out)
	}
	// Deferred is no verdict now: the later phase has come.
	if status, out := w.answerDeferred(task, delivery.VerdictDeferred, "later"); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "is not a verdict for") {
		t.Fatalf("deferred again: %d %v", status, out)
	}
	if status, out := w.answerDeferred(task, delivery.VerdictBacked, "the pull request's CI at "+head[:7]+" ran it: success"); status != 200 {
		t.Fatalf("backed: %d %v", status, out)
	}
	w.until("ready to merge once kept", func() bool {
		w.sync()
		w.pump()
		return w.taskStatus(task) == "ready_to_merge"
	})
	if ok, detail := w.storedDeferred(task); !ok || detail != "1 deferred check(s) kept" {
		t.Errorf("the deferred item once kept: %v %q", ok, detail)
	}
	if n := len(w.deferredWakes(task)); n != 1 {
		t.Errorf("%d second asks, want exactly one", n)
	}
}

// A late answer binds to the ask it names, never to a newer one: dude asks
// at H1, the conductor goes to keep the check there, a push moves the
// pull request to H2, and dude asks again at H2. The conductor's answer
// about H1, arriving late, is refused (its ask is superseded) and keeps
// nothing at H2: the task stays in review. A backed answer to the ask at
// H2 makes it ready.
func TestALateAnswerAboutOldHeadsKeepsNothingAtNewOnes(t *testing.T) {
	w, task := deferredWorld(t)
	w.until("the conductor asked at H1", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(task)) == 1
	})
	_, _, first := w.secondAsk(task)
	h2 := w.movedHead()
	w.until("the conductor asked again at H2", func() bool {
		w.sync()
		w.reweigh(task)
		w.pump()
		return len(w.deferredWakes(task)) == 2
	})
	_, _, second := w.secondAsk(task)
	if first == second {
		t.Fatalf("one ask %s at both heads", first)
	}
	if ok, detail := w.storedDeferred(task); ok || detail != deferredDetail("the conductor is asked to keep it at this head") {
		t.Fatalf("the deferred item at H2: %v %q", ok, detail)
	}
	// The late answer about H1.
	if status, out := w.answerAsk(task, first, delivery.VerdictBacked, "bench.yml at H1 completed: success"); status != 422 ||
		!strings.Contains(fmt.Sprint(out["error"]), fmt.Sprintf("ask %q is superseded", first)) {
		t.Fatalf("the late answer about H1: %d %v", status, out)
	}
	w.weighedTwice(task)
	if n := w.readyEvents(task); n != 0 {
		t.Fatalf("ready to merge at %s on an answer about the heads before", h2[:7])
	}
	if ok, detail := w.storedDeferred(task); ok || detail != deferredDetail("the conductor is asked to keep it at this head") {
		t.Fatalf("the deferred item after the late answer: %v %q", ok, detail)
	}
	if status, out := w.answerAsk(task, second, delivery.VerdictBacked, "the pull request's CI at "+h2[:7]+" ran it: success"); status != 200 {
		t.Fatalf("backed at H2: %d %v", status, out)
	}
	w.until("ready to merge once kept at H2", func() bool {
		w.sync()
		w.pump()
		return w.taskStatus(task) == "ready_to_merge"
	})
}

// Backed at H1, the check is kept at H1 only: a push to H2 holds the pull
// request in review and asks again there.
func TestACheckKeptAtOldHeadsIsAskedAgainAtNewOnes(t *testing.T) {
	w, task := deferredWorld(t)
	w.until("the conductor asked at H1", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(task)) == 1
	})
	if status, out := w.answerDeferred(task, delivery.VerdictBacked, "the pull request's CI ran it: success"); status != 200 {
		t.Fatalf("backed at H1: %d %v", status, out)
	}
	w.until("ready at H1", func() bool {
		w.sync()
		w.pump()
		return w.taskStatus(task) == "ready_to_merge"
	})
	w.movedHead()
	w.until("asked again at H2", func() bool {
		w.sync()
		w.reweigh(task)
		w.pump()
		return len(w.deferredWakes(task)) == 2
	})
	w.weighedTwice(task)
	if s := w.taskStatus(task); s == "ready_to_merge" {
		t.Fatal("ready at H2 on a check kept at H1")
	}
	if ok, detail := w.storedDeferred(task); ok || detail != deferredDetail("the conductor is asked to keep it at this head") {
		t.Fatalf("the deferred item at H2: %v %q", ok, detail)
	}
}

// Unanswered by its deadline, the second ask goes to a person: the same
// checks_unverified escalation a reject raises, once, offering waive and
// stop. Before the deadline, nothing is raised.
func TestAnUnansweredSecondAskGoesToAPersonAtItsDeadline(t *testing.T) {
	w, task := deferredWorld(t)
	w.until("the conductor asked to keep the deferred check", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(task)) == 1
	})
	// The delivery wakes itself at the deadline: no sync sees it pass.
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND workflow_type = 'task.delivery' AND status = 'waiting'
		AND wake_at BETWEEN (SELECT (payload->>'deadline')::timestamptz FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'discharge')
		AND (SELECT (payload->>'deadline')::timestamptz + interval '1 minute' FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'discharge')`,
		task, delivery.EvRunChecksAsked); n != 1 {
		t.Errorf("the delivery does not wake at the ask's deadline")
	}
	w.weighedTwice(task)
	if raised, asked := w.dischargeRaised(task); raised+asked != 0 {
		t.Fatalf("%d/%d escalations before the deadline", raised, asked)
	}
	mustExec(t, w.owner, `UPDATE events SET payload = jsonb_set(payload, '{deadline}', to_jsonb(now() - interval '1 second'))
		WHERE task_id = $1 AND event_type = $2 AND payload ? 'discharge'`, task, delivery.EvRunChecksAsked)
	w.until("the discharge escalation", func() bool {
		w.reweigh(task)
		w.pump()
		return w.escalationReason(task) == delivery.ReasonChecksUnverified
	})
	if ok, detail := w.storedDeferred(task); ok || detail != deferredDetail("the conductor did not answer by the ask's deadline: a person settles it") {
		t.Errorf("the deferred item: %v %q", ok, detail)
	}
	// The conductor holds it; at its deadline it goes to a person.
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now(), state = jsonb_set(state, '{escalation,raisedAt}', to_jsonb(now() - interval '1 hour'))
		WHERE task_id = $1 AND workflow_type = 'task.delivery' AND status = 'waiting'`, task)
	w.until("the escalation with a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
	for range 3 {
		w.sync()
		w.pump()
	}
	if raised, asked := w.dischargeRaised(task); raised != 1 || asked != 1 {
		t.Fatalf("%d discharge escalations raised, %d asked of a person, want 1 and 1", raised, asked)
	}
	if got := w.str(`SELECT payload->'detail'->>'message' FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'kind' = 'escalation'`, task); got != "Deferred checks not kept:\n- "+fakeagent.DeferredCommand+
		": deferred, then the conductor did not answer the ask to keep it by its deadline" {
		t.Errorf("the escalation says %q", got)
	}
	owner, _, _ := w.people(task)
	if status, out := w.decide(owner, task, delivery.ActionWaive, "bench is run by hand on release"); status != 200 {
		t.Fatalf("waive: %d %v", status, out)
	}
	w.until("ready to merge once a person waived it", func() bool {
		w.sync()
		w.pump()
		return w.taskStatus(task) == "ready_to_merge"
	})
}

// A person's waiver of another check keeps no deferred promise: bench is
// deferred and lint rejected, so the report is held after the rounds; a
// person waives lint at checks_unverified (its judged row lists no
// checks), the pull request opens and CI goes green, and still the task is
// not ready: the conductor is asked to keep bench.
func TestAWaiverOfAnotherCheckKeepsNoDeferredPromise(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":0}' WHERE organization_id = $1`, w.org)
	w.gh.SetChecks("success")
	// The scripted conductor rejects the failure and defers the browser
	// check, as the trial's did lint and bench.
	wi := w.checksDelivery(fakeagent.ChecksDeferredModel, &checksConductor{reject: true, keepDeferred: true})
	w.until("the checks escalation", func() bool { return w.checksEscalations(wi) == 1 })
	w.until("the escalation with a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' ORDER BY created_at LIMIT 1`, wi)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->'checks' @> '[{"result":"deferred"}]'`,
		impl, delivery.EvRunChecksJudged); n != 1 {
		t.Fatalf("%d judgements deferring the browser check", n)
	}
	owner, _, _ := w.people(wi)
	// Every writer reports the same (the fixer and simplifier too): each
	// held report is waived, so each Run's newest judgement lists no check.
	w.until("the pull request, each held report waived", func() bool {
		if w.taskStatus(wi) == "awaiting_input" && w.escalationReason(wi) == delivery.ReasonChecksUnverified {
			if status, out := w.decide(owner, wi, delivery.ActionWaive, "make test's TestClock failure predates this change"); status != 200 {
				t.Fatalf("waive: %d %v", status, out)
			}
		}
		return len(w.gh.Pulls()) == 1
	})
	if n := w.count(`SELECT count(*) FROM (SELECT DISTINCT ON (r.id) e.payload FROM runs r JOIN events e ON e.run_id = r.id
		WHERE r.task_id = $1 AND e.event_type = $2 ORDER BY r.id, e.cursor DESC) newest WHERE payload->'checks' @> '[{"result":"deferred"}]'`,
		wi, delivery.EvRunChecksJudged); n != 0 {
		t.Fatalf("%d writers' newest judgements still defer the check: the scenario needs every one waived", n)
	}
	if got := w.str(`SELECT payload->>'checks' FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor DESC LIMIT 1`,
		impl, delivery.EvRunChecksJudged); got != "[]" {
		t.Fatalf("the waiver's judgement lists %s", got)
	}
	w.until("the conductor asked to keep the deferred check", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(wi)) == 1
	})
	w.weighedTwice(wi)
	if n := w.readyEvents(wi); n != 0 {
		t.Fatal("ready to merge with the deferred check never run: the waiver of another check kept it")
	}
	if ok, detail := w.storedDeferred(wi); ok || !strings.HasPrefix(detail, fmt.Sprintf("1 deferred check(s) not kept: %q (deferred: ", fakeagent.DeferredCommand)) ||
		!strings.HasSuffix(detail, " — the conductor is asked to keep it at this head") {
		t.Fatalf("the deferred item: %v %q", ok, detail)
	}
}

// Rejected at the second ask, the check is not kept: not ready, and the
// rejection goes to a person as a held report does after the rounds
// (checks_unverified), with no retry offered: there is no writer's turn.
// A person's waiver settles it, and the task is ready to merge.
func TestADeferredCheckTheConductorRejectsWaitsOnAPerson(t *testing.T) {
	w, task := deferredWorld(t)
	w.until("the conductor asked to keep the deferred check", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(task)) == 1
	})
	if status, out := w.answerDeferred(task, delivery.VerdictReject, "bench.yml was not dispatched: the person declined"); status != 200 {
		t.Fatalf("reject: %d %v", status, out)
	}
	w.until("the checks escalation", func() bool {
		w.pump()
		return w.escalationReason(task) == delivery.ReasonChecksUnverified
	})
	// The conductor holds it; at its deadline it goes to a person.
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now(), state = jsonb_set(state, '{escalation,raisedAt}', to_jsonb(now() - interval '1 hour'))
		WHERE task_id = $1 AND workflow_type = 'task.delivery' AND status = 'waiting'`, task)
	w.until("the escalation handed to a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, task); n != 1 {
		t.Fatalf("%d escalation questions asked of a person, want 1", n)
	}
	if got := w.str(`SELECT payload->>'actions' FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`,
		task); got != `["waive", "stop"]` {
		t.Errorf("offered %s, want waive and stop", got)
	}
	if ok, detail := w.storedDeferred(task); ok || detail != deferredDetail("the conductor rejected it (bench.yml was not dispatched: the person declined): a person settles it") {
		t.Errorf("the deferred item: %v %q", ok, detail)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, task); n != 0 {
		t.Fatal("ready to merge with a deferred check rejected")
	}
	owner, _, _ := w.people(task)
	status, out := w.decide(owner, task, delivery.ActionWaive, "bench is run by hand on release")
	if status != 200 {
		t.Fatalf("waive: %d %v", status, out)
	}
	w.until("ready to merge once a person waived it", func() bool {
		w.sync()
		w.pump()
		return w.taskStatus(task) == "ready_to_merge"
	})
}

// Exactly once: weighing the gate again, and a restart between recording
// the second ask's wake and its delivery (the wake's row there,
// undelivered, the syncer new), record no second ask and deliver one wake.
func TestTheSecondChecksAskIsRecordedAndDeliveredOnce(t *testing.T) {
	w, task := deferredWorld(t)
	ctx := context.Background()
	// The workflow alone (no syncer sweep): the ask is recorded, its wake
	// not delivered.
	w.until("the second ask recorded", func() bool {
		w.sync()
		if _, err := w.runtime.Tick(ctx, 10); err != nil {
			t.Fatal(err)
		}
		return len(w.deferredWakes(task)) == 1
	})
	// The gate weighed again and again (its timer due each time): no new
	// ask.
	for range 3 {
		w.reweigh(task)
		if _, err := w.runtime.Tick(ctx, 10); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key LIKE 'checks:%:d%' AND delivered_at IS NULL`, task); n != 1 {
		t.Fatalf("%d undelivered second asks' wakes before the restart, want 1", n)
	}
	w.syncer.Stop()
	w.syncer = &phases.Syncer{DB: w.syncer.DB, Lux: w.syncer.Lux, Forges: w.syncer.Forges, Log: w.log, Agent: w.syncer.Agent,
		KeepFor: w.syncer.KeepFor, ParkAfter: w.syncer.ParkAfter, ConductorWarm: w.syncer.ConductorWarm, WakeWindow: w.syncer.WakeWindow}
	t.Cleanup(w.syncer.Stop)
	w.until("the wake delivered", func() bool {
		w.sync()
		w.pump()
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key LIKE 'checks:%:d%' AND delivered_at IS NOT NULL`, task) == 1
	})
	for range 5 {
		w.reweigh(task)
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'discharge'`, task, delivery.EvRunChecksAsked); n != 1 {
		t.Errorf("%d second asks recorded, want 1", n)
	}
	if n := len(w.deferredWakes(task)); n != 1 {
		t.Errorf("%d second asks' wakes, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken' AND strpos(payload::text, $2) > 0`,
		task, fakeagent.KeepDeferred); n != 1 {
		t.Errorf("the conductor was woken %d times with the second ask, want once", n)
	}
}

// reweigh has the delivery waiting on its pull request weigh its gate
// again now: its timer (reweighAt) due.
func (w *world) reweigh(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{reweighAt}', to_jsonb(now())), wake_at = now()
		WHERE task_id = $1 AND workflow_type = 'task.delivery' AND step = 'awaitPullRequest' AND status = 'waiting'`, task)
}

// movedHead moves the pull request to a new head a review need not see
// (GitHub's Update branch: a pure merge of main), as a person might: its
// head.
func (w *world) movedHead() string {
	w.t.Helper()
	w.gh.CommitFiles("main", "Other work", map[string]string{"OTHER.md": "other\n"})
	head := w.gh.MergeInto(w.gh.Pull(1).Head, "main", nil)
	if head == "" {
		w.t.Fatal("could not merge main in")
	}
	return head
}

// A deferred check held when the delivery leaves its conductor (handed to
// Deliver's policy) after dude asked the conductor to keep it: no
// conductor will answer, so the item says a person settles it (not that
// the conductor is asked), and a person is asked, once, by the same
// checks_unverified escalation a reject raises. At new heads no conductor
// is asked. A waiver settles it, and the task is ready to merge.
func TestADeferredCheckHandedToDeliverWaitsOnAPerson(t *testing.T) {
	w, task := deferredWorld(t)
	w.until("the conductor asked to keep the deferred check", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(task)) == 1
	})
	// The delivery leaves its conductor, as a person's accept of Deliver's
	// defaults hands it over (SetDeciderTx), and its head moves.
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		d, err := delivery.LoadDelivery(context.Background(), tx, task)
		if err != nil {
			return err
		}
		_, err = delivery.SetDeciderTx(context.Background(), tx, w.org, d.WorkflowID, &d.State, delivery.DeciderPolicy, "",
			delivery.WhyAcceptedDefaults, "human", "test")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if d := w.decider(task); d != delivery.DeciderPolicy {
		t.Fatalf("decided by %s after the handover", d)
	}
	w.movedHead()
	w.until("the discharge escalation with a person", func() bool {
		w.sync()
		w.reweigh(task)
		w.pump()
		return w.taskStatus(task) == "awaiting_input"
	})
	w.weighedTwice(task)
	if n := len(w.deferredWakes(task)); n != 1 {
		t.Errorf("%d second asks, want only the one made before the handover", n)
	}
	if ok, detail := w.storedDeferred(task); ok || detail != deferredDetail("no conductor decides this delivery: a person settles it") {
		t.Errorf("the deferred item: %v %q", ok, detail)
	}
	if raised, asked := w.dischargeRaised(task); raised != 0 || asked != 1 {
		t.Fatalf("%d discharge escalations held by a conductor, %d asked of a person, want 0 and 1", raised, asked)
	}
	if got := w.str(`SELECT payload->'detail'->>'message' FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'kind' = 'escalation'`, task); got != "Deferred checks not kept:\n- "+fakeagent.DeferredCommand+
		": deferred (Deliver's defaults: not-run), and no conductor decides this delivery to keep it" {
		t.Errorf("the escalation says %q", got)
	}
	if got := w.str(`SELECT payload->>'actions' FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`,
		task); got != `["waive", "stop"]` {
		t.Errorf("offered %s, want waive and stop", got)
	}
	if n := w.readyEvents(task); n != 0 {
		t.Fatal("ready to merge with the deferred check never run")
	}
	owner, _, _ := w.people(task)
	if status, out := w.decide(owner, task, delivery.ActionWaive, "bench is run by hand on release"); status != 200 {
		t.Fatalf("waive: %d %v", status, out)
	}
	w.until("ready to merge once a person waived it", func() bool {
		w.sync()
		w.pump()
		return w.taskStatus(task) == "ready_to_merge"
	})
	if raised, asked := w.dischargeRaised(task); raised != 0 || asked != 1 {
		t.Errorf("%d/%d discharge escalations after the waiver, want 0 and 1", raised, asked)
	}
}
