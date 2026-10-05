package orchestrator_test

// A conductor whose checkout is stopped mid-rebase: what it is told, and
// its publish, while running and once stopped and resumed. lux keeps the
// checkout through a stop and resume (on any host: lux's own E2E); the
// fake lux keeps the Run's one checkout.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// rebaseLine is what the conductor is told of a rebase in progress in its
// checkout of the task's repository.
const rebaseLine = "target: a rebase is in progress in your checkout"

// conflictingRebase leaves the conductor's checkout mid-rebase onto the
// task branch: its commit of FIXED.md over the fixer's conflicts.
func (e *editing) conflictingRebase() {
	e.t.Helper()
	e.git("fetch", "-q", "origin")
	e.git("update-ref", "refs/remotes/lux/"+e.branch, e.gh.SHA(e.branch))
	c := exec.Command("git", "-C", e.work(), "-c", "user.name=c", "-c", "user.email=c@x", "rebase", "lux/"+e.branch)
	if out, err := c.CombinedOutput(); err == nil {
		e.t.Fatalf("the rebase did not stop on a conflict: %s", out)
	}
	if !e.midRebase() {
		e.t.Fatal("no rebase in progress")
	}
}

func (e *editing) midRebase() bool {
	_, err := os.Stat(filepath.Join(e.work(), e.git("rev-parse", "--git-path", "rebase-merge")))
	return err == nil
}

// diverged is newEditing with the conductor's own commit of FIXED.md on
// the task branch, and a fixer's of the same file after it: the two
// conflict.
func diverged(t *testing.T) *editing {
	t.Helper()
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"FIXED.md": "mine\n"})
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("after the review round", func() bool { return e.decisionAt(e.task) == delivery.PointReviewed })
	e.must(e.task, "start_phase", `{"phase":"fix"}`)
	e.until("after the fix", func() bool { return e.decisionAt(e.task) == delivery.PointFixed })
	e.wokenWith(e.task, "after a fix")
	return e
}

// A running conductor mid-rebase is told so with what it hears next,
// with the rebase's own commands and no switch.
func TestARunningConductorMidRebaseIsToldHowToFinishIt(t *testing.T) {
	e := diverged(t)
	e.conflictingRebase()
	if status, _ := e.chat(e.task, "how is it going?"); status != 200 {
		t.Fatalf("chat %d", status)
	}
	line := e.heard(rebaseLine)
	if !strings.Contains(line, "`git rebase --continue`, or `git rebase --abort`") || strings.Contains(line, "switch") {
		t.Errorf("told %q", line)
	}
	if !e.midRebase() {
		t.Error("the sync ended the rebase")
	}
}
