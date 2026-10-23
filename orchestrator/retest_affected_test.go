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
// that passed every criterion: no tester is started, the passed verdicts
// stand, the skip is in the task's ledger with the verdicts it rests on,
// the pull request takes the fix, and its Verification says the verdict was
// not tested again.
func TestACIOnlyFixAfterAPassingTestStartsNoTester(t *testing.T) {
	w := newPRFixWorld(t)
	first := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'test'`, w.wi)
	w.ciOnlyFixLands()
	if !slices.Equal(w.phases, []string{"fix", "review", "simplify"}) {
		t.Errorf("after the comment: %v, want the fix, its review and the simplifier; no tester", w.phases)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'test'`, w.wi); n != 1 {
		t.Errorf("%d test Runs, want the first alone\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'fromRunId' = $3
		AND payload->'paths' = '["target/.github/workflows/ci.yml"]'::jsonb
		AND jsonb_path_query_array(payload->'criteria', '$[*].status') = '["pass", "pass"]'::jsonb`,
		w.wi, delivery.EvTestSkipped, first); n != 1 {
		t.Errorf("%d skip events naming %s, the CI path and its two passes", n, first)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 1 {
		t.Errorf("%d fix moves, want the pull request moved to the fix", n)
	}
	body := w.gh.Pull(1).Body
	for _, want := range []string{"- **pass** — it greets", "Not tested again since"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pull request's description lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Stale") {
		t.Errorf("the description calls the carried verdict stale:\n%s", body)
	}
}

// The same CI-only fix, after a test that did not pass, or with a finding
// of the tester's a fix was sent for (low, so the gate passed): a tester
// is started, and no skip recorded.
func TestACIOnlyFixIsTestedWhenThePreviousTestFailedOrHasAFindingToJudge(t *testing.T) {
	for _, c := range []struct{ name, setup string }{
		{"a failed criterion", `UPDATE acceptance_verdicts SET status = 'fail' WHERE task_id = $1 AND criterion = 2`},
		{"a finding to judge", `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description, fix_attempts)
			SELECT 'fnd_judge_' || r.task_id, r.organization_id, r.task_id, r.id, 'acceptance', 'low', 'The greeting flickers', 'Seen once.', 1
			FROM runs r WHERE r.task_id = $1 AND r.phase = 'test'`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPRFixWorld(t)
			mustExec(t, w.owner, c.setup, w.wi)
			w.ciOnlyFixLands()
			if !slices.Contains(w.phases, "test") {
				t.Errorf("after the comment: %v, want a tester", w.phases)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, w.wi, delivery.EvTestSkipped); n != 0 {
				t.Errorf("%d skips, want none", n)
			}
		})
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
