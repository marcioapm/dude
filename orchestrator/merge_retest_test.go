package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// mergeAtTheGate is a conducted task implemented, tested (passed) and at
// the pull request gate; main then gains a commit of path from the task's
// own content (same: the task's FACTORY.md as the task wrote it, a clean
// merge of a file it changed) or of a file it did not touch; the person
// answers Draft and the conductor opens. It returns the task and the test
// Runs before the opening.
func mergeAtTheGate(t *testing.T, same bool) (*world, string, int) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	setPolicy(w, task, `{"test":true}`)
	w.implemented(task)
	w.carriedOut(task, "start_phase", `{"phase":"test"}`)
	if at := w.decisionAt(task); at != delivery.PointBeforePR {
		t.Fatalf("at %q after the test, want before the pull request\n%s", at, w.describeRuns())
	}
	tests := w.phaseRuns(task, "test")
	branch := delivery.BranchFor(task, 1)
	if same {
		w.commitFileIn(w.gh.Repo, "main", "FACTORY.md", gitOut(t, w.gh.Repo, "show", branch+":FACTORY.md")+"\n", "The same file, elsewhere")
	} else {
		w.commitFileIn(w.gh.Repo, "main", "OTHER.md", "other\n", "Another task's change")
	}
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chat(task, "Draft"); status != 200 {
		t.Fatalf("answer Draft: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.t.Cleanup(func() {
		if t.Failed() {
			var st, step string
			_ = w.owner.QueryRow(context.Background(), `SELECT step, state::text FROM workflow_runs WHERE task_id = $1`, task).Scan(&step, &st)
			t.Logf("step %s state %s", step, st)
		}
	})
	return w, task, tests
}

// The person answered Draft; GitHub merges main, which touched none of the
// task's files, into the branch: no tester starts, the skip is recorded
// with the merge's paths, and the pull request opens as a draft at the
// merged head.
func TestADraftOpensAtTheMergedHeadWithoutAnotherTestWhenMainTouchedNoneOfTheTasksFiles(t *testing.T) {
	w, task, tests := mergeAtTheGate(t, false)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	branch := delivery.BranchFor(task, 1)
	head := w.gh.SHA(branch)
	if got := gitOut(t, w.gh.Repo, "rev-list", "--count", head+"..main"); got != "0" {
		t.Fatalf("the pull request lacks main: %s behind", got)
	}
	if pr := w.gh.Pulls()[0]; !pr.Draft || w.gh.SHA(pr.Head) != head {
		t.Errorf("pull request draft %v at %s, want a draft at the merged head %s", pr.Draft, w.gh.SHA(pr.Head), head)
	}
	if n := w.phaseRuns(task, "test"); n != tests {
		t.Errorf("%d test Runs, want the %d before the merge\n%s", n, tests, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action' AND payload->>'action' = 'merge-base'`, task); n != 1 {
		t.Errorf("%d merge-base lines, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'why' = $3
		AND payload->'paths' = '["target/OTHER.md"]'::jsonb AND payload->'heads'->>'target' = $4`,
		task, delivery.EvTestSkipped, delivery.MergeUntouchedWhy, head); n != 1 {
		t.Errorf("%d skips naming the merge's path at the merged head, want 1", n)
	}
	test := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'test'`, task)
	body := w.gh.Pulls()[0].Body
	for _, want := range []string{"Not tested again after the merge of the target branch: it touched none of the task's files; these verdicts are test " + test + "'s.", "- **pass** — it greets"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pull request's description lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Stale") {
		t.Errorf("the description calls the verdict the merge carried stale:\n%s", body)
	}
	// The conductor hears it with its next note.
	if line := w.str(`SELECT line FROM conductor_wakes WHERE task_id = $1 AND key LIKE 'base-moved:merge:%'`, task); !strings.Contains(line,
		"not tested again because it touched none of the task's files (1 paths), and test "+test+"'s verdicts stand") {
		t.Errorf("the conductor's line: %q", line)
	}
}

// The same, but main's change is to a file the task changed: the merged
// head is tested again, as before.
func TestAMergeOfMainTouchingTheTasksFileIsTestedBeforeThePullRequest(t *testing.T) {
	w, task, tests := mergeAtTheGate(t, true)
	w.until("a tester of the merged head", func() bool { return w.phaseRuns(task, "test") > tests || len(w.gh.Pulls()) > 0 })
	if n := w.phaseRuns(task, "test"); n <= tests {
		t.Fatalf("%d test Runs, want one more for the merged head\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvTestSkipped); n != 0 {
		t.Errorf("%d skips of a merge that touched the task's file", n)
	}
}
