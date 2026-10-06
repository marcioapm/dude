package orchestrator_test

// A conductor whose checkout is stopped mid-rebase: what it is told, and
// its publish, while running and once stopped and resumed. lux keeps the
// checkout through a stop and resume (on any host: lux's own E2E); the
// fake lux keeps the Run's one checkout.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
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

// refusedMidRebase publishes from a checkout mid-rebase: refused for the
// rebase, the conductor woken saying so, and the task branch unmoved.
func (e *editing) refusedMidRebase() {
	e.t.Helper()
	before := e.gh.SHA(e.branch)
	status, why := e.published(e.publish())
	want := "target: a rebase is in progress in your checkout: finish or abort it, then publish."
	if status != delivery.PublishRefused || why != want {
		e.t.Errorf("publish %s: %q, want refused saying %q", status, why, want)
	}
	e.wokenWith(e.task, "was refused, nothing moved: "+want)
	if got := e.gh.SHA(e.branch); got != before {
		e.t.Errorf("the task branch moved to %s", got)
	}
	if !e.midRebase() {
		e.t.Error("the publish ended the rebase")
	}
}

// finishRebase resolves the conflict and continues the rebase.
func (e *editing) finishRebase() {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.work(), "FIXED.md"), []byte("both\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.git("add", "FIXED.md")
	c := exec.Command("git", "-C", e.work(), "-c", "user.name=c", "-c", "user.email=c@x", "rebase", "--continue")
	c.Env = append(os.Environ(), "GIT_EDITOR=true")
	if out, err := c.CombinedOutput(); err != nil {
		e.t.Fatalf("rebase --continue: %v %s", err, out)
	}
}

// A publish mid-rebase is refused, for good: the publish is settled, not
// retried, and nothing moves.
func TestAPublishMidRebaseIsRefused(t *testing.T) {
	e := diverged(t)
	e.conflictingRebase()
	e.refusedMidRebase()
}

// A conductor stopped mid-rebase and resumed: the resume's sync keeps the
// checkout, naming the rebase; the conductor hears it with the message
// that resumed it; the rebase is still there and publish is refused. Once
// the rebase is finished, publish fast-forwards the task branch.
func TestAConductorStoppedMidRebaseAndResumedFinishesItThenPublishes(t *testing.T) {
	e := diverged(t)
	e.conflictingRebase()
	conflicted, unmerged := e.conflict()
	e.syncer.ConductorWarm = 1
	e.until("the conductor stopped", func() bool { _, status, _ := e.conductor(e.task); return status == "paused" })
	e.syncer.ConductorWarm = 1 << 40
	lr := e.luxRunOf(e.cond)
	// The task branch moves on while it is stopped (a person's commit on
	// GitHub), so the rebase is now behind it.
	e.gh.CommitOnTop(e.branch, "later")
	before := e.lastSync(lr).seq
	told := len(e.inputsSaying(lr, rebaseLine))
	if status, _ := e.chat(e.task, "back to it"); status != 200 {
		t.Fatalf("chat %d", status)
	}
	var sync lux.SyncResult
	e.until("the resume's sync", func() bool {
		s := e.lastSync(lr)
		sync = s.SyncResult
		return s.seq > before && s.RequestID == ""
	})
	if sync.Status != lux.SyncKept || sync.Operation != lux.OperationRebase || sync.Behind != 1 {
		t.Errorf("the resume's sync %+v, want kept for a rebase, 1 behind", sync)
	}
	e.until("the rebase line after the resume", func() bool { return len(e.inputsSaying(lr, rebaseLine)) > told })
	line := e.inputsSaying(lr, rebaseLine)[told]
	if want := "target: a rebase is in progress in your checkout (1 behind the task branch): resolve and `git rebase --continue`, " +
		"or `git rebase --abort`; then `git merge lux/" + e.branch + "`."; !strings.Contains(line, want) {
		t.Errorf("heard %q, want %q", line, want)
	}
	if id, _, _ := e.conductor(e.task); id != e.cond {
		t.Fatalf("a new conductor %s, not the one resumed", id)
	}
	if !e.midRebase() {
		t.Fatal("the resume ended the rebase")
	}
	if got, entries := e.conflict(); got != conflicted || entries != unmerged {
		t.Fatalf("after the resume FIXED.md is %q (unmerged %q), want %q (%q)", got, entries, conflicted, unmerged)
	}
	e.refusedMidRebase()

	e.finishRebase()
	e.git("fetch", "-q", "origin")
	e.git("merge", "-q", "--no-edit", e.gh.SHA(e.branch))
	sha := e.git("rev-parse", "HEAD")
	status, why := e.published(e.publish())
	if status != delivery.PublishPublished {
		t.Fatalf("publish after the rebase %s: %s", status, why)
	}
	if got := e.gh.SHA(e.branch); got != sha {
		t.Errorf("the task branch at %s, want the rebased %s", got, sha)
	}
	if got := e.git("show", sha+":FIXED.md"); got != "both" {
		t.Errorf("published FIXED.md %q, want the resolution", got)
	}
}

// conflict is FIXED.md's bytes and its unmerged index entries, which a
// conflicted rebase leaves (both non-empty, the bytes with markers).
func (e *editing) conflict() (bytes, unmerged string) {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.work(), "FIXED.md"))
	if err != nil {
		e.t.Fatal(err)
	}
	unmerged = e.git("ls-files", "-u", "--", "FIXED.md")
	if unmerged == "" || !strings.Contains(string(b), "<<<<<<<") {
		e.t.Fatalf("FIXED.md is not in conflict: %q (unmerged %q)", b, unmerged)
	}
	return string(b), unmerged
}

// inputsSaying is every input the lux Run's agent was given containing
// want, in order.
func (e *editing) inputsSaying(luxRunID, want string) []string {
	var out []string
	for _, in := range runOf(e, luxRunID).Inputs {
		if strings.Contains(in, want) {
			out = append(out, in)
		}
	}
	return out
}

func runOf(e *editing, luxRunID string) *fakelux.Run {
	for _, r := range e.lux.Runs() {
		if r.ID == luxRunID {
			return r
		}
	}
	e.t.Fatalf("no lux Run %s", luxRunID)
	return nil
}

type syncSeen struct {
	lux.SyncResult
	seq int64
}

// lastSync is the lux Run's latest git.sync event, and its event id.
func (e *editing) lastSync(luxRunID string) syncSeen {
	e.t.Helper()
	var last syncSeen
	frames, err := e.syncer.Lux.Events(context.Background(), luxRunID, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, f := range frames {
		if f.EventType == "git.sync" {
			last = syncSeen{seq: f.EventID}
			_ = json.Unmarshal(f.EventData, &last.SyncResult)
		}
	}
	return last
}
