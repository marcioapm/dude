package orchestrator_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// roundReview is one review Run of a task, in creation order: its round,
// its range and its first message.
type roundReview struct {
	ID, Of, Base, Prompt string
	Round                int
}

func (w *world) roundReviews(task string) []roundReview {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(review_range->>'of', ''),
			COALESCE(review_range->'base'->>'target', ''), '', COALESCE(review_round, 0)
		FROM runs WHERE task_id = $1 AND phase = 'review' AND replaced_by IS NULL ORDER BY created_at, id`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[roundReview])
	if err != nil {
		w.t.Fatal(err)
	}
	for i := range out {
		out[i].Prompt = w.promptOf(out[i].ID)
	}
	return out
}

// roundsWorld is a conducted world whose first review raises one evidenced
// medium finding, whose later reviews judge it fixed, and whose fixer and
// simplifier each commit.
func roundsWorld(t *testing.T) *world {
	w := conducting(t)
	// A real model, so a reviewer's prompt is the one a real one reads.
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: high", "FACTORY.md says the wrong thing") + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		case "fix", "simplify":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	return w
}

// The review round counts per loop: round 1 is the implementation's first
// review, round 2 the review after its fix, and the simplifier's diff is a
// loop of its own whose first review is round 1 again. Each review Run
// stores its round, and each finding the round of the review that raised
// it.
func TestTheReviewRoundCountsPerLoopAndStartsAgainForTheSimplifier(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 2", func() bool { return w.decisionAt(task) == delivery.PointReviewed && len(w.roundReviews(task)) > 1 })
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.until("the simplifier's review", func() bool {
		rs := w.roundReviews(task)
		return w.decisionAt(task) == delivery.PointReviewed && rs[len(rs)-1].Of == "simplify"
	})
	var rounds []int
	byRun := map[int]int{}
	for _, r := range w.roundReviews(task) {
		if len(rounds) == 0 || rounds[len(rounds)-1] != r.Round {
			rounds = append(rounds, r.Round)
		}
		byRun[r.Round]++
	}
	if fmt.Sprint(rounds) != "[1 2 1]" {
		t.Errorf("review rounds in order: %v, want [1 2 1]\n%s", rounds, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM review_findings f JOIN runs r ON r.id = f.run_id
		WHERE f.task_id = $1 AND f.review_round IS NOT DISTINCT FROM r.review_round AND f.review_round = 1`, task); n != 1 {
		t.Errorf("findings stored with their review's round 1: %d, want 1", n)
	}
}

// A pull request fix's narrow loop is a loop of its own: whatever round
// the loop before it reached, its first review is round 1, over the fix's
// push (pull_request_fix), not a round of the earlier loop.
func TestTheRoundStartsAgainForAPullRequestFixsNarrowLoop(t *testing.T) {
	w := newPRFixWorldDirect(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"round":4,"roundHeads":{"target":"0000000"}}'::jsonb WHERE task_id = $1`, w.wi)
	w.comment("Please rename the greeting.")
	w.until("the fix's review", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2`,
			w.wi, delivery.RangePullRequestFix) > 0
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
		AND review_round IS DISTINCT FROM 1`, w.wi, delivery.RangePullRequestFix); n != 0 {
		t.Errorf("%d reviews of the pull request's fix not round 1\n%s", n, w.describeRuns())
	}
}

// A reviewer restarted keeps the round of the Run it replaces.
func TestARestartedReviewerKeepsItsRound(t *testing.T) {
	w, task, runs := restartable(t)
	stuck := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET review_round = 3 WHERE id = $1`, stuck)
	out := w.must(task, "restart_run", `{"run":"`+stuck+`","note":"again"}`)
	fresh, _ := out["run"].(string)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND review_round = 3`, fresh); n != 1 {
		t.Errorf("the replacement %s is not round 3", fresh)
	}
}

// reviewedRange is one completed review's range in the task's one
// repository: from its range's base (the branch's merge-base with main
// when it reviews the whole branch) to the commit it checked out.
func (w *world) reviewedRanges(task string) [][2]string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT COALESCE(review_range->'base'->>'target', ''), base_refs->>'target'
		FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed' AND replaced_by IS NULL ORDER BY created_at`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	pairs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var p [2]string
		return p, r.Scan(&p[0], &p[1])
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return pairs
}

// git runs git in the fake GitHub's repository.
func (w *world) gitLines(args ...string) []string {
	w.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", w.gh.Repo}, args...)...).Output()
	if err != nil {
		w.t.Fatalf("git %v: %v", args, err)
	}
	return strings.Fields(string(out))
}

// unreviewed is every commit on the task's branch, from main to head, that
// no completed review's range covered.
func (w *world) unreviewed(task, head string) []string {
	w.t.Helper()
	covered := map[string]bool{}
	for _, r := range w.reviewedRanges(task) {
		base := r[0]
		if base == "" {
			base = w.gitLines("merge-base", "main", r[1])[0]
		}
		for _, c := range w.gitLines("rev-list", base+".."+r[1]) {
			covered[c] = true
		}
	}
	var missed []string
	for _, c := range w.gitLines("rev-list", "main.."+head) {
		if !covered[c] {
			missed = append(missed, c)
		}
	}
	return missed
}

// toRoundTwo is a conducted task taken through round 1 and its fix to the
// conductor's decision after round 2; between, before the fix, between
// runs: what lands on the branch besides the fix (a person's push).
func (w *world) toRoundTwo(task string, between func()) {
	w.t.Helper()
	w.roundTwo(task, between, nil)
}

// toRoundTwoAfterFix is toRoundTwo with afterFix run at the decision after
// the fix, before round 2 starts.
func (w *world) toRoundTwoAfterFix(task string, afterFix func()) {
	w.t.Helper()
	w.roundTwo(task, nil, afterFix)
}

func (w *world) roundTwo(task string, between, afterFix func()) {
	w.t.Helper()
	w.talk(task)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	if between != nil {
		between()
	}
	w.must(task, "decide", `{"action":"next"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	if afterFix != nil {
		afterFix()
	}
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 2", func() bool {
		return w.decisionAt(task) == delivery.PointReviewed && w.count(`SELECT count(*) FROM runs WHERE task_id = $1
			AND phase = 'review' AND status = 'completed' AND review_round = 2`, task) > 0
	})
}

// A conducted round 2 reviews only what changed since round 1 reviewed
// the branch (fix_round, from round 1's head), told why; every commit of
// the branch falls in some round's range.
func TestRoundTwoReviewsOnlyTheFixsCommits(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundTwo(task, nil)
	rs := w.roundReviews(task)
	first, second := rs[0], rs[len(rs)-1]
	head1 := w.str(`SELECT base_refs->>'target' FROM runs WHERE id = $1`, first.ID)
	if first.Of != "" || second.Round != 2 || second.Of != delivery.RangeFixRound || second.Base != head1 {
		t.Errorf("round 1 %q; round %d %q from %q, want fix_round from round 1's head %s\n%s",
			first.Of, second.Round, second.Of, second.Base, head1, w.describeRuns())
	}
	head := w.str(`SELECT base_refs->>'target' FROM runs WHERE id = $1`, second.ID)
	if missed := w.unreviewed(task, head); len(missed) > 0 {
		t.Errorf("commits no round reviewed: %v", missed)
	}
	// Its range as the review prompt renders one: the section naming it.
	var scope string
	for _, part := range strings.Split(delivery.Prompt(delivery.PhaseReview, delivery.PromptInput{
		ReviewBase: map[string]string{"target": head1}, ReviewOf: delivery.RangeFixRound}), "\n\n") {
		if strings.Contains(part, head1+"..HEAD") {
			scope = part
		}
	}
	if scope == "" || !strings.Contains(second.Prompt, scope) {
		t.Errorf("round 2's reviewer is not told its range %q:\n%s", scope, second.Prompt)
	}
}

// A commit that lands between rounds besides the fix — a person's push to
// the task branch while the conductor triages round 1 — is in round 2's
// range: no commit falls outside every round.
func TestAPushBetweenRoundsIsInTheNextRoundsRange(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	var pushed string
	w.toRoundTwo(task, func() {
		branch := delivery.BranchFor(task, 1)
		pushed = w.gh.CommitFiles(branch, "A person's own change", map[string]string{"NOTES.md": "mine\n"})
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{heads,target}', to_jsonb($2::text)) WHERE task_id = $1`, task, pushed)
	})
	rs := w.roundReviews(task)
	head := w.str(`SELECT base_refs->>'target' FROM runs WHERE id = $1`, rs[len(rs)-1].ID)
	if missed := w.unreviewed(task, head); len(missed) > 0 {
		t.Errorf("commits no round reviewed: %v (the person's %s)\n%s", missed, pushed, w.describeRuns())
	}
}

// Two commits after round 1 — the fix's, and one more on top of it before
// the next round starts (a second commit at the after-fix decision) — are
// both round 2's.
func TestTwoCommitsAfterARoundAreBothTheNextRounds(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	var fixHead string
	w.toRoundTwoAfterFix(task, func() {
		fixHead = w.str(`SELECT state->'heads'->>'target' FROM workflow_runs WHERE task_id = $1`, task)
		pushed := w.gh.CommitFiles(delivery.BranchFor(task, 1), "A second commit", map[string]string{"NOTES.md": "more\n"})
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{heads,target}', to_jsonb($2::text)) WHERE task_id = $1`, task, pushed)
	})
	rs := w.roundReviews(task)
	second := rs[len(rs)-1]
	head := w.str(`SELECT base_refs->>'target' FROM runs WHERE id = $1`, second.ID)
	if n := len(w.gitLines("rev-list", second.Base+".."+head)); n < 2 || head == fixHead {
		t.Errorf("round 2's range %s..%s has %d commits, want the fix's and the one after it", second.Base, head, n)
	}
	if missed := w.unreviewed(task, head); len(missed) > 0 {
		t.Errorf("commits no round reviewed: %v\n%s", missed, w.describeRuns())
	}
}

// A Deliver delivery's later round keeps today's range: the whole branch.
func TestADeliverRoundTwoReviewsTheWholeBranch(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.deliver(task)
	w.until("round 2", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed' AND review_round = 2`, task) > 0
	})
	for _, r := range w.roundReviews(task) {
		if r.Round <= 2 && r.Of != "" {
			t.Errorf("Deliver's round %d reviews %q, want the whole branch", r.Round, r.Of)
		}
	}
}

// What earlier rounds decided reaches a conducted loop's later reviewer: a
// finding the conductor dismissed in round 1, with its reason, is listed
// to round 2's, and to round 3's with the finding round 2 judged fixed.
func TestTheReviewerIsShownWhatEarlierRoundsDecided(t *testing.T) {
	w := conducting(t)
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: high", "FACTORY.md says the wrong thing") +
					"---\n" + evidencedFinding("severity: medium", "FACTORY.md could be shorter") + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	task := w.task()
	var kept, dismissed string
	w.toRoundTwo(task, func() {
		kept = w.str(`SELECT id FROM review_findings WHERE task_id = $1 AND title = 'FACTORY.md says the wrong thing'`, task)
		dismissed = w.str(`SELECT id FROM review_findings WHERE task_id = $1 AND title = 'FACTORY.md could be shorter'`, task)
		w.must(task, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"Its length is the house style"}`, dismissed))
	})
	rs := w.roundReviews(task)
	second := rs[len(rs)-1]
	if !strings.Contains(second.Prompt, dismissed) || !strings.Contains(second.Prompt, "Its length is the house style") ||
		strings.Contains(second.Prompt, "- "+kept) {
		t.Errorf("round 2's reviewer is not shown %s dismissed with its reason alone:\n%s\n%+v\n%s", dismissed, second.Prompt, rs, w.describeRuns())
	}
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	var third string
	w.until("round 3", func() bool {
		third = w.promptOf(w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 3`, task))
		return third != ""
	})
	judge := w.str(`SELECT resolved_by_run_id FROM review_findings WHERE id = $1 AND status = 'resolved'`, kept)
	if judge == "" || !strings.Contains(third, "- "+kept) || !strings.Contains(third, judge) || !strings.Contains(third, dismissed) {
		t.Errorf("round 3's reviewer is not shown %s judged fixed by %q and %s dismissed:\n%s", kept, judge, dismissed, third)
	}
}

// A conducted loop's fixer is told its round and how a round is fixed;
// a Deliver delivery's fixer is not, and its prompt has no round.
func TestTheConductedFixerIsToldItsRound(t *testing.T) {
	for _, conducted := range []bool{true, false} {
		t.Run(map[bool]string{true: "conducted", false: "deliver"}[conducted], func(t *testing.T) {
			w := roundsWorld(t)
			w.onModel("fixer", "llm-fix")
			task := w.task()
			if conducted {
				w.talk(task)
				w.implemented(task)
				w.must(task, "decide", `{"action":"next"}`)
				w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
				w.must(task, "decide", `{"action":"next"}`)
			} else {
				w.deliver(task)
			}
			var prompt string
			w.until("the fixer's prompt", func() bool {
				prompt = w.promptOf(w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'fix'`, task))
				return prompt != ""
			})
			// The paragraph as a conducted fixer of round 1 is given it.
			told := delivery.Prompt(delivery.PhaseFix, delivery.PromptInput{ConductedLoop: true, Round: 1})
			plain := delivery.Prompt(delivery.PhaseFix, delivery.PromptInput{})
			var first string
			for _, part := range strings.Split(told, "\n\n") {
				if !strings.Contains(plain, part) {
					first = part
					break
				}
			}
			if first == "" {
				t.Fatalf("a conducted fixer's prompt adds nothing:\n%s", told)
			}
			if got := strings.Contains(prompt, first); got != conducted {
				t.Errorf("fixer told its round: %v, want %v\n%s", got, conducted, prompt)
			}
			round := w.count(`SELECT COALESCE(max(review_round), 0) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
			if want := map[bool]int{true: 1, false: 0}[conducted]; round != want {
				t.Errorf("the fix Run's round %d, want %d", round, want)
			}
		})
	}
}
