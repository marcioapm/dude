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
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// waitingFor gives the world's deliveries wait as a quiet conductor's
// time to decide (Store.DecisionWait), in place of QuietDecisionWait.
func (w *world) waitingFor(wait time.Duration) {
	w.runtime.Register(w.deliveryWaiting(wait))
}

// deliveryWaiting is the delivery workflow with wait as a quiet
// conductor's time to decide, for a test to hook before registering.
func (w *world) deliveryWaiting(wait time.Duration) *workflow.Definition {
	store := &delivery.Store{DB: w.app, DecisionWait: wait, SyncPullRequest: w.prs.Sync}
	return delivery.Workflow(store, forge.Resolver{DB: w.app})
}

// lapse moves the task's quiet decision past its deadline (by the
// database's clock), and wakes its step now.
func (w *world) lapse(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{decision,decideBy}', to_jsonb(now() - interval '1 second')),
		wake_at = now() WHERE task_id = $1 AND step = 'conductorDecision'`, task)
}

// quietUnavailable starts the task quietly and waits for it to stop for a
// person at conductor_unavailable.
func (w *world) quietUnavailable(task string) {
	w.t.Helper()
	w.startQuiet(task)
	w.until("conductor_unavailable", func() bool { return w.escalationReason(task) == delivery.ReasonConductorUnavailable })
}

// unavailable is how many times the task stopped at conductor_unavailable.
func (w *world) unavailable(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'reason' = $2`, task, delivery.ReasonConductorUnavailable)
}

// defaultsState is the delivery's voice/defaultsAccepted/defaulted.
func (w *world) defaultsState(task string) string {
	return w.str(`SELECT COALESCE(state->>'voice', '') || '/' || COALESCE(state->>'defaultsAccepted', 'false') || '/'
		|| COALESCE(state->>'defaulted', '0') FROM workflow_runs WHERE task_id = $1`, task)
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
	w.quietUnavailable(task)
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
// back to Deliver is the policy's again). Defaults that clear reach the
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
		w.quietUnavailable(task)
		if patch != "" {
			setPolicy(w, task, patch)
		}
		if status, out := w.decide(owner, task, "accept", ""); status != 200 {
			t.Fatalf("accept: %d %v", status, out)
		}
		return task
	}
	t.Run("to the pull request", func(t *testing.T) {
		w := conducting(t)
		task := accepted(t, w, "")
		w.until("the pull request, by the defaults", func() bool { return len(w.gh.Pulls()) == 1 })
		if n := w.unavailable(task); n != 1 {
			t.Errorf("%d conductor_unavailable escalations, want 1 after accept", n)
		}
		// The decision accepted is said as the person's, not a deadline.
		if n := w.defaulted(task, delivery.DefaultedAccepted); n != 1 {
			t.Errorf("%d decisions defaulted as accepted, want 1", n)
		}
		if n := w.defaultedNotices(task, delivery.DefaultedAccepted); n != 1 {
			t.Errorf("%d Chat notices of the accepted defaults, want 1", n)
		}
		if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
			AND payload->>'about' = 'decision_defaulted' AND payload->>'reason' = $2`, task, delivery.DefaultedAccepted); n != 1 {
			t.Errorf("%d notices with reason accepted, want 1", n)
		}
	})
	t.Run("a review that never clears", func(t *testing.T) {
		w := conducting(t)
		w.onModel("reviewer", fakeagent.StuckModel)
		task := accepted(t, w, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9,"maxStillRounds":0}`)
		w.until("the policy's exit", func() bool { return w.escalationReason(task) == "stuck" })
		if n := w.unavailable(task); n != 1 {
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
	// The test moves each deadline itself (lapse): a default is one decision.
	parked := func() bool { return w.decisionAt(task) != "" && w.decisionAt(task) != delivery.PointBeforePR }
	defaults := func(n int) {
		t.Helper()
		for want := w.defaulted(task, "") + n; w.defaulted(task, "") < want; {
			w.until("a quiet decision", parked)
			w.lapse(task)
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
	def := w.deliveryWaiting(time.Hour)
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
	w.lapse(task)
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

// A quiet decision made chatty by a person's message, then handed back
// quietly once its old deadline is past, waits for the conductor afresh:
// the chatty step forgot the quiet wait, so the defaults do not take it at
// once on the old deadline.
func TestAQuietDecisionHandedBackAfterChattyWaitsAfresh(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	task := w.task()
	w.quietAfterImplement(task)
	if status, out := w.chat(task, "let me talk this one through"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	// The quiet wait's wake comes, and finds the decision chatty; its old
	// deadline is long past by the time it is handed back.
	w.lapse(task)
	w.until("the chatty decision parked with no wake", func() bool {
		return w.str(`SELECT COALESCE(wake_at::text, '') || '/' || status FROM workflow_runs WHERE task_id = $1`, task) == "/waiting"
	})
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = CASE WHEN state->'decision' ? 'decideBy'
		THEN jsonb_set(state, '{decision,decideBy}', to_jsonb(now() - interval '1 hour')) ELSE state END WHERE task_id = $1`, task)
	if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
		t.Fatalf("hand back quietly: %d %v", status, out)
	}
	w.until("the quiet wait, restarted", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'waiting'
			AND (state->'decision'->>'decideBy')::timestamptz > now() + interval '30 minutes'`, task) == 1 || w.defaulted(task, "") > 0
	})
	for range 3 {
		w.pump()
	}
	if n := w.defaulted(task, ""); n != 0 {
		t.Fatalf("%d defaults on the quiet hand-back, want a fresh wait", n)
	}
	if at := w.decisionAt(task); at != delivery.PointImplemented {
		t.Errorf("decision at %q, want still after the implementer", at)
	}
}

// The defaults' notice is said once per decision: a transition that lost
// its lease after the step said it (another poller's now) is replayed, and
// the replay says nothing more.
func TestTheDefaultsAreSaidOnceAcrossALostTransition(t *testing.T) {
	w := conducting(t)
	def := w.deliveryWaiting(time.Hour)
	var once sync.Once
	var armed atomic.Bool
	task := w.task()
	def.BeforeCommit = func(_, step, next string) {
		if step == "conductorDecision" && next == "review" && armed.Load() {
			once.Do(func() {
				mustExec(t, w.owner, `UPDATE workflow_runs SET locked_until = now() - interval '1 second' WHERE task_id = $1`, task)
			})
		}
	}
	w.runtime.Register(def)
	w.quietAfterImplement(task)
	armed.Store(true)
	w.lapse(task)
	w.until("the review, by the defaults", func() bool { return w.phaseRuns(task, "review") > 0 })
	for range 3 {
		w.pump()
	}
	if n := w.defaulted(task, ""); n != 1 {
		t.Errorf("%d decision_defaulted events across the lost transition, want 1", n)
	}
	if n := w.defaultedNotices(task, delivery.DefaultedDeadline); n != 1 {
		t.Errorf("%d Chat notices across the lost transition, want 1", n)
	}
}

// A conductor Run that failed before the wait began is not this decision's
// failure: the decision waits out its time, not defaulted as run_failed.
func TestAConductorRunThatFailedBeforeTheWaitDoesNotDefault(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	task := w.task()
	w.quietAfterImplement(task)
	w.until("the conductor woken", func() bool { id, _, _ := w.conductor(task); return id != "" })
	id, _, _ := w.conductor(task)
	mustExec(t, w.owner, `UPDATE runs SET status = 'failed', ended_at = (SELECT (state->'decision'->>'since')::timestamptz
		FROM workflow_runs WHERE task_id = $2) - interval '1 minute', error = 'an earlier failure' WHERE id = $1`, id, task)
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, task)
	w.until("the decision looked at again", func() bool {
		return w.str(`SELECT status || '/' || COALESCE((wake_at > now() + interval '30 minutes')::text, '') FROM workflow_runs
			WHERE task_id = $1`, task) == "waiting/true" || w.defaulted(task, "") > 0
	})
	if n := w.defaulted(task, ""); n != 0 {
		t.Fatalf("%d defaults for a Run that failed before the wait", n)
	}
}

// accept takes the decision by Deliver's defaults at once, however long
// the conductor's wait: not an hour later.
func TestAcceptingTakesTheDecisionAtOnce(t *testing.T) {
	w := conducting(t)
	w.waitingFor(100 * time.Millisecond)
	task := w.task()
	owner, _, _ := w.people(task)
	w.quietUnavailable(task)
	before := w.defaulted(task, "")
	w.waitingFor(time.Hour)
	if status, out := w.decide(owner, task, "accept", ""); status != 200 {
		t.Fatalf("accept: %d %v", status, out)
	}
	w.until("the accepted decision, by the defaults", func() bool { return w.defaulted(task, "") > before })
}

// A person who takes a delivery over in Chat after accepting Deliver's
// defaults wants the conductor's judgement back: the chatty takeover
// gives up the accepted defaults (SetDeciderTx), and the review rounds are
// the conductor's again.
func TestAChatTakeoverGivesUpTheAcceptedDefaults(t *testing.T) {
	// acceptedThenChat runs the task quietly to conductor_unavailable with
	// a reviewer that never clears, accepts the defaults, and once the
	// delivery has taken them writes in the task's Chat.
	acceptedThenChat := func(t *testing.T, w *world) (task, owner string) {
		t.Helper()
		w.waitingFor(100 * time.Millisecond)
		w.onModel("reviewer", fakeagent.StuckModel)
		task = w.task()
		owner, _, _ = w.people(task)
		w.quietUnavailable(task)
		if status, out := w.decide(owner, task, "accept", ""); status != 200 {
			t.Fatalf("accept: %d %v", status, out)
		}
		w.until("the defaults accepted", func() bool {
			return w.str(`SELECT COALESCE(state->>'defaultsAccepted', '') FROM workflow_runs WHERE task_id = $1`, task) == "true"
		})
		if status, out := w.chat(task, "let me take this one over"); status != 200 && status != 201 {
			t.Fatalf("chat: %d %v", status, out)
		}
		if got := w.defaultsState(task); got != "chatty/false/0" {
			t.Errorf("voice/defaultsAccepted/defaulted %s after the Chat takeover, want chatty/false/0", got)
		}
		return task, owner
	}
	// reviewRound waits for the delivery to park for the conductor after a
	// review, or to leave the loop by the policy's exit (escalated, ok
	// false). The takeover may land mid-fix: the conductor takes the fix's
	// decision (next: the review), as a conductor's turn would.
	reviewRound := func(t *testing.T, w *world, task string) bool {
		t.Helper()
		exited := func() bool { r := w.escalationReason(task); return r != "" && r != delivery.ReasonConductorUnavailable }
		parked := func() bool {
			at := w.decisionAt(task)
			return at == delivery.PointReviewed || at == delivery.PointFixed || exited()
		}
		w.until("a round parked for the conductor, or the policy's exit", parked)
		if w.decisionAt(task) == delivery.PointFixed {
			w.must(task, "decide", `{"action":"next"}`)
			w.until("the review round, or the policy's exit", func() bool {
				return w.decisionAt(task) == delivery.PointReviewed || exited()
			})
		}
		return !exited() && w.decisionAt(task) == delivery.PointReviewed
	}
	t.Run("the review rounds park for the conductor", func(t *testing.T) {
		w := conducting(t)
		task, _ := acceptedThenChat(t, w)
		// The policy's loop bound tight: a review round the policy still
		// decided would exit stuck at its first still round.
		setPolicy(w, task, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9,"maxStillRounds":0}`)
		if !reviewRound(t, w, task) {
			t.Fatalf("the review loop exited %q by the policy after a Chat takeover, want it parked for the conductor at %s",
				w.escalationReason(task), delivery.PointReviewed)
		}
	})
	t.Run("handed back quietly, three defaults stop for a person again", func(t *testing.T) {
		w := conducting(t)
		task, owner := acceptedThenChat(t, w)
		if !reviewRound(t, w, task) {
			t.Fatalf("the review loop exited %q after a Chat takeover, want it parked for the conductor", w.escalationReason(task))
		}
		if status, out := w.callAs(owner, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy"}); status != 200 {
			t.Fatalf("let it finish quietly: %d %v", status, out)
		}
		if got := w.defaultsState(task); got != "quiet/false/0" {
			t.Errorf("voice/defaultsAccepted/defaulted %s handed back quietly, want quiet/false/0", got)
		}
		before := w.defaulted(task, "")
		w.until("conductor_unavailable again, or another exit", func() bool {
			r := w.escalationReason(task)
			return w.unavailable(task) == 2 || r != "" && r != delivery.ReasonConductorUnavailable
		})
		if n := w.unavailable(task); n != 2 {
			t.Fatalf("%d conductor_unavailable escalations (now %q), want a second after the quiet hand-back", n, w.escalationReason(task))
		}
		if n := w.defaulted(task, "") - before; n != delivery.MaxDefaulted {
			t.Errorf("%d defaults after the quiet hand-back before the stop, want %d", n, delivery.MaxDefaulted)
		}
	})
}

// A Chat takeover that lands while the step taking the accepted defaults
// runs is not undone by that step's transition: the step read the
// delivery quiet and accepted, and its defaultsAccepted and defaulted are
// the takeover's (false, 0) once it commits.
func TestAStepDoesNotUndoAChatTakeoverThatLandedWhileItRan(t *testing.T) {
	w := conducting(t)
	def := w.deliveryWaiting(100 * time.Millisecond)
	var once sync.Once
	var armed atomic.Bool
	status, during := 0, ""
	task := w.task()
	def.BeforeCommit = func(_, step, next string) {
		if step == "conductorDecision" && next != "conductorDecision" && armed.Load() {
			once.Do(func() {
				status, _ = w.chat(task, "let me take this one over")
				during = w.defaultsState(task)
			})
		}
	}
	w.runtime.Register(def)
	owner, _, _ := w.people(task)
	w.quietUnavailable(task)
	armed.Store(true)
	if code, out := w.decide(owner, task, "accept", ""); code != 200 {
		t.Fatalf("accept: %d %v", code, out)
	}
	w.until("the accepted decision, by the defaults", func() bool { return w.defaulted(task, delivery.DefaultedAccepted) > 0 })
	if status != 200 && status != 201 {
		t.Fatalf("the Chat takeover never raced the defaults' step (chat answered %d)", status)
	}
	if during != "chatty/false/0" {
		t.Fatalf("voice/defaultsAccepted/defaulted %s right after the Chat takeover, want chatty/false/0", during)
	}
	if got := w.defaultsState(task); got != "chatty/false/0" {
		t.Errorf("voice/defaultsAccepted/defaulted %s once the step committed, want chatty/false/0", got)
	}
}
