package orchestrator_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
)

const (
	ciWorkflow     = "name: CI\non:\n  push:\n  pull_request:\n  workflow_dispatch:\njobs: {}\n"
	deployWorkflow = "name: Deploy\non:\n  workflow_dispatch:\n    inputs:\n      env: {}\njobs: {}\n"
	pushWorkflow   = "name: Lint\non: [push, pull_request]\njobs: {}\n"
)

// dispatchWorld is a conducted task in review whose branch has three
// workflows: ci.yml (CI, dispatchable), deploy.yml (dispatch only) and
// lint.yml (no workflow_dispatch).
func dispatchWorld(t *testing.T) (*world, string, string) {
	t.Helper()
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	branch := w.str(`SELECT head_branch FROM pull_requests WHERE task_id = $1`, task)
	w.gh.CommitFiles(branch, "workflows", map[string]string{".github/workflows/ci.yml": ciWorkflow,
		".github/workflows/deploy.yml": deployWorkflow, ".github/workflows/lint.yml": pushWorkflow})
	return w, task, branch
}

// supervise is one pass of the workflow supervisor, every dispatch due.
func (w *world) supervise() {
	w.t.Helper()
	if _, err := w.prs.SuperviseDispatches(context.Background(), 0); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) workflowWakes(task string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'workflow' ORDER BY created_at`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (w *world) dispatched(task, args string) string {
	w.t.Helper()
	out := w.must(task, "run_workflow", args)
	id, _ := out["dispatchId"].(string)
	if id == "" {
		w.t.Fatalf("run_workflow: %v", out)
	}
	return id
}

// A workflow with no workflow_dispatch trigger at the ref is refused, and
// nothing is dispatched or recorded.
func TestRunWorkflowRefusesAWorkflowWithNoDispatchTrigger(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.refused(task, "run_workflow", `{"workflow":"lint.yml"}`, "has no workflow_dispatch trigger")
	w.refused(task, "run_workflow", `{"workflow":"nope.yml"}`, "there is no .github/workflows/nope.yml")
	w.gh.Set(func(s *fakegithub.Server) {
		if len(s.Dispatches) != 0 {
			t.Errorf("dispatched %v", s.Dispatches)
		}
	})
	if n := w.count(`SELECT count(*) FROM workflow_dispatches WHERE task_id = $1`, task); n != 0 {
		t.Errorf("%d dispatches recorded", n)
	}
}

// CI the change already runs is the conductor's to dispatch; a workflow
// that is not CI needs the person's answered question on this task naming
// it: not another task's, not an unanswered one, not one about something
// else.
func TestRunWorkflowNeedsThePersonsAnswerForAWorkflowThatIsNotCI(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	w.dispatched(task, `{"workflow":"ci.yml"}`)

	w.refused(task, "run_workflow", `{"workflow":"deploy.yml"}`, "it is not CI this change already runs, and the person decides it. Ask them (ask_person)")
	other := w.task()
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, prompt, status, answer, answered_at, addressee)
		VALUES ('q_other', $1, $2, 'Run deploy?', 'answered', 'yes', now(), 'person'),
		       ('q_open', $1, $3, 'Run deploy?', 'open', NULL, NULL, 'person'),
		       ('q_conductor', $1, $3, 'Run deploy?', 'answered', 'yes', now(), 'conductor'),
		       ('q_else', $1, $3, 'Merge it?', 'answered', 'yes', now(), 'person'),
		       ('q_deploy', $1, $3, 'May I run deploy on the branch?', 'answered', 'yes', now(), 'person')`, w.org, other, task)
	for _, q := range []string{"q_other", "q_open", "q_conductor"} {
		w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"`+q+`"}`, q+" is not a question of this task a person has answered")
	}
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_else"}`, "q_else does not name deploy.yml")
	id := w.dispatched(task, `{"workflow":"deploy.yml","questionId":"q_deploy","inputs":{"env":"staging"}}`)

	w.gh.Set(func(s *fakegithub.Server) {
		if len(s.Dispatches) != 2 || s.Dispatches[1].Workflow != "deploy.yml" || s.Dispatches[1].Ref != branch || s.Dispatches[1].Inputs["env"] != "staging" {
			t.Errorf("dispatched %+v", s.Dispatches)
		}
	})
	if got := w.str(`SELECT workflow || ' ' || ref || ' ' || (inputs->>'env') || ' ' || question_id || ' ' || state
		FROM workflow_dispatches WHERE id = $1`, id); got != "deploy.yml "+branch+" staging q_deploy binding" {
		t.Errorf("dispatch row %q", got)
	}
}

// Exactly one candidate run is bound; two are unknown, never a guess; none
// is unknown only once the bound has passed.
func TestADispatchBindsOnlyItsOneRun(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	one := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.supervise()
	if state := w.str(`SELECT state FROM workflow_dispatches WHERE id = $1`, one); state != delivery.DispatchBinding {
		t.Fatalf("no run yet: %s", state)
	}
	now := w.str(`SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`)
	w.gh.SetWorkflowRun(901, fakegithub.WorkflowRun{Workflow: "ci.yml", Branch: branch, Event: "workflow_dispatch", Status: "queued", CreatedAt: now})
	w.gh.SetWorkflowRun(950, fakegithub.WorkflowRun{Workflow: "ci.yml", Branch: branch, Event: "push", Status: "queued", CreatedAt: now})
	w.supervise()
	if got := w.str(`SELECT state || ' ' || run_id || ' ' || run_status FROM workflow_dispatches WHERE id = $1`, one); got != "bound 901 queued" {
		t.Fatalf("one candidate: %s", got)
	}

	// Two more runs for a second dispatch: which is it is not told.
	two := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.gh.SetWorkflowRun(902, fakegithub.WorkflowRun{Workflow: "ci.yml", Branch: branch, Event: "workflow_dispatch", Status: "queued", CreatedAt: now})
	w.gh.SetWorkflowRun(903, fakegithub.WorkflowRun{Workflow: "ci.yml", Branch: branch, Event: "workflow_dispatch", Status: "queued", CreatedAt: now})
	w.supervise()
	if got := w.str(`SELECT state || ' ' || COALESCE(run_id::text, 'none') FROM workflow_dispatches WHERE id = $1`, two); got != "unknown none" {
		t.Fatalf("two candidates: %s", got)
	}
	if wakes := w.workflowWakes(task); len(wakes) != 1 || !strings.Contains(wakes[0], "dude does not guess") || !strings.Contains(wakes[0], "is unknown") {
		t.Fatalf("wakes %q", wakes)
	}

	// None: binding while within the bound, unknown past it.
	w.gh.Branch("release", branch, "release")
	three := w.dispatched(task, `{"workflow":"ci.yml","ref":"release"}`)
	w.supervise()
	if state := w.str(`SELECT state FROM workflow_dispatches WHERE id = $1`, three); state != delivery.DispatchBinding {
		t.Fatalf("none within the bound: %s", state)
	}
	mustExec(t, w.owner, `UPDATE workflow_dispatches SET dispatched_at = now() - interval '6 minutes' WHERE id = $1`, three)
	w.supervise()
	if got := w.str(`SELECT state || ': ' || reason FROM workflow_dispatches WHERE id = $1`, three); !strings.HasPrefix(got, "unknown: no run of ci.yml on release") {
		t.Fatalf("none past the bound: %s", got)
	}
}

// Level-triggered: with no webhook or event delivered at all, a run that
// completes on GitHub is noticed by the next pass, from the state it reads
// and stores; and its outcome wakes the conductor once, across passes and
// a restart of the supervisor.
func TestAMissedEventChangesNothingAndTheOutcomeWakesOnce(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 920 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.supervise()
	if got := w.str(`SELECT state || ' ' || run_id || ' ' || run_status FROM workflow_dispatches WHERE id = $1`, id); got != "bound 920 queued" {
		t.Fatalf("bound: %s", got)
	}
	w.gh.UpdateWorkflowRun(920, func(r *fakegithub.WorkflowRun) { r.Status = "in_progress" })
	w.supervise()
	if len(w.workflowWakes(task)) != 0 {
		t.Fatal("woken while running")
	}
	// Completed on GitHub; no webhook is sent, no event delivered.
	w.gh.UpdateWorkflowRun(920, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "failure" })
	w.supervise()
	wakes := w.workflowWakes(task)
	if len(wakes) != 1 || !strings.Contains(wakes[0], "completed: failure, attempt 1 (https://github.com/acme/target/actions/runs/920)") {
		t.Fatalf("wakes %q", wakes)
	}
	// More passes, and a new supervisor as after a restart: no second wake.
	w.supervise()
	restarted := &prs.Syncer{DB: w.prs.DB, Forges: w.prs.Forges, Log: w.log}
	for range 3 {
		mustExec(t, w.owner, `UPDATE workflow_dispatches SET last_read_at = NULL WHERE id = $1`, id)
		if _, err := restarted.SuperviseDispatches(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.workflowWakes(task)); n != 1 {
		t.Fatalf("%d wakes for one outcome", n)
	}
	w.until("the conductor told the conclusion", func() bool {
		for _, s := range w.woken(task) {
			if strings.Contains(s, "completed: failure") {
				return true
			}
		}
		return false
	})
}

// Reads that fail in a row are "cannot tell", never "not finished": past
// the bound the conductor is told dude cannot see the run, with the
// error. A failure followed by a read that succeeds resets the count.
func TestFailedReadsWakeCannotSeeAndASuccessResetsTheCount(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 930 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.supervise()
	failing := func(on bool) {
		w.gh.Set(func(s *fakegithub.Server) {
			s.Intercept = nil
			if on {
				s.Intercept = func(r *http.Request) int {
					if strings.Contains(r.URL.Path, "/actions/runs/930") {
						return http.StatusBadGateway
					}
					return 0
				}
			}
		})
	}
	failing(true)
	w.supervise()
	w.supervise()
	failing(false)
	w.supervise()
	if n := w.count(`SELECT failed_reads FROM workflow_dispatches WHERE id = $1`, id); n != 0 {
		t.Fatalf("a read that succeeded left %d failed reads", n)
	}
	failing(true)
	for range delivery.DispatchCannotSeeReads - 1 {
		w.supervise()
	}
	if len(w.workflowWakes(task)) != 0 {
		t.Fatal("woken before the bound")
	}
	w.supervise()
	wakes := w.workflowWakes(task)
	if len(wakes) != 1 || !strings.Contains(wakes[0], "dude cannot see this workflow run") || !strings.Contains(wakes[0], "502") ||
		strings.Contains(wakes[0], "completed") {
		t.Fatalf("wakes %q", wakes)
	}
	w.supervise()
	if n := len(w.workflowWakes(task)); n != 1 {
		t.Fatalf("%d cannot-see wakes for one stretch", n)
	}
}

// A re-run is the same run with attempt n+1: supervision follows the
// latest attempt and the outcome names the one it ended on.
func TestARerunAttemptIsFollowed(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 940 })
	w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.supervise()
	w.gh.UpdateWorkflowRun(940, func(r *fakegithub.WorkflowRun) { r.Status, r.Attempt = "in_progress", 2 })
	w.supervise()
	if got := w.str(`SELECT run_attempt || ' ' || run_status FROM workflow_dispatches WHERE task_id = $1`, task); got != "2 in_progress" {
		t.Fatalf("stored %s", got)
	}
	w.gh.UpdateWorkflowRun(940, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion, r.Attempt = "completed", "success", 3 })
	w.supervise()
	if wakes := w.workflowWakes(task); len(wakes) != 1 || !strings.Contains(wakes[0], "completed: success, attempt 3") {
		t.Fatalf("wakes %q", wakes)
	}
}

// wait_for workflowEnded: at its deadline with the run still going, the
// wake says "still running, last seen <state> at <time>", never failed;
// and a wait open on a dispatch carries its outcome, with no second wake.
func TestAWorkflowWaitsDeadlineReportsTheLastSeenState(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 960 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.refused(task, "wait_for", `{"conditions":[{"workflowEnded":"wfd_nope"}],"deadline":"30m"}`, "wfd_nope is not a workflow this task dispatched")
	out := w.must(task, "wait_for", `{"conditions":[{"workflowEnded":"`+id+`"}],"deadline":"30m"}`)
	waitID, _ := out["waitId"].(string)
	w.gh.UpdateWorkflowRun(960, func(r *fakegithub.WorkflowRun) { r.Status = "in_progress" })
	w.supervise()
	w.pump()
	mustExec(t, w.owner, `UPDATE conductor_waits SET deadline_at = now() - interval '1 second' WHERE id = $1`, waitID)
	w.pump()
	digest := w.str(`SELECT COALESCE(digest, '') FROM conductor_waits WHERE id = $1`, waitID)
	if !strings.Contains(digest, "workflow ci.yml ("+id+") still running, last seen in_progress at ") || strings.Contains(digest, "fail") {
		t.Fatalf("deadline digest %q", digest)
	}

	// Waited on again; it completes: the wait's wake is the one wake.
	out = w.must(task, "wait_for", `{"conditions":[{"workflowEnded":"`+id+`"}],"deadline":"30m"}`)
	waitID, _ = out["waitId"].(string)
	w.gh.UpdateWorkflowRun(960, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "success" })
	w.supervise()
	w.pump()
	if by := w.str(`SELECT COALESCE(ended_by, '') FROM conductor_waits WHERE id = $1`, waitID); by != delivery.WaitHeld {
		t.Fatalf("the wait ended %q", by)
	}
	if digest := w.str(`SELECT digest FROM conductor_waits WHERE id = $1`, waitID); !strings.Contains(digest, "completed: success") {
		t.Errorf("digest %q", digest)
	}
	if wakes := w.workflowWakes(task); len(wakes) != 0 {
		t.Errorf("a second wake beside the wait's: %q", wakes)
	}
	// pull_requests lists it as stored.
	listed := w.must(task, "pull_requests", `{}`)
	ds, _ := listed["dispatches"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["conclusion"] != "success" || ds[0].(map[string]any)["state"] != "completed" {
		t.Errorf("pull_requests dispatches %v", listed["dispatches"])
	}
}
