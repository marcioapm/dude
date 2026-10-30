package orchestrator_test

// The conductor's wait_for (delivery/waits.go) through the orchestrator's
// real code: the tool called with the conductor's token, the pull
// request's checks read by the PR syncer from a webhook, and the sweep's
// evaluator and wakes delivering the note.

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// waitNotes are the conductor's wake notes that carry a wait's reason.
func (w *world) waitNotes(task string) []string {
	w.t.Helper()
	var out []string
	for _, n := range w.woken(task) {
		if strings.Contains(n, "Your wait cwt_") {
			out = append(out, n)
		}
	}
	return out
}

// reviewWaitedOn is a task in review on its pull request, its checks
// pending, whose decisions a person handed to the conductor: its
// conductor, between turns.
func (w *world) reviewWaitedOn() (task, conductor string) {
	w.t.Helper()
	w.gh.SetChecks("pending")
	task = w.reviewing()
	w.sync()
	if status, out := w.chat(task, "mine now"); status != 201 || out["decider"] != "conductor" {
		w.t.Fatalf("take-over: %d %v", status, out)
	}
	conductor, _, _ = w.conductor(task)
	w.until("the conductor's turn", func() bool { return len(w.said(conductor)) > 0 })
	return task, conductor
}

// statusWebhook is GitHub's status delivery for a commit of acme/target.
func statusWebhook(sha string) map[string]any {
	return map[string]any{"sha": sha, "state": "success", "repository": map[string]any{"full_name": "acme/target"}}
}

// The conductor waits on its pull request's checks and ends its turn; a
// status webhook makes the PR syncer read them green, and the next sweep
// wakes it once, saying so. Later sweeps send nothing more.
func TestTheConductorWaitsForItsPullRequestsChecksAndIsWokenOnce(t *testing.T) {
	w := conducting(t)
	task, _ := w.reviewWaitedOn()
	out := w.must(task, "wait_for", `{"conditions":[{"prChecksFinal":{"pr":"1"}}],"deadline":"30m","note":"then ask for review"}`)
	waitID, _ := out["waitId"].(string)
	if waitID == "" || out["holds"] != nil {
		t.Fatalf("wait_for: %v", out)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.waiting'`, task); n != 1 {
		t.Fatalf("%d conductor.waiting events", n)
	}
	for range 3 {
		w.pump()
	}
	if got := w.waitNotes(task); len(got) != 0 {
		t.Fatalf("woken while the checks are pending: %q", got)
	}
	head := w.str(`SELECT head_sha FROM pull_requests WHERE task_id = $1`, task)
	w.gh.SetChecks("success")
	w.webhook("status", statusWebhook(head))
	if checks := w.str(`SELECT checks::text FROM pull_requests WHERE task_id = $1`, task); checks != "passing" {
		t.Fatalf("the webhook left the checks %s", checks)
	}
	w.until("the wait's note", func() bool { return len(w.waitNotes(task)) == 1 })
	note := w.waitNotes(task)[0]
	if !strings.Contains(note, "target#1 checks passing at "+head[:7]) || !strings.Contains(note, "then ask for review") {
		t.Errorf("the note:\n%s", note)
	}
	if by := w.str(`SELECT ended_by FROM conductor_waits WHERE id = $1`, waitID); by != delivery.WaitHeld {
		t.Errorf("the wait ended %q", by)
	}
	for range 5 {
		w.pump()
	}
	if got := w.waitNotes(task); len(got) != 1 {
		t.Errorf("%d notes for one wait", len(got))
	}
}

// A wait that holds while the delivery parks on a decision for the
// conductor, within the wake window: one note carries both reasons.
func TestAWaitAndADecisionParkWithinTheWindowAreOneNote(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b := scripted(spec)
			b.Hang = true
			return b
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	var implementer string
	w.until("the implementer running", func() bool {
		implementer = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND status = 'running' AND lux_state = 'running'`, task)
		return implementer != ""
	})
	w.must(task, "wait_for", `{"conditions":[{"runsEnded":["`+implementer+`"]}],"deadline":"1h"}`)
	// A window long enough that both reasons are recorded within it.
	w.syncer.WakeWindow = 3 * time.Second
	w.lux.EndTurn(w.luxRunOf(implementer))
	w.until("the wait's reason delivered", func() bool {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'wait' AND delivered_at IS NOT NULL`, task) == 1
	})
	var reasons []string
	rows, err := w.owner.Query(context.Background(), `SELECT payload->>'reasons' FROM events
		WHERE task_id = $1 AND event_type = 'conductor.woken' AND payload->'reasons' ? 'wait'`, task)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r string
		_ = rows.Scan(&r)
		reasons = append(reasons, r)
	}
	rows.Close()
	if len(reasons) != 1 || !strings.Contains(reasons[0], `"decision"`) {
		t.Fatalf("the notes carrying the wait: %v", reasons)
	}
	if at := w.decisionAt(task); at != delivery.PointImplemented {
		t.Errorf("the delivery is at %q", at)
	}
}

// A wait on a task its owner stopped: its deadline records the reason,
// held until the pick-up; Resume delivers it to the kept conductor.
func TestAWaitOnAStoppedTaskWakesOnlyAfterThePickUp(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	w.must(task, "wait_for", `{"deadline":"30m"}`)
	w.stop(ana, task, map[string]any{"note": "hold on"})
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	mustExec(t, w.owner, `UPDATE conductor_waits SET deadline_at = now() - interval '1 second' WHERE task_id = $1`, task)
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'wait' AND delivered_at IS NULL`, task); n != 1 {
		t.Fatalf("%d wait reasons held", n)
	}
	if got := w.conductorRuns(task); len(got) != 1 {
		t.Fatalf("a wait on a stopped task started a conductor: %v", got)
	}
	w.pickUp(ana, task, map[string]any{"action": "resume"})
	w.until("the reason delivered to the kept conductor", func() bool {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'wait' AND conductor_run_id = $2`, task, conductor) == 1
	})
}

// A person's Stop keeps the implementer a wait is on, aborted for the
// pick-up: that is not its end. The wait stays open through the Stop and
// the Resume that brings the Run back, and holds once the Run completes.
func TestAStopDoesNotEndTheRunsAWaitIsOn(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			// At work until ended here, the resume included.
			b.Hang, b.NeverWake = true, true
		}
		return b
	}
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	var implementer string
	w.until("the implementer running", func() bool {
		implementer = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND status = 'running' AND lux_state = 'running'`, task)
		return implementer != ""
	})
	waitID, _ := w.must(task, "wait_for", `{"conditions":[{"runsEnded":["`+implementer+`"]}],"deadline":"1h"}`)["waitId"].(string)
	if waitID == "" {
		t.Fatal("no wait opened")
	}
	open := func() bool {
		return w.str(`SELECT COALESCE(ended_by, '') FROM conductor_waits WHERE id = $1`, waitID) == ""
	}
	w.stop(ana, task, map[string]any{"note": "hold on"})
	w.until("the conductor and the implementer kept", func() bool { return w.keptRun(conductor) && w.keptRun(implementer) })
	for range 3 {
		w.sweep()
		w.pump()
	}
	if !open() {
		t.Fatalf("the Stop ended the wait: %q", w.str(`SELECT ended_by FROM conductor_waits WHERE id = $1`, waitID))
	}
	w.pickUp(ana, task, map[string]any{"action": "resume", "note": "Go on."})
	w.until("the implementer running again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, implementer) == 1
	})
	w.sweep()
	if !open() {
		t.Fatalf("the Resume ended the wait: %q", w.str(`SELECT ended_by FROM conductor_waits WHERE id = $1`, waitID))
	}
	w.until("the wait held", func() bool {
		w.lux.EndTurn(w.luxRunOf(implementer))
		return w.str(`SELECT COALESCE(ended_by, '') FROM conductor_waits WHERE id = $1`, waitID) == delivery.WaitHeld
	})
	if s := w.str(`SELECT status::text FROM runs WHERE id = $1`, implementer); s != "completed" {
		t.Fatalf("the implementer is %s", s)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'wait'`, task); n != 1 {
		t.Fatalf("%d wait reasons, want 1", n)
	}
}

// A person stopping the conductor alone ends its wait: no note starts the
// conductor they just stopped.
func TestAbortingTheConductorEndsItsWait(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	w.must(task, "wait_for", `{"deadline":"30m"}`)
	if status, body := w.call("/internal/runs/"+conductor+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort the conductor: %d %v", status, body)
	}
	if by := w.str(`SELECT COALESCE(ended_by, '') FROM conductor_waits WHERE task_id = $1`, task); by != delivery.WaitConductorEnded {
		t.Fatalf("the wait ended %q", by)
	}
	mustExec(t, w.owner, `UPDATE conductor_waits SET deadline_at = now() - interval '1 second' WHERE task_id = $1`, task)
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'wait'`, task); n != 0 {
		t.Fatalf("an ended wait recorded %d reasons", n)
	}
	if got := w.conductorRuns(task); len(got) != 1 {
		t.Errorf("a conductor started after the person stopped it: %v", got)
	}
}

// Try again under the conductor ends the stopped conductor's wait: the new
// one is briefed afresh and waits on nothing it did not ask for.
func TestTryingAgainEndsTheStoppedConductorsWait(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	w.must(task, "wait_for", `{"deadline":"30m"}`)
	w.stop(ana, task, map[string]any{"note": "wrong plan"})
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	if by := w.str(`SELECT COALESCE(ended_by, '') FROM conductor_waits WHERE task_id = $1`, task); by != "" {
		t.Fatalf("a Stop ended the wait: %q", by)
	}
	w.pickUp(ana, task, map[string]any{"action": "retry", "note": "Ask me first."})
	if by := w.str(`SELECT COALESCE(ended_by, '') FROM conductor_waits WHERE task_id = $1`, task); by != delivery.WaitConductorEnded {
		t.Fatalf("Try again left the wait %q", by)
	}
}

// waitReasonsLeft counts the task's wait reasons not yet settled, and
// the notes the task's conductors were told carrying one.
func (w *world) waitReasonsLeft(task string) (pending, told int) {
	w.t.Helper()
	pending = w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'wait' AND delivered_at IS NULL`, task)
	told = w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken' AND payload->'reasons' ? 'wait'`, task)
	return pending, told
}

// A wait whose deadline passed while the task was stopped has its reason
// held for the pick-up; Try again ends that conductor, and the reason with
// it: the new conductor is not told an old wait's digest.
func TestTryingAgainSettlesTheStoppedConductorsHeldWaitReason(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	w.must(task, "wait_for", `{"deadline":"30m"}`)
	w.stop(ana, task, map[string]any{"note": "wrong plan"})
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	mustExec(t, w.owner, `UPDATE conductor_waits SET deadline_at = now() - interval '1 second' WHERE task_id = $1`, task)
	for range 3 {
		w.pump()
	}
	if pending, _ := w.waitReasonsLeft(task); pending != 1 {
		t.Fatalf("%d wait reasons held for the pick-up, want 1", pending)
	}
	w.pickUp(ana, task, map[string]any{"action": "retry", "note": "Ask me first."})
	w.until("the new conductor", func() bool {
		runs := w.conductorRuns(task)
		return len(runs) == 2 && len(w.said(runs[1])) > 0
	})
	for range 3 {
		w.pump()
	}
	if pending, told := w.waitReasonsLeft(task); pending != 0 || told != 0 {
		t.Fatalf("after Try again: %d wait reasons pending, %d notes carrying one", pending, told)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wake_attempts o JOIN conductor_wakes c ON c.id = o.wake_id
		WHERE c.task_id = $1 AND c.kind = 'wait'`, task); n != 0 {
		t.Fatalf("the old wait's reason went out %d times", n)
	}
}

// A wait whose deadline passed while its conductor was mid-turn has its
// reason held for the turn's end; a person aborting the conductor settles
// it: no next sweep starts a conductor for it.
func TestAbortingTheConductorMidTurnSettlesItsHeldWaitReason(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	w.must(task, "wait_for", `{"deadline":"30m"}`)
	mustExec(t, w.owner, `UPDATE runs SET turn_done_at = NULL WHERE id = $1`, conductor)
	mustExec(t, w.owner, `UPDATE conductor_waits SET deadline_at = now() - interval '1 second' WHERE task_id = $1`, task)
	w.sweep()
	if pending, told := w.waitReasonsLeft(task); pending != 1 || told != 0 {
		t.Fatalf("mid-turn: %d wait reasons pending, %d told", pending, told)
	}
	if status, body := w.call("/internal/runs/"+conductor+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort the conductor: %d %v", status, body)
	}
	// Past the wake window: a reason left pending would be delivered now.
	mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = now() - interval '1 minute' WHERE task_id = $1`, task)
	for range 3 {
		w.sweep()
		w.pump()
	}
	if got := w.conductorRuns(task); len(got) != 1 {
		t.Fatalf("a conductor started after the person stopped it: %v", got)
	}
	if pending, told := w.waitReasonsLeft(task); pending != 0 || told != 0 {
		t.Fatalf("after the abort: %d wait reasons pending, %d notes carrying one", pending, told)
	}
}

// A conductor replaced after its call was authenticated, before the tool
// ran, opens no wait.
func TestASupersededConductorOpensNoWait(t *testing.T) {
	w := conducting(t)
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: w.log, BeforeCall: func(tool string) {
		if tool == "wait_for" {
			once.Do(func() { close(paused); <-resume })
		}
	}}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	task := w.task()
	conductor := w.talk(task)
	spec := w.conductorSpecOf(task)
	done := make(chan string, 1)
	go func() {
		status, body := w.callTool(tools.URL, spec, "wait_for", `{"deadline":"30m"}`)
		if status != 422 {
			body = "status " + body
		}
		done <- body
	}()
	<-paused
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'terminated' WHERE id = $1`, conductor)
	if status, out := w.chat(task, "are you there?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	if next, _, _ := w.conductor(task); next == conductor {
		t.Fatal("no replacement conductor")
	}
	close(resume)
	if body := <-done; !strings.Contains(body, "no longer this task's conductor") {
		t.Fatalf("the replaced conductor's wait_for: %s", body)
	}
	if n := w.count(`SELECT count(*) FROM conductor_waits WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("%d waits", n)
	}
}
