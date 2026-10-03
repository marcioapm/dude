package orchestrator_test

// The conductor's decisions (design: the conductor, step 3), through the
// orchestrator's real code: Chat and the API, the workflow, the syncer's
// wakes, and the conductor's own tools called with its Run's token, as its
// agent would call them.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
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

// wokenWith waits for a note to the conductor saying want, and for the
// conductor to have read it: lux's read receipt for the note's input, or,
// for a conductor dude started with it, its first prompt carrying it.
func (w *world) wokenWith(task, want string) string {
	w.t.Helper()
	var note, runID, directive string
	w.until("a note saying "+want, func() bool {
		rows, err := w.owner.Query(context.Background(), `SELECT payload->>'text', run_id, COALESCE(payload->>'directiveId', '')
			FROM events WHERE task_id = $1 AND event_type = 'conductor.woken' ORDER BY cursor DESC`, task)
		if err != nil {
			w.t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var n, r, d string
			if err := rows.Scan(&n, &r, &d); err != nil {
				w.t.Fatal(err)
			}
			if strings.Contains(n, want) {
				note, runID, directive = n, r, d
				return true
			}
		}
		return false
	})
	w.until("the conductor to read it", func() bool {
		lr := w.luxRunOf(runID)
		if lr == "" {
			return false
		}
		if directive == "" {
			// Its briefing, which lux started it with.
			return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND strpos(prompt, $2) > 0`, runID, note) == 1 &&
				w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message'`, runID) > 0
		}
		for _, rec := range w.lux.Records(lr) {
			if data, _ := rec["data"].(map[string]any); rec["type"] == lux.RecordInputConsumed && data["requestId"] == directive {
				return true
			}
		}
		return false
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
		// A is the reviewer's; B another blocking finding of the round.
		var a, run string
		_ = w.owner.QueryRow(context.Background(), `SELECT id, run_id FROM review_findings WHERE task_id = $1`, task).Scan(&a, &run)
		mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title,
				description, suggested_fix)
			SELECT 'fnd_b_'||$1, organization_id, task_id, run_id, category, severity, 'B', 'b', 'b'
			FROM review_findings WHERE id = $2`, task, a)
		w.must(task, "start_phase", `{"phase":"fix","findings":["`+a+`"]}`)
		w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.refused(task, "start_phase", `{"phase":"fix","findings":["`+a+`"]}`, "the bound per finding")
		fixes := w.phaseRuns(task, "fix")
		w.refused(task, "start_phase", `{"phase":"fix","findings":["fnd_b_`+task+`"]}`, "re-review")
		w.refused(task, "start_phase", `{"phase":"fix"}`, "re-review")
		if n := w.phaseRuns(task, "fix"); n != fixes {
			t.Errorf("a fix started past the stuck finding")
		}
		w.must(task, "start_phase", `{"phase":"review"}`)
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
	t.Run("the organization's fix rounds per pull request", func(t *testing.T) {
		w := conducting(t)
		mustExec(t, w.owner, `UPDATE forge_credentials SET settings = settings || '{"fixRoundsPerPr":1}' WHERE organization_id = $1`, w.org)
		task := w.task()
		w.deliver(task)
		// The per-review budget is not what stops it: plenty left.
		setPolicy(w, task, `{"maxPrFixIterations":9}`)
		w.until("review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
		w.chat(task, "I'll take it from here")
		w.gh.Comment(1, "alice", "Please rename the greeting.")
		w.until("feedback to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("the fix", func() bool { return w.fixes(task) == 1 && w.taskStatus(task) == "review" })
		w.gh.Comment(1, "alice", "And the farewell.")
		w.until("feedback again", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
		decided := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decided'`, task)
		w.refused(task, "start_phase", `{"phase":"fix"}`, "the organization's bound")
		w.refused(task, "decide", `{"action":"next"}`, "the organization's bound")
		for range 3 {
			w.pump()
		}
		if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decided'`, task); n != decided {
			t.Errorf("%d decisions recorded past the bound", n-decided)
		}
		if n := w.fixes(task); n != 1 {
			t.Errorf("%d fixer Runs, want the one within the bound", n)
		}
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
	// Who left it so reaches both readers: the findings tool, listed and
	// by id, and a replacement conductor's briefing. A person's acceptance
	// still reads as theirs.
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, status,
			resolution_note)
		SELECT 'fnd_p_'||$1, organization_id, task_id, run_id, category, 'low', 'P', 'accepted', 'fine as it is'
		FROM review_findings WHERE id = $2`, task, id)
	settled := func(out map[string]any) map[string]string {
		got := map[string]string{}
		fs, _ := out["findings"].([]any)
		for _, f := range fs {
			m, _ := f.(map[string]any)
			got[m["id"].(string)], _ = m["settled"].(string)
		}
		return got
	}
	list := settled(w.must(task, "findings", `{}`))
	if !strings.HasPrefix(list[id], "dismissed by the conductor: FACTORY.md is generated") || list["fnd_p_"+task] != "accepted by a person" {
		t.Errorf("the findings list says %v", list)
	}
	byID := w.must(task, "findings", `{"ids":["`+id+`"]}`)
	if s := settled(byID)[id]; !strings.HasPrefix(s, "dismissed by the conductor") || strings.Contains(s, "person") || strings.Contains(s, "fixed") {
		t.Errorf("the finding by id says %q", s)
	}
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		b, err := delivery.Briefing(context.Background(), tx, task, "run_next", "", "hello")
		if err != nil {
			return err
		}
		for _, l := range strings.Split(b, "\n") {
			switch {
			case strings.Contains(l, id) && !strings.Contains(l, "dismissed by the conductor: FACTORY.md is generated"):
				t.Errorf("the briefing's line: %q", l)
			case strings.Contains(l, "fnd_p_"+task) && !strings.Contains(l, "accepted by a person"):
				t.Errorf("the briefing's line for a person's: %q", l)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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

// sweep runs the syncer once, as one tick of its loop.
func (w *world) sweep() {
	w.t.Helper()
	if _, err := w.syncer.Sweep(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// reason records a reason to wake the task's conductor, created at age ago
// by the database's clock.
func (w *world) reason(task, key, line string, age time.Duration) {
	w.t.Helper()
	ctx := context.Background()
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.RecordWakeTx(ctx, tx, w.org, task, "decision", key, line)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	mustExec(w.t, w.owner, `UPDATE conductor_wakes SET created_at = now() - make_interval(secs => $3) WHERE task_id = $1 AND key = $2`,
		task, key, age.Seconds())
}

// Coalesced: reasons arriving within the window are one note, one turn.
// The window slides with the newest: an old first reason and a fresh one
// wait; aged, the latest delivers one note carrying all.
func TestWakesArrivingTogetherAreOneNote(t *testing.T) {
	w := conducting(t)
	w.syncer.WakeWindow = 15 * time.Second
	task := w.task()
	w.talk(task)
	w.reason(task, "test:a", "reason a", time.Minute)
	w.reason(task, "test:b", "reason b", 30*time.Second)
	w.reason(task, "test:c", "reason c", 0)
	for range 3 {
		w.sweep()
	}
	if n := len(w.woken(task)); n != 0 {
		t.Fatalf("woken %d times within the window of the newest reason", n)
	}
	mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = now() - interval '20 seconds' WHERE task_id = $1 AND key = 'test:c'`, task)
	w.sweep()
	if n := len(w.woken(task)); n != 1 {
		t.Fatalf("%d notes once the newest aged past the window, want 1", n)
	}
	note := w.wokenWith(task, "3 things")
	for _, r := range []string{"reason a", "reason b", "reason c"} {
		if !strings.Contains(note, r) {
			t.Errorf("the note lacks %q: %q", r, note)
		}
	}
	for range 3 {
		w.sweep()
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
	w.reason(task, "test:mid", "while busy", time.Minute)
	for range 3 {
		w.sweep()
	}
	if n := len(w.woken(task)); n != 0 {
		t.Fatalf("woken mid-turn")
	}
	mustExec(t, w.owner, `UPDATE runs SET turn_done_at = now() WHERE id = $1`, id)
	w.sweep()
	if n := len(w.woken(task)); n != 1 {
		t.Fatalf("%d notes once the turn ended, want 1", n)
	}
	w.wokenWith(task, "while busy")
}

// The one safety net: a conductor asleep long with a Run of its own still
// in flight is woken once, however long it stays so.
func TestASleepingConductorIsWokenOnceForARunInFlight(t *testing.T) {
	w := conducting(t)
	w.syncer.SafetyAfter = 30 * time.Minute
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	id, _, _ := w.conductor(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'`, task) == 1
	})
	asleep := func(d time.Duration) {
		mustExec(t, w.owner, `UPDATE runs SET turn_done_at = now() - make_interval(secs => $2) WHERE id = $1`, id, d.Seconds())
		mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = LEAST(created_at, now() - interval '1 minute') WHERE task_id = $1`, task)
	}
	asleep(10 * time.Minute)
	for range 3 {
		w.sweep()
	}
	if n := len(w.woken(task)); n != 0 {
		t.Fatalf("woken before the safety net's time")
	}
	asleep(time.Hour)
	w.sweep() // records the reason
	asleep(time.Hour)
	w.sweep() // delivers it
	note := w.wokenWith(task, "Still in flight")
	if !strings.Contains(note, "implement Run run_") {
		t.Errorf("note: %q", note)
	}
	// Asleep as long again, the same Run in flight: no second note, however
	// often it is re-aged and swept (each sweep's reasons aged before the next).
	for range 3 {
		asleep(2 * time.Hour)
		w.sweep()
	}
	// Whatever those sweeps recorded is delivered now: aged, with the
	// safety net out of reach so it records nothing more.
	w.syncer.SafetyAfter = 1000 * time.Hour
	asleep(2 * time.Hour)
	w.sweep()
	if n := len(w.woken(task)); n != 1 {
		t.Errorf("the safety net woke it %d times (%d safety reasons)", n,
			w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'safety'`, task))
	}
}

// A wake note lux fails before the conductor read it gives its reasons
// back: the next sweep tells the conductor again. A note the conductor
// read is never told twice, even if a failure is reported after.
func TestAFailedWakeNoteIsToldAgain(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	w.lux.InputGate = make(chan struct{})
	ctx := context.Background()
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.RecordWakeTx(ctx, tx, w.org, task, "decision", "test:lost", "the note lux lost")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var first string
	w.until("the first note", func() bool {
		_ = w.owner.QueryRow(ctx, `SELECT COALESCE(payload->>'directiveId', '') FROM events WHERE task_id = $1
			AND event_type = 'conductor.woken' ORDER BY cursor LIMIT 1`, task).Scan(&first)
		return first != "" && w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL`, first) == 1
	})
	w.lux.FailInput(w.luxRunOf(conductor), first, "the agent stopped before reading it")
	w.until("the note again", func() bool { return len(w.woken(task)) == 2 })
	notes := w.woken(task)
	if !strings.Contains(notes[1], "the note lux lost") {
		t.Errorf("the second note: %q", notes[1])
	}
	close(w.lux.InputGate)
	w.wokenWith(task, "the note lux lost")
	for range 3 {
		w.pump()
	}
	if n := len(w.woken(task)); n != 2 {
		t.Errorf("%d notes, want the failed one and its retelling", n)
	}

	// Read: a failure reported after changes nothing.
	var second string
	_ = w.owner.QueryRow(ctx, `SELECT payload->>'directiveId' FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'
		ORDER BY cursor DESC LIMIT 1`, task).Scan(&second)
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error { return delivery.RequeueWakesTx(ctx, tx, second) }); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, task); n != 0 {
		t.Errorf("%d reasons pending again after the conductor read their note", n)
	}
}

// A conductor whose container stopped mid-turn, with no turn end seen,
// does not hold the task's live slot against a decision's note: its
// replacement is started with the note, no person's message needed.
func TestAWakeReplacesAConductorWhoseContainerStopped(t *testing.T) {
	w := conducting(t)
	task := w.task()
	first := w.talk(task)
	mustExec(t, w.owner, `UPDATE runs SET turn_done_at = NULL, lux_state = 'stopped' WHERE id = $1`, first)
	ctx := context.Background()
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.RecordWakeTx(ctx, tx, w.org, task, "decision", "test:stopped", "decide after the stop")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.until("a new conductor", func() bool {
		id, _, _ := w.conductor(task)
		return id != first
	})
	w.wokenWith(task, "decide after the stop")
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message'`, task); n != 1 {
		t.Errorf("%d chat messages: the replacement needed a person", n)
	}
}

// A conductor replaced after its call was authenticated, before the tool
// ran, cannot take its successor's decision: its call is refused, and the
// decision waits for the new one.
func TestASupersededConductorDecidesNothing(t *testing.T) {
	w := conducting(t)
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, BeforeCall: func(tool string) {
		if tool == "start_phase" {
			once.Do(func() { close(paused); <-resume })
		}
	}}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	task := w.task()
	w.talk(task)
	oldSpec := w.conductorSpecOf(task)
	first, _, _ := w.conductor(task)
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		status, body := w.callTool(tools.URL, oldSpec, "start_phase", `{"phase":"implement"}`)
		done <- result{status, body}
	}()
	<-paused
	// Meanwhile its container stops, and the next message replaces it.
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'stopped' WHERE id = $1`, first)
	if status, out := w.chat(task, "are you there?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	if second, _, _ := w.conductor(task); second == first {
		t.Fatal("no replacement conductor")
	}
	close(resume)
	r := <-done
	if r.status != 422 || !strings.Contains(r.body, "no longer this task's conductor") {
		t.Fatalf("the replaced conductor's call: %d %s", r.status, r.body)
	}
	for range 3 {
		w.pump()
	}
	if w.decisionAt(task) != delivery.PointStart || w.phaseRuns(task, "implement") != 0 {
		t.Errorf("the replaced conductor took the decision")
	}
}

// A take-over that commits while a step's mechanics run, between the
// step's claim and its transition: the decision that step reaches is the
// conductor's, not the policy's it read when it was claimed.
func TestATakeOverWhileAStepRunsTakesItsDecision(t *testing.T) {
	for _, c := range []struct{ step, point, nextPhase string }{
		{"awaitImplement", delivery.PointImplemented, "review"},
		{"awaitReview", delivery.PointReviewed, "fix"},
		{"awaitFix", delivery.PointFixed, "review"},
		{"test", delivery.PointBeforePR, ""},
		{"awaitPullRequest", delivery.PointPRFeedback, "fix"},
	} {
		t.Run(c.step, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			var once sync.Once
			def := delivery.Workflow(&delivery.Store{DB: w.app}, forge.Resolver{DB: w.app})
			def.BeforeCommit = func(_, step, next string) {
				if step == c.step && next != step {
					once.Do(func() {
						if status, out := w.chat(task, "I'll take it from here"); status != 201 || out["decider"] != "conductor" {
							t.Errorf("chat: %d %v", status, out)
						}
					})
				}
			}
			w.runtime.Register(def)
			w.deliver(task)
			before := 0
			if c.step == "awaitPullRequest" {
				// The feedback arrives on the open pull request; its fixes
				// before are the review loop's.
				w.until("review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
				before = w.phaseRuns(task, c.nextPhase)
				w.gh.Comment(1, "alice", "Please rename the greeting.")
				w.until(c.point, func() bool { w.sync(); return w.decisionAt(task) == c.point })
			} else if c.nextPhase != "" {
				w.until("the step", func() bool { return w.decider(task) == "conductor" })
				before = w.phaseRuns(task, c.nextPhase)
			}
			w.until(c.point, func() bool { return w.decisionAt(task) == c.point })
			for range 3 {
				w.pump()
			}
			if c.nextPhase != "" && w.phaseRuns(task, c.nextPhase) != before {
				t.Errorf("a %s Run started without the conductor's decision", c.nextPhase)
			}
			if c.step != "awaitPullRequest" && len(w.gh.Pulls()) != 0 {
				t.Errorf("a pull request opened without the conductor's decision")
			}
			w.wokenWith(task, "Decision waiting")
		})
	}
}

// stepTo advances the task's delivery one workflow step at a time, the
// syncer running between, until it is about to run step.
func (w *world) stepTo(task, step string) {
	w.t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var at string
		_ = w.owner.QueryRow(ctx, `SELECT step FROM workflow_runs WHERE task_id = $1`, task).Scan(&at)
		if at == step {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("the delivery never reached %s (at %s)\n%s", step, at, w.describeRuns())
		}
		if _, err := w.runtime.Tick(ctx, 1); err != nil {
			w.t.Fatal(err)
		}
		if _, err := w.syncer.Sweep(ctx); err != nil {
			w.t.Fatal(err)
		}
		if _, err := phases.NotifyFinished(ctx, w.app, func(ctx context.Context, org, wf, runID, status, key string) error {
			return w.runtime.Signal(ctx, org, wf, delivery.SignalPhaseFinished, map[string]string{"runId": runID, "status": status}, key)
		}); err != nil {
			w.t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The pull request gate holds at the opening itself: a policy delivery
// that chose to open, taken over before it did, parks at the gate and
// asks; Draft carries to the opening.
func TestATakeOverBeforeTheOpeningAsksFirst(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.deliver(task)
	w.stepTo(task, "openPullRequest")
	if status, out := w.chat(task, "hold on before it opens"); status != 201 || out["decider"] != "conductor" {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("the gate", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened after the take-over", n)
	}
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "has not been asked")
	w.must(task, "decide", `{"action":"ask_person"}`)
	w.chat(task, "Draft")
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if !w.gh.Pulls()[0].Draft {
		t.Error("Draft opened a pull request ready for review")
	}
}

// Let Deliver finish it at the gate: refused while the person has not
// answered Open or Draft, unless they confirm Deliver opens it now.
func TestHandingBackAtTheGateNeedsTheOpening(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	handBack := func(body map[string]any) (int, map[string]any) {
		return w.handBack(task, body)
	}
	if status, out := handBack(map[string]any{"decider": "policy"}); status != 409 || !gateRefused(out) {
		t.Fatalf("hand-back with the gate unasked: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"ask_person"}`)
	w.chat(task, "Show me the diff")
	if status, out := handBack(map[string]any{"decider": "policy"}); status != 409 || !gateRefused(out) {
		t.Fatalf("hand-back after Show me the diff: %d %v", status, out)
	}
	for range 3 {
		w.pump()
	}
	if w.decider(task) != "conductor" || len(w.gh.Pulls()) != 0 {
		t.Fatalf("a refused hand-back changed the delivery: decider %s, %d pull requests", w.decider(task), len(w.gh.Pulls()))
	}
	if status, out := handBack(map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
		t.Fatalf("confirmed hand-back: %d %v", status, out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}
