package orchestrator_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// roundReview is one review Run of a task, in creation order: its round,
// its range, the commit it checked out and its first message.
type roundReview struct {
	ID, Of, Base, Head, Prompt string
	Round                      int
}

func (w *world) roundReviews(task string) []roundReview {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(review_range->>'of', ''),
			COALESCE(review_range->'base'->>'target', ''), COALESCE(base_refs->>'target', ''), '', COALESCE(review_round, 0)
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

// lastReview is the task's latest review Run.
func (w *world) lastReview(task string) roundReview {
	w.t.Helper()
	rs := w.roundReviews(task)
	if len(rs) == 0 {
		w.t.Fatalf("no review Run\n%s", w.describeRuns())
	}
	return rs[len(rs)-1]
}

const (
	wrongThing = "FACTORY.md says the wrong thing"
	shorter    = "FACTORY.md could be shorter"
)

// scriptRounds has w's reviewers on a real model, so a reviewer's prompt
// is the one a real one reads: the first review raises findings (YAML
// documents), each later one answers verdict on F1, and the fixer and
// simplifier each commit.
func (w *world) scriptRounds(verdict string, findings ...string) *world {
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + strings.Join(findings, "---\n") + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: " + verdict + "\n```\n"}
		case "fix", "simplify":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	return w
}

// roundsWorld is a conducted world whose first review raises one evidenced
// high finding, whose later reviews judge it fixed, and whose fixer and
// simplifier each commit.
func roundsWorld(t *testing.T) *world {
	return conducting(t).scriptRounds("fixed", evidencedFinding("severity: high", wrongThing))
}

// reviewOf is the category and round of the review Run lux is asked to
// start; "", 0 for any other Run. It runs on fakelux's goroutine, beside
// the test's: read on a connection of w.app's pool, never w.owner (one
// connection, the test goroutine's), and never failing the test.
func (w *world) reviewOf(spec map[string]any) (category string, round int) {
	labels, _ := spec["labels"].(map[string]any)
	if labels["dude.phase"] != "review" {
		return "", 0
	}
	_ = w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT COALESCE(category, ''), COALESCE(review_round, 0) FROM runs WHERE id = $1`,
			fmt.Sprint(labels["dude.run"])).Scan(&category, &round)
	})
	return category, round
}

// crashRoundOneSecurity has round 1's security reviewer crash, once.
func (w *world) crashRoundOneSecurity() {
	scripted := w.lux.Decide
	var crashed atomic.Bool
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if category, round := w.reviewOf(spec); round == 1 && category == "security" && crashed.CompareAndSwap(false, true) {
			return fakelux.Behaviour{Crash: true}
		}
		return scripted(spec)
	}
}

// reviewedRound says a review of round n completed and the conductor is
// at the decision after it.
func (w *world) reviewedRound(task string, n int) bool {
	return w.decisionAt(task) == delivery.PointReviewed && w.count(`SELECT count(*) FROM runs WHERE task_id = $1
		AND phase = 'review' AND status = 'completed' AND review_round = $2`, task, n) > 0
}

// toRoundOne is a conducted task implemented and taken to the conductor's
// decision after round 1.
func (w *world) toRoundOne(task string) {
	w.t.Helper()
	w.talk(task)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
}

// toRoundTwo takes a conducted task at the decision after round 1 through
// its fix to the decision after round 2; afterFix, when not nil, runs at
// the decision after the fix, before round 2 starts.
func (w *world) toRoundTwo(task string, afterFix func()) {
	w.t.Helper()
	w.must(task, "decide", `{"action":"next"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	if afterFix != nil {
		afterFix()
	}
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 2", func() bool { return w.reviewedRound(task, 2) })
}

// The review round counts per loop: round 1 is the implementation's first
// review, round 2 the review after its fix, and the simplifier's diff is a
// loop of its own whose first review is round 1 again. Each review Run
// stores its round, and each finding the round of the review that raised
// it.
func TestTheReviewRoundCountsPerLoopAndStartsAgainForTheSimplifier(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.toRoundTwo(task, nil)
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.until("the simplifier's review", func() bool {
		return w.decisionAt(task) == delivery.PointReviewed && w.lastReview(task).Of == "simplify"
	})
	var rounds []int
	for _, r := range w.roundReviews(task) {
		if len(rounds) == 0 || rounds[len(rounds)-1] != r.Round {
			rounds = append(rounds, r.Round)
		}
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

// gitLines runs git in the fake GitHub's repository: its output's words.
func (w *world) gitLines(args ...string) []string {
	w.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", w.gh.Repo}, args...)...).Output()
	if err != nil {
		w.t.Fatalf("git %v: %v", args, err)
	}
	return strings.Fields(string(out))
}

// unreviewed is every commit on the task's branch, from main to head, that
// no completed review's range covered: from its range's base (the
// branch's merge-base with main when it reviews the whole branch) to the
// commit it checked out.
func (w *world) unreviewed(task, head string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT COALESCE(review_range->'base'->>'target', ''), base_refs->>'target'
		FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed' AND replaced_by IS NULL ORDER BY created_at`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	ranges, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var p [2]string
		return p, r.Scan(&p[0], &p[1])
	})
	if err != nil {
		w.t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, r := range ranges {
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

// allReviewed says every commit of the task's branch, up to the latest
// review's head, falls in some completed review's range.
func (w *world) allReviewed(task string) {
	w.t.Helper()
	if missed := w.unreviewed(task, w.lastReview(task).Head); len(missed) > 0 {
		w.t.Errorf("commits no round reviewed: %v\n%s", missed, w.describeRuns())
	}
}

// pushed commits a person's change to the task branch and moves the
// delivery's heads to it: what lands on the branch besides dude's Runs.
func (w *world) pushed(task, message, notes string) string {
	w.t.Helper()
	sha := w.gh.CommitFiles(delivery.BranchFor(task, 1), message, map[string]string{"NOTES.md": notes})
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{heads,target}', to_jsonb($2::text)) WHERE task_id = $1`, task, sha)
	return sha
}

// A conducted round 2 reviews only what changed since round 1 reviewed
// the branch (fix_round, from round 1's head), told why; every commit of
// the branch falls in some round's range.
func TestRoundTwoReviewsOnlyTheFixsCommits(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.toRoundTwo(task, nil)
	rs := w.roundReviews(task)
	first, second := rs[0], rs[len(rs)-1]
	if first.Of != "" || second.Round != 2 || second.Of != delivery.RangeFixRound || second.Base != first.Head {
		t.Errorf("round 1 %q; round %d %q from %q, want fix_round from round 1's head %s\n%s",
			first.Of, second.Round, second.Of, second.Base, first.Head, w.describeRuns())
	}
	w.allReviewed(task)
	// Its range as the review prompt renders one: the section naming it.
	var scope string
	for _, part := range strings.Split(delivery.Prompt(delivery.PhaseReview, delivery.PromptInput{
		ReviewBase: map[string]string{"target": first.Head}, ReviewOf: delivery.RangeFixRound}), "\n\n") {
		if strings.Contains(part, first.Head+"..HEAD") {
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
	w.toRoundOne(task)
	w.pushed(task, "A person's own change", "mine\n")
	w.toRoundTwo(task, nil)
	w.allReviewed(task)
}

// Two commits after round 1 — the fix's, and one more on top of it before
// the next round starts (a second commit at the after-fix decision) — are
// both round 2's.
func TestTwoCommitsAfterARoundAreBothTheNextRounds(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	var fixHead string
	w.toRoundTwo(task, func() {
		fixHead = w.str(`SELECT state->'heads'->>'target' FROM workflow_runs WHERE task_id = $1`, task)
		w.pushed(task, "A second commit", "more\n")
	})
	second := w.lastReview(task)
	if n := len(w.gitLines("rev-list", second.Base+".."+second.Head)); n < 2 || second.Head == fixHead {
		t.Errorf("round 2's range %s..%s has %d commits, want the fix's and the one after it", second.Base, second.Head, n)
	}
	w.allReviewed(task)
}

// A Deliver delivery's later round reviews the whole branch.
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
	w := conducting(t).scriptRounds("fixed", evidencedFinding("severity: high", wrongThing), evidencedFinding("severity: medium", shorter))
	task := w.task()
	w.toRoundOne(task)
	kept := w.str(`SELECT id FROM review_findings WHERE task_id = $1 AND title = $2`, task, wrongThing)
	dismissed := w.dismissShorter(task)
	w.toRoundTwo(task, nil)
	second := w.lastReview(task)
	if !strings.Contains(second.Prompt, dismissed) || !strings.Contains(second.Prompt, "Its length is the house style") ||
		strings.Contains(second.Prompt, "- "+kept) {
		t.Errorf("round 2's reviewer is not shown %s dismissed with its reason alone:\n%s\n%s", dismissed, second.Prompt, w.describeRuns())
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

// dismissShorter is the conductor dismissing round 1's shorter finding,
// with its reason: its id.
func (w *world) dismissShorter(task string) string {
	w.t.Helper()
	id := w.str(`SELECT id FROM review_findings WHERE task_id = $1 AND title = $2`, task, shorter)
	w.must(task, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"Its length is the house style"}`, id))
	return id
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
				w.toRoundOne(task)
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

// conductedPR is a task with its pull request open, which the conductor
// was asked to look after: the delivery waits on it.
func (w *world) conductedPR() string {
	w.t.Helper()
	task := w.prOpen(w.task(), prAt{})
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		w.t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	return task
}

// mainMergedInto is another task's work landing on main, and main merged
// into branch on GitHub, as its Update branch does: the merge.
func (w *world) mainMergedInto(branch string) string {
	w.t.Helper()
	w.gh.CommitFiles("main", "Another task's work", map[string]string{"OTHER.md": "theirs\n"})
	return w.gh.MergeInto(branch, "main", nil)
}

// notMainsChange says review r, after main was merged in at merge, is the
// whole branch or starts at the merge, and its range covers none of
// main's commits since the task branch left it: what another task wrote
// is never a round's change.
func (w *world) notMainsChange(r roundReview, merge string) {
	w.t.Helper()
	if r.Of != "" && r.Base != merge {
		w.t.Errorf("round %d reviews %s from %s, want the whole branch or from the merge %s\n%s", r.Round, r.Of, r.Base, merge, w.describeRuns())
	}
	if r.Base == "" {
		return
	}
	ranged := map[string]bool{}
	for _, c := range w.gitLines("rev-list", r.Base+".."+r.Head) {
		ranged[c] = true
	}
	for _, c := range w.gitLines("rev-list", r.Head+"^1..main") {
		if ranged[c] {
			w.t.Errorf("round %d's range has main's commit %s\n%s", r.Round, c, w.describeRuns())
		}
	}
}

// A person's Update branch merges main into the open pull request between
// two conducted rounds: the next round, started from GitHub's head (the
// merge), is the whole branch or starts at the merge — never a range from
// the round before's head that carries main's commits as the round's.
func TestABaseMergeBetweenRoundsIsNotTheNextRoundsChange(t *testing.T) {
	w := conducting(t)
	task := w.conductedPR()
	review := func(n int) roundReview {
		w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
		w.until("the review's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
		r := w.lastReview(task)
		w.must(task, "decide", `{"action":"next"}`)
		w.until("waiting again", func() bool { return w.waitingOnPR(task) })
		if r.Round != n {
			t.Fatalf("review round %d, want %d\n%s", r.Round, n, w.describeRuns())
		}
		return r
	}
	review(1)
	merge := w.mainMergedInto(w.gh.Pull(1).Head)
	w.notMainsChange(review(2), merge)
}

// A lens of round 1 fails on the open pull request, a person's Update
// branch merges main into it, and a person retries the lens: the retry
// starts from GitHub's head (the merge), so round 2 is the whole branch or
// starts at the merge — never fix_round from the heads before the merge,
// which would carry main's commits as the round's change.
func TestARetriedLensAfterAnUpdateBranchDoesNotNarrowOverMain(t *testing.T) {
	w := conducting(t)
	w.crashRoundOneSecurity()
	task := w.conductedPR()
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness","security"]}`)
	w.until("the failed reviewer escalated", func() bool { return w.escalationReason(task) == "review_failed" })
	merge := w.mainMergedInto(w.gh.Pull(1).Head)
	w.retryReview(task)
	w.until("round 1's review decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting again", func() bool { return w.waitingOnPR(task) })
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("round 2's review decision", func() bool { return w.reviewedRound(task, 2) })
	second := w.lastReview(task)
	if second.Round != 2 {
		t.Fatalf("review round %d, want 2\n%s", second.Round, w.describeRuns())
	}
	w.notMainsChange(second, merge)
}

// In a one-repository task a lens of round 1 fails on the open pull
// request, a person pushes P to it and retries the lens, and after the
// round dude edits the branch (E): round 2 is the whole branch, or a range
// that has P — never fix_round from P, which round 1's other lenses never
// saw.
func TestARetriedLensAfterAPersonsPushInAOneRepositoryTaskReviewsTheWholeBranch(t *testing.T) {
	w := conducting(t)
	w.crashRoundOneSecurity()
	task := w.conductedPR()
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness","security"]}`)
	w.until("the failed reviewer escalated", func() bool { return w.escalationReason(task) == "review_failed" })
	p := w.gh.CommitFiles(w.gh.Pull(1).Head, "A person's push", map[string]string{"PERSON.md": "theirs\n"})
	w.retryReview(task)
	w.until("round 1's review decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting again", func() bool { return w.waitingOnPR(task) })
	w.pushed(task, "dude's edit", "dude\n")
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("round 2's review decision", func() bool { return w.reviewedRound(task, 2) })
	second := w.lastReview(task)
	if second.Round != 2 {
		t.Fatalf("review round %d, want 2\n%s", second.Round, w.describeRuns())
	}
	if second.Of == "" {
		return
	}
	for _, c := range w.gitLines("rev-list", second.Base+".."+second.Head) {
		if c == p {
			return
		}
	}
	t.Errorf("round 2 reviews %s from %s to %s, which leaves out the person's push %s\n%s",
		second.Of, second.Base, second.Head, p, w.describeRuns())
}

// retryReview is a person retrying the review that escalated.
func (w *world) retryReview(task string) {
	w.t.Helper()
	if status, body := w.call("/internal/tasks/"+task+"/decide", map[string]any{"action": "retry"}); status != 200 {
		w.t.Fatalf("retry: %d %v", status, body)
	}
}

// openPullRequest answers the conductor's pull-request question Open, at
// the gate, and opens it.
func (w *world) openPullRequest(task string) {
	w.t.Helper()
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chat(task, "Open"); status != 200 {
		w.t.Fatalf("answer Open: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
}

// reviewBeforePR is the review the conductor starts at the decision
// before the pull request, once it has its decision.
func (w *world) reviewBeforePR(task string) roundReview {
	w.t.Helper()
	w.until("the decision after the merge", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the review's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	return w.lastReview(task)
}

// Before the pull request, dude merges the moved base into the task
// branch as it opens it (whenBehind update). The review the conductor
// starts at the pull request's wait is the whole branch or starts at the
// merge, never one carrying main's commits as its change.
func TestDudesBaseMergeBeforeThePullRequestIsNotTheNextRoundsChange(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.reachGate(task)
	w.gh.CommitFiles("main", "Another task's work", map[string]string{"OTHER.md": "theirs\n"})
	w.openPullRequest(task)
	w.until("the base merged in", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'action' = 'merge-base'`, task) > 0
	})
	review := w.reviewBeforePR(task)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'action' = 'merge-base'`, task); n != 1 {
		t.Fatalf("%d merges of the base before the pull request, want 1\n%s", n, w.describeRuns())
	}
	merge := w.str(`SELECT payload->>'head' FROM events WHERE task_id = $1 AND payload->>'action' = 'merge-base'`, task)
	w.notMainsChange(review, merge)
}

// Main is merged into the task branch on GitHub before the gate's answer,
// and dude adopts the moved head as it opens the pull request. The review
// the conductor starts next is the whole branch or starts at the merge,
// never carrying main's commits.
func TestABaseMergeOnGitHubBeforeThePullRequestIsNotTheNextRoundsChange(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.reachGate(task)
	merge := w.mainMergedInto(delivery.BranchFor(task, 1))
	w.openPullRequest(task)
	w.notMainsChange(w.reviewBeforePR(task), merge)
}

// publishedDuringRoundOne is a conducted round 1 of two lenses
// (correctness, security) created at H1, whose reviewers lux holds until
// the conductor's publish moves the task branch to H2 (its commit on
// GitHub and the delivery's heads, as PublishedTx advances them). With
// failOne, the security reviewer crashes and a person retries it, at H2.
// Round 1's findings go to a fix (H3) and round 2 follows. Returns H1.
func (w *world) publishedDuringRoundOne(task string, failOne bool) string {
	w.t.Helper()
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if category, round := w.reviewOf(spec); round == 1 && category == "correctness" {
			return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: high", wrongThing) + "```\n"}
		}
		return scripted(spec)
	}
	if failOne {
		w.crashRoundOneSecurity()
	}
	hold := make(chan struct{})
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(hold)
		}
	}
	w.t.Cleanup(release)
	w.talk(task)
	w.implemented(task)
	// On fakelux's goroutine: w.app's pool, never w.owner, and no t.Fatal
	// (the hook may run after the test ends).
	w.lux.BeforeStart(func(luxID string) {
		var review bool
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM runs WHERE lux_run_id = $1 AND phase = 'review')`,
				luxID).Scan(&review)
		}); err != nil || !review {
			return
		}
		select {
		case <-hold:
		case <-time.After(30 * time.Second):
		}
	})
	h1 := w.str(`SELECT state->'heads'->>'target' FROM workflow_runs WHERE task_id = $1`, task)
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness","security"]}`)
	w.until("round 1's reviewers created", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, task) == 2 &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitReview'`, task) == 1
	})
	w.pushed(task, "The conductor's edit", "conductor\n")
	release()
	w.lux.BeforeStart(nil)
	if failOne {
		w.until("the failed reviewer escalated", func() bool { return w.escalationReason(task) == "review_failed" })
		w.retryReview(task)
	}
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.toRoundTwo(task, nil)
	return h1
}

// roundTwoFromH1 says round 2 is fix_round from H1, the heads round 1's
// lenses were created at, and no commit falls outside every round's range.
func (w *world) roundTwoFromH1(task, h1 string) {
	w.t.Helper()
	second := w.lastReview(task)
	if second.Round != 2 || second.Of != delivery.RangeFixRound || second.Base != h1 {
		w.t.Errorf("round %d %q from %q, want fix_round from round 1's heads %s\n%s", second.Round, second.Of, second.Base, h1, w.describeRuns())
	}
	w.allReviewed(task)
}

// The conductor publishes while round 1's lenses work at H1: round 2,
// after the fix, starts at H1, so the publish is in its range.
func TestAPublishDuringARoundIsInTheNextRoundsRange(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	h1 := w.publishedDuringRoundOne(task, false)
	w.roundTwoFromH1(task, h1)
}

// A lens of round 1 fails after the conductor's publish and a person
// retries it at H2: the round's heads stay H1, what the lenses that
// completed saw, and round 2 starts there.
func TestARetriedLensKeepsTheRoundsHeads(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	h1 := w.publishedDuringRoundOne(task, true)
	w.roundTwoFromH1(task, h1)
}

// resumedRoundTwo is a conducted delivery with resumeRounds on, as the
// default policy has it, on lux throughout: round 1 raises two findings,
// the conductor dismisses one and sends the other to a fix; round 2's
// reviewer, resumed (fix_round), judges it still there, and with
// toFix the conductor sends it to round 2's fix, whose fixer is resumed.
// Returns the dismissed finding.
func (w *world) resumedRoundTwo(task string, toFix bool) string {
	w.t.Helper()
	w.scriptRounds("still", evidencedFinding("severity: high", wrongThing), evidencedFinding("severity: medium", shorter))
	w.onModel("fixer", "llm-fix")
	mustExec(w.t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"resumeRounds":true}' WHERE id = $1`, w.project)
	w.talk(task)
	setPolicy(w, task, `{"resumeRounds":true}`)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	dismissed := w.dismissShorter(task)
	w.toRoundTwo(task, nil)
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 2
		AND review_range->>'of' = $2`, task, delivery.RangeFixRound); n == 0 || !resumedReview(w) {
		w.t.Fatalf("round 2's reviewer is not resumed over fix_round\n%s", w.describeRuns())
	}
	if toFix {
		w.must(task, "decide", `{"action":"next"}`)
		w.until("round 2's fixer resumed", func() bool {
			for _, r := range w.luxRunsOf("fix") {
				if r.Resumed > 0 && len(w.lux.Inputs(r.ID)) > 0 {
					return true
				}
			}
			return false
		})
	}
	return dismissed
}

// inputsOf is every input the phase's agents on lux were given.
func (w *world) inputsOf(phase string) []string {
	var inputs []string
	for _, r := range w.luxRunsOf(phase) {
		inputs = append(inputs, w.lux.Inputs(r.ID)...)
	}
	return inputs
}

// A conducted loop's round 2 reviewer, resumed (the default), is told in
// its round message its round and what earlier rounds decided, as a fresh
// one's prompt has them.
func TestAResumedRoundTwoReviewerIsToldItsRound(t *testing.T) {
	w := conducting(t)
	dismissed := w.resumedRoundTwo(w.task(), false)
	for _, in := range w.inputsOf("review") {
		if strings.Contains(in, "## Round 2") && strings.Contains(in, dismissed) && strings.Contains(in, "Its length is the house style") {
			return
		}
	}
	t.Errorf("no resumed reviewer's round message tells round 2 and %s dismissed:\n%q", dismissed, w.inputsOf("review"))
}

// A conducted loop's round 2 fixer, resumed, is told its round and how a
// round is fixed, as a fresh conducted fixer is.
func TestAResumedRoundTwoFixerIsToldItsRound(t *testing.T) {
	w := conducting(t)
	w.resumedRoundTwo(w.task(), true)
	for _, in := range w.inputsOf("fix") {
		if strings.Contains(in, "## Fix round 2") {
			return
		}
	}
	t.Errorf("no resumed fixer's round message tells fix round 2:\n%q", w.inputsOf("fix"))
}
