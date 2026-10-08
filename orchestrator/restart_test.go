package orchestrator_test

// Restarting a phase Run (design: F6): the conductor's restart_run and the
// owner's Restart put a fresh Run of the same phase and category in the
// stuck one's slot; the round waits for it and goes on.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// restartable is a conducted task at a review round whose two reviewers
// hang: it returns the task and the reviewers.
func restartable(t *testing.T) (*world, string, []string) {
	w := conducting(t)
	// A real model, so a reviewer's prompt is the one a real one reads.
	w.onModel("reviewer", "llm-review")
	w.hangingReviews(taskCall)
	task := w.task()
	w.conductedReview(task)
	return w, task, w.reviewersOpen(task, 2)
}

// The conductor is told of a hung reviewer, restarts it, and the new one
// completes: the round goes on to the conductor's next decision, with no
// review_failed for the reviewer it replaced.
func TestRestartRunReplacesTheRunInItsSlot(t *testing.T) {
	w, task, runs := restartable(t)
	stuck, other := runs[0], runs[1]
	w.openSince(stuck, 31*time.Minute)
	w.wokenWith(task, "made no progress")
	out := w.must(task, "restart_run", `{"run":"`+stuck+`","note":"Read the worker yourself; do not use the task tool."}`)
	fresh, _ := out["run"].(string)
	if fresh == "" || fresh == stuck {
		t.Fatalf("restart_run answered %v", out)
	}
	var phase, category, oldCategory, replaced, status string
	_ = w.owner.QueryRow(context.Background(), `SELECT n.phase::text, COALESCE(n.category, ''), COALESCE(o.category, ''),
			COALESCE(o.replaced_by, ''), o.status::text
		FROM runs n, runs o WHERE n.id = $1 AND o.id = $2`, fresh, stuck).Scan(&phase, &category, &oldCategory, &replaced, &status)
	if phase != "review" || category != oldCategory || replaced != fresh || status != "aborted" {
		t.Errorf("new %s/%s for %s; old replaced by %q, %s", phase, category, oldCategory, replaced, status)
	}
	var pending string
	_ = w.owner.QueryRow(context.Background(), `SELECT state->>'pendingRunIds' FROM workflow_runs WHERE task_id = $1`, task).Scan(&pending)
	if !strings.Contains(pending, fresh) || strings.Contains(pending, stuck) || !strings.Contains(pending, other) {
		t.Errorf("the delivery waits on %s", pending)
	}
	w.until("the new reviewer on lux", func() bool { return w.luxRunOf(fresh) != "" })
	// Nothing resumes the Run it replaced: its lux Run is terminated.
	w.until("the replaced reviewer terminated in lux", func() bool {
		return slices.Contains(w.lux.CallsOf(w.luxRunOf(stuck)), "cancel")
	})
	if p := w.promptOf(fresh); !strings.Contains(p, "Restarted by the conductor: Read the worker yourself") {
		t.Errorf("the new reviewer is not told why:\n%s", p)
	}
	// The other reviewer finishes too: the round is complete.
	w.lux.FinishTools(w.luxRunOf(other))
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = $1`, other)
	w.until("the round's decision", func() bool { return w.decisionAt(task) != "" })
	if r := w.escalationReason(task); r != "" {
		t.Errorf("the round escalated %s", r)
	}
	var restarted map[string]any
	var raw []byte
	_ = w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE task_id = $1 AND event_type = 'run.restarted'`, task).Scan(&raw)
	_ = json.Unmarshal(raw, &restarted)
	if restarted["from"] != stuck || restarted["to"] != fresh || restarted["by"] != "conductor" ||
		!strings.Contains(restarted["note"].(string), "do not use the task tool") {
		t.Errorf("run.restarted = %v", restarted)
	}
}

// promptOf is what lux was told to tell a Run's agent.
func (w *world) promptOf(runID string) string {
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] == runID {
			return spec.Workload.Prompt
		}
	}
	return ""
}

// A tier given is checked, recorded and run on.
func TestRestartRunOnAnotherTier(t *testing.T) {
	w, task, runs := restartable(t)
	w.refused(task, "restart_run", `{"run":"`+runs[0]+`","note":"x","tier":"no such tier"}`, "no model tier")
	tier := w.onModel("implementer", "llm-big")
	out := w.must(task, "restart_run", `{"run":"`+runs[0]+`","note":"bigger model","tier":"`+onModelTier("llm-big")+`"}`)
	fresh, _ := out["run"].(string)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.restarted' AND payload->>'tier' = $2`, task, tier); n != 1 {
		t.Errorf("the tier is not recorded")
	}
	w.until("the new reviewer on lux", func() bool { return w.luxRunOf(fresh) != "" })
	var spec lux.Spec
	for _, r := range w.lux.Runs() {
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] == fresh {
			break
		}
	}
	if spec.Labels["dude.model"] != "llm-big" {
		t.Errorf("the restarted reviewer runs on %q, want llm-big", spec.Labels["dude.model"])
	}
}

// Refused for another task's Run, a Run the delivery does not wait on, and
// one that ended.
func TestRestartRunIsRefusedForRunsNotItsToRestart(t *testing.T) {
	w, task, runs := restartable(t)
	otherTask := w.task()
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_elsewhere', $1, $2, $3, 1, 'running', 'review', 'reviewer')`, w.org, w.project, otherTask)
	w.refused(task, "restart_run", `{"run":"run_elsewhere","note":"x"}`, "not a Run of this task")
	var impl string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task).Scan(&impl)
	w.refused(task, "restart_run", `{"run":"`+impl+`","note":"x"}`, "has ended")
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_aside', $1, $2, $3, 1, 'running', 'review', 'reviewer')`, w.org, w.project, task)
	w.refused(task, "restart_run", `{"run":"run_aside","note":"x"}`, "not a Run the delivery waits on")
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task); n != 3 {
		t.Errorf("%d reviewers after refused restarts", n)
	}
	_ = runs
}

// A plain delivery whose reviewer hangs: its owner's Restart does the same,
// as them, and the round goes on to the pull request.
func TestTheOwnersRestartGoesOnFromTheBanner(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	w.hangingReviews(taskCall)
	wi := w.task()
	w.deliver(wi)
	w.assignOwner(wi, ana)
	runs := w.reviewersOpen(wi, 2)
	w.openSince(runs[0], 3*time.Hour)
	w.pump()
	if w.stalls(runs[0]) != 1 {
		t.Fatalf("the owner was not told")
	}
	if status, _ := w.callAs(bo, "/internal/runs/"+runs[0]+"/restart", map[string]any{"note": "x"}); status != 403 {
		t.Errorf("someone else restarted it: %d", status)
	}
	for _, id := range runs {
		status, body := w.callAs(ana, "/internal/runs/"+id+"/restart", map[string]any{"note": "Try again without sub-agents."})
		if status != 200 {
			t.Fatalf("restart: %d %v", status, body)
		}
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.restarted' AND actor_id = $2
		AND NOT payload ? 'by'`, wi, ana); n != 2 {
		t.Errorf("%d restarts recorded as Ana's, want 2", n)
	}
	if status, _ := w.callAs(ana, "/internal/runs/"+runs[0]+"/restart", map[string]any{}); status != 409 {
		t.Errorf("restarting a Run that ended: %d", status)
	}
}
