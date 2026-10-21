package orchestrator_test

// Picking a task stopped under the conductor back up (api/recover.go):
// Resume goes on with the same conductor; Try again and Start over each
// get a new one, briefed; Start over keeps who decided unless asked, and
// an adopted pull request stays open for the next attempt.

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// stoppedAtDecision is a conductor task stopped by its owner at its first
// decision, the conductor kept: the task, its owner, the conductor.
func (w *world) stoppedAtDecision() (task, ana, conductor string) {
	w.t.Helper()
	task = w.task()
	ana = w.ownedBy(task, "Ana")
	conductor = w.talk(task)
	w.stop(ana, task, map[string]any{"note": "wrong plan"})
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	return task, ana, conductor
}

func (w *world) pickUp(as, task string, body map[string]any) map[string]any {
	w.t.Helper()
	status, out := w.callAs(as, "/internal/tasks/"+task+"/recover", body)
	if status != 200 {
		w.t.Fatalf("recover %v: %d %v", body, status, out)
	}
	return out
}

func (w *world) conductorRuns(task string) []string {
	return w.ids(`SELECT id FROM runs WHERE task_id = $1 AND role = 'conductor' AND kind = 'agent' ORDER BY created_at, id`, task)
}

// Resume: the same conductor's lux Run goes on, told the note; the
// delivery waits on its decision again, and the conductor still decides.
func TestResumingUnderTheConductorGoesOnWithTheSameConductor(t *testing.T) {
	w := conducting(t)
	task, ana, conductor := w.stoppedAtDecision()
	out := w.pickUp(ana, task, map[string]any{"action": "resume", "note": "Plan it again, smaller."})
	if out["decider"] != "conductor" {
		t.Errorf("resume answered %v", out)
	}
	w.until("the same conductor to run again", func() bool {
		r := w.luxRunFor(conductor)
		return r != nil && r.Resumed == 1 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, conductor) == 1
	})
	if got := w.conductorRuns(task); len(got) != 1 {
		t.Errorf("a resume made a new conductor: %v", got)
	}
	w.until("the decision to wait again", func() bool { return w.decisionAt(task) == delivery.PointStart })
	if d := w.decider(task); d != "conductor" {
		t.Errorf("after a resume %s decides", d)
	}
	if s := w.taskStatus(task); s != "running" {
		t.Errorf("a resumed task is %s", s)
	}
	w.until("the note to reach the conductor", func() bool {
		return slicesContainsSub(w.luxRunFor(conductor).Inputs, "Plan it again, smaller.")
	})
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.recovered'
		AND payload->>'action' = 'resume' AND payload->>'decider' = 'conductor' AND payload->'runIds' ? $2`, task, conductor); n != 1 {
		t.Errorf("task.recovered does not name the conductor resumed")
	}
}

// Try again under the conductor: a new conductor on the same attempt,
// briefed with the note and the stopped one's conversation; the stopped
// one stays aborted and is let go.
func TestTryingAgainUnderTheConductorStartsANewConductorOnTheAttempt(t *testing.T) {
	w := conducting(t)
	task, ana, conductor := w.stoppedAtDecision()
	w.pickUp(ana, task, map[string]any{"action": "retry", "note": "Ask me before planning."})
	runs := w.conductorRuns(task)
	if len(runs) != 2 || runs[0] != conductor {
		t.Fatalf("conductors after Try again: %v", runs)
	}
	next := runs[1]
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND NOT keep`, conductor); n != 1 {
		t.Errorf("the stopped conductor was not let go")
	}
	if n := w.count(`SELECT count(DISTINCT attempt) FROM runs WHERE task_id = $1`, task); n != 1 {
		t.Errorf("Try again made a new attempt")
	}
	prompt := w.str(`SELECT prompt FROM runs WHERE id = $1`, next)
	for _, want := range []string{"Ask me before planning.", "try again on this attempt", "Let's talk this task through"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the new conductor's briefing lacks %q:\n%s", want, prompt)
		}
	}
	w.until("the decision to wait again", func() bool { return w.decisionAt(task) == delivery.PointStart })
	if d := w.decider(task); d != "conductor" {
		t.Errorf("after Try again %s decides", d)
	}
}

// Start over under the conductor, decider unsaid: attempt 2, decided by a
// new conductor of attempt 2 briefed with the note and attempt 1's account.
func TestStartingOverUnderTheConductorKeepsTheConductorDeciding(t *testing.T) {
	w := conducting(t)
	task, ana, conductor := w.stoppedAtDecision()
	out := w.pickUp(ana, task, map[string]any{"action": "restart", "note": "Start from the API."})
	if out["decider"] != "conductor" {
		t.Errorf("restart answered %v", out)
	}
	runs := w.conductorRuns(task)
	if len(runs) != 2 || runs[0] != conductor {
		t.Fatalf("conductors after Start over: %v", runs)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND attempt = 2`, runs[1]); n != 1 {
		t.Errorf("the new conductor is not attempt 2's")
	}
	prompt := w.str(`SELECT prompt FROM runs WHERE id = $1`, runs[1])
	for _, want := range []string{"Start from the API.", "started it over as attempt 2", "What attempt 1 did: conductor aborted"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("attempt 2's conductor briefing lacks %q:\n%s", want, prompt)
		}
	}
	if d := w.decider(task); d != "conductor" {
		t.Errorf("attempt 2 is decided by %s", d)
	}
	w.until("attempt 2's first decision", func() bool { return w.decisionAt(task) == delivery.PointStart })
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state->>'attempt')::int = 2`, task); n != 1 {
		t.Errorf("no delivery for attempt 2")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.recovered'
		AND payload->>'action' = 'restart' AND payload->>'decider' = 'conductor'`, task); n != 1 {
		t.Errorf("task.recovered does not record who decides")
	}
}

// Start over handing attempt 2 to Deliver: no conductor is started, the
// change is recorded, and Deliver starts the implementer.
func TestStartingOverCanHandTheNextAttemptToDeliver(t *testing.T) {
	w := conducting(t)
	task, ana, _ := w.stoppedAtDecision()
	w.pickUp(ana, task, map[string]any{"action": "restart", "decider": "policy"})
	if got := w.conductorRuns(task); len(got) != 1 {
		t.Errorf("Start over to Deliver started a conductor: %v", got)
	}
	if d := w.decider(task); d != "policy" {
		t.Errorf("attempt 2 is decided by %s", d)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decider_changed'
		AND payload->>'from' = 'conductor' AND payload->>'to' = 'policy' AND payload->>'why' = 'start over'`, task); n != 1 {
		t.Errorf("the hand-over to Deliver was not recorded")
	}
	w.until("attempt 2's implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2 AND phase = 'implement'`, task) == 1
	})
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/recover", map[string]any{"action": "retry", "decider": "policy"}); status != 400 {
		t.Errorf("a decider with Try again: %d %v", status, body)
	}
}

// Stop and start attempt 2 on a Deliver task, handed to the conductor:
// one request, attempt 2 decided by a new conductor.
func TestStopAndStartOverHandsTheNextAttemptToTheConductor(t *testing.T) {
	w := conducting(t)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Reply: "Hello."}
	}
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	w.deliver(task)
	w.until("the implementer to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'`, task) == 1
	})
	out := w.stop(ana, task, map[string]any{"andRestart": true, "decider": "conductor", "note": "Talk it through first."})
	if out["status"] != "running" || out["attempt"] != float64(2) || out["decider"] != "conductor" {
		t.Fatalf("stop and start over answered %v", out)
	}
	if d := w.decider(task); d != "conductor" {
		t.Errorf("attempt 2 is decided by %s", d)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor' AND attempt = 2`, task); n != 1 {
		t.Errorf("attempt 2 has %d conductors", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'aborted' AND NOT keep`, task); n != 1 {
		t.Errorf("attempt 1's implementer was not let go")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.stopped'
		AND payload->>'decider' = 'policy' AND payload->>'nextDecider' = 'conductor'`, task); n != 1 {
		t.Errorf("task.stopped does not say who decided and who decides next")
	}
}

// An adopted pull request is never closed by Start over: attempt 2 goes on
// on its head branch, and the pull request stays the task's, open.
func TestStartingOverAnAdoptedPullRequestCarriesItOn(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	n := w.gh.OpenAsPerson("alice", "feature/half", "main", "Half done")
	task := w.adopted("pr", n, "feature/half", "main", delivery.ModeReview)
	ana := w.ownedBy(task, "Ana")
	w.startDelivery(task)
	w.until("a reviewer of the adopted pull request", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'`, task) > 0
	})
	out := w.stop(ana, task, map[string]any{"andRestart": true})
	if out["attempt"] != float64(2) {
		t.Fatalf("stop and start over answered %v", out)
	}
	if p := w.gh.Pull(n); p.State != "open" {
		t.Errorf("the adopted pull request is %s", p.State)
	}
	if b := w.str(`SELECT state->>'branch' FROM workflow_runs WHERE task_id = $1 AND (state->>'attempt')::int = 2`, task); b != "feature/half" {
		t.Errorf("attempt 2 works on %q, want the pull request's head branch", b)
	}
	w.until("attempt 2's reviewer, on the same pull request", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2 AND phase = 'review'`, task) > 0
	})
	if c := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND number = $2 AND state = 'open' AND adopted`, task, n); c != 1 {
		t.Errorf("the adopted pull request is no longer the task's, open")
	}
}

// Stopped with both on lux, the conductor at work and a reviewer it
// started mid-turn: Resume takes up both of the same lux Runs, the
// conductor still decides, and the reviewer's finish is what moves the
// parked delivery on to its next decision.
func TestResumingAConductorAndItsLiveReviewerOnLuxTakesBothUp(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			// Mid-review until the resume's note wakes it.
			b.Hang = true
		}
		return b
	}
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	conductor := w.talk(task)
	w.implemented(task)
	w.must(task, "start_phase", `{"phase":"review"}`)
	w.until("a reviewer at work on lux", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'
			AND lux_run_id IS NOT NULL AND lux_state = 'running'`, task) > 0
	})
	reviewers := w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY id`, task)
	conductorLux := w.luxRunOf(conductor)
	if conductorLux == "" {
		t.Fatal("the conductor is not on lux")
	}
	reviewerLux := map[string]string{}
	for _, id := range reviewers {
		reviewerLux[id] = w.luxRunOf(id)
	}

	w.stop(ana, task, map[string]any{"note": "hold on"})
	w.until("the conductor and every reviewer to be kept", func() bool {
		if !w.keptRun(conductor) {
			return false
		}
		for _, id := range reviewers {
			if !w.keptRun(id) {
				return false
			}
		}
		return true
	})

	out := w.pickUp(ana, task, map[string]any{"action": "resume", "note": "Go on."})
	if out["decider"] != "conductor" {
		t.Errorf("resume answered %v", out)
	}
	w.until("the conductor's own lux Run resumed and running", func() bool {
		r := w.luxRunFor(conductor)
		return r != nil && r.Resumed == 1 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, conductor) == 1
	})
	if got := w.luxRunOf(conductor); got != conductorLux {
		t.Errorf("conductor lux Run %q after resume, want %q", got, conductorLux)
	}
	for _, id := range reviewers {
		w.until("reviewer "+id+"'s own lux Run resumed", func() bool {
			r := w.luxRunFor(id)
			return r != nil && r.Resumed == 1
		})
		if got := w.luxRunOf(id); got != reviewerLux[id] {
			t.Errorf("reviewer %s lux Run %q after resume, want %q", id, got, reviewerLux[id])
		}
	}
	if got := w.conductorRuns(task); len(got) != 1 {
		t.Errorf("a resume made a new conductor: %v", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task); n != len(reviewers) {
		t.Errorf("%d reviewers after resume, want the %d resumed", n, len(reviewers))
	}
	if d := w.decider(task); d != "conductor" {
		t.Errorf("after the resume %s decides", d)
	}
	w.until("the resumed reviewers' finish to move the delivery to after review", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'`, task) == len(reviewers) &&
			w.decisionAt(task) == delivery.PointReviewed
	})
	if s := w.taskStatus(task); s != "running" {
		t.Errorf("a resumed task is %s", s)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, conductor); n != 1 {
		t.Errorf("the conductor is no longer running after the review")
	}
}

func slicesContainsSub(items []string, want string) bool {
	for _, s := range items {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
