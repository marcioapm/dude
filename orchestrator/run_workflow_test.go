package orchestrator_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
)

const (
	ciWorkflow     = "name: CI\non:\n  push:\n  pull_request:\n  workflow_dispatch:\njobs: {}\n"
	deployWorkflow = "name: Deploy\non:\n  workflow_dispatch:\n    inputs:\n      env: {}\njobs: {}\n"
	pushWorkflow   = "name: Lint\non: [push, pull_request]\njobs: {}\n"
	// A production deploy on every push to main, and a release on tags:
	// both run on push, and neither is this change's CI.
	prodWorkflow    = "name: Prod\non:\n  push:\n    branches: [main]\n  workflow_dispatch:\njobs: {}\n"
	releaseWorkflow = "name: Release\non:\n  push:\n    tags: ['v*']\n  workflow_dispatch:\njobs: {}\n"
	nightlyWorkflow = "name: Nightly\non:\n  schedule:\n    - cron: '0 3 * * *'\n  workflow_dispatch:\njobs: {}\n"
	docsWorkflow    = "name: Docs\non:\n  workflow_dispatch:\njobs: {}\n"
)

// dispatchWorld is a conducted task in review whose branch has these
// workflows: ci.yml (CI, dispatchable), deploy.yml and deploy-docs.yml
// (dispatch only), lint.yml (no workflow_dispatch), prod.yml (push to
// main), release.yml (push tags) and nightly.yml (schedule).
func dispatchWorld(t *testing.T) (*world, string, string) {
	t.Helper()
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	branch := w.str(`SELECT head_branch FROM pull_requests WHERE task_id = $1`, task)
	w.gh.CommitFiles(branch, "workflows", map[string]string{".github/workflows/ci.yml": ciWorkflow,
		".github/workflows/deploy.yml": deployWorkflow, ".github/workflows/lint.yml": pushWorkflow,
		".github/workflows/prod.yml": prodWorkflow, ".github/workflows/release.yml": releaseWorkflow,
		".github/workflows/nightly.yml": nightlyWorkflow, ".github/workflows/deploy-docs.yml": docsWorkflow})
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
// that is not CI needs the person's answered question on this task offering
// the approving choice for that workflow and ref, answered with it: not
// another task's, not an unanswered one, not one about something else, not
// one naming deploy-docs.yml for deploy.yml, not a "no", and not one spent
// on a dispatch already.
func TestRunWorkflowNeedsThePersonsAnswerForAWorkflowThatIsNotCI(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	w.dispatched(task, `{"workflow":"ci.yml"}`)

	approve := delivery.DispatchApproval("deploy.yml", branch)
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml"}`, "it is not CI this change already runs, and the person decides it. Ask them (ask_person) with the choices \""+approve+"\" and \""+delivery.DispatchDecline+"\"")
	other := w.task()
	choices := `["` + approve + `", "` + delivery.DispatchDecline + `"]`
	docs := `["` + delivery.DispatchApproval("deploy-docs.yml", branch) + `", "` + delivery.DispatchDecline + `"]`
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, prompt, options, status, answer, answered_at, addressee)
		VALUES ('q_other', $1, $2, 'Run deploy.yml?', $4::jsonb, 'answered', $5, now(), 'person'),
		       ('q_open', $1, $3, 'Run deploy.yml?', $4::jsonb, 'open', NULL, NULL, 'person'),
		       ('q_conductor', $1, $3, 'Run deploy.yml?', $4::jsonb, 'answered', $5, now(), 'conductor'),
		       ('q_else', $1, $3, 'Merge it?', '["Yes", "No"]', 'answered', 'Yes', now(), 'person'),
		       ('q_prose', $1, $3, 'May I run deploy.yml on the branch?', '[]', 'answered', 'yes', now(), 'person'),
		       ('q_no', $1, $3, 'Run deploy.yml?', $4::jsonb, 'answered', 'No. Do not deploy.', now(), 'person'),
		       ('q_decline', $1, $3, 'Run deploy.yml?', $4::jsonb, 'answered', $7, now(), 'person'),
		       ('q_docs', $1, $3, 'Run deploy-docs.yml?', $6::jsonb, 'answered', $8, now(), 'person'),
		       ('q_deploy', $1, $3, 'Run deploy.yml?', $4::jsonb, 'answered', $5, now(), 'person')`,
		w.org, other, task, choices, approve, docs, delivery.DispatchDecline, delivery.DispatchApproval("deploy-docs.yml", branch))
	for _, q := range []string{"q_other", "q_open", "q_conductor"} {
		w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"`+q+`"}`, q+" is not a question of this task a person has answered")
	}
	for _, q := range []string{"q_else", "q_prose", "q_docs"} {
		w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"`+q+`"}`, q+" does not offer the choice \""+approve+"\"")
	}
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_no"}`, `q_no was answered "No. Do not deploy.", not "`+approve+`": the person did not approve`)
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_decline"}`, "the person did not approve")
	// Approved for this ref, not another.
	w.gh.Branch("release", branch, "release")
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","ref":"release","questionId":"q_deploy"}`, "q_deploy does not offer the choice")
	id := w.dispatched(task, `{"workflow":"deploy.yml","questionId":"q_deploy","inputs":{"env":"staging"}}`)
	// One answer, one dispatch.
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_deploy","inputs":{"env":"production"}}`, "q_deploy already authorized dispatch "+id)

	w.gh.Set(func(s *fakegithub.Server) {
		if len(s.Dispatches) != 2 || s.Dispatches[1].Workflow != "deploy.yml" || s.Dispatches[1].Ref != branch || s.Dispatches[1].Inputs["env"] != "staging" {
			t.Errorf("dispatched %+v", s.Dispatches)
		}
	})
	if got := w.str(`SELECT workflow || ' ' || ref || ' ' || (inputs->>'env') || ' ' || question_id || ' ' || state
		FROM workflow_dispatches WHERE id = $1`, id); got != "deploy.yml "+branch+" staging q_deploy bound" {
		t.Errorf("dispatch row %q", got)
	}
}

// Free CI is a workflow that runs for this task's own change, dispatched
// on the task's branch. A deploy on push to main, a release on tags, a
// schedule, and CI on another ref all need the person.
func TestOnlyTheTasksOwnCIIsFree(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	for _, wf := range []string{"prod.yml", "release.yml", "nightly.yml"} {
		w.refused(task, "run_workflow", `{"workflow":"`+wf+`"}`, "it is not CI this change already runs")
	}
	w.gh.Branch("release", branch, "release")
	w.refused(task, "run_workflow", `{"workflow":"ci.yml","ref":"release"}`, "release is not this task's branch")
	// refs/heads/ is stripped: the task's branch, free.
	id := w.dispatched(task, `{"workflow":"ci.yml","ref":"refs/heads/`+branch+`"}`)
	if got := w.str(`SELECT ref FROM workflow_dispatches WHERE id = $1`, id); got != branch {
		t.Errorf("ref %q", got)
	}
	w.gh.Set(func(s *fakegithub.Server) {
		if len(s.Dispatches) != 1 || s.Dispatches[0].Ref != branch {
			t.Errorf("dispatched %+v", s.Dispatches)
		}
	})
}

// An older GitHub (204, no run id): exactly one new candidate run is
// bound; two are unknown, never a guess; none is unknown only once the
// bound has passed.
func TestADispatchBindsOnlyItsOneRun(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchLegacy = true })
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
	three := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.supervise()
	if state := w.str(`SELECT state FROM workflow_dispatches WHERE id = $1`, three); state != delivery.DispatchBinding {
		t.Fatalf("none within the bound: %s", state)
	}
	mustExec(t, w.owner, `UPDATE workflow_dispatches SET dispatched_at = now() - interval '6 minutes' WHERE id = $1`, three)
	w.supervise()
	if got := w.str(`SELECT state || ': ' || reason FROM workflow_dispatches WHERE id = $1`, three); !strings.HasPrefix(got, "unknown: no run of ci.yml on "+branch) {
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
	// A racing pass that read the run before the first one committed
	// settles the same completed read: still one wake.
	done := forge.RunState{ID: 920, Status: "completed", Conclusion: "failure", Attempt: 1}
	for range 2 {
		if err := w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			return delivery.SettleDispatchTx(context.Background(), tx, w.org, id, delivery.DispatchRead{Run: &done})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.workflowWakes(task)); n != 1 {
		t.Fatalf("%d wakes after racing passes", n)
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

// A re-run is the same run with attempt n+1, and GitHub re-runs only a
// completed run: the completed dispatch is still read, the re-run reopens
// it, the stale outcome is not reported, and the new attempt's outcome
// wakes the conductor again.
func TestARerunAttemptIsFollowed(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 940 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.supervise()
	if err := w.gh.Rerun(940); err == nil {
		t.Fatal("the fake re-ran a run still queued")
	}
	w.gh.UpdateWorkflowRun(940, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "failure" })
	w.supervise()
	if wakes := w.workflowWakes(task); len(wakes) != 1 || !strings.Contains(wakes[0], "completed: failure, attempt 1") {
		t.Fatalf("wakes %q", wakes)
	}
	if err := w.gh.Rerun(940); err != nil {
		t.Fatal(err)
	}
	w.supervise()
	if got := w.str(`SELECT state || ' ' || run_attempt || ' ' || run_status || ' ' || COALESCE(outcome, 'none') FROM workflow_dispatches WHERE id = $1`, id); got != "bound 2 queued none" {
		t.Fatalf("stored %s", got)
	}
	// A wait opened now waits on the re-run: the old failure is not told.
	out := w.must(task, "wait_for", `{"conditions":[{"workflowEnded":"`+id+`"}],"deadline":"30m"}`)
	if out["holds"] == true {
		t.Fatalf("a wait on a re-run held on the stale outcome: %v", out)
	}
	w.must(task, "wait_for", `{"deadline":"30m"}`)
	w.gh.UpdateWorkflowRun(940, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "success" })
	w.supervise()
	if wakes := w.workflowWakes(task); len(wakes) != 2 || !strings.Contains(wakes[1], "completed: success, attempt 2") {
		t.Fatalf("wakes %q", wakes)
	}
	// Past DispatchFollowAfter it is no longer read.
	mustExec(t, w.owner, `UPDATE workflow_dispatches SET completed_at = now() - interval '31 minutes', last_read_at = NULL WHERE id = $1`, id)
	w.supervise()
	if n := w.count(`SELECT count(*) FROM workflow_dispatches WHERE id = $1 AND last_read_at IS NULL`, id); n != 1 {
		t.Error("a completed dispatch past the bound was read")
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
