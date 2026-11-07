package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A runner failure can leave the fixer nothing to push. A re-run failing on
// that unchanged head must wake another fixer; rereading the same failure must not.
func TestChecksFailingAgainOnAnUnchangedHeadAfterAFixThatPushedNothingGetAnotherFix(t *testing.T) {
	testChecksFailingAgainOnAnUnchangedHeadAfterAFixThatPushedNothingGetAnotherFix(t)
}

func testChecksFailingAgainOnAnUnchangedHeadAfterAFixThatPushedNothingGetAnotherFix(t *testing.T) {
	for _, via := range []string{"check run", "commit status"} {
		t.Run(via, func(t *testing.T) {
			w := newWorld(t)
			// Once the pull request is open, every fix finds nothing to change.
			var prOpen atomic.Bool
			scripted := w.lux.Decide
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				if labels, _ := spec["labels"].(map[string]any); prOpen.Load() && labels["dude.phase"] == "fix" {
					return fakelux.Behaviour{Reply: "The runner was shut down; nothing in the change to fix. Re-run the job."}
				}
				return scripted(spec)
			}
			passing, pending, failing := "run:success", "run:pending", "run:failure"
			failsAs := func(id int64) { w.gh.Set(func(s *fakegithub.Server) { s.CheckRunID = id }) }
			if via == "commit status" {
				passing, pending, failing = "success", "pending", "failure"
				failsAs = func(id int64) { w.gh.Set(func(s *fakegithub.Server) { s.StatusID = id }) }
			}
			failsAs(501)
			w.gh.SetChecks(passing)
			wi := w.reviewing()
			prOpen.Store(true)
			head := w.gh.SHA(w.gh.Pull(1).Head)

			w.gh.SetChecks(failing)
			w.until("a fix for the first failure, done, and waiting again", func() bool {
				w.sync()
				return w.prFixesDone(wi) == 1 && w.waitingOnPR(wi)
			})
			if got := w.gh.SHA(w.gh.Pull(1).Head); got != head {
				t.Fatalf("the fixer that changed nothing moved the head: %s, was %s", got, head)
			}

			// A re-run creates a new check run or commit status on the same head.
			if via == "check run" {
				if code, body := w.callAs(w.person("owner"), "/internal/pull-requests/"+w.prOf(wi)+"/rerun-failed", map[string]any{}); code != 200 {
					t.Fatalf("rerun: %d %v", code, body)
				}
			} else {
				w.gh.SetChecks(pending)
				failsAs(502)
			}
			w.untilChecks(wi, "pending")
			w.gh.SetChecks(failing)
			w.until("a fix for the re-run's failure, done, and waiting again", func() bool {
				w.sync()
				return w.prFixesDone(wi) == 2 && w.waitingOnPR(wi)
			})

			// A forbidden check-runs read is treated as pending. For statuses,
			// simulate that uncertainty without changing the status ID.
			if via == "check run" {
				w.gh.Set(func(s *fakegithub.Server) { s.CheckRunsForbidden = true })
				w.untilChecks(wi, "pending")
				w.gh.Set(func(s *fakegithub.Server) { s.CheckRunsForbidden = false })
			} else {
				w.gh.SetChecks(pending)
				w.untilChecks(wi, "pending")
				w.gh.SetChecks(failing)
			}
			w.untilChecks(wi, "failing")
			for range 3 {
				w.sync()
				w.pump()
			}
			if n := w.fixes(wi); n != 2 {
				t.Fatalf("%d fixes of CI, want 2: the same failing check read again started another", n)
			}
		})
	}
}

// A conductor may wait for the remaining jobs after the first failure. A
// different job failing must reach it even though neither head nor rollup moved.
func TestDistinctFailedJobsOnTheSameHeadWakeAWaitingConductor(t *testing.T) {
	w, task := conductedDirectWorld(t)
	head := w.gh.SHA(w.gh.Pull(1).Head)

	// The shared fake's listing has one check run; its CheckRuns map only
	// supplies details. Override just the listing to model parallel jobs,
	// keeping all other GitHub reads on the existing fake.
	type job struct {
		id                       int64
		name, status, conclusion string
	}
	var jobs atomic.Value
	setJobs := func(js ...job) { jobs.Store(js) }
	setJobs(job{501, "unit", "in_progress", ""}, job{502, "integration", "in_progress", ""})
	github := w.gh.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			fakegithub.Graphql(w.gh)(rw, r)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/target/commits/"+head+"/check-runs" {
			github.ServeHTTP(rw, r)
			return
		}
		runs := []map[string]any{}
		for _, j := range jobs.Load().([]job) {
			runs = append(runs, map[string]any{
				"id": j.id, "name": j.name, "status": j.status, "conclusion": j.conclusion,
				"app":      map[string]string{"slug": "github-actions"},
				"html_url": fmt.Sprintf("https://github.test/acme/target/runs/%d", j.id),
			})
		}
		rw.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(rw).Encode(map[string]any{"total_count": len(runs), "check_runs": runs}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	mustExec(t, w.owner, `UPDATE forge_credentials SET api_base_url = $2 WHERE organization_id = $1`, w.org, server.URL)
	w.gh.SetChecks("none")
	w.untilChecks(task, "pending")

	signals := func() int {
		return w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
			WHERE r.task_id = $1 AND s.name = $2`, task, delivery.SignalPRFeedback)
	}
	keys := func() int {
		return w.count(`SELECT count(DISTINCT s.idempotency_key) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
			WHERE r.task_id = $1 AND s.name = $2`, task, delivery.SignalPRFeedback)
	}
	checksJSON := func() string {
		var checks string
		if err := w.owner.QueryRow(context.Background(), `SELECT checks_json::text FROM pull_requests WHERE task_id = $1`, task).Scan(&checks); err != nil {
			t.Fatal(err)
		}
		return checks
	}
	assertSignals := func(want int) {
		t.Helper()
		if got := signals(); got != want {
			t.Fatalf("%d PR feedback signals, want %d", got, want)
		}
		if got := keys(); got != want {
			t.Fatalf("%d distinct PR feedback signal keys, want %d", got, want)
		}
	}
	wait := func() {
		t.Helper()
		w.must(task, "decide", `{"action":"wait","note":"Wait for the remaining CI jobs."}`)
		w.until("waiting on the pull request again", func() bool { return w.waitingOnPR(task) })
	}
	wake := func(before int, previous string) {
		t.Helper()
		w.until("the new failed job to reach the conductor", func() bool {
			w.sync()
			return w.decisionAt(task) == delivery.PointPRFeedback
		})
		w.untilChecks(task, "failing")
		if got := checksJSON(); got == previous {
			t.Fatal("the new failed job did not change checks_json")
		}
		assertSignals(before + 1)
		if got := w.gh.SHA(w.gh.Pull(1).Head); got != head {
			t.Fatalf("head moved from %s to %s", head, got)
		}
	}

	before, pending := signals(), checksJSON()
	setJobs(job{501, "unit", "completed", "failure"}, job{502, "integration", "in_progress", ""})
	wake(before, pending)
	first := checksJSON()
	wait()
	w.syncRounds(3)
	assertSignals(before + 1)
	if !w.waitingOnPR(task) {
		t.Fatal("the same failed job woke the conductor after wait")
	}

	setJobs(job{501, "unit", "completed", "failure"}, job{502, "integration", "completed", "failure"})
	wake(before+1, first)
	wait()
	// GitHub may return the identical jobs in another order.
	setJobs(job{502, "integration", "completed", "failure"}, job{501, "unit", "completed", "failure"})
	w.syncRounds(3)
	assertSignals(before + 2)
	if !w.waitingOnPR(task) {
		t.Fatal("an order-only checks read woke the conductor")
	}

	// A rerun replaces unit's id without changing its name or the head;
	// integration keeps the rollup failing even while unit runs again.
	setJobs(job{503, "unit", "in_progress", ""}, job{502, "integration", "completed", "failure"})
	w.syncRounds(3)
	assertSignals(before + 2)
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND checks::text = 'failing'`, task); n != 1 {
		t.Fatal("the other failed job did not keep the rollup failing during the rerun")
	}
	if !w.waitingOnPR(task) {
		t.Fatal("the pending rerun woke the conductor")
	}
	rerunning := checksJSON()
	setJobs(job{503, "unit", "completed", "failure"}, job{502, "integration", "completed", "failure"})
	wake(before+2, rerunning)
	wait()
	setJobs(job{502, "integration", "completed", "failure"}, job{503, "unit", "completed", "failure"})
	w.syncRounds(3)
	assertSignals(before + 3)
	if !w.waitingOnPR(task) {
		t.Fatal("the same rerun failure woke the conductor again")
	}
}

// prFixesDone: the task's fixes of pull request feedback that finished.
func (w *world) prFixesDone(wi string) int {
	return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb
		AND status = 'completed'`, wi)
}

// untilChecks syncs until dude has recorded the task's pull request's
// checks as state.
func (w *world) untilChecks(wi, state string) {
	w.t.Helper()
	w.until("checks recorded as "+state, func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND checks::text = $2`, wi, state) == 1
	})
}
