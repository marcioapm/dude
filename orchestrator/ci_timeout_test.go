package orchestrator_test

// A job GitHub cancelled at its time limit, through the fake GitHub: the
// pull request reads as failing, the conductor is asked to decide on the
// feedback naming that check as timed out, and github_check_log reads its
// log. A job a person cancelled is not failing and wakes nobody.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

func TestAJobCancelledAtItsTimeLimitRaisesTheFeedbackDecision(t *testing.T) {
	// conductedDirectWorld without its sync_base tools server, whose tools
	// have no GitHub connection: github_check_log is read here too.
	w := conducting(t)
	task := w.prOpen(w.task(), prAt{})
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	downloads := httptest.NewServer(w.gh.LogDownloads())
	t.Cleanup(downloads.Close)
	feedbackSignals := func() int {
		return w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
			WHERE r.task_id = $1 AND s.name = $2 AND s.payload->>'kind' = 'actionable'`, task, delivery.SignalPRFeedback)
	}
	before := feedbackSignals()

	// A person cancelled the job: GitHub annotates nothing about time. Only
	// readiness may change (passing to pending): nothing to act on.
	w.gh.Set(func(s *fakegithub.Server) {
		s.CheckRunID = 601
		s.CheckRuns = map[int64]fakegithub.CheckRun{601: {App: "github-actions"}}
	})
	w.gh.SetChecks("run:cancelled")
	w.untilChecks(task, "pending")
	w.syncRounds(3)
	if n := feedbackSignals(); n != before {
		var all string
		_ = w.owner.QueryRow(context.Background(), `SELECT string_agg(s.payload::text, ' | ') FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
			WHERE r.task_id = $1 AND s.name = $2`, task, delivery.SignalPRFeedback).Scan(&all)
		t.Fatalf("a job a person cancelled raised %d feedback signals: %s", n-before, all)
	}
	if !w.waitingOnPR(task) || w.decisionAt(task) == delivery.PointPRFeedback {
		t.Fatal("a job a person cancelled woke the conductor")
	}

	// The re-run hangs and GitHub cancels it at timeout-minutes.
	w.gh.Set(func(s *fakegithub.Server) {
		s.CheckRunID = 602
		s.CheckRuns[602] = fakegithub.CheckRun{App: "github-actions", Annotations: []fakegithub.Annotation{
			{Path: ".github", Line: 1, Level: "failure", Message: "The job has exceeded the maximum execution time of 6m0s"}}}
	})
	w.gh.SetJobLog(602, "2026-11-11T10:00:01.000Z === RUN   TestSum\n2026-11-11T10:06:00.000Z ##[error]The operation was canceled.\n", downloads.URL)
	w.until("the timed-out job to reach the conductor", func() bool {
		w.sync()
		return w.decisionAt(task) == delivery.PointPRFeedback
	})
	w.untilChecks(task, "failing")
	// A completed check run never changes: once stored, its verdict is
	// reused by every later sync and ready gate of the same head, and its
	// annotations are not read again.
	annotationReads := func() int {
		n := 0
		for _, r := range w.gh.Requests() {
			if r == "GET /repos/acme/target/check-runs/602/annotations" {
				n++
			}
		}
		return n
	}
	read := annotationReads()
	w.syncRounds(3)
	if n := annotationReads(); n != read {
		t.Fatalf("three more syncs read the timed-out job's annotations %d more times, want none", n-read)
	}
	if n := feedbackSignals(); n != before+1 {
		t.Fatalf("%d actionable feedback signals for the timed-out job, want 1", n-before)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.checks_changed'
		AND payload->>'to' = 'failing'`, task); n == 0 {
		var all string
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(string_agg(payload::text, ' | '), '') FROM events
			WHERE task_id = $1 AND event_type = 'pull_request.checks_changed'`, task).Scan(&all)
		t.Fatalf("no checks_changed to failing: %s", all)
	}
	var feedback string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'prFeedback', '[]')::text FROM workflow_runs
		WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, task).Scan(&feedback); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(feedback, `"runId": 602`) || !strings.Contains(feedback, `"timedOut": true`) || strings.Contains(feedback, `"runId": 601`) {
		t.Fatalf("the decision's feedback does not name the timed-out check: %s", feedback)
	}
	log := w.must(task, "github_check_log", `{"repo":"acme/target","checkId":602}`)
	if text, _ := quoted(field(log, "log").(string)); !strings.Contains(text, "=== RUN   TestSum") {
		t.Fatalf("the timed-out job's log: %v", log)
	}
}
