package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The conductor's request_limit: asked of the task's people through its
// question, approved by someone who may change the setting, recorded as a
// task-scoped grant the checks and limits read.

// setRole makes the person behind key an admin or a member.
func (w *world) setRole(key, role string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE people SET role = $2 WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, key, role)
}

// limitRequestAt is a conducted task waiting on its pull request with its
// review round's fix rounds spent (the policy's 2), owned by Ana, a member;
// Cy is an organisation admin.
func limitRequestAt(t *testing.T) (w *world, task, ana, cy string) {
	w = conducting(t)
	task = w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	ana, cy = w.person("Ana"), w.person("Cy")
	w.setRole(ana, "member")
	w.setRole(cy, "admin")
	w.assignOwner(task, ana)
	setPolicy(w, task, `{"maxPrFixIterations":2}`)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{prIteration}', '2') WHERE task_id = $1`, task)
	w.refused(task, "start_phase", `{"phase":"fix","note":"Rename it."}`, "at their bound")
	return w, task, ana, cy
}

// questionOf is the question's choices' labels and whom it is for ("" for
// the task's people).
func (w *world) questionOf(id string) (labels []string, to string) {
	w.t.Helper()
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT items, COALESCE(to_person, '') FROM questions WHERE id = $1`, id).Scan(&raw, &to); err != nil {
		w.t.Fatal(err)
	}
	var items []struct {
		Choices []struct{ Label string }
	}
	_ = json.Unmarshal(raw, &items)
	for _, c := range items[0].Choices {
		labels = append(labels, c.Label)
	}
	return labels, to
}

// pick answers the question with its choice i, as person.
func (w *world) pick(person, question string, i int) (int, map[string]any) {
	w.t.Helper()
	return w.callAs(person, "/internal/questions/"+question+"/answer", map[string]any{"answers": []map[string]any{{"choices": []int{i}}}})
}

func (w *world) grants(task string) int {
	return w.count(`SELECT COALESCE(jsonb_array_length(state->'limitGrants'), 0) FROM workflow_runs WHERE task_id = $1`, task)
}

// The fix-round limit per review round: the question goes to the task's
// people with Allow and No; the owner, a member, may not approve (only an
// admin changes the delivery policy) and their approval records nothing; a
// second request while it is open is refused; the admin's approval grants
// exactly one more round, which limits shows with who granted it, and the
// check allows that round and not another.
func TestAnAdminRaisesTheFixRoundsForOneTask(t *testing.T) {
	w, task, ana, cy := limitRequestAt(t)
	bo := w.person("Bo")
	w.setRole(bo, "member")

	out := w.must(task, "request_limit", `{"limit":"perReviewRound","to":3,"reason":"The reviewer's last finding needs one more round."}`)
	q, _ := out["questionId"].(string)
	labels, to := w.questionOf(q)
	if strings.Join(labels, "|") != "Allow 1 more fix round per review round for this task|No" || to != "" {
		t.Fatalf("asked %q to %q", labels, to)
	}
	w.refused(task, "request_limit", `{"limit":"perReviewRound","to":4,"reason":"again"}`, "waiting on its answer already")
	w.refused(task, "request_limit", `{"limit":"rerun","reason":"CI flaked"}`, "you already have a question waiting")

	if status, _ := w.pick(bo, q, 0); status != 403 {
		t.Fatalf("a member who is not the owner answered: %d", status)
	}
	if status, body := w.pick(ana, q, 0); status != 403 || !strings.Contains(fmt.Sprint(body["error"]), "only an organisation admin") {
		t.Fatalf("the owner, a member, approved: %d %v", status, body)
	}
	if w.grants(task) != 0 || w.conductorQuestion(task) != q {
		t.Fatalf("a refused approval recorded something: %d grants, open question %q", w.grants(task), w.conductorQuestion(task))
	}
	if status, body := w.pick(cy, q, 0); status != 200 {
		t.Fatalf("the admin's approval: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'limit.granted'
		AND payload->>'limit' = 'perReviewRound' AND (payload->>'extra')::int = 1 AND payload->>'byName' = 'Cy'`, task); n != 1 {
		t.Fatalf("%d limit.granted events", n)
	}

	round := obj(field(w.limitsOf(task), "fixRounds", "perReviewRound"))
	if num(round["base"]) != 2 || num(round["limit"]) != 3 || num(round["remaining"]) != 1 ||
		round["line"] != "delivery policy 2 + 1 granted by Cy for this task = 3" || obj(field(round, "granted", 0))["by"] != "Cy" {
		t.Fatalf("limits after the grant: %v", round)
	}
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	w.must(task, "start_phase", `{"phase":"fix","note":"Rename greet to welcome."}`)
	w.until("the granted fix on the branch, and waiting again", func() bool { return w.gh.SHA(branch) != before && w.waitingOnPR(task) })
	w.refused(task, "start_phase", `{"phase":"fix","note":"And another."}`, "3 of 3 this review round")
}

// "No" grants nothing: the limit and the check stay as they were, and the
// request is closed, so another may be made.
func TestNoToALimitRequestRecordsNothing(t *testing.T) {
	w, task, ana, _ := limitRequestAt(t)
	q, _ := w.must(task, "request_limit", `{"limit":"perReviewRound","to":4,"reason":"two more rounds"}`)["questionId"].(string)
	if status, body := w.pick(ana, q, 1); status != 200 {
		t.Fatalf("the owner's No: %d %v", status, body)
	}
	if w.grants(task) != 0 || w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'limit.granted'`, task) != 0 {
		t.Fatal("No recorded a grant")
	}
	if round := obj(field(w.limitsOf(task), "fixRounds", "perReviewRound")); num(round["limit"]) != 2 || round["base"] != nil {
		t.Fatalf("limits after No: %v", round)
	}
	w.refused(task, "start_phase", `{"phase":"fix","note":"Rename it."}`, "at their bound")
	w.must(task, "request_limit", `{"limit":"perReviewRound","to":3,"reason":"one more, then"}`)
}

// One more rerun on a head: the owner may approve it (a rerun is a person's
// action on the pull request, not an admin's setting); the check then allows
// exactly one more rerun on that head.
func TestTheOwnerAllowsOneMoreRerunOnAHead(t *testing.T) {
	w, task, ana, _ := limitRequestAt(t)
	w.refused(task, "request_limit", `{"limit":"rerun","reason":"CI flaked"}`, "no request needed")
	w.gh.SetChecks("run:failure")
	w.sync()
	w.must(task, "rerun_failed_checks", `{"pr":"1"}`)
	// CI fails again on the same commit.
	w.gh.SetChecks("run:failure")
	w.sync()
	w.refused(task, "rerun_failed_checks", `{"pr":"1"}`, "were run again on this head already")

	q, _ := w.must(task, "request_limit", `{"limit":"rerun","pr":"1","reason":"The runner ran out of disk; nothing in the change."}`)["questionId"].(string)
	labels, _ := w.questionOf(q)
	if len(labels) != 2 || !strings.HasPrefix(labels[0], "Allow one more rerun on target#1 at ") {
		t.Fatalf("rerun choices %q", labels)
	}
	if status, body := w.pick(ana, q, 0); status != 200 {
		t.Fatalf("the owner's approval: %d %v", status, body)
	}
	head := obj(field(w.limitsOf(task), "failedCheckReruns", "perHead", 0))
	if head["used"] != false || num(head["allowed"]) != 2 || num(head["reruns"]) != 1 || !strings.Contains(head["line"].(string), "granted by Ana") {
		t.Fatalf("the head's reruns after the grant: %v", head)
	}
	w.must(task, "rerun_failed_checks", `{"pr":"1"}`)
	w.gh.SetChecks("run:failure")
	w.sync()
	w.refused(task, "rerun_failed_checks", `{"pr":"1"}`, "were run again on this head already")
}

// A request needs a live conductor's reason and a sane target.
func TestALimitRequestIsCheckedBeforeAnyoneIsAsked(t *testing.T) {
	w, task, _, _ := limitRequestAt(t)
	w.refused(task, "request_limit", `{"limit":"budget","reason":"x"}`, `no limit "budget" to raise`)
	w.refused(task, "request_limit", `{"limit":"perReviewRound","to":3}`, "say why")
	w.refused(task, "request_limit", `{"limit":"perReviewRound","to":2,"reason":"x"}`, "ask for 3 to 12")
	w.refused(task, "request_limit", `{"limit":"perReviewRound","to":13,"reason":"x"}`, "ask for 3 to 12")
	if q := w.conductorQuestion(task); q != "" {
		t.Fatalf("a refused request asked %s", q)
	}
}
