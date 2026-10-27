package orchestrator_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Under each choice before the pull request, a tested task whose target
// branch gains a commit of a file it did not touch as the opening starts:
// GitHub merges main in before the pull request, no tester starts for the
// merged head, and the pull request opens there, a draft for Open as draft
// and for the owner's Draft at the hold of Ask me first, ready for Open it.
func TestEachPullRequestChoiceOpensAtTheMergedHeadWithoutAnotherTest(t *testing.T) {
	for _, c := range []struct {
		choice string
		draft  bool
	}{
		{delivery.PullRequestAsk, true},
		{delivery.PullRequestDraft, true},
		{delivery.PullRequestOpen, false},
	} {
		t.Run(c.choice, func(t *testing.T) {
			w := newWorld(t)
			ana := w.person("Ana")
			var once sync.Once
			var tests atomic.Int64
			w.gated(func(step, next string) {
				if next != "openPullRequest" || step == next {
					return
				}
				once.Do(func() {
					tests.Store(int64(w.count(`SELECT count(*) FROM runs WHERE organization_id = $1 AND phase = 'test'`, w.org)))
					w.commitFileIn(w.gh.Repo, "main", "OTHER.md", "other\n", "Another task's change")
				})
			}).Store(true)
			w.projectChoice(c.choice)
			task := w.testing("fake/scripted")
			w.assignOwner(task, ana)
			if c.choice == delivery.PullRequestAsk {
				w.until("the hold", func() bool { return w.heldBeforePR(task) || len(w.gh.Pulls()) > 0 })
				if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true, "draft": true}); status != 200 {
					t.Fatalf("the owner's Open as draft: %d %v", status, out)
				}
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			head := w.gh.SHA(delivery.BranchFor(task, 1))
			if got := gitOut(t, w.gh.Repo, "rev-list", "--count", head+"..main"); got != "0" {
				t.Fatalf("the pull request lacks main: %s behind", got)
			}
			if pr := w.gh.Pulls()[0]; pr.Draft != c.draft || w.gh.SHA(pr.Head) != head {
				t.Errorf("pull request draft %v at %s, want draft %v at the merged head %s", pr.Draft, w.gh.SHA(pr.Head), c.draft, head)
			}
			if n := w.phaseRuns(task, "test"); n == 0 || int64(n) != tests.Load() {
				t.Errorf("%d test Runs, want the %d before the merge\n%s", n, tests.Load(), w.describeRuns())
			}
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'why' = $3
				AND payload->'heads'->>'target' = $4`, task, delivery.EvTestSkipped, delivery.MergeUntouchedWhy, head); n != 1 {
				t.Errorf("%d skips of the merge at the merged head, want 1", n)
			}
			holds := map[string]int{delivery.PullRequestAsk: 1}[c.choice]
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = 'before_pull_request'`,
				task, delivery.EvDecisionAwaited); n != holds {
				t.Errorf("%d holds before the pull request, want %d", n, holds)
			}
		})
	}
}

// The project's policy stops resolving while GitHub's merge of main is
// skipped: the opening after the skip reads the choice again and escalates
// rather than open.
func TestAChoiceUnresolvedAfterAMergeSkipEscalates(t *testing.T) {
	w := newWorld(t)
	var commit, broke sync.Once
	w.gated(func(step, next string) {
		if next == "openPullRequest" && step != next {
			commit.Do(func() { w.commitFileIn(w.gh.Repo, "main", "OTHER.md", "other\n", "Another task's change") })
		}
		if step == "openPullRequest" && next == "test" {
			broke.Do(func() {
				mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"alwaysReviewers":["no-such-reviewer"]}'::jsonb WHERE id = $1`, w.project)
			})
		}
	}).Store(true)
	task := w.testing("fake/scripted")
	w.until("the escalation", func() bool { return w.escalationReason(task) != "" || len(w.gh.Pulls()) > 0 })
	if r := w.escalationReason(task); r != delivery.ReasonChoiceUnresolved {
		t.Fatalf("escalation %q, want %q\n%s", r, delivery.ReasonChoiceUnresolved, w.describeRuns())
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened with the choice unresolved", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'why' = $3`,
		task, delivery.EvTestSkipped, delivery.MergeUntouchedWhy); n != 1 {
		t.Errorf("%d skips of the merge, want 1", n)
	}
	if n := w.phaseRuns(task, "test"); n != 1 {
		t.Errorf("%d test Runs, want 1\n%s", n, w.describeRuns())
	}
}
