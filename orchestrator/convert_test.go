package orchestrator_test

// The boot sweep (step 3b, PR C1): every live delivery no conductor
// decides, in flight from before every delivery had one, goes to a quiet
// conductor (delivery.ConvertInFlight) — wherever it is parked — except
// one its person handed to Deliver by accepting its defaults. Through the
// real workflow, API and fake lux and GitHub.

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// convert runs the boot sweep once, and returns its counts.
func (w *world) convert() delivery.Converted {
	w.t.Helper()
	n, err := delivery.ConvertInFlight(context.Background(), w.app)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// convertedQuietly says the sweep handed the task to a quiet conductor,
// recorded once.
func (w *world) convertedQuietly(task string) {
	w.t.Helper()
	if got := w.deciderState(task); got != "conductor/quiet/false" && got != "conductor/quiet/true" {
		w.t.Errorf("after the sweep: %s, want the conductor, quiet", got)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state->>'hadConductor')::boolean`, task); n != 1 {
		w.t.Errorf("converted without the conductor's fix bar (hadConductor)")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decider_changed'
		AND payload->>'to' = 'conductor' AND payload->>'voice' = 'quiet' AND payload->>'why' = $2`, task, delivery.WhyEveryDelivery); n != 1 {
		w.t.Errorf("%d decider changes recorded for the sweep, want 1", n)
	}
}

// decisionWakes counts the reasons to wake the task's conductor for a
// decision of its delivery's (keyed wf:decision:N): kind decision, or a
// conducted round's own (round).
func (w *world) decisionWakes(task string) int {
	return w.count(`SELECT count(*) FROM conductor_wakes c JOIN workflow_runs wf ON wf.task_id = c.task_id
		WHERE c.task_id = $1 AND c.key LIKE wf.id || ':decision:%'`, task)
}

// Parked awaiting its implementer: converted, the implementer goes on, and
// its end is the quiet conductor's first decision.
func TestTheSweepConvertsADeliveryAwaitingItsImplementer(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	w.deliver(task)
	w.advanced()
	impl := w.pendingPhaseRuns(task)
	if len(impl) != 1 {
		t.Fatalf("no implementer pending\n%s", w.describeRuns())
	}
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("sweep: %+v", n)
	}
	w.convertedQuietly(task)
	if n := w.decisionWakes(task); n != 0 {
		t.Errorf("%d decision wakes before the implementer ended", n)
	}
	w.finished(impl[0])
	w.advanced()
	if at := w.decisionAt(task); at != delivery.PointImplemented {
		t.Fatalf("at %q after the implementer, want the conductor's after implement\n%s", at, w.describeRuns())
	}
	if n := w.decisionWakes(task); n != 1 {
		t.Errorf("%d decision wakes after the implementer, want 1", n)
	}
}

// Parked on a review round: converted before the step claims it, the
// round's end is the quiet conductor's (a decision parked for it), and the
// loop has the conductor's fix bar.
func TestTheSweepConvertsADeliveryInAReviewRound(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	w.deliver(task)
	review := w.upTo(task, "review")
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("sweep: %+v", n)
	}
	w.convertedQuietly(task)
	w.finished(review)
	w.advanced()
	if at := w.decisionAt(task); at != delivery.PointReviewed {
		t.Fatalf("at %q after the round, want the conductor's after review\n%s", at, w.describeRuns())
	}
	if n := w.decisionWakes(task); n != 1 {
		t.Errorf("%d decision wakes after the round, want 1", n)
	}
}

// Parked on a fix: its end is the quiet conductor's decision after the fix,
// where Deliver's policy took none.
func TestTheSweepConvertsADeliveryInAFix(t *testing.T) {
	w := conductorWorld(t)
	scripted := w.lux.Decide
	var rounds int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "review" {
			rounds++
			if rounds == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: blocking", "FACTORY.md is wrong") + "```\n"}
			}
		}
		return scripted(spec)
	}
	task := w.task()
	w.deliver(task)
	fix := w.upTo(task, "fix")
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("sweep: %+v", n)
	}
	w.convertedQuietly(task)
	w.finished(fix)
	w.advanced()
	if at := w.decisionAt(task); at != delivery.PointFixed {
		t.Fatalf("at %q after the fix, want the conductor's after fix\n%s", at, w.describeRuns())
	}
	if n := w.decisionWakes(task); n != 1 {
		t.Errorf("%d decision wakes after the fix, want 1", n)
	}
}

// Parked on its pull request: converted, it goes on waiting there, with
// nothing to decide and nobody woken.
func TestTheSweepConvertsADeliveryWaitingOnItsPullRequest(t *testing.T) {
	w := conductorWorld(t)
	task := w.reviewing()
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("sweep: %+v", n)
	}
	w.convertedQuietly(task)
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest' AND status = 'waiting'`, task); n != 1 {
		t.Errorf("no longer waiting on its pull request\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != 0 {
		t.Errorf("%d reasons to wake the conductor at the pull request's wait", n)
	}
}

// Stopped at an escalation for a person: the conductor is told of it once,
// however often the sweep runs; the person still decides it.
func TestTheSweepTellsAnEscalationOnce(t *testing.T) {
	w := conductorWorld(t)
	task := w.escalated()
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("sweep: %+v", n)
	}
	if n := w.convert(); n.Converted != 0 {
		t.Errorf("a second sweep converted %+v", n)
	}
	w.convertedQuietly(task)
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'escalation'`, task); n != 1 {
		t.Errorf("the escalation told %d times, want once", n)
	}
	if s := w.taskStatus(task); s != "awaiting_input" {
		t.Errorf("task %s, want still awaiting its person", s)
	}
}

// Held by Deliver before its pull request (Ask me first): converted, it is
// still Deliver's hold — no conductor woken — and its owner's Open opens it.
func TestTheSweepKeepsDeliversHoldOpeningOnOpen(t *testing.T) {
	w := conductorWorld(t)
	ana := w.person("Ana")
	task := w.atTheHold(ana)
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("sweep: %+v", n)
	}
	w.convertedQuietly(task)
	for range 3 {
		w.pump()
	}
	if !w.heldBeforePR(task) || len(w.gh.Pulls()) != 0 {
		t.Fatalf("the hold did not hold after the sweep\n%s", w.describeRuns())
	}
	if n := w.decisionWakes(task); n != 0 {
		t.Errorf("%d decision wakes at Deliver's hold, want none", n)
	}
	if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
		t.Fatalf("the owner's Open: %d %v", status, out)
	}
	w.until("the pull request, on the owner's Open", func() bool { return len(w.gh.Pulls()) == 1 })
}

// Handed back to Deliver's policy while parked at the conductor's decision
// after implement, not yet taken (a hand-back from before voices): the
// sweep records the decision's wake, once, keyed as the decision's own, and
// the quiet conductor's wait for it starts.
func TestTheSweepWakesTheConductorForAnUntakenDecisionOnce(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'voice'
		|| '{"decider":"policy","handedBack":true}'::jsonb WHERE task_id = $1`, task)
	mustExec(t, w.owner, `DELETE FROM conductor_wakes WHERE task_id = $1`, task)
	awaited := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionAwaited)
	var first, second delivery.Converted
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); first, _ = delivery.ConvertInFlight(context.Background(), w.app) }()
	go func() { defer wg.Done(); second, _ = delivery.ConvertInFlight(context.Background(), w.app) }()
	wg.Wait()
	if first.Converted+second.Converted != 1 {
		t.Errorf("two sweeps converted %+v and %+v, want one in all", first, second)
	}
	w.convertedQuietly(task)
	if n := w.decisionWakes(task); n != 1 {
		t.Errorf("%d decision wakes, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionAwaited); n != awaited+1 {
		t.Errorf("%d decisions awaited, want %d", n, awaited+1)
	}
	w.advanced()
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'conductorDecision'
		AND state->'decision' ? 'decideBy' AND state->'decision'->>'point' = $2`, task, delivery.PointImplemented); n != 1 {
		t.Errorf("the quiet conductor's wait did not start\n%s", w.describeRuns())
	}
}

// The decision's wake already recorded (a hand-back after the conductor was
// woken for it): the sweep converts the delivery, records no second wake,
// and awaits the decision no second time.
func TestTheSweepAwaitsADecisionWhoseWakeIsRecordedNoMore(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'voice'
		|| '{"decider":"policy","handedBack":true}'::jsonb WHERE task_id = $1`, task)
	mustExec(t, w.owner, `DELETE FROM conductor_wakes WHERE task_id = $1`, task)
	mustExec(t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
		SELECT 'cwk_recorded', $1, $2, 'decision', wf.id || ':decision:' || COALESCE(wf.state->>'decisions', '0'), 'recorded'
		FROM workflow_runs wf WHERE wf.task_id = $2`, w.org, task)
	if n := w.decisionWakes(task); n != 1 {
		t.Fatalf("%d decision wakes before the sweep, want the one recorded", n)
	}
	awaited := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionAwaited)
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("sweep: %+v", n)
	}
	w.convertedQuietly(task)
	if n := w.decisionWakes(task); n != 1 {
		t.Errorf("%d decision wakes, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionAwaited); n != awaited {
		t.Errorf("%d decisions awaited, want %d: its wake was recorded already", n, awaited)
	}
}

// A delivery whose person accepted Deliver's defaults is left Deliver's.
func TestTheSweepSkipsADeliveryWhosePersonAcceptedTheDefaults(t *testing.T) {
	w := conducting(t)
	task, _ := w.acceptedDelivery()
	n := w.convert()
	if n.Converted != 0 || n.Accepted != 1 {
		t.Errorf("sweep: %+v, want one skipped as accepted", n)
	}
	if got := w.deciderState(task); got[:7] != "policy/" {
		t.Errorf("an accepted delivery converted: %s", got)
	}
}

// Several deliveries, two sweeps at once (replicas booting): each is
// converted once, recorded once.
func TestTwoSweepsAtOnceConvertEachDeliveryOnce(t *testing.T) {
	w := conductorWorld(t)
	var tasks []string
	for range 4 {
		task := w.task()
		w.deliver(task)
		tasks = append(tasks, task)
	}
	w.advanced()
	var a, b delivery.Converted
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a, errA = delivery.ConvertInFlight(context.Background(), w.app) }()
	go func() { defer wg.Done(); b, errB = delivery.ConvertInFlight(context.Background(), w.app) }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("sweeps: %v, %v", errA, errB)
	}
	if a.Converted+b.Converted != len(tasks) {
		t.Errorf("converted %d in all, want %d", a.Converted+b.Converted, len(tasks))
	}
	for _, task := range tasks {
		w.convertedQuietly(task)
	}
}

// A delivery whose branch the conductor's publish moves is skipped, for the
// next sweep; once it settles, the next sweep converts it.
func TestTheSweepSkipsADeliveryWhilePublishMovesItsBranch(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	w.deliver(task)
	w.advanced()
	impl := w.pendingPhaseRuns(task)[0]
	mustExec(t, w.owner, `INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, attempt, status)
		VALUES ('cpb_mv', $1, $2, $3, 'req_mv', 1, 'moving')`, w.org, task, impl)
	if n := w.convert(); n.Converted != 0 || n.Moving != 1 {
		t.Fatalf("sweep while moving: %+v", n)
	}
	if d := w.decider(task); d != delivery.DeciderPolicy {
		t.Errorf("converted while moving: %s", d)
	}
	mustExec(t, w.owner, `UPDATE conductor_publishes SET status = 'published' WHERE id = 'cpb_mv'`)
	if n := w.convert(); n.Converted != 1 {
		t.Fatalf("the next sweep: %+v", n)
	}
	w.convertedQuietly(task)
}

// A worker mid-step on the row (its lease, no row lock): the sweep converts
// it meanwhile, and the step's transition keeps the new decider.
func TestAStepRunningAcrossTheSweepKeepsTheNewDecider(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	var once sync.Once
	armed := w.gated(func(step, next string) {
		once.Do(func() {
			if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				_, err := delivery.ConvertToQuietTx(context.Background(), tx, w.org, task)
				return err
			}); err != nil {
				t.Errorf("sweep mid-step: %v", err)
			}
		})
	})
	armed.Store(true)
	w.deliver(task)
	w.advanced()
	armed.Store(false)
	w.convertedQuietly(task)
	if n := w.convert(); n.Converted != 0 {
		t.Errorf("a converted delivery converted again: %+v", n)
	}
}
