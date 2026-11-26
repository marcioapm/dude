package orchestrator_test

// github_branch_runs, called with a conductor's Run token against the fake
// GitHub: the base branch's recent runs, newest first, each with its
// jobs' conclusions; refused outside the task's repositories with nothing
// asked of GitHub; counted against the hourly reads.

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// mainHistory gives acme/target three runs on main, in no particular id
// order, and one on another branch: unit failed on one of them only.
func mainHistory(w *world) {
	job := func(id int64, name, conclusion string) fakegithub.WorkflowJob {
		return fakegithub.WorkflowJob{ID: id, Name: name, Status: "completed", Conclusion: conclusion}
	}
	w.gh.SetWorkflowRun(43000000700, fakegithub.WorkflowRun{Name: "CI", Branch: "main", Event: "push", HeadSHA: "sha700", Status: "completed",
		Conclusion: "success", CreatedAt: "2026-11-11T09:00:00Z", Jobs: []fakegithub.WorkflowJob{job(7001, "unit", "success"), job(7002, "lint", "success")}})
	w.gh.SetWorkflowRun(43000000702, fakegithub.WorkflowRun{Name: "CI", Branch: "main", Event: "push", HeadSHA: "sha702", Status: "completed",
		Conclusion: "success", CreatedAt: "2026-11-11T11:00:00Z", Jobs: []fakegithub.WorkflowJob{job(7021, "unit", "success"), job(7022, "lint", "success")}})
	w.gh.SetWorkflowRun(43000000701, fakegithub.WorkflowRun{Name: "CI", Branch: "main", Event: "push", HeadSHA: "sha701", Status: "completed",
		Conclusion: "failure", CreatedAt: "2026-11-11T10:00:00Z", Jobs: []fakegithub.WorkflowJob{job(7011, "unit", "failure"), job(7012, "lint", "success")}})
	w.gh.SetWorkflowRun(43000000703, fakegithub.WorkflowRun{Name: "CI", Branch: "feature", Event: "pull_request", HeadSHA: "sha703", Status: "completed",
		Conclusion: "failure", CreatedAt: "2026-11-11T12:00:00Z", Jobs: []fakegithub.WorkflowJob{job(7031, "unit", "failure")}})
}

func TestAConductorReadsTheBaseBranchsRecentRuns(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	mainHistory(w)
	for _, args := range []string{`{"repo":"acme/target"}`, `{}`, `{"repo":"acme/target","branch":"main"}`} {
		out := w.must(task, "github_branch_runs", args)
		if branch, _ := quoted(field(out, "branch").(string)); branch != "main" || field(out, "repo") != "acme/target" {
			t.Fatalf("%s: %v", args, out)
		}
		runs, _ := out["runs"].([]any)
		if len(runs) != 3 {
			t.Fatalf("%s: %d runs, want main's 3: %v", args, len(runs), runs)
		}
		for i, want := range []struct {
			id         float64
			sha        string
			unit       string
			createdAt  string
			conclusion string
		}{{43000000702, "sha702", "success", "2026-11-11T11:00:00Z", "success"}, {43000000701, "sha701", "failure", "2026-11-11T10:00:00Z", "failure"},
			{43000000700, "sha700", "success", "2026-11-11T09:00:00Z", "success"}} {
			run := runs[i]
			if field(run, "id") != want.id || field(run, "headSha") != want.sha || field(run, "createdAt") != want.createdAt ||
				field(run, "conclusion") != want.conclusion {
				t.Fatalf("%s: run %d is %v, want %v", args, i, run, want)
			}
			if name, _ := quoted(field(run, "jobs", 0, "name").(string)); name != "unit" || field(run, "jobs", 0, "conclusion") != want.unit {
				t.Fatalf("%s: run %d's jobs %v", args, i, field(run, "jobs"))
			}
		}
		if !strings.Contains(field(out, "next").(string), "github_check_log") {
			t.Fatalf("%s: next %v", args, out["next"])
		}
	}
	other := w.must(task, "github_branch_runs", `{"repo":"acme/target","branch":"feature"}`)
	if runs, _ := other["runs"].([]any); len(runs) != 1 || field(runs[0], "id") != float64(43000000703) {
		t.Fatalf("feature's runs: %v", other)
	}
	// Recorded as reads, without what GitHub said.
	var payload string
	if err := w.owner.QueryRow(context.Background(), `SELECT payload::text FROM events WHERE task_id = $1 AND event_type = 'github.read'
		AND payload->>'tool' = 'github_branch_runs' ORDER BY cursor LIMIT 1`, task).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"branch": "main"`) || !strings.Contains(payload, `"repo": "acme/target"`) || strings.Contains(payload, "sha70") {
		t.Fatalf("ledger %s", payload)
	}
}

func TestBranchRunsOutsideTheTasksRepositoriesAreRefusedUnasked(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	mainHistory(w)
	before := w.requests()
	w.refused(task, "github_branch_runs", `{"repo":"acme/web"}`, "acme/web is not a repository you may read: only your task's project, acme/target")
	w.refused(task, "github_branch_runs", `{"repo":"acme/target","branch":"`+strings.Repeat("b", 256)+`"}`, "branch: at most 255 characters")
	if w.requests() != before {
		t.Error("a refused branch runs read reached GitHub")
	}
}

func TestBranchRunsReadsCountAgainstTheHour(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	mainHistory(w)
	w.must(task, "github_branch_runs", `{"repo":"acme/target"}`)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.tool.attempted'
		AND payload->>'tool' = 'github_branch_runs'`, task); n != 1 {
		t.Fatalf("%d branch runs attempts", n)
	}
	// 58 more reads of the hour: with the one above, 59; one is left.
	w.spendGitHubReads(task, 58)
	w.must(task, "github_branch_runs", `{"repo":"acme/target"}`)
	before := w.requests()
	w.refused(task, "github_branch_runs", `{"repo":"acme/target"}`, "at most 60 GitHub reads an hour")
	// Its reads count against the other GitHub tools' hour too.
	w.refused(task, "github_issue", `{"repo":"acme/target","number":1}`, "at most 60 GitHub reads an hour")
	if w.requests() != before {
		t.Error("the 61st read reached GitHub")
	}
}

// What GitHub said (a run's head SHA, a job's name) is in none of the
// task's events: the read is recorded by its size, the call's result
// withheld, as for the other GitHub reads.
func TestBranchRunsResultStaysOutOfTheLedger(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	mainHistory(w)
	w.gh.SetWorkflowRun(43000000704, fakegithub.WorkflowRun{Name: "CI", Branch: "main", Event: "push", HeadSHA: "okapi-sha-704", Status: "completed",
		Conclusion: "failure", CreatedAt: "2026-11-11T13:00:00Z", Jobs: []fakegithub.WorkflowJob{
			{ID: 50000007041, Name: "pangolin-job", Status: "completed", Conclusion: "failure"}}})
	out := w.must(task, "github_branch_runs", `{"repo":"acme/target"}`)
	if field(out, "runs", 0, "headSha") != "okapi-sha-704" {
		t.Fatalf("read %v", out)
	}
	for _, word := range []string{"okapi-sha-704", "pangolin-job"} {
		if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload::text LIKE '%' || $2 || '%'`, task, word); c != 0 {
			t.Errorf("%q is in %d of the task's events", word, c)
		}
	}
}

// nightlies gives acme/target's main two scheduled runs of the nightly
// workflow, older than its pushes: the newest runs alone never show them.
func nightlies(w *world) {
	for i, id := range []int64{43000000800, 43000000801} {
		w.gh.SetWorkflowRun(id, fakegithub.WorkflowRun{Name: "Nightly warehouse", Branch: "main", Event: "schedule", HeadSHA: "sha80" + string(rune('0'+i)),
			Status: "completed", Conclusion: "failure", CreatedAt: "2026-11-10T03:00:0" + string(rune('0'+i)) + "Z",
			Workflow: "nightly_warehouse_snowflake.yml", WorkflowID: 991,
			Jobs: []fakegithub.WorkflowJob{{ID: id*10 + 1, Name: "warehouse", Status: "completed", Conclusion: "failure"}}})
	}
	w.gh.SetWorkflowRun(43000000802, fakegithub.WorkflowRun{Name: "Nightly warehouse", Branch: "main", Event: "workflow_dispatch", HeadSHA: "sha802",
		Status: "completed", Conclusion: "success", CreatedAt: "2026-11-10T05:00:00Z", Workflow: "nightly_warehouse_snowflake.yml", WorkflowID: 991})
}

func runIDs(out map[string]any) []float64 {
	var ids []float64
	runs, _ := out["runs"].([]any)
	for _, r := range runs {
		ids = append(ids, field(r, "id").(float64))
	}
	return ids
}

// A workflow (its file or id) and an event narrow the branch's runs: a
// nightly is found by event schedule and its workflow, on the default
// branch when none is named. Each is its own read, recorded with what
// narrowed it: two reads differing only by event are two answers.
func TestBranchRunsNarrowedByWorkflowAndEvent(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	mainHistory(w)
	nightlies(w)
	if got := runIDs(w.must(task, "github_branch_runs", `{"repo":"acme/target"}`)); len(got) != 6 || got[0] != 43000000702 {
		t.Fatalf("unnarrowed: %v", got)
	}
	for _, tc := range []struct {
		args string
		want []float64
	}{
		{`{"event":"schedule"}`, []float64{43000000801, 43000000800}},
		{`{"repo":"acme/target","workflow":"nightly_warehouse_snowflake.yml"}`, []float64{43000000802, 43000000801, 43000000800}},
		{`{"workflow":"991","event":"schedule","branch":"main"}`, []float64{43000000801, 43000000800}},
		{`{"workflow":"nightly_warehouse_snowflake.yml","event":"workflow_dispatch"}`, []float64{43000000802}},
	} {
		out := w.must(task, "github_branch_runs", tc.args)
		if got := runIDs(out); !slices.Equal(got, tc.want) {
			t.Errorf("%s: runs %v, want %v", tc.args, got, tc.want)
		}
		if branch, _ := quoted(field(out, "branch").(string)); branch != "main" {
			t.Errorf("%s: branch %v", tc.args, out["branch"])
		}
	}
	// The last two reads differ only by event: two requests to GitHub's
	// workflow listing, two answers, two reads recorded and counted.
	listings := func() int {
		n := 0
		for _, r := range w.gh.Requests() {
			if strings.HasSuffix(r, "/actions/workflows/nightly_warehouse_snowflake.yml/runs") {
				n++
			}
		}
		return n
	}
	before := listings()
	attempts := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.tool.attempted' AND payload->>'tool' = 'github_branch_runs'`, task)
	schedule := runIDs(w.must(task, "github_branch_runs", `{"workflow":"nightly_warehouse_snowflake.yml","event":"schedule"}`))
	dispatch := runIDs(w.must(task, "github_branch_runs", `{"workflow":"nightly_warehouse_snowflake.yml","event":"workflow_dispatch"}`))
	if slices.Equal(schedule, dispatch) || listings() != before+2 {
		t.Fatalf("schedule %v, dispatch %v, %d listings", schedule, dispatch, listings()-before)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.tool.attempted' AND payload->>'tool' = 'github_branch_runs'`, task); n != attempts+2 {
		t.Fatalf("%d reads counted, want %d", n, attempts+2)
	}
	// schedule: the read just made; workflow_dispatch: the case above too.
	for event, want := range map[string]int{"schedule": 1, "workflow_dispatch": 2} {
		if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'github.read' AND payload->>'tool' = 'github_branch_runs'
			AND payload->>'workflow' = 'nightly_warehouse_snowflake.yml' AND payload->>'event' = $2 AND payload->>'branch' = 'main'`, task, event); n != want {
			t.Errorf("%d github.read events with event %s, want %d", n, event, want)
		}
	}
}

// A workflow that is not one file's bare name or an id, and an event
// that is not an event's name, are refused before GitHub is asked.
func TestBranchRunsRefuseABadWorkflowOrEventUnasked(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	mainHistory(w)
	before := w.requests()
	for _, workflow := range []string{"../ci.yml", ".github/workflows/ci.yml", "ci.json", "ci", "0", "-1", "ci..yml", "a%2Fb.yml",
		strings.Repeat("a", 252) + ".yml", "1.5", "ci.yml?x=1"} {
		w.refused(task, "github_branch_runs", `{"workflow":`+strconv.Quote(workflow)+`}`, "workflow is a workflow's file name ending .yml or .yaml")
	}
	for _, event := range []string{"Schedule", "pull-request", "push&x=1", "push ", "a1", strings.Repeat("a", 65)} {
		if strings.TrimSpace(event) != event {
			continue
		}
		w.refused(task, "github_branch_runs", `{"event":`+strconv.Quote(event)+`}`, "event is a GitHub Actions event name")
	}
	if w.requests() != before {
		t.Error("a refused branch runs read reached GitHub")
	}
}
