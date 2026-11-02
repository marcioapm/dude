package orchestrator_test

// A quiet conductor never stalls its delivery (step 3b, PR B): a decision
// it does not take within its wait is taken by Deliver's defaults, with a
// notice in Chat; with no model it is taken at once; three in a row stop
// for a person (conductor_unavailable). Through the real workflow, syncer,
// API and the fake lux and GitHub; the wait test-sized.

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// waitingFor gives the world's deliveries wait as a quiet conductor's
// time to decide (Store.DecisionWait), in place of QuietDecisionWait.
func (w *world) waitingFor(wait time.Duration) {
	store := &delivery.Store{DB: w.app, DecisionWait: wait, SyncPullRequest: w.prs.Sync}
	w.runtime.Register(delivery.Workflow(store, forge.Resolver{DB: w.app}))
}

// defaulted is how many decisions the task's delivery took by Deliver's
// defaults, for reason ("" any).
func (w *world) defaulted(task, reason string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND ($3 = '' OR payload->>'reason' = $3)`, task, delivery.EvDecisionDefaulted, reason)
}

// defaultedNotices is how many Chat notices said a decision was taken by
// Deliver's defaults, with the line said.
func (w *world) defaultedNotices(task, reason string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
		AND payload->>'about' = 'decision_defaulted' AND payload->>'text' = $2`, task, delivery.DefaultedLine(reason))
}

// quietAfterImplement starts the task quietly and waits for its first
// decision after the implementer: parked for the quiet conductor.
func (w *world) quietAfterImplement(task string) {
	w.t.Helper()
	w.startQuiet(task)
	w.until("the decision after the implementer", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
}

// A quiet conductor that does not decide (the scripted one answers a wake
// with prose) has its decision taken by Deliver's defaults once its wait
// is over: the review starts, Chat says so, and the ledger records it.
func TestAQuietDecisionNobodyTakesGoesOnByTheDefaults(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Second)
	task := w.task()
	w.quietAfterImplement(task)
	if d := w.str(`SELECT COALESCE(state->'decision'->>'decideBy', '') FROM workflow_runs WHERE task_id = $1`, task); d == "" {
		t.Fatalf("a quiet decision parked with no deadline")
	}
	if wake := w.str(`SELECT COALESCE(wake_at::text, '') FROM workflow_runs WHERE task_id = $1`, task); wake == "" {
		t.Fatalf("a quiet decision parked with no wake")
	}
	w.until("the review, by the defaults", func() bool { return w.phaseRuns(task, "review") > 0 })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = $3
		AND payload->>'reason' = $4 AND payload->>'policy' = 'review' AND (payload->>'count')::int = 1`,
		task, delivery.EvDecisionDefaulted, delivery.PointImplemented, delivery.DefaultedDeadline); n != 1 {
		t.Errorf("%d decision_defaulted events at after_implement, want 1", n)
	}
	if n := w.defaultedNotices(task, delivery.DefaultedDeadline); n != 1 {
		t.Errorf("%d Chat notices of the defaults, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionTaken); n != 0 {
		t.Errorf("%d conductor decisions recorded for a default", n)
	}
}

// Talk it through waits for its conductor and person, with no deadline:
// a chatty decision nobody takes stays parked.
func TestAChattyDecisionNobodyTakesStillWaits(t *testing.T) {
	w := conducting(t)
	w.waitingFor(100 * time.Millisecond)
	task := w.task()
	w.talk(task)
	w.carriedOut(task, "start_phase", `{"phase":"implement"}`)
	w.until("the decision after the implementer", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	if wake := w.str(`SELECT COALESCE(wake_at::text, '') FROM workflow_runs WHERE task_id = $1`, task); wake != "" {
		t.Errorf("a chatty decision parked with a wake at %s", wake)
	}
	time.Sleep(500 * time.Millisecond)
	for range 3 {
		w.pump()
	}
	if at := w.decisionAt(task); at != delivery.PointImplemented || w.phaseRuns(task, "review") != 0 || w.defaulted(task, "") != 0 {
		t.Fatalf("a chatty decision went on: at %q, %d reviews, %d defaulted", at, w.phaseRuns(task, "review"), w.defaulted(task, ""))
	}
}

// No conductor tier: a quiet delivery takes Deliver's defaults at once,
// starts no conductor Run, and so has none failing on every sweep.
func TestAQuietConductorWithNoModelTakesTheDefaultsAtOnce(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models - 'conductor' WHERE id = $1`, w.project)
	task := w.task()
	w.startQuiet(task)
	w.until("the review, by the defaults", func() bool { return w.phaseRuns(task, "review") > 0 })
	// Other news for the conductor (a stall, say) starts none either: it
	// waits until a model is set, rather than fail a Run every sweep.
	mustExec(t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_nomodel', $1, $2, 'stalled', 'stalled:nomodel', 'A reviewer stalled.', now() - interval '1 hour')`, w.org, task)
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 0 {
		t.Errorf("%d conductor Runs for a conductor with no model", n)
	}
	if n := w.defaulted(task, delivery.DefaultedNoModel); n < 1 {
		t.Errorf("no decision defaulted for no_model")
	}
	if n := w.defaultedNotices(task, delivery.DefaultedNoModel); n < 1 {
		t.Errorf("no Chat notice of the defaults")
	}
}

// A conductor whose Run failed after the decision was awaited cannot take
// it: the defaults are taken then, not at the end of the wait.
func TestAQuietConductorWhoseRunFailedTakesTheDefaults(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	task := w.task()
	w.quietAfterImplement(task)
	w.until("the conductor woken", func() bool { id, _, _ := w.conductor(task); return id != "" })
	id, _, _ := w.conductor(task)
	mustExec(t, w.owner, `UPDATE runs SET status = 'failed', ended_at = now(), error = 'the provider failed' WHERE id = $1`, id)
	// The deadline is an hour off; the next look at the decision takes it.
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, task)
	w.until("the review, by the defaults", func() bool { return w.phaseRuns(task, "review") > 0 })
	if n := w.defaulted(task, delivery.DefaultedRunFailed); n != 1 {
		t.Errorf("%d decisions defaulted for run_failed, want 1", n)
	}
}

// Three decisions in a row left to the defaults stop for a person
// (conductor_unavailable), who is woken on the banner; their retry waits
// for the conductor again with the count reset.
func TestThreeDefaultsInARowStopForAPerson(t *testing.T) {
	w := conducting(t)
	w.waitingFor(200 * time.Millisecond)
	task := w.task()
	owner, _, _ := w.people(task)
	w.startQuiet(task)
	w.until("conductor_unavailable", func() bool { return w.escalationReason(task) == delivery.ReasonConductorUnavailable })
	if n := w.defaulted(task, ""); n != delivery.MaxDefaulted {
		t.Errorf("%d defaults before the escalation, want %d", n, delivery.MaxDefaulted)
	}
	if s := w.taskStatus(task); s != "awaiting_input" {
		t.Errorf("task %s at conductor_unavailable, want awaiting_input", s)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'reason' = $2 AND payload->'actions' = '["retry","accept","stop"]'::jsonb`, task, delivery.ReasonConductorUnavailable); n != 1 {
		t.Errorf("%d banners offering retry, accept, stop", n)
	}
	at := w.str(`SELECT state->'decision'->>'point' FROM workflow_runs WHERE task_id = $1`, task)
	before := w.defaulted(task, "")
	if status, out := w.decide(owner, task, "retry", ""); status != 200 {
		t.Fatalf("retry: %d %v", status, out)
	}
	w.until("waiting on the conductor again", func() bool { return w.decisionAt(task) == at })
	if n := w.str(`SELECT COALESCE(state->>'defaulted', '0') FROM workflow_runs WHERE task_id = $1`, task); n != "0" {
		t.Errorf("defaulted %s after a retry, want 0", n)
	}
	// The wait is the conductor's again: past it, the defaults go on.
	w.until("a default after the retry", func() bool { return w.defaulted(task, "") > before })
}

// accept on conductor_unavailable: Deliver's defaults finish the
// delivery, Deliver's bounds included (CONTRIBUTING: a delivery handed
// back to Deliver is the policy's again). Renamed from
// TestAcceptingTheDefaultsGoesOnWithoutABound: it went on with no bound,
// so a review that never clears ran forever. Defaults that clear reach the
// pull request with no second conductor_unavailable; a review that never
// clears stops at the policy's own exit (stuck, at one fix per finding),
// not in an endless review and fix loop.
func TestAcceptingTheDefaultsFinishesByDeliversBounds(t *testing.T) {
	// accepted runs the task quietly to conductor_unavailable, patches its
	// policy (patch, "" none), and accepts the defaults.
	accepted := func(t *testing.T, w *world, patch string) string {
		t.Helper()
		w.waitingFor(100 * time.Millisecond)
		task := w.task()
		owner, _, _ := w.people(task)
		w.startQuiet(task)
		w.until("conductor_unavailable", func() bool { return w.escalationReason(task) == delivery.ReasonConductorUnavailable })
		if patch != "" {
			setPolicy(w, task, patch)
		}
		if status, out := w.decide(owner, task, "accept", ""); status != 200 {
			t.Fatalf("accept: %d %v", status, out)
		}
		return task
	}
	unavailable := func(w *world, task string) int {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
			AND payload->>'reason' = $2`, task, delivery.ReasonConductorUnavailable)
	}
	t.Run("to the pull request", func(t *testing.T) {
		w := conducting(t)
		task := accepted(t, w, "")
		w.until("the pull request, by the defaults", func() bool { return len(w.gh.Pulls()) == 1 })
		if n := unavailable(w, task); n != 1 {
			t.Errorf("%d conductor_unavailable escalations, want 1 after accept", n)
		}
	})
	t.Run("a review that never clears", func(t *testing.T) {
		w := conducting(t)
		w.onModel("reviewer", fakeagent.StuckModel)
		task := accepted(t, w, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9,"maxStillRounds":0}`)
		w.until("the policy's exit", func() bool { return w.escalationReason(task) == "stuck" })
		if n := unavailable(w, task); n != 1 {
			t.Errorf("%d conductor_unavailable escalations, want 1 after accept", n)
		}
		if it := w.iteration(task); it > 9 {
			t.Errorf("iteration %d past the policy's bound", it)
		}
	})
}

// A decision the conductor takes resets the count: two defaults, a
// decision, two defaults do not stop for a person.
func TestAConductorsDecisionResetsTheDefaults(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	task := w.task()
	w.startQuiet(task)
	// The test moves each deadline itself: a default is one decision.
	lapse := func() {
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{decision,decideBy}', to_jsonb(now() - interval '1 second')),
			wake_at = now() WHERE task_id = $1 AND step = 'conductorDecision'`, task)
	}
	parked := func() bool { return w.decisionAt(task) != "" && w.decisionAt(task) != delivery.PointBeforePR }
	defaults := func(n int) {
		t.Helper()
		for want := w.defaulted(task, "") + n; w.defaulted(task, "") < want; {
			w.until("a quiet decision", parked)
			lapse()
			got := w.defaulted(task, "")
			w.until("the default", func() bool { return w.defaulted(task, "") > got })
		}
	}
	defaults(2)
	w.until("a quiet decision", parked)
	if at := w.decisionAt(task); at == delivery.PointReviewed {
		w.finishReview(task)
	} else {
		w.must(task, "decide", `{"action":"next"}`)
	}
	w.until("the decision carried out", func() bool {
		return w.str(`SELECT COALESCE(state->>'defaulted', '0') FROM workflow_runs WHERE task_id = $1`, task) == "0"
	})
	defaults(2)
	if r := w.escalationReason(task); r == delivery.ReasonConductorUnavailable {
		t.Fatalf("escalated after two defaults, a decision and two defaults")
	}
}

// The conductor's decide at the deadline's moment: exactly one decision is
// taken. Its decide lands while the step taking the defaults runs, before
// that step commits: it is refused (the delivery is not waiting, its row
// claimed), and the defaults alone go on. A decide that commits first is
// carried out by the step instead (TestAConductorsDecisionResetsTheDefaults).
func TestTheConductorRacingTheDeadlineTakesOneDecision(t *testing.T) {
	w := conducting(t)
	store := &delivery.Store{DB: w.app, DecisionWait: time.Hour, SyncPullRequest: w.prs.Sync}
	def := delivery.Workflow(store, forge.Resolver{DB: w.app})
	var once sync.Once
	var armed atomic.Bool
	status := 0
	task := w.task()
	def.BeforeCommit = func(_, step, next string) {
		if step == "conductorDecision" && next == "review" && armed.Load() {
			once.Do(func() { status, _ = w.as(task, "decide", `{"action":"next"}`) })
		}
	}
	w.runtime.Register(def)
	w.quietAfterImplement(task)
	w.until("the conductor woken", func() bool { id, _, _ := w.conductor(task); return id != "" })
	w.conductorSpecOf(task)
	armed.Store(true)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{decision,decideBy}', to_jsonb(now() - interval '1 second')),
		wake_at = now() WHERE task_id = $1`, task)
	w.until("the review", func() bool { return w.phaseRuns(task, "review") > 0 })
	for range 3 {
		w.pump()
	}
	if status == 0 {
		t.Fatalf("the conductor's decide never raced the defaults")
	}
	taken := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = $3`,
		task, delivery.EvDecisionTaken, delivery.PointImplemented)
	defaulted := w.defaulted(task, "")
	if taken+defaulted != 1 || status == 200 {
		t.Fatalf("%d conductor decisions and %d defaults at one decision (decide answered %d)", taken, defaulted, status)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task); n != 1 {
		t.Errorf("%d reviews started at one decision", n)
	}
}
