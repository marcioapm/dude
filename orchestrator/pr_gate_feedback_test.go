package orchestrator_test

// Feedback while Deliver holds a delivery before its pull request: the
// owner's items start a fix round, which comes back to the hold at its new
// heads; a signal whose items are handled starts nothing; and Open is
// refused while an item is open.

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func (w *world) holdRound(task string) (round, commitsSince int) {
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE((state->'hold'->>'round')::int, 0),
		COALESCE((state->'hold'->>'commitsSince')::int, -1) FROM workflow_runs WHERE task_id = $1`, task).Scan(&round, &commitsSince)
	return round, commitsSince
}

// holdSentBack says the task's hold records its feedback being fixed.
func (w *world) holdSentBack(task string) bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state->'hold'->>'sentBack')::boolean`, task) == 1
}

// openItems records items open on the task's current attempt, by author's
// person, in one batch, with no signal to the delivery.
func (w *world) openItems(task, author, batch string, ids ...string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		SELECT $4, $1, $2, task_attempt($2), person_id, $4 FROM api_keys WHERE id = $3`, w.org, task, author, batch)
	for _, id := range ids {
		mustExec(w.t, w.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind,
				anchor, status, submitted_at)
			SELECT $5, $1, $2, task_attempt($2), $4, person_id, 'Open', 'general', '{"kind":"general"}', 'open', now()
			FROM api_keys WHERE id = $3`, w.org, task, author, batch, id)
	}
}

// An item submitted at the hold, general or pinned on the preview, goes to
// a fixer with the task running "fixing your feedback"; the fixer resolves
// it, the round is reviewed again, and the hold comes back at the new heads
// as round 2, which the owner opens.
func TestFeedbackAtTheHoldStartsAFixRoundAndComesBack(t *testing.T) {
	for _, kind := range []string{"general", "preview"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t)
			ana := w.person("Ana")
			task := w.atTheHold(ana)
			var before map[string]string
			_ = w.owner.QueryRow(context.Background(), `SELECT state->'heads' FROM workflow_runs WHERE task_id = $1`, task).Scan(&before)
			fixesBefore := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
			ar := w.resolveAsItPublishes(task, "fbi_held", itsNewHead, nil)
			var code int
			var body map[string]any
			if kind == "general" {
				code, body = w.feedback(ana, task, "fbi_held", "Name the saved filter after its query.")
			} else {
				code, body = w.previewFeedback(ana, task, "fbi_held", "The Save button is cut off.", "")
			}
			if code != 201 || body["routedTo"] != "workflow" || body["waiting"] != nil {
				t.Fatalf("submit at the hold: %d %v", code, body)
			}
			w.until("a fixer given the item", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND 'fbi_held' = ANY(feedback_item_ids)`, task) > 0
			})
			if !w.holdSentBack(task) {
				t.Error("the hold does not say its feedback was sent back")
			}
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
				AND payload->>'status' = 'running' AND payload->>'reason' = $2`, task, delivery.ReasonFixingFeedback); n != 1 {
				t.Errorf("%d moves to running for the owner's feedback", n)
			}
			w.until("back at the hold, round 2", func() bool { r, _ := w.holdRound(task); return r == 2 && w.heldBeforePR(task) })
			if w.holdSentBack(task) {
				t.Error("back at the hold, it still says sent back")
			}
			if fixer, _, answer := ar.state(); fixer == "" || !strings.HasPrefix(answer, "200 ") {
				t.Fatalf("the fixer's resolution: %s %s", fixer, answer)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'
				AND created_at > (SELECT min(created_at) FROM runs WHERE task_id = $1 AND 'fbi_held' = ANY(feedback_item_ids))`, task); n == 0 {
				t.Error("the fix round was not reviewed")
			}
			if w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task) <= fixesBefore {
				t.Error("no fix ran")
			}
			if _, since := w.holdRound(task); since < 1 {
				t.Errorf("%d commits since the first hold", since)
			}
			if len(w.gh.Pulls()) != 0 || w.readyForReview(task) != 2 {
				t.Fatalf("%d pull requests, %d moves to ready for review", len(w.gh.Pulls()), w.readyForReview(task))
			}
			if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
				t.Fatalf("Open at round 2: %d %v", status, out)
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			var at map[string]string
			_ = w.owner.QueryRow(context.Background(), `SELECT state->'gateAt'->'heads' FROM workflow_runs WHERE task_id = $1`, task).Scan(&at)
			if at["target"] == "" || at["target"] == before["target"] {
				t.Errorf("opened at %v, want the new heads, not %v", at, before)
			}
		})
	}
}

// A feedback signal whose items are handled already starts nothing: the
// delivery stays held. An item open with no signal refuses the owner's
// Open with how many are open.
func TestAtTheHoldHandledFeedbackStartsNothingAndOpenFeedbackRefusesOpen(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.atTheHold(ana)
	fixes := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
	var wf string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM workflow_runs WHERE task_id = $1`, task).Scan(&wf); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := w.runtime.Signal(ctx, w.org, wf, delivery.SignalHumanFeedback, map[string]any{"itemIds": []string{"fbi_gone"}}, "handled"); err != nil {
		t.Fatal(err)
	}
	w.until("the handled signal taken", func() bool { return w.signalsTaken(task) })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task); n != fixes || !w.heldBeforePR(task) {
		t.Fatalf("a handled signal: %d fixes (was %d), held %v", n, fixes, w.heldBeforePR(task))
	}
	w.openItems(task, ana, "fbt_quiet", "fbi_q1", "fbi_q2")
	status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true})
	e, _ := out["error"].(map[string]any)
	if status != 409 || e["code"] != "feedback_open" || e["message"] != "2 feedback items still open" {
		t.Fatalf("Open with two items open: %d %v", status, out)
	}
	for range 3 {
		w.pump()
	}
	if len(w.gh.Pulls()) != 0 {
		t.Fatal("a pull request opened past open feedback")
	}
}

// A fixer that commits but leaves the owner's item open: the round's
// review/fix loop runs out and escalates on the item, and nothing opens.
// Once the owner settles the item and tries again, the delivery comes back
// to the hold, where Done without a pull request is still theirs.
func TestAFixerThatLeavesTheItemOpenEscalatesFromTheHold(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.atTheHold(ana)
	if code, body := w.feedback(ana, task, "fbi_left", "Name the saved filter after its query."); code != 201 {
		t.Fatalf("submit at the hold: %d %v", code, body)
	}
	w.until("the escalation", func() bool { return w.escalationReason(task) != "" || len(w.gh.Pulls()) > 0 })
	var reason, ids string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'escalation'->>'reason', ''),
		COALESCE(state->'escalation'->'detail'->>'feedbackIds', '') FROM workflow_runs WHERE task_id = $1`, task).Scan(&reason, &ids)
	if reason != "stuck" || !strings.Contains(ids, "fbi_left") {
		t.Fatalf("escalation %q on %q, want stuck on fbi_left", reason, ids)
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened past an open item", n)
	}
	if s := w.feedbackStatus("fbi_left"); s == "resolved" {
		t.Fatalf("the item is %s", s)
	}
	if code, body := w.callAs(ana, "/internal/feedback/fbi_left/resolve", map[string]any{}); code != 200 {
		t.Fatalf("the owner settles the item: %d %v", code, body)
	}
	if code, body := w.decide(ana, task, "retry", "The item is settled; go on."); code != 200 {
		t.Fatalf("try again: %d %v", code, body)
	}
	w.until("back at the hold", func() bool { return w.heldBeforePR(task) || len(w.gh.Pulls()) > 0 })
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened after the retry", n)
	}
	if status, out := w.callAs(ana, "/internal/tasks/"+task+"/done-without-pull-request", map[string]any{}); status != 200 {
		t.Fatalf("Done without back at the hold: %d %v", status, out)
	}
	if s := w.taskStatus(task); s != "done" {
		t.Fatalf("task %s, want done", s)
	}
}

// Under the conductor the hold's feedback is the conductor's, as before: a
// wake reason, and no fix the workflow starts on its own.
func TestUnderTheConductorFeedbackAtTheGateWakesTheConductor(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.reachGate(task)
	ana := w.person("Ana")
	fixes := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
	if code, body := w.feedback(ana, task, "fbi_cond", "Name the saved filter after its query."); code != 201 || body["routedTo"] != "conductor" {
		t.Fatalf("submit: %d %v", code, body)
	}
	w.until("the conductor's feedback wake", func() bool {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeFeedback) > 0
	})
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeFeedback); n != 1 {
		t.Errorf("%d feedback wakes", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task); n != fixes {
		t.Errorf("the workflow started a fix under the conductor")
	}
}
