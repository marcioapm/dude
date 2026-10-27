package orchestrator_test

// github_run_jobs, called with a session's and a conductor's Run tokens
// against the fake GitHub: a run's jobs by id or by link, then a failed
// job's log through github_check_log; refused outside the caller's
// repositories, counted against the hourly reads.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// nightlyRun is acme/target's run 812: lint passed, integration failed at
// "Run tests".
func nightlyRun(w *world) {
	w.gh.SetWorkflowRun(812, fakegithub.WorkflowRun{Name: "Nightly", Branch: "main", Event: "schedule", HeadSHA: "abc123",
		Status: "completed", Conclusion: "failure", Jobs: []fakegithub.WorkflowJob{
			{ID: 9001, Name: "lint", Status: "completed", Conclusion: "success", Steps: []string{"Set up job", "Lint"}},
			{ID: 9002, Name: "integration", Status: "completed", Conclusion: "failure", Steps: []string{"Set up job"},
				FailedSteps: []string{"Run tests"}},
		}})
}

func TestARunsJobsLeadToTheFailedJobsLog(t *testing.T) {
	s, run := brainstorming(t)
	nightlyRun(s.world)
	downloads := httptest.NewServer(s.gh.LogDownloads())
	t.Cleanup(downloads.Close)
	s.gh.Set(func(g *fakegithub.Server) { g.CheckRuns = map[int64]fakegithub.CheckRun{9002: fakegithub.ActionsJob()} })
	s.gh.SetJobLog(9002, "2026-10-26T03:00:01.000Z FAIL TestExport: timeout after 30s\n2026-10-26T03:00:02.000Z ##[error]Process completed with exit code 1.\n",
		downloads.URL)

	for _, args := range []string{`{"repo":"acme/target","runId":"812"}`,
		`{"runId":"https://github.com/acme/target/actions/runs/812/job/9002"}`} {
		out := s.read(run, "github_run_jobs", args)
		if name, _ := quoted(field(out, "run", "name").(string)); name != "Nightly" || field(out, "run", "event") != "schedule" ||
			field(out, "run", "headSha") != "abc123" || field(out, "run", "id") != float64(812) {
			t.Fatalf("%s: run %v", args, out["run"])
		}
		failed := field(out, "jobs", 1)
		if field(failed, "id") != float64(9002) || field(failed, "conclusion") != "failure" ||
			field(failed, "url") != "https://github.com/acme/target/actions/runs/812/job/9002" {
			t.Fatalf("%s: jobs %v", args, out["jobs"])
		}
		if step, _ := quoted(field(failed, "failedSteps", 0).(string)); step != "Run tests" {
			t.Fatalf("%s: failed steps %v", args, field(failed, "failedSteps"))
		}
		if field(out, "jobs", 0, "failedSteps") != nil || !strings.Contains(field(out, "next").(string), "github_check_log") {
			t.Fatalf("%s: %v", args, out)
		}
	}
	log := s.read(run, "github_check_log", `{"repo":"acme/target","checkId":9002}`)
	if text, _ := quoted(field(log, "log").(string)); !strings.Contains(text, "FAIL TestExport: timeout after 30s") {
		t.Fatalf("the failed job's log: %v", log)
	}
	// Each read counted against the hour, recorded without what GitHub said.
	session := s.sessionOf(run)
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'agent.tool.attempted'
		AND payload->>'tool' = 'github_run_jobs'`, session); n != 2 {
		t.Fatalf("%d run_jobs attempts", n)
	}
	var payload string
	if err := s.owner.QueryRow(context.Background(), `SELECT payload::text FROM events WHERE session_id = $1 AND event_type = 'github.read'
		AND payload->>'tool' = 'github_run_jobs' LIMIT 1`, session).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "Run tests") || !strings.Contains(payload, `"runId": 812`) {
		t.Fatalf("ledger %s", payload)
	}
}

func TestARunOutsideTheCallersRepositoriesIsRefusedUnasked(t *testing.T) {
	s, run := brainstorming(t)
	nightlyRun(s.world)
	before := s.requests()
	for _, c := range []struct{ args, want string }{
		{`{"repo":"acme/web","runId":"812"}`, "is not a repository you may read"},
		{`{"runId":"https://github.com/acme/web/actions/runs/812"}`, "is not a repository you may read"},
		{`{"repo":"acme/target","runId":"https://github.com/acme/web/actions/runs/812"}`, "is a run of another repository"},
		{`{"repo":"acme/target","runId":"nightly"}`, "runId is the run's id"},
		{`{"repo":"acme/target","runId":"0"}`, "runId is the run's id"},
	} {
		status, out := s.tool(run, "github_run_jobs", c.args)
		if status != 422 || !strings.Contains(fmt.Sprint(out["error"]), c.want) {
			t.Errorf("%s: %d %v, want %q", c.args, status, out, c.want)
		}
	}
	if s.requests() != before {
		t.Error("a refused run read reached GitHub")
	}
}

func TestRunJobsReadsCountAgainstTheHour(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	nightlyRun(w)
	conductor, _, _ := w.conductor(task)
	w.must(task, "github_run_jobs", `{"repo":"acme/target","runId":"812"}`)
	// 59 more reads of the hour, by an earlier Run of the task, an hour's minute ago.
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role)
		SELECT 'run_earlier_reader', organization_id, project_id, task_id, attempt, 'completed', role FROM runs WHERE id = $1`, conductor)
	mustExec(t, w.owner, `UPDATE runs SET created_at = now() - interval '1 day' WHERE id = 'run_earlier_reader'`)
	mustExec(t, w.owner, `INSERT INTO events (id, organization_id, task_id, run_id, event_type, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_read_' || n, organization_id, task_id, 'run_earlier_reader', 'agent.tool.attempted', now() - interval '2 minutes',
			'agent', 'run_earlier_reader', 'agent', '{"tool":"github_issue"}'::jsonb
		FROM runs, generate_series(1, 59) n WHERE runs.id = $1`, conductor)
	before := w.requests()
	w.refused(task, "github_run_jobs", `{"repo":"acme/target","runId":"812"}`, "at most 60 GitHub reads an hour")
	if w.requests() != before {
		t.Error("the 61st read reached GitHub")
	}
}
