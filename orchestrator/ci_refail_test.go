package orchestrator_test

import (
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// CI failed because its runner was shut down, not because of the change.
// The fixer rightly pushed nothing, so the head stayed where it was; the
// job was re-run and failed again on that head. That second failure is a
// new one, and gets a fixer of its own: the delivery must not go on
// waiting on a pull request whose checks are red. The same checks failing
// again are not new: read as pending in between, they wake nobody.
// Through check runs (Actions), re-run as a person re-runs them, and
// through commit statuses alike.
func TestChecksFailingAgainOnAnUnchangedHeadAfterAFixThatPushedNothingGetAnotherFix(t *testing.T) {
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

			// Re-run: pending, then failing again as a new check on the same
			// head. An Actions job re-run is a new check run; a CI that posts
			// statuses posts a new one.
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

			// The same failing check, read as pending for a moment and failing
			// again: not a new failure. GitHub refusing the check-runs read
			// is read as pending; for statuses, the same status read as
			// pending stands in for any such reading.
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
