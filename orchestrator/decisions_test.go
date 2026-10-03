package orchestrator_test

// The conductor's decisions (design: the conductor, step 3), through the
// orchestrator's real code: Chat and the API, the workflow, the syncer's
// wakes, and the conductor's own tools called with its Run's token, as its
// agent would call them.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// conducting is a world whose conductor has dude's tools, stays warm for
// the test, and is woken as soon as a reason arrives.
func conducting(t *testing.T) *world {
	w := conductorWorld(t)
	w.withTools()
	w.syncer.ConductorWarm = time.Hour
	w.syncer.WakeWindow = 50 * time.Millisecond
	return w
}

// talk is Talk it through on a task not started: its conductor, briefed,
// and the delivery parked on its first decision.
func (w *world) talk(task string) string {
	w.t.Helper()
	status, out := w.call("/internal/tasks/"+task+"/talk", map[string]any{})
	if status != 201 || out["created"] != true || out["decider"] != "conductor" {
		w.t.Fatalf("talk: %d %v", status, out)
	}
	runID, _ := out["runId"].(string)
	w.until("the conductor to answer", func() bool { return len(w.said(runID)) >= 1 })
	w.until("the start's decision", func() bool { return w.decisionAt(task) == delivery.PointStart })
	return runID
}

// decisionAt is the decision point the task's delivery is parked on for
// its conductor, "" for none.
func (w *world) decisionAt(task string) string {
	var point string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'decision'->>'point', '') FROM workflow_runs
		WHERE task_id = $1 AND step = 'conductorDecision' AND status = 'waiting' AND NOT state->'decision' ? 'taken'`, task).Scan(&point)
	return point
}

// decider is who decides the task's delivery, as its workflow says.
func (w *world) decider(task string) string {
	var d string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->>'decider', 'policy') FROM workflow_runs WHERE task_id = $1
		ORDER BY created_at DESC LIMIT 1`, task).Scan(&d)
	return d
}

// conductorSpecOf is the lux spec the task's conductor was submitted with.
func (w *world) conductorSpecOf(task string) string {
	w.t.Helper()
	id, _, _ := w.conductor(task)
	w.until("the conductor on lux", func() bool { return w.luxRunOf(id) != "" })
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] == id {
			return string(r.Spec)
		}
	}
	w.t.Fatalf("no lux Run for the conductor %s", id)
	return ""
}

// as calls a tool as the task's conductor, with its Run's token.
func (w *world) as(task, tool, args string) (int, map[string]any) {
	w.t.Helper()
	status, body := w.callTool(w.syncer.Agent.ToolsURL, w.conductorSpecOf(task), tool, args)
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return status, out
}

// must calls a tool as the conductor, which must accept it.
func (w *world) must(task, tool, args string) map[string]any {
	w.t.Helper()
	status, out := w.as(task, tool, args)
	if status != 200 {
		w.t.Fatalf("%s %s: %d %v", tool, args, status, out)
	}
	return out
}

// refused calls a tool as the conductor, which must refuse it saying want.
func (w *world) refused(task, tool, args, want string) {
	w.t.Helper()
	status, out := w.as(task, tool, args)
	msg, _ := out["error"].(string)
	if status != 422 || !strings.Contains(msg, want) {
		w.t.Fatalf("%s %s: %d %v, want refused saying %q", tool, args, status, out, want)
	}
}

// woken is what dude's notes to the task's conductor said, in order.
func (w *world) woken(task string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload->>'text' FROM events
		WHERE task_id = $1 AND event_type = 'conductor.woken' ORDER BY cursor`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// wokenWith waits for a note to the conductor saying want, and that the
// conductor heard it (its input).
func (w *world) wokenWith(task, want string) string {
	w.t.Helper()
	var note string
	w.until("a note saying "+want, func() bool {
		for _, n := range w.woken(task) {
			if strings.Contains(n, want) {
				note = n
				return true
			}
		}
		return false
	})
	w.until("the conductor to hear it", func() bool {
		return w.count(`SELECT count(*) FROM directives d JOIN events e ON e.task_id = d.task_id AND e.event_type = 'conductor.woken'
			AND e.payload->>'directiveId' = d.id WHERE d.task_id = $1 AND d.delivered_at IS NULL`, task) == 0
	})
	return note
}

func (w *world) phaseRuns(task, phase string) int {
	return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase::text = $2`, task, phase)
}

// Talk it through: a delivery decided by the conductor, waiting on the
// start, and no implementer before it says so. Then the whole of a
// conducted delivery: the spec written into the task, the implementer it
// starts, the review it asks for, a fix of the one finding, a clean review,
// and the pull request after the person answered Open.
func TestTalkItThroughToAPullRequest(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IS NOT NULL`, task); n != 0 {
		t.Fatalf("%d phase Runs before the conductor decided anything", n)
	}
	for range 5 {
		w.pump()
	}
	if n := w.phaseRuns(task, "implement"); n != 0 {
		t.Fatalf("an implementer started without the conductor")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decider_changed'
		AND payload->>'to' = 'conductor'`, task); n != 1 {
		t.Errorf("%d decider changes recorded, want 1", n)
	}
	// The start wakes nobody: the person's message was the conductor's turn.
	if n := len(w.woken(task)); n != 0 {
		t.Errorf("%d notes at the start", n)
	}

	w.must(task, "update_task", `{"goal":"Say hello in Portuguese, as agreed in Chat.","acceptanceCriteria":["it says olá"]}`)
	w.must(task, "start_phase", `{"phase":"implement","note":"keep it to one file"}`)
	w.until("the implementer to run", func() bool { return w.specOf("implement") != nil })
	if p := w.specOf("implement").Workload.Prompt; p == "" {
		t.Fatal("no implementer prompt")
	}
	var conductorOf, note string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(conductor_run_id, ''), COALESCE(conductor_note, '') FROM runs
		WHERE task_id = $1 AND phase = 'implement'`, task).Scan(&conductorOf, &note)
	if conductorOf != conductor || note != "keep it to one file" {
		t.Errorf("the implementer is the conductor's %q with note %q", conductorOf, note)
	}

	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	n := w.wokenWith(task, "after implement")
	if !strings.Contains(n, "implement run_") || !strings.Contains(n, "1 files changed") || strings.Contains(n, "FACTORY.md") {
		t.Errorf("the note after implement: %q", n)
	}
	w.must(task, "decide", `{"action":"next"}`)

	w.until("after the review round", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	n = w.wokenWith(task, "after a review round")
	if !strings.Contains(n, "1 open findings (1 blocking)") || strings.Contains(n, "does not record the fix") {
		t.Errorf("the note after review: %q", n)
	}
	w.must(task, "start_phase", `{"phase":"fix"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.wokenWith(task, "after a fix")
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the clean round", func() bool {
		return w.decisionAt(task) == delivery.PointReviewed && w.phaseRuns(task, "review") == 2
	})
	w.must(task, "decide", `{"action":"next"}`)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })

	w.refused(task, "decide", `{"action":"open_pull_request"}`, "has not been asked")
	w.must(task, "decide", `{"action":"ask_person","note":"Reviewed clean; tests ran in the implementer."}`)
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "has not answered")
	if status, out := w.chat(task, "Open"); status != 200 || out["questionId"] == nil {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if w.gh.Pulls()[0].Draft {
		t.Error("Open opened a draft")
	}
	w.until("review", func() bool { return w.taskStatus(task) == "review" })
	if body := w.gh.Pulls()[0].Body; !strings.Contains(body, "it says olá") {
		t.Errorf("the pull request does not carry the agreed criteria:\n%s", body)
	}
	// Every phase Run was the conductor's.
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IS NOT NULL AND conductor_run_id IS NULL`, task); n != 0 {
		t.Errorf("%d phase Runs not marked the conductor's", n)
	}
}

// update_task: written into the task with what it was, and refused once
// an implementer started on the attempt.
func TestUpdateTaskOnlyBeforeTheImplementer(t *testing.T) {
	w := conducting(t)
	// A real model's implementer is given the composed prompt (the
	// scripted one is given its script); the fake lux plays it alike.
	w.onModel("implementer", "claude-opus-5-5")
	task := w.task()
	w.talk(task)
	w.must(task, "update_task", `{"acceptanceCriteria":["it greets in Portuguese"]}`)
	var goal, criteria string
	_ = w.owner.QueryRow(context.Background(), `SELECT goal, acceptance_criteria::text FROM tasks WHERE id = $1`, task).Scan(&goal, &criteria)
	if goal != "Say hello" || criteria != `["it greets in Portuguese"]` {
		t.Errorf("the task is %q %s", goal, criteria)
	}
	var before string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'before'->>'acceptanceCriteria' FROM events
		WHERE task_id = $1 AND event_type = 'task.updated'`, task).Scan(&before)
	if before != `["it greets"]` {
		t.Errorf("the event's before: %s", before)
	}
	w.must(task, "start_phase", `{"phase":"implement","note":"Use the existing greeter module."}`)
	w.until("the implementer", func() bool { return w.specOf("implement") != nil })
	if p := w.specOf("implement").Workload.Prompt; !strings.Contains(p, "it greets in Portuguese") {
		t.Errorf("the implementer's prompt lacks the agreed criterion")
	} else if !strings.Contains(p, "## From the task's conductor") || !strings.Contains(p, "Use the existing greeter module.") {
		t.Errorf("the implementer's prompt lacks the conductor's note")
	}
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.refused(task, "update_task", `{"goal":"Something else entirely, now."}`, "an implementer has started")
}

// Taking over a delivered task: the first message mid-delivery hands the
// decisions to the conductor. The step running finishes, and the next
// decision is the conductor's: no reviewer until it says so.
func TestTakingOverMidDelivery(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.deliver(task)
	// The workflow alone, until the implementer exists; the syncer has not
	// run it yet.
	for w.phaseRuns(task, "implement") == 0 {
		if _, err := w.runtime.Tick(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	if w.decider(task) != "policy" {
		t.Fatalf("decider before: %s", w.decider(task))
	}
	if status, out := w.chat(task, "let me steer this one"); status != 201 || out["decider"] != "conductor" {
		t.Fatalf("chat: %d %v", status, out)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decider_changed'
		AND payload->>'from' = 'policy' AND payload->>'to' = 'conductor'`, task); n != 1 {
		t.Errorf("%d decider changes", n)
	}
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	if n := w.phaseRuns(task, "review"); n != 0 {
		t.Fatalf("a reviewer started after the take-over")
	}
	w.wokenWith(task, "after implement")
	w.must(task, "decide", `{"action":"next"}`)
	w.until("a review", func() bool { return w.phaseRuns(task, "review") == 1 })
}

// Let Deliver finish it: the decision waited on goes to the policy at once,
// and the rest of the delivery is Deliver's — to a pull request with no
// question. A later message does not take it over again.
func TestLetDeliverFinishIt(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	if status, out := w.call("/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy"}); status != 200 {
		t.Fatalf("hand back: %d %v", status, out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1`, task); n != 0 {
		t.Errorf("%d questions on a delivery Deliver finished", n)
	}
	if status, out := w.chat(task, "anything left?"); status != 200 || out["decider"] != "policy" {
		t.Errorf("a later message: %d %v", status, out)
	}
	if w.decider(task) != "policy" {
		t.Errorf("decider after a later message: %s", w.decider(task))
	}
	w.refused(task, "start_phase", `{"phase":"review"}`, "Deliver takes this task's decisions")
}

// A merged or closed task's conductor is read-only: Chat does not hand it
// the decisions, and each of its decisions is refused, offering a
// follow-up task instead.
func TestAFinishedTasksConductorIsReadOnly(t *testing.T) {
	for _, end := range []string{"merged", "closed"} {
		t.Run(end, func(t *testing.T) {
			w := conducting(t)
			task, _ := w.delivered()
			w.until("review", func() bool { return w.taskStatus(task) == "review" })
			if end == "merged" {
				w.gh.Merge(1)
			} else {
				w.gh.Close(1)
			}
			w.until("the task to end", func() bool { w.sync(); return w.taskStatus(task) == "done" || w.taskStatus(task) == "aborted" })
			if status, out := w.chat(task, "please change the greeting"); status != 201 || out["decider"] != "policy" {
				t.Fatalf("chat: %d %v", status, out)
			}
			for _, call := range [][2]string{{"start_phase", `{"phase":"fix"}`}, {"decide", `{"action":"next"}`},
				{"update_task", `{"goal":"A goal long enough to be one."}`}, {"dismiss_finding", `{"id":"fnd_x","reason":"no"}`}} {
				w.refused(task, call[0], call[1], "follow-up task")
			}
			// The world numbers its tasks by hand; the project's counter
			// is what an agent's create_task takes the next from.
			mustExec(t, w.owner, `UPDATE projects SET next_task_number = 100 WHERE id = $1`, w.project)
			w.must(task, "create_task", `{"title":"Change the greeting","goal":"A follow-up asked for in Chat after merge."}`)
			if n := w.count(`SELECT count(*) FROM tasks t JOIN runs r ON r.id = t.created_by_run_id WHERE r.task_id = $1`, task); n != 1 {
				t.Errorf("the follow-up is not linked to the task: %d", n)
			}
		})
	}
}

// The pull request gate: an answer at an older head opens nothing, and
// Draft opens a draft.
func TestThePullRequestGateFollowsTheHead(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	// Straight on to the pull request: a simplifier, then (the test step is
	// off) the gate.
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	w.must(task, "decide", `{"action":"ask_person"}`)
	w.chat(task, "Open")
	// Another round moves the head.
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.until("before the pull request again", func() bool {
		return w.decisionAt(task) == delivery.PointBeforePR && w.phaseRuns(task, "simplify") == 2
	})
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "earlier head")
	w.must(task, "decide", `{"action":"ask_person"}`)
	w.chat(task, "Show me the diff")
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "not Open or Draft")
	w.must(task, "decide", `{"action":"ask_person"}`)
	w.chat(task, "Draft")
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if !w.gh.Pulls()[0].Draft {
		t.Error("Draft opened a pull request ready for review")
	}
}

// The policy's bounds bound the conductor: review rounds, fix attempts per
// finding, pull request fix rounds — each refused saying to ask the person.
func TestTheBoundsRefuseTheConductor(t *testing.T) {
	t.Run("review rounds", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.talk(task)
		setPolicy(w, task, `{"maxReviewIterations":2}`)
		w.must(task, "start_phase", `{"phase":"implement"}`)
		w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
		w.must(task, "decide", `{"action":"next"}`)
		w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.must(task, "start_phase", `{"phase":"review"}`)
		w.until("round 2, clean", func() bool { return w.decisionAt(task) == delivery.PointReviewed && w.phaseRuns(task, "review") == 2 })
		w.refused(task, "start_phase", `{"phase":"review"}`, "2 of 2 rounds spent")
		w.refused(task, "start_phase", `{"phase":"fix"}`, "the person how to go on")
	})
	t.Run("fix attempts per finding", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.talk(task)
		setPolicy(w, task, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9}`)
		w.must(task, "start_phase", `{"phase":"implement"}`)
		w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
		w.must(task, "decide", `{"action":"next"}`)
		w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.refused(task, "start_phase", `{"phase":"fix"}`, "the bound per finding")
	})
	t.Run("pull request fix rounds", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.deliver(task)
		setPolicy(w, task, `{"maxPrFixIterations":1}`)
		w.until("review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
		w.chat(task, "I'll take it from here")
		w.gh.Comment(1, "alice", "Please rename the greeting.")
		w.until("feedback to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
		w.wokenWith(task, "pull request feedback")
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("the fix", func() bool { return w.fixes(task) == 1 && w.taskStatus(task) == "review" })
		w.gh.Comment(1, "alice", "And the farewell.")
		w.until("feedback again", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
		w.refused(task, "start_phase", `{"phase":"fix"}`, "1 of 1 this review round")
		w.refused(task, "decide", `{"action":"next"}`, "the person how to go on")
		w.must(task, "decide", `{"action":"wait","note":"asking Márcio first"}`)
		w.until("waiting on the pull request", func() bool { return w.taskStatus(task) == "review" })
	})
}

// setPolicy changes the policy of the task's delivery, as a project's
// would have set it at the start.
func setPolicy(w *world, task, patch string) {
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{policy}', state->'policy' || $2::jsonb)
		WHERE task_id = $1`, task, patch)
}

// dismiss_finding: the finding is left as it is, its reason shown, and the
// review round it blocked is clear.
func TestDismissAFinding(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	var id string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM review_findings WHERE task_id = $1`, task).Scan(&id)
	w.refused(task, "dismiss_finding", `{"id":"`+id+`","reason":"  "}`, "say why")
	w.must(task, "dismiss_finding", `{"id":"`+id+`","reason":"FACTORY.md is generated; the record is elsewhere"}`)
	var status, note string
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text, resolution_note FROM review_findings WHERE id = $1`, id).Scan(&status, &note)
	if status != "accepted" || !strings.Contains(note, "FACTORY.md is generated") {
		t.Errorf("the finding is %s: %q", status, note)
	}
	// What the policy would do now: the round is clear, on to simplify.
	w.must(task, "decide", `{"action":"next"}`)
	w.until("simplify", func() bool { return w.phaseRuns(task, "simplify") == 1 })
	if n := w.phaseRuns(task, "fix"); n != 0 {
		t.Errorf("a fix ran for a dismissed finding")
	}
}

// An escalation still goes to a person; the conductor is told, and only
// the person decides.
func TestAnEscalationGoesToAPersonAndWakesTheConductor(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Crash: true}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the escalation", func() bool { return w.escalationReason(task) == "implement_failed" })
	n := w.wokenWith(task, "Escalated to a person: implement failed")
	if !strings.Contains(n, "Only the person decides") {
		t.Errorf("note: %q", n)
	}
	w.refused(task, "decide", `{"action":"next"}`, "not waiting on a decision")
	if status, out := w.callAs(w.person("owner"), "/internal/tasks/"+task+"/decide", map[string]string{"action": "stop"}); status != 200 {
		t.Fatalf("the person's decision: %d %v", status, out)
	}
}

// The scripted conductor carries out the tool calls a message names
// ("tool: NAME {json}"), in its first turn and later ones: what the e2e
// suites drive its decisions with.
func TestTheScriptedConductorDecidesWhatItIsTold(t *testing.T) {
	w := conducting(t)
	task := w.task()
	status, out := w.chat(task, "Plan it with me.\ntool: update_task {\"acceptanceCriteria\":[\"it says olá\"]}")
	if status != 201 || out["decider"] != "conductor" {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("the criteria written", func() bool {
		return w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND acceptance_criteria = '["it says olá"]'`, task) == 1
	})
	w.until("the start's decision", func() bool { return w.decisionAt(task) == delivery.PointStart })
	w.chat(task, "Go.\ntool: start_phase {\"phase\":\"implement\"}")
	w.until("the implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
}

// Coalesced: reasons arriving within the window are one note, one turn.
// A decision and the Run it followed are one wake, not two.
func TestWakesArrivingTogetherAreOneNote(t *testing.T) {
	w := conducting(t)
	w.syncer.WakeWindow = 2 * time.Second
	task := w.task()
	w.talk(task)
	ctx := context.Background()
	for _, key := range []string{"a", "b", "c"} {
		if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			_, err := delivery.RecordWakeTx(ctx, tx, w.org, task, "decision", "test:"+key, "reason "+key)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		w.pump()
	}
	if n := len(w.woken(task)); n != 0 {
		t.Fatalf("woken %d times within the window", n)
	}
	w.syncer.WakeWindow = 50 * time.Millisecond
	note := w.wokenWith(task, "3 things")
	for _, r := range []string{"reason a", "reason b", "reason c"} {
		if !strings.Contains(note, r) {
			t.Errorf("the note lacks %q: %q", r, note)
		}
	}
	for range 3 {
		w.pump()
	}
	if n := len(w.woken(task)); n != 1 {
		t.Errorf("%d notes, want 1", n)
	}
	id, _, _ := w.conductor(task)
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, id); n != 1 {
		t.Errorf("%d directives for the conductor, want one turn", n)
	}
}

// A conductor mid-turn is not interrupted: the reasons wait for its turn
// to end, then arrive as one note.
func TestAConductorMidTurnHearsItsReasonsAfter(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	id, _, _ := w.conductor(task)
	mustExec(t, w.owner, `UPDATE runs SET turn_done_at = NULL WHERE id = $1`, id)
	ctx := context.Background()
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.RecordWakeTx(ctx, tx, w.org, task, "decision", "test:mid", "while busy")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	for range 3 {
		w.pump()
	}
	if n := len(w.woken(task)); n != 0 {
		t.Fatalf("woken mid-turn")
	}
	mustExec(t, w.owner, `UPDATE runs SET turn_done_at = now() WHERE id = $1`, id)
	w.wokenWith(task, "while busy")
}

// The one safety net: a conductor asleep long with a Run of its own still
// in flight is woken once, however long it stays so.
func TestASleepingConductorIsWokenOnceForARunInFlight(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'`, task) == 1
	})
	for range 3 {
		w.pump()
	}
	if n := len(w.woken(task)); n != 0 {
		t.Fatalf("woken before the safety net's time")
	}
	w.syncer.SafetyAfter = 200 * time.Millisecond
	time.Sleep(300 * time.Millisecond)
	note := w.wokenWith(task, "Still in flight")
	if !strings.Contains(note, "implement Run run_") {
		t.Errorf("note: %q", note)
	}
	for range 5 {
		w.pump()
		time.Sleep(60 * time.Millisecond)
	}
	if n := len(w.woken(task)); n != 1 {
		t.Errorf("the safety net woke it %d times", n)
	}
}
