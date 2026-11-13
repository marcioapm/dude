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

// answerDeferred answers the open second ask as the conductor.
func (w *world) answerDeferred(task, verdict, reason string) (int, map[string]any) {
	w.t.Helper()
	row := w.rows(`SELECT run_id, payload->>'turn' FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'discharge'
		ORDER BY cursor DESC LIMIT 1`, task, delivery.EvRunChecksAsked)
	if len(row) != 1 {
		w.t.Fatalf("%d second asks", len(row))
	}
	return w.as(task, "decide_checks", fmt.Sprintf(`{"runId":%q,"turn":%s,"checks":[{"command":%q,"verdict":%q,"reason":%q}],"missing":{"verdict":"none","reason":"not asked"}}`,
		row[0][0], row[0][1], fakeagent.DeferredCommand, verdict, reason))
}

// The trial: the deferred check holds the pull request in review, CI green
// and every other item holding, and the conductor is woken once to keep
// it. Its answer at the second ask is backed or reject: deferred again is
// refused. Backed, citing the completed run it dispatched, the task is
// ready to merge.
func TestADeferredCheckHoldsReadyUntilTheConductorKeepsIt(t *testing.T) {
	w, task := deferredWorld(t)
	w.until("the conductor asked to keep the deferred check", func() bool {
		w.sync()
		w.pump()
		return len(w.deferredWakes(task)) == 1
	})
	for range 5 {
		w.sync()
		w.pump()
	}
	if s := w.taskStatus(task); s == "ready_to_merge" {
		t.Fatal("ready to merge with a deferred check never run")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, task); n != 0 {
		t.Fatalf("%d ready_to_merge events", n)
	}
	ok, detail := w.storedDeferred(task)
	if ok || !strings.Contains(detail, fmt.Sprintf("%q", fakeagent.DeferredCommand)) || !strings.Contains(detail, "Deliver's defaults: not-run") ||
		!strings.Contains(detail, "the conductor is asked to keep it at this head") {
		t.Fatalf("the deferred item: %v %q", ok, detail)
	}
	wakes := w.deferredWakes(task)
	if len(wakes) != 1 || !strings.Contains(wakes[0], fakeagent.KeepDeferred) || !strings.Contains(wakes[0], fakeagent.DeferredCommand) {
		t.Fatalf("second asks' wakes: %q", wakes)
	}
	// Deferred is no verdict now: the later phase has come.
	if status, out := w.answerDeferred(task, delivery.VerdictDeferred, "later"); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "is not a verdict for") {
		t.Fatalf("deferred again: %d %v", status, out)
	}
	if status, out := w.answerDeferred(task, delivery.VerdictBacked, "dispatch wd_1 (bench.yml on the branch) completed: success"); status != 200 {
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
	if got := w.str(`SELECT COALESCE(state->'escalation'->>'actions', '') FROM workflow_runs WHERE task_id = $1 AND workflow_type = 'task.delivery'
		ORDER BY created_at DESC LIMIT 1`, task); strings.Contains(got, "retry") || !strings.Contains(got, delivery.ActionWaive) {
		t.Errorf("offered %s, want waive and stop", got)
	}
	if ok, detail := w.storedDeferred(task); ok || !strings.Contains(detail, "the conductor rejected it (bench.yml was not dispatched") {
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
	for range 3 {
		w.sync()
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
		w.sync()
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

// With no conductor deciding (Deliver's rules), deferred is never a
// verdict: a check not run is rejected for a person, so nothing is
// deferred, no second ask is made, and the gate's deferred item holds.
func TestWithNoConductorNothingIsDeferred(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	wi := w.reviewing()
	w.until("the gate stored", func() bool {
		w.sync()
		w.pump()
		_, detail := w.storedDeferred(wi)
		return detail != ""
	})
	if ok, detail := w.storedDeferred(wi); !ok || detail != "no check deferred" {
		t.Errorf("the deferred item: %v %q", ok, detail)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'discharge'`, wi, delivery.EvRunChecksAsked); n != 0 {
		t.Errorf("%d second asks with no conductor", n)
	}
}
