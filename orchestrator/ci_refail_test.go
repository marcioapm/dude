package orchestrator_test

import (
	"sync/atomic"
	"testing"

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
