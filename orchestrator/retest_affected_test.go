package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// ciOnly is a play: each fix of the pull request's feedback changes only
// its CI workflow, and the simplifier after it changes nothing.
func ciOnly(phase string, _ int, b *fakelux.Behaviour) {
	switch phase {
	case "fix":
		b.Commit = map[string]string{".github/workflows/ci.yml": "on: [push, pull_request]\n"}
		b.Message = "Run CI on pull requests"
	case "simplify":
		b.Commit = nil
	}
}

// ciOnlyFixLands has a collaborator ask for a fix the pull request's fixer
// plays as ciOnly, and waits for it on the pull request, the task back in
// review.
func (w *prFixWorld) ciOnlyFixLands() {
	w.t.Helper()
	w.play = ciOnly
	w.comment("Please run CI on pull requests too.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
}

// A pull request's fix that changes only CI configuration, after a test
// that passed every criterion: no tester is started (a direct fix is never
// tested again; the pull request's CI is its test), the pull request takes
// the fix by its own push, and its Verification keeps the passed verdicts,
// marked stale at the head they were tested at.
func TestACIOnlyFixAfterAPassingTestStartsNoTester(t *testing.T) {
	w := newPRFixWorld(t)
	w.ciOnlyFixLands()
	w.until("waiting on the pull request again", func() bool { return w.waitingOnPR(w.wi) })
	if !slices.Equal(w.phases, []string{"fix", "review"}) {
		t.Errorf("after the comment: %v, want the fix and its review; no simplifier, no tester", w.phases)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'test'`, w.wi); n != 1 {
		t.Errorf("%d test Runs, want the first alone\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 0 {
		t.Errorf("%d promotions, want none: the fix is pushed to the pull request", n)
	}
	body := w.gh.Pull(1).Body
	for _, want := range []string{"- **pass** — it greets", "Tested at `" + w.before[:7] + "`. **Stale:**"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pull request's description lacks %q:\n%s", want, body)
		}
	}
}

// A resumed tester after a fix of the product is told to retest what the
// change can affect and carry the rest from its previous round, named by
// its id; nothing tells it to start again from the start.
func TestAResumedTesterIsToldWhatItMayCarry(t *testing.T) {
	w := resuming(t)
	w.onModel("qa_browser", fakeagent.TestFailOnceModel)
	wi := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	w.names(wi, w.repoID)
	p := resumingPolicy()
	p.Test = true
	w.start(wi, p)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	rows, err := w.owner.Query(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'test' ORDER BY created_at, id`, wi)
	if err != nil {
		t.Fatal(err)
	}
	tests, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(tests) != 2 {
		t.Fatalf("test Runs %v (%v), want the failed one and its re-test", tests, err)
	}
	luxTests := w.luxRunsOf("test")
	if len(luxTests) != 1 {
		t.Fatalf("%d testers submitted, want one resumed", len(luxTests))
	}
	var round string
	for _, input := range w.lux.Inputs(luxTests[0].ID) {
		if strings.Contains(input, "## A new round") {
			round = input
		}
	}
	for _, want := range []string{"decide which criteria it can affect", "status: carried", "from_round: " + tests[0], "When in doubt, test it again.", "repeat its `check` line exactly as before"} {
		if !strings.Contains(round, want) {
			t.Errorf("the round message lacks %q:\n%s", want, round)
		}
	}
	if strings.Contains(round, "from the start") {
		t.Errorf("the round message still says to test from the start:\n%s", round)
	}
}

// A change too large to go on over: the next tester is fresh, told it
// tests afresh, and may carry nothing.
func TestALargeChangeGetsAFreshTesterOfEveryCriterion(t *testing.T) {
	w := resuming(t)
	// Not a scripted model: lux is given dude's prompt. The first test fails.
	w.onModel("qa_browser", "llm-test")
	scripted, tests := w.lux.Decide, 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "test" {
			if tests++; tests == 1 {
				b.Reply = fakeagent.Tester(fakeagent.TestFailModel, 1, 0).Reply
			}
		}
		return b
	}
	wi := w.task()
	w.names(wi, w.repoID)
	w.gh.CompareAnswer = func(base, head string, answer map[string]any) {
		answer["files"] = []any{map[string]any{"filename": "big.go", "status": "modified", "additions": delivery.MaxResumeDeltaLines, "deletions": 1}}
	}
	p := resumingPolicy()
	p.Test = true
	w.start(wi, p)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	luxTests := w.luxRunsOf("test")
	if len(luxTests) != 2 || luxTests[0].Resumed != 0 {
		t.Fatalf("%d testers submitted, want a fresh second one", len(luxTests))
	}
	prompt := luxTests[1].Prompt()
	if !strings.Contains(prompt, "this round tests afresh") || strings.Contains(prompt, "status: carried") {
		t.Errorf("the fresh tester is not told to test every criterion afresh:\n%s\n%s", prompt, w.describeRuns())
	}
}
