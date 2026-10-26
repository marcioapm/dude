package orchestrator_test

import (
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// ABS-30 (2026-10-26): CI failed because its runner was shut down, not
// because of the change. The fixer rightly pushed nothing, so the head
// stayed where it was; the job was re-run and failed again on that head.
// That second failure is a new one, and gets a fixer of its own: the
// delivery must not go on waiting on a pull request whose checks are red.
// The same failure read again is still not news.
func TestChecksFailingAgainOnAnUnchangedHeadAfterAFixThatPushedNothingGetAnotherFix(t *testing.T) {
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
	w.gh.SetChecks("run:success")
	wi := w.reviewing()
	prOpen.Store(true)
	head := w.gh.SHA(w.gh.Pull(1).Head)

	w.gh.SetChecks("run:failure")
	w.until("a fix for the first failure, done, and waiting again", func() bool {
		w.sync()
		return w.prFixesDone(wi) == 1 && w.waitingOnPR(wi)
	})
	if got := w.gh.SHA(w.gh.Pull(1).Head); got != head {
		t.Fatalf("the fixer that changed nothing moved the head: %s, was %s", got, head)
	}

	// The job re-run: pending, then failing again as a new check run on
	// the same head.
	w.gh.SetChecks("run:pending")
	w.sync()
	w.gh.Set(func(s *fakegithub.Server) { s.CheckRunID = 78 })
	w.gh.SetChecks("run:failure")
	w.until("a fix for the re-run's failure, done, and waiting again", func() bool {
		w.sync()
		return w.prFixesDone(wi) == 2 && w.waitingOnPR(wi)
	})
	for range 3 {
		w.sync()
		w.pump()
	}
	if n := w.fixes(wi); n != 2 {
		t.Fatalf("%d fixes of CI, want 2: the same failure read again started another", n)
	}
}

// prFixesDone: the task's fixes of pull request feedback that finished.
func (w *world) prFixesDone(wi string) int {
	return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb
		AND status = 'completed'`, wi)
}
