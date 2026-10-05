package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A checkout stopped mid-operation is told the operation and the commands
// git accepts in it, whatever the sync's status; never a switch, and a
// merge of the task branch only after the operation is finished.
func TestACheckoutLineMidOperationGivesOnlyWhatGitAccepts(t *testing.T) {
	for _, c := range []struct {
		op, status string
		behind     int
		want       []string
	}{
		{lux.OperationRebase, lux.SyncKept, 3, []string{
			"app: a rebase is in progress in your checkout (3 behind the task branch): " +
				"resolve and `git rebase --continue`, or `git rebase --abort`; then `git merge lux/dude/t/1`."}},
		{lux.OperationMerge, lux.SyncKept, 1, []string{"a merge is in progress", "`git merge --continue`", "`git merge --abort`"}},
		{lux.OperationCherryPick, lux.SyncKept, 1, []string{"a cherry-pick is in progress", "`git cherry-pick --continue`", "`git cherry-pick --abort`"}},
		{lux.OperationRevert, lux.SyncKept, 1, []string{"a revert is in progress", "`git revert --continue`", "`git revert --abort`"}},
		{lux.OperationSequencer, lux.SyncKept, 1, []string{"a cherry-pick or revert of several commits is in progress",
			"`git cherry-pick --continue`", "`git cherry-pick --abort`", "`git revert --continue`"}},
		// Not behind, or only fetched: the operation is still told.
		{lux.OperationRebase, "up-to-date", 0, []string{"app: a rebase is in progress in your checkout (0 behind the task branch): " +
			"resolve and `git rebase --continue`, or `git rebase --abort`."}},
		{lux.OperationMerge, "fetched", 2, []string{"a merge is in progress", "(2 behind the task branch)"}},
	} {
		line := CheckoutLine(lux.SyncResult{Repo: "app", Ref: "dude/t/1", Status: c.status, Behind: c.behind, Ahead: 1,
			Dirty: true, Diverged: true, Operation: c.op})
		for _, want := range c.want {
			if !strings.Contains(line, want) {
				t.Errorf("%s %s: %q lacks %q", c.op, c.status, line, want)
			}
		}
		if strings.Contains(line, "switch") {
			t.Errorf("%s: %q advises a switch", c.op, line)
		}
		// A merge of the task branch is advised only after the operation's own commands.
		if i := strings.Index(line, "`git merge lux/"); i >= 0 && i < strings.Index(line, "--abort") {
			t.Errorf("%s: %q advises a merge before the operation is finished", c.op, line)
		}
		if c.behind == 0 && strings.Contains(line, "lux/") {
			t.Errorf("%s: %q merges a task branch it is not behind", c.op, line)
		}
	}
}
