package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// resuming is the world with rounds going on from the last: its project's
// policy, and the delivery's, keep reviewers and testers for their next
// round.
func resuming(t *testing.T) *world {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"resumeRounds":true}' WHERE id = $1`, w.project)
	return w
}

// A conductor's start_phase review whose reviewer is resumed gets the
// conductor's note in its round message, as a fresh reviewer gets it in
// its prompt.
func TestDirectedResumedReviewReceivesConductorNote(t *testing.T) {
	w := resuming(t)
	w.onModel("conductor", "fake/scripted")
	w.withTools()
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if status, out := w.chat(wi, "Look after the pull request."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on PR", func() bool { return w.waitingOnPR(wi) })
	w.must(wi, "start_phase", `{"phase":"review","categories":["correctness"],"note":"Inspect rollback and reproduce the timeout before approving."}`)
	w.until("directed review finished", func() bool { return w.decisionAt(wi) == "after_review" })
	for _, lr := range w.luxRunsOf("review") {
		for _, input := range w.lux.Inputs(lr.ID) {
			if strings.Contains(input, "## A new round") && strings.Contains(input, "Inspect rollback and reproduce the timeout before approving.") {
				return
			}
		}
	}
	t.Fatal("resumed agent did not receive conductor note")
}

// A project image changed during the fix (A to B): the continuation is not
// resumed on A; it is replaced by a replayed fresh Run submitted on B.
func TestChangedImageReplacesContinuedReviewer(t *testing.T) {
	w := resuming(t)
	w.onModel("reviewer", "llm-review")
	wi := w.task()
	w.names(wi, w.repoID)
	var once sync.Once
	w.lux.BeforeStart(func(id string) {
		if fix := w.luxRunsOf("fix"); len(fix) == 1 && fix[0].ID == id {
			once.Do(func() {
				mustExec(t, w.owner, `UPDATE projects SET runtime_image = 'agent:image-b' WHERE id=$1`, w.project)
			})
		}
	})
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	reviews := w.reviewRows(wi)
	if len(reviews) < 3 || reviews[1].How != delivery.ContinuesResume ||
		reviews[2].Continues != reviews[0].ID || reviews[2].How != delivery.ContinuesReplay {
		t.Fatalf("reviews: %+v; want the first, its continuation, and a replay of the first", reviews)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND replaced_by = $2`, reviews[1].ID, reviews[2].ID); n != 1 {
		t.Fatal("the continuation on image A was not replaced")
	}
	luxReviews := w.luxRunsOf("review")
	if len(luxReviews) != 2 || luxReviews[0].Resumed != 0 {
		t.Fatalf("reviewers: %+v", luxReviews)
	}
	var spec struct {
		Image struct {
			Ref string `json:"ref"`
		} `json:"image"`
	}
	if err := json.Unmarshal(luxReviews[1].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Image.Ref != "agent:image-b" {
		t.Fatalf("fresh image %q", spec.Image.Ref)
	}
}

func resumingPolicy() delivery.Policy {
	p := scriptedPolicy()
	p.ResumeRounds = true
	return p
}

// luxRunsOf are the fake lux's Runs of a dude phase, in submit order.
func (w *world) luxRunsOf(phase string) []*fakelux.Run {
	var out []*fakelux.Run
	for _, r := range w.lux.Runs() {
		var spec struct {
			Labels map[string]string `json:"labels"`
		}
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.phase"] == phase {
			out = append(out, r)
		}
	}
	return out
}

// reviewRow is one review Run as the test reads it.
type reviewRow struct {
	ID, Continues, How, LuxRun string
	Base                       map[string]string
}

func (w *world) reviewRows(task string) []reviewRow {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(continues_run_id, ''), COALESCE(continues_how, ''),
			COALESCE((SELECT payload->>'luxRunId' FROM events e WHERE e.run_id = r.id AND e.event_type = 'run.lease.acquired'), r.lux_run_id, ''),
			base_refs
		FROM runs r WHERE task_id = $1 AND phase = 'review' ORDER BY created_at, id`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []reviewRow
	for rows.Next() {
		var r reviewRow
		if err := rows.Scan(&r.ID, &r.Continues, &r.How, &r.LuxRun, &r.Base); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// A re-test takes the failed test's tester back up: a new Run continuing
// it on the same lux Run, moved to the fix, which records its own verdicts
// for the new head — the gate reads those, not the first test's — and
// judges the finding it raised.
func TestARetestResumesThePreviousTester(t *testing.T) {
	w := resuming(t)
	w.onModel("qa_browser", fakeagent.TestFailOnceModel)
	wi := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	w.names(wi, w.repoID)
	p := resumingPolicy()
	p.Test = true
	w.start(wi, p)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(continues_run_id, ''), COALESCE(continues_how, ''), base_refs->>'target'
		FROM runs WHERE task_id = $1 AND phase = 'test' ORDER BY created_at, id`, wi)
	if err != nil {
		t.Fatal(err)
	}
	type testRun struct{ ID, Continues, How, Head string }
	tests, err := pgx.CollectRows(rows, pgx.RowToStructByPos[testRun])
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 {
		t.Fatalf("%d tests, want the failed one and its re-test", len(tests))
	}
	if tests[1].Continues != tests[0].ID || tests[1].How != delivery.ContinuesResume {
		t.Fatalf("the re-test continues %q (%s), want %s resumed", tests[1].Continues, tests[1].How, tests[0].ID)
	}
	if luxTests := w.luxRunsOf("test"); len(luxTests) != 1 || luxTests[0].Resumed != 1 {
		t.Fatalf("%d testers submitted to lux, want one, resumed once", len(luxTests))
	}
	// Each test has its own verdicts: the first failed criterion 1, the
	// re-test of the fix's head passed both.
	for i, want := range []string{"fail,pass", "pass,pass"} {
		var got string
		if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(string_agg(status::text, ',' ORDER BY criterion), '')
			FROM acceptance_verdicts WHERE run_id = $1`, tests[i].ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("test %d's verdicts = %q, want %q", i+1, got, want)
		}
	}
	var status, resolvedBy string
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text, COALESCE(resolved_by_run_id, '') FROM review_findings
		WHERE task_id = $1 AND run_id = $2`, wi, tests[0].ID).Scan(&status, &resolvedBy)
	if status != "resolved" || resolvedBy != tests[1].ID {
		t.Errorf("the tester's finding is %s by %q, want resolved by the re-test %s", status, resolvedBy, tests[1].ID)
	}
}

// A re-review whose reviewer lux can no longer resume (an operator
// cancelled it while the fix ran) is replaced in its slot by a fresh Run:
// its first message is the first round's conversation, and it reviews the
// commits since that round, judging the finding it raised.
func TestAnUnresumableReviewerIsReplacedByAFreshRunGivenItsConversation(t *testing.T) {
	w := resuming(t)
	// Not a scripted model: lux is given the prompt itself, which fakelux's
	// scripted agent plays all the same.
	w.onModel("reviewer", "llm-review")
	wi := w.task()
	w.names(wi, w.repoID)
	var once sync.Once
	w.lux.BeforeStart(func(luxRunID string) {
		if fix := w.luxRunsOf("fix"); len(fix) == 1 && fix[0].ID == luxRunID {
			once.Do(func() { w.lux.CancelInLux(w.luxRunsOf("review")[0].ID) })
		}
	})
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	reviews := w.reviewRows(wi)
	if len(reviews) < 3 {
		t.Fatalf("%d review Runs, want the first, its continuation and the fresh one in its slot", len(reviews))
	}
	first, cont, fresh := reviews[0], reviews[1], reviews[2]
	if cont.Continues != first.ID || cont.How != delivery.ContinuesResume {
		t.Fatalf("the re-review %s continues %q (%s), want %s resumed", cont.ID, cont.Continues, cont.How, first.ID)
	}
	if fresh.Continues != first.ID || fresh.How != delivery.ContinuesReplay {
		t.Fatalf("the Run in its slot continues %q (%s), want %s replayed", fresh.Continues, fresh.How, first.ID)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND replaced_by = $2`, cont.ID, fresh.ID); n != 1 {
		t.Fatal("the continuation was not replaced by the fresh Run")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.restarted' AND payload->>'by' = 'dude'`, cont.ID); n != 1 {
		t.Fatal("no run.restarted by dude")
	}
	luxReviews := w.luxRunsOf("review")
	if len(luxReviews) != 2 {
		t.Fatalf("%d reviewers submitted to lux, want the first and the fresh one", len(luxReviews))
	}
	prompt := luxReviews[1].Prompt()
	if !strings.Contains(prompt, "## The conversation so far") || !strings.Contains(prompt, "One problem:") {
		t.Fatalf("the fresh reviewer's prompt does not replay the first round's conversation:\n%s", prompt)
	}
	if want := "`" + first.Base["target"] + "..HEAD`"; !strings.Contains(prompt, want) {
		t.Errorf("the fresh reviewer does not review the commits since the last round (%s):\n%s", want, prompt)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND review_range->>'of' = $2 AND review_range->'base'->>'target' = $3`,
		fresh.ID, delivery.RangeSinceLastRound, first.Base["target"]); n != 1 {
		t.Error("the fresh reviewer's range is not since the last round")
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'resolved' AND resolved_by_run_id = $2 AND fix_attempts = 1`,
		wi, fresh.ID); n != 1 {
		t.Error("the fresh reviewer did not judge the finding, once attempted")
	}
}

// A reviewer that comes back from its resume with a blank session has
// none of its first round: its report is not taken, and a fresh Run given
// the first round's conversation takes its slot.
func TestABlankSessionOnResumeIsReplacedByAFreshRun(t *testing.T) {
	w := resuming(t)
	w.onModel("reviewer", "llm-review")
	wi := w.task()
	w.names(wi, w.repoID)
	var once sync.Once
	w.lux.BeforeStart(func(luxRunID string) {
		if fix := w.luxRunsOf("fix"); len(fix) == 1 && fix[0].ID == luxRunID {
			once.Do(func() { w.lux.LoseSession(w.luxRunsOf("review")[0].ID) })
		}
	})
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	reviews := w.reviewRows(wi)
	if len(reviews) < 3 {
		t.Fatalf("%d review Runs, want the first, its continuation and the fresh one in its slot", len(reviews))
	}
	first, cont, fresh := reviews[0], reviews[1], reviews[2]
	if cont.How != delivery.ContinuesResume || fresh.Continues != first.ID || fresh.How != delivery.ContinuesReplay {
		t.Fatalf("continuation %s (%s), then %s continues %q (%s)", cont.ID, cont.How, fresh.ID, fresh.Continues, fresh.How)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.session.replaced'`, cont.ID); n != 1 {
		t.Fatal("the blank session was not recorded on the continuation")
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE run_id = $1`, cont.ID); n != 0 {
		t.Errorf("the blank continuation's report was taken: %d findings", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND replaced_by = $2`, cont.ID, fresh.ID); n != 1 {
		t.Fatal("the continuation was not replaced by the fresh Run")
	}
	if p := w.luxRunsOf("review")[1].Prompt(); !strings.Contains(p, "## The conversation so far") {
		t.Errorf("the fresh reviewer is not given the first round's conversation:\n%s", p)
	}
}

// A fix that changed more than MaxResumeDeltaLines gets fresh eyes: a new
// reviewer, no conversation, the whole branch, and the first round's lux
// Run let go.
func TestALargeChangeGetsAFreshReviewerOfTheWholeBranch(t *testing.T) {
	w := resuming(t)
	w.onModel("reviewer", "llm-review")
	wi := w.task()
	w.names(wi, w.repoID)
	w.gh.CompareAnswer = func(base, head string, answer map[string]any) {
		answer["files"] = []any{map[string]any{"filename": "big.go", "status": "modified", "additions": delivery.MaxResumeDeltaLines, "deletions": 1}}
	}
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	reviews := w.reviewRows(wi)
	if len(reviews) < 2 {
		t.Fatalf("%d review Runs", len(reviews))
	}
	first, second := reviews[0], reviews[1]
	if second.Continues != "" {
		t.Fatalf("the re-review continues %q (%s), want a fresh Run", second.Continues, second.How)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'fresh' = 'large_delta'`, second.ID); n != 1 {
		t.Error("the re-review's run.created does not say it is fresh for a large change")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND review_range IS NULL`, second.ID); n != 1 {
		t.Error("the fresh re-review has a range, want the whole branch")
	}
	luxReviews := w.luxRunsOf("review")
	if len(luxReviews) < 2 || luxReviews[0].Resumed != 0 {
		t.Fatalf("%d reviewers submitted, the first resumed %d times; want a second submitted and none resumed", len(luxReviews), luxReviews[0].Resumed)
	}
	p := luxReviews[1].Prompt()
	if strings.Contains(p, "## The conversation so far") || strings.Contains(p, "..HEAD`") {
		t.Errorf("the fresh re-review was given a conversation or a range:\n%s", p)
	}
	if !strings.Contains(p, "**F1**") {
		t.Errorf("the fresh re-review is not given the finding to judge:\n%s", p)
	}
	w.until("the first round's lux Run let go", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel' AND NOT keep`, first.ID) == 1
	})
}

// A re-review takes the first round's reviewer back up: a new Run that
// continues it, on the same lux Run, moved to the fix's head and told what
// changed and which of its findings to judge. Its verdict resolves the
// finding it raised, which keeps its id and its one fix attempt; the agent's
// cost is counted once across the two rows.
func TestASecondReviewRoundResumesTheFirstReviewer(t *testing.T) {
	w := resuming(t)
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	reviews := w.reviewRows(wi)
	if len(reviews) < 2 {
		t.Fatalf("%d review Runs, want the first round and its re-review", len(reviews))
	}
	first, second := reviews[0], reviews[1]
	if second.Continues != first.ID || second.How != delivery.ContinuesResume {
		t.Fatalf("the re-review %s continues %q (%s), want %s resumed", second.ID, second.Continues, second.How, first.ID)
	}
	if first.LuxRun == "" || second.LuxRun != "" && second.LuxRun != first.LuxRun {
		t.Fatalf("lux Runs: first %q, re-review %q", first.LuxRun, second.LuxRun)
	}
	// Every round went on in the one lux Run: no second reviewer was
	// submitted.
	luxReviews := w.luxRunsOf("review")
	if len(luxReviews) != 1 || luxReviews[0].ID != first.LuxRun {
		t.Fatalf("%d reviewers submitted to lux, want the first round's only", len(luxReviews))
	}
	lr := luxReviews[0]
	if len(lr.ResumeSyncs) == 0 || len(lr.ResumeSyncs[0]) != 1 || lr.ResumeSyncs[0][0].Ref != second.Base["target"] {
		t.Fatalf("resume syncs %v, want the checkout moved to the fix's head %s", lr.ResumeSyncs, second.Base["target"])
	}
	inputs := w.lux.Inputs(lr.ID)
	var round string
	for _, in := range inputs {
		if strings.Contains(in, "## A new round") {
			round = in
			break
		}
	}
	if round == "" {
		t.Fatalf("the resumed reviewer was never told of its new round; inputs: %q", inputs)
	}
	if want := "git log " + first.Base["target"] + "..HEAD"; !strings.Contains(round, want) {
		t.Errorf("the round message does not name the commits since (%s):\n%s", want, round)
	}
	if !strings.Contains(round, "**F1**") || !strings.Contains(round, "verdicts:") {
		t.Errorf("the round message does not list the finding to judge:\n%s", round)
	}
	if !strings.Contains(round, "outside the lines it touched") {
		t.Errorf("the round message does not warn the change may break what it did not touch:\n%s", round)
	}

	// The finding: raised once, by the first round, judged fixed by the
	// second, its one fix attempt kept.
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("%d findings, want the one raised", n)
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND run_id = $2 AND status = 'resolved' AND fix_attempts = 1`,
		wi, first.ID); n != 1 {
		t.Fatalf("the first round's finding is not resolved on its own id with one attempt")
	}
	// The agent's running cost is its lux Run's: split across the rows,
	// never counted twice.
	if n := w.count(`SELECT count(*) FROM (SELECT sum(agent_cost_usd) AS s FROM runs WHERE task_id = $1 AND phase = 'review') x
		WHERE s = 0.01`, wi); n != 1 {
		t.Fatal("the reviewers' agent cost is not the one lux Run's 0.01")
	}
}
