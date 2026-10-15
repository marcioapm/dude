package orchestrator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// simplifierFinding is what the review of the simplifier's diff raises,
// once, in the scripted reviewer's format.
const simplifierFinding = "One problem:\n\n```yaml\n---\nseverity: blocking\ncategory: correctness\nfile: SIMPLE.md\nline: 1\n" +
	"title: The simplification drops the greeting's fallback\n" +
	"description: \"Traced: SIMPLE.md:1 removes the default the greeting used.\"\nsuggested_fix: Put the fallback back.\n```\n"

// rangeSpecs are the lux specs of the task's reviews limited to a range
// of, in the order they were submitted.
func (w *world) rangeSpecs(of string) []lux.Spec {
	var out []lux.Spec
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.phase"] == "review" && spec.Labels["dude.review_range"] == of {
			out = append(out, spec)
		}
	}
	return out
}

// The simplifier changes code nobody reviewed: a review of its diff alone,
// by the policy's required reviewers, told the range from the head before
// it to HEAD. Its finding goes to the fixer; the fix is reviewed (over the
// same range), the simplifier does not run again, and the test then the
// pull request follow.
func TestTheSimplifiersDiffIsReviewedAndItsFindingFixedBeforeTheTest(t *testing.T) {
	w := newWorld(t)
	// A reviewer on a model the fake lux plays as the scripted one, whose
	// spec carries dude's own prompt rather than a script.
	w.onModel("reviewer", "llm-reviewer")
	scripted := w.lux.Decide
	var simplifyReviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if rangeReview(spec) == delivery.RangeSimplify {
			if simplifyReviews.Add(1) == 1 {
				return fakelux.Behaviour{Reply: simplifierFinding}
			}
			return fakelux.Behaviour{Reply: "Reviewed the fix of the simplification.\n\n" + fakeagent.Verdict}
		}
		return scripted(spec)
	}
	wi := w.testing("fake/scripted")
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	if n := w.phaseRuns(wi, "simplify"); n != 1 {
		t.Errorf("%d simplifications, want one: never again after its diff's fix", n)
	}
	specs := w.rangeSpecs(delivery.RangeSimplify)
	if len(specs) != 2 {
		t.Fatalf("%d reviews of the simplifier's diff, want its round and the fix's re-review\n%s", len(specs), w.describeRuns())
	}
	// From the head before the simplifier: the review loop's fix.
	var before, simplified string
	_ = w.owner.QueryRow(context.Background(), `SELECT heads->'target'->>'sha' FROM runs WHERE task_id = $1 AND phase = 'fix'
		ORDER BY created_at LIMIT 1`, wi).Scan(&before)
	_ = w.owner.QueryRow(context.Background(), `SELECT heads->'target'->>'sha' FROM runs WHERE task_id = $1 AND phase = 'simplify'`,
		wi).Scan(&simplified)
	for i, spec := range specs {
		for _, want := range []string{"Review `" + before + "..HEAD`:", "behaviour-preservation check of the simplifier's diff"} {
			if !strings.Contains(spec.Workload.Prompt, want) {
				t.Errorf("review %d of the simplifier's diff is not told %q:\n%s", i+1, want, spec.Workload.Prompt)
			}
		}
		if strings.Contains(spec.Workload.Prompt, "`git merge-base`") {
			t.Errorf("review %d of the simplifier's diff was sent to the whole branch", i+1)
		}
	}
	if specs[0].Labels["dude.review_range"] != delivery.RangeSimplify {
		t.Errorf("labels = %v", specs[0].Labels)
	}
	// The first saw the simplifier's commit; its finding went to the fixer,
	// and the re-review judged it fixed.
	var status, fixedBy string
	_ = w.owner.QueryRow(context.Background(), `SELECT f.status::text, COALESCE((SELECT r.id FROM runs r WHERE r.task_id = f.task_id
			AND r.phase = 'fix' AND f.id = ANY(r.finding_ids)), '')
		FROM review_findings f WHERE f.task_id = $1 AND f.title LIKE 'The simplification drops%'`, wi).Scan(&status, &fixedBy)
	if status != "resolved" || fixedBy == "" {
		t.Errorf("the simplifier's finding is %q, sent to fix %q; want resolved after a fix", status, fixedBy)
	}
	if n := w.phaseRuns(wi, "fix"); n != 2 {
		t.Errorf("%d fixes, want the review loop's and the simplifier's diff's", n)
	}
	// Then the tester, on what the pull request opened with.
	var tested string
	_ = w.owner.QueryRow(context.Background(), `SELECT base_refs->>'target' FROM runs WHERE task_id = $1 AND phase = 'test'`, wi).Scan(&tested)
	if head := w.branchHead(wi); tested != head || tested == simplified {
		t.Errorf("tested %s; the branch is at %s, the simplifier left %s: want the fix of its diff tested", tested, head, simplified)
	}
	if order := w.phaseOrder(wi); !slices.Equal(order, []string{"implement", "review", "fix", "review", "simplify", "review", "fix", "review", "test"}) {
		t.Errorf("phases = %v", order)
	}
}

// A simplifier that changes nothing adds no review: straight to the test.
func TestASimplifierThatChangesNothingAddsNoReview(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
			return fakelux.Behaviour{Reply: "Simple enough already."}
		}
		return scripted(spec)
	}
	wi := w.testing("fake/scripted")
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if order := w.phaseOrder(wi); !slices.Equal(order, []string{"implement", "review", "fix", "review", "simplify", "test"}) {
		t.Errorf("phases = %v", order)
	}
	if n := len(w.rangeSpecs(delivery.RangeSimplify)); n != 0 {
		t.Errorf("%d reviews of a simplification that changed nothing", n)
	}
}

// phaseOrder is the task's phase Runs, oldest first.
func (w *world) phaseOrder(wi string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT phase::text FROM runs WHERE task_id = $1 AND phase IS NOT NULL
		AND NOT (phase = 'review' AND category IS DISTINCT FROM 'correctness') ORDER BY created_at, id`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		out = append(out, p)
	}
	return out
}

// prFixWorld delivers a task with the tester on to an open pull request,
// and arms a watch: once armed, every phase Run lux is asked for after it
// records the pull request branch's head as it was then. tester plays the
// candidate's tests (after arm), "" for the scripted pass.
type prFixWorld struct {
	*world
	wi, branch, before string
	// web's pull request head before the comment, with two repositories.
	webBefore      string
	armed          atomic.Bool
	mu             chan struct{}
	seen           []string // the PR branch's head at each Run lux started after arm
	seenWeb        []string // web's, the same way
	phases         []string
	candidateTests atomic.Int32
	// The candidate's tests, in order: the scripted tester's model each
	// plays; past the end, a pass.
	testers []string
	// Called as the candidate's first test starts.
	onTest func()
	// Changes what an armed Run does, by phase and its index among the
	// armed Runs of that phase (from 1).
	play func(phase string, n int, b *fakelux.Behaviour)
}

func newPRFixWorld(t *testing.T, testers ...string) *prFixWorld {
	return newPRFixWorldIn(t, false, testers...)
}

// newPRFixWorldIn is newPRFixWorld, across target and web (both writable,
// a pull request in each) when web is set.
func newPRFixWorldIn(t *testing.T, web bool, testers ...string) *prFixWorld {
	w := &prFixWorld{world: newWorld(t), mu: make(chan struct{}, 1), testers: testers}
	scripted := w.lux.Decide
	count := map[string]int{}
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		phase, _ := labels["dude.phase"].(string)
		if !w.armed.Load() {
			return scripted(spec)
		}
		w.mu <- struct{}{}
		w.seen = append(w.seen, w.gh.SHA(w.branch))
		w.seenWeb = append(w.seenWeb, w.web.SHA(w.branch))
		w.phases = append(w.phases, phase)
		count[phase]++
		n := count[phase]
		<-w.mu
		b := scripted(spec)
		if phase == "test" {
			n := int(w.candidateTests.Add(1))
			if n == 1 && w.onTest != nil {
				w.onTest()
			}
			if n <= len(w.testers) {
				criteria, _ := strconv.Atoi(labels["dude.criteria"].(string))
				b.Reply = fakeagent.Tester(w.testers[n-1], criteria, 0).Reply
			}
		}
		if w.play != nil {
			w.play(phase, n, &b)
		}
		return b
	}
	// A reviewer the fake lux plays as the scripted one, whose spec carries
	// dude's own prompt.
	w.onModel("reviewer", "llm-reviewer")
	if web {
		w.wi = w.testingAcross("fake/scripted")
	} else {
		w.wi = w.testing("fake/scripted")
	}
	w.until("a pull request", func() bool {
		return len(w.gh.Pulls()) == 1 && (!web || len(w.web.Pulls()) == 1) && w.taskStatus(w.wi) == "review"
	})
	w.branch = w.gh.Pull(1).Head
	w.before = w.gh.SHA(w.branch)
	w.webBefore = w.web.SHA(w.branch)
	return w
}

// testingAcross is testing on a task that changes target and web.
func (w *world) testingAcross(model string) string {
	w.t.Helper()
	w.onModel("qa_browser", model)
	wi := w.task()
	mustExec(w.t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	w.addWeb(wi, "write")
	p := scriptedPolicy()
	p.Test = true
	w.start(wi, p)
	return wi
}

// comment arms the watch and has a collaborator ask for a change.
func (w *prFixWorld) comment(body string) {
	w.armed.Store(true)
	w.gh.Comment(1, "alice", body)
}

// shownBeforeTheGate is every head the pull request's branch had while a
// Run of the fix's check started, other than where it was before the fix.
func (w *prFixWorld) shownBeforeTheGate() []string {
	w.mu <- struct{}{}
	defer func() { <-w.mu }()
	var out []string
	for i, sha := range w.seen {
		if sha != w.before {
			out = append(out, w.phases[i]+"@"+sha)
		}
	}
	return out
}

func (w *prFixWorld) candidate() string { return w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) }

// state reads the delivery's state field as JSON text.
func (w *prFixWorld) state(field string) string {
	var v string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->>$2, '') FROM workflow_runs WHERE task_id = $1`, w.wi, field).Scan(&v)
	return v
}

// A comment on the pull request: the fix goes to the candidate branch, is
// reviewed over its own range, tested on every criterion, and only then
// is the pull request's branch fast-forwarded to it; its reviewers are
// asked again, and its description's Verification is about the commit it
// shows. The pull request's branch never held the fix before the gate.
func TestAPullRequestFixIsReviewedAndTestedBeforeThePullRequestShowsIt(t *testing.T) {
	w := newPRFixWorld(t)
	w.askingFor("jerv")
	w.gh.Review(1, "jerv", "COMMENTED")
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
	if shown := w.shownBeforeTheGate(); len(shown) > 0 {
		t.Errorf("the pull request showed the fix before its review and test: %v", shown)
	}
	if !slices.Equal(w.phases, []string{"fix", "review", "test"}) {
		t.Errorf("after the comment: %v, want the fix, its review and the test", w.phases)
	}
	head := w.gh.SHA(w.branch)
	// The fix's Run published to the candidate branch, which the pull
	// request's branch was moved to, by fast-forward from where it was.
	var fixBranch, fixHead string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(branch, ''), heads->'target'->>'sha' FROM runs
		WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb`, w.wi).Scan(&fixBranch, &fixHead)
	if fixBranch != delivery.CandidateBranch(w.wi, 1) || fixHead != head {
		t.Errorf("the fix published to %q at %s; the pull request is at %s", fixBranch, fixHead, head)
	}
	if log := w.gh.Log(w.branch); !strings.HasPrefix(log[0], "Address review findings") {
		t.Errorf("branch history = %v", log)
	}
	specs := w.rangeSpecs(delivery.RangePullRequestFix)
	if len(specs) != 1 || !strings.Contains(specs[0].Workload.Prompt, "Review `"+w.before+"..HEAD`:") ||
		!strings.Contains(specs[0].Workload.Prompt, "only the fix made for the open pull request's feedback") {
		t.Fatalf("%d reviews of the fix's range: %v", len(specs), specs)
	}
	var tested string
	_ = w.owner.QueryRow(context.Background(), `SELECT heads->>'target' FROM acceptance_verdicts WHERE task_id = $1
		ORDER BY created_at DESC LIMIT 1`, w.wi).Scan(&tested)
	if tested != head {
		t.Errorf("the latest verdict is at %s, the pull request at %s", tested, head)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'
		AND payload->>'from' = $2 AND payload->>'to' = $3`, w.wi, w.before, head); n != 1 {
		t.Errorf("%d moves recorded", n)
	}
	// Asked again, once the pull request shows the fix.
	if events := w.rerequests(w.wi); len(events) != 1 || events[0]["head"] != head ||
		!slices.Equal(strs(events[0]["reviewersRequested"]), []string{"jerv"}) {
		t.Errorf("re-requests %v", events)
	}
	body := w.gh.Pull(1).Body
	for _, want := range []string{"## Verification", "Tested at `" + head[:7] + "`.", "- **pass** — it greets", "Opened by the dude factory."} {
		if !strings.Contains(body, want) {
			t.Errorf("the pull request's description lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Stale") || strings.Contains(body, w.before[:7]) {
		t.Errorf("the description is about the commit before the fix:\n%s", body)
	}
	if w.candidate() != "" {
		t.Errorf("the candidate branch is left at %s", w.candidate())
	}
}

// The candidate's test fails: back to the fixer, still on the candidate,
// reviewed and tested again; it passes, and only then the pull request
// moves. The round counts once against both pull request budgets, whatever
// it went through: a per-round budget of one leaves a second comment
// past it.
func TestAPullRequestFixThatFailsItsTestIsFixedOnTheCandidateAndCountsOnce(t *testing.T) {
	w := newPRFixWorld(t, fakeagent.TestFailModel)
	setPolicy(w.world, w.wi, `{"maxPrFixIterations":1}`)
	var atFailure string
	w.onTest = func() { atFailure = w.gh.SHA(w.branch) }
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
	if shown := w.shownBeforeTheGate(); len(shown) > 0 || atFailure != w.before {
		t.Errorf("a candidate that failed its test reached the pull request: %v (at the test: %s)", shown, atFailure)
	}
	if !slices.Equal(w.phases, []string{"fix", "review", "test", "fix", "review", "test"}) {
		t.Errorf("after the comment: %v", w.phases)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND branch = $2`,
		w.wi, delivery.CandidateBranch(w.wi, 1)); n != 2 {
		t.Errorf("%d fixes on the candidate, want the pull request's and the test's", n)
	}
	if got := w.verdicts(w.wi); strings.Join(got, ",") != "pass,pass" {
		t.Errorf("the verdict at the pull request's head: %v", got)
	}
	if p, f := w.state("prIteration"), w.state("prFixesByRepo"); p != "1" || f != `{"target": 1}` {
		t.Errorf("the round counted prIteration=%s prFixes=%s, want once", p, f)
	}
	// A second comment in the same review round is past the one fix.
	w.gh.Comment(1, "alice", "And the farewell.")
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != "pr_loop_exhausted" {
		t.Errorf("escalated %q", r)
	}
}

// A person pushes to the pull request while its fix is checked: the
// candidate is not moved over their commit, and a person is asked.
func TestAPushWhileAFixIsCheckedIsNeverOverwritten(t *testing.T) {
	w := newPRFixWorld(t)
	var theirs string
	w.onTest = func() { theirs = w.gh.CommitOnTop(w.branch, "A person's own fix") }
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q, want %s\n%s", r, delivery.ReasonPullRequestMoved, w.describeRuns())
	}
	if at := w.gh.SHA(w.branch); at != theirs || theirs == "" {
		t.Errorf("the pull request is at %s, the person's commit %s", at, theirs)
	}
	if c := w.candidate(); c == "" || c == theirs {
		t.Errorf("the candidate is at %q", c)
	}
	var detail map[string]any
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'detail' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&detail)
	if detail["found"] != theirs || detail["expected"] != w.before || detail["repo"] != "target" {
		t.Errorf("detail = %v", detail)
	}
	// Retry fixes the same feedback again, from their commit.
	owner := w.person("owner")
	w.assignOwner(w.wi, owner)
	if code, body := w.decide(owner, w.wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != theirs && w.taskStatus(w.wi) == "review"
	})
	if log := w.gh.Log(w.branch); !slices.Contains(log, "A person's own fix") {
		t.Errorf("the person's commit is gone: %v", log)
	}
	if p := w.state("prIteration"); p != "1" {
		t.Errorf("prIteration = %s after a retry of the round, want 1", p)
	}
}

// The candidate's test cannot run the application: a person waives it,
// with a reason, and the pull request moves, its Verification saying so.
func TestABlockedTestOfAPullRequestFixIsWaivedAndMoved(t *testing.T) {
	w := newPRFixWorld(t, fakeagent.TestBlockedModel)
	owner, _, _ := w.people(w.wi)
	w.comment("Please rename the greeting.")
	w.until("the gate to stop", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonTestBlocked {
		t.Fatalf("escalated %q", r)
	}
	if w.gh.SHA(w.branch) != w.before {
		t.Fatal("a blocked test's fix reached the pull request")
	}
	if code, body := w.decide(owner, w.wi, delivery.ActionWaive, "Checked by hand on staging."); code != 200 {
		t.Fatalf("waive: %d %v", code, body)
	}
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
	head := w.gh.SHA(w.branch)
	body := w.gh.Pull(1).Body
	for _, want := range []string{"Tested at `" + head[:7] + "`.", "- **blocked, waived by Ana: Checked by hand on staging.** — it greets"} {
		if !strings.Contains(body, want) {
			t.Errorf("the description lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Stale") {
		t.Errorf("the waived verdict is about the commit shown:\n%s", body)
	}
}

// rangesNaming are the prompts of the task's range reviews of of that
// tell the reviewer repo's range from base.
func (w *world) rangesNaming(of, repo, base string) int {
	n := 0
	for _, spec := range w.rangeSpecs(of) {
		if strings.Contains(spec.Workload.Prompt, "in `"+repo+"`, `"+base+"..HEAD`") {
			n++
		}
	}
	return n
}

// The fix of the simplifier's diff changes a repository the simplifier did
// not: that repository joins the range its re-review is told, from where
// it stood before the fix.
func TestARepositoryAFixOfTheSimplifiersDiffChangesFirstIsReviewed(t *testing.T) {
	w := newWorld(t)
	w.onModel("reviewer", "llm-reviewer")
	scripted := w.lux.Decide
	var simplifyReviews, fixes atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if rangeReview(spec) == delivery.RangeSimplify {
			if simplifyReviews.Add(1) == 1 {
				return fakelux.Behaviour{Reply: simplifierFinding}
			}
			return fakelux.Behaviour{Reply: "Reviewed the fix of the simplification.\n\n" + fakeagent.Verdict}
		}
		b := scripted(spec)
		if labels["dude.phase"] == "fix" && fixes.Add(1) == 2 {
			b.Commit = map[string]string{"web:UNREVIEWED.md": "the fix of the simplification\n"}
		}
		return b
	}
	wi := w.testingAcross("fake/scripted")
	w.until("a pull request in each repository", func() bool { return len(w.gh.Pulls()) == 1 && len(w.web.Pulls()) == 1 })

	var webBefore, webFixed string
	_ = w.owner.QueryRow(context.Background(), `SELECT base_refs->>'web', heads->'web'->>'sha' FROM runs
		WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at DESC LIMIT 1`, wi).Scan(&webBefore, &webFixed)
	if webBefore == "" || webFixed == "" || webFixed == webBefore {
		t.Fatalf("the simplifier's fix did not change web: from %q to %q\n%s", webBefore, webFixed, w.describeRuns())
	}
	specs := w.rangeSpecs(delivery.RangeSimplify)
	if len(specs) != 2 {
		t.Fatalf("%d reviews of the simplifier's diff", len(specs))
	}
	if !strings.Contains(specs[1].Workload.Prompt, "in `web`, `"+webBefore+"..HEAD`") {
		t.Errorf("the re-review is not told web's range from %s:\n%s", webBefore, specs[1].Workload.Prompt)
	}
	if !strings.Contains(specs[0].Workload.Prompt, "Review in `target`, `") {
		t.Errorf("a range among two checkouts names no repository:\n%s", specs[0].Workload.Prompt)
	}
	// web's pull request opened at the fix the re-review checked out.
	if at := w.web.SHA(delivery.BranchFor(wi, 1)); at != webFixed {
		t.Errorf("web's pull request is at %s, the reviewed fix %s", at, webFixed)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND review_range->>'of' = $2 AND review_range->'base'->>'web' = $3 AND base_refs->>'web' = $4`,
		wi, delivery.RangeSimplify, webBefore, webFixed); n != 1 {
		t.Errorf("%d reviews of web's fix told its range", n)
	}
}

// The candidate's test fails and its fix changes a repository the pull
// request's fix did not: that repository's pull request moves only after
// a review told its range; each pull request's stored description is its
// own, though both have the same number.
func TestARepositoryATestFixOfACandidateChangesFirstIsReviewedBeforeItsPullRequestMoves(t *testing.T) {
	w := newPRFixWorldIn(t, true, fakeagent.TestFailModel)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 2 {
			b.Commit = map[string]string{"web:WEB.md": "the test's fix\n"}
		}
	}
	w.comment("Please rename the greeting.")
	w.until("both fixes on the pull requests", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.web.SHA(w.branch) != w.webBefore && w.taskStatus(w.wi) == "review"
	})
	if !slices.Equal(w.phases, []string{"fix", "review", "test", "fix", "review", "test"}) {
		t.Errorf("after the comment: %v", w.phases)
	}
	for i, sha := range w.seenWeb {
		if sha != w.webBefore {
			t.Errorf("web's pull request was at %s as %s started", sha, w.phases[i])
		}
	}
	if n := w.rangesNaming(delivery.RangePullRequestFix, "web", w.webBefore); n != 1 {
		t.Errorf("%d reviews told web's range from %s, want the test fix's re-review", n, w.webBefore)
	}
	if n := w.rangesNaming(delivery.RangePullRequestFix, "target", w.before); n != 2 {
		t.Errorf("%d reviews told target's range from %s, want both", n, w.before)
	}
	for _, s := range []*fakegithub.Server{w.gh, w.web} {
		var stored string
		_ = w.owner.QueryRow(context.Background(), `SELECT pr.body FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.task_id = $1 AND r.name = $2`, w.wi, strings.TrimPrefix(s.Slug, "acme/")).Scan(&stored)
		if body := s.Pull(1).Body; stored != body {
			t.Errorf("%s#1's stored description is not its own:\n%s\nGitHub has:\n%s", s.Slug, stored, body)
		}
	}
}

// A person pushes to one pull request while a fix of two is checked: no
// branch is moved, the other's included, and a person is asked.
func TestAPushToOnePullRequestMovesNeitherOfTwo(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "addressed for both"}
		}
	}
	var theirs string
	w.onTest = func() { theirs = w.web.CommitOnTop(w.branch, "A person's own fix") }
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	if at := w.gh.SHA(w.branch); at != w.before {
		t.Errorf("target's pull request moved to %s while web's refused the fix", at)
	}
	if at := w.web.SHA(w.branch); at != theirs {
		t.Errorf("web is at %s, the person's commit %s", at, theirs)
	}
}

// Someone pushes between promotion's read of a branch and its move: the
// move is refused, the person's commit kept, and a pull request already
// moved is recorded at its new head, its description about it.
func TestAPushRacingThePromotionIsNeverOverwritten(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "addressed for both"}
		}
	}
	var raced atomic.Bool
	var theirs atomic.Value
	w.web.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) && raced.CompareAndSwap(false, true) {
				theirs.Store(w.web.CommitOnTop(w.branch, "A person's own fix"))
			}
			return 0
		}
	})
	w.comment("Please rename the greeting.")
	w.sync()
	w.until("a person asked", func() bool { return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	person, _ := theirs.Load().(string)
	if at := w.web.SHA(w.branch); person == "" || at != person {
		t.Errorf("web is at %s, the person's commit %q", at, person)
	}
	var detail map[string]any
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'detail' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&detail)
	if detail["repo"] != "web" || detail["found"] != person || detail["expected"] != w.webBefore {
		t.Errorf("detail = %v", detail)
	}
	// target moved before the race: recorded, and described, at its head.
	moved := w.gh.SHA(w.branch)
	if moved == w.before {
		t.Fatal("target did not move before web's race")
	}
	var stored string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(pr.head_sha, '') FROM pull_requests pr
		JOIN repositories r ON r.id = pr.repository_id WHERE pr.task_id = $1 AND r.name = 'target'`, w.wi).Scan(&stored)
	if stored != moved {
		t.Errorf("target's pull request is recorded at %s; GitHub has %s", stored, moved)
	}
	var heads map[string]string
	if err := json.Unmarshal([]byte(w.state("heads")), &heads); err != nil {
		t.Fatal(err)
	}
	if heads["target"] != moved {
		t.Errorf("delivery retained target at %s; GitHub has %s", heads["target"], moved)
	}
	if body := w.gh.Pull(1).Body; !strings.Contains(body, "target `"+moved[:7]+"`") {
		t.Errorf("target's Verification is not about %s:\n%s", moved[:7], body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'
		AND payload->>'repo' = 'target' AND payload->>'to' = $2`, w.wi, moved); n != 1 {
		t.Errorf("%d moves of target recorded", n)
	}
}

// Every reviewer of a candidate's round fails and a person accepts the
// round: the test runs, and the pull request takes the fix with no review
// round past the person's decision.
func TestAnAcceptedFailedReviewOfACandidateIsHonouredAtPromotion(t *testing.T) {
	w := newPRFixWorld(t)
	owner, _, _ := w.people(w.wi)
	var crashing atomic.Bool
	crashing.Store(true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && crashing.Load() {
			*b = fakelux.Behaviour{Crash: true}
		}
	}
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != "review_failed" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	crashing.Store(false)
	w.mu <- struct{}{}
	decided := len(w.phases)
	<-w.mu
	if code, body := w.decide(owner, w.wi, "accept", "Reviewed it myself."); code != 200 {
		t.Fatalf("accept: %d %v", code, body)
	}
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
	if after := w.phases[decided:]; !slices.Equal(after, []string{"test"}) {
		t.Errorf("after the person accepted: %v, want the test alone", after)
	}
}

// candidateFinding is what a review of a pull request's fix raises, in the
// scripted reviewer's format.
const candidateFinding = "One problem:\n\n```yaml\n---\nseverity: blocking\ncategory: correctness\nfile: FIXED.md\nline: 1\n" +
	"title: The rename misses the farewell\n" +
	"description: \"Traced: FIXED.md:1 renames the greeting only.\"\nsuggested_fix: Rename the farewell too.\n```\n"

// A finding of the candidate's first review is still open after a fix that
// also changed a second repository: the range grew, and the next fix is
// still sent that finding.
func TestAFindingOfACandidateIsStillFixedAfterItsRangeGrows(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		switch {
		case phase == "review" && n == 1:
			b.Reply = candidateFinding
		case phase == "review" && n == 2:
			b.Reply = "Not yet.\n\n" + fakeagent.StillVerdict
		case phase == "review":
			b.Reply = "Reviewed the fix.\n\n" + fakeagent.Verdict
		case phase == "fix" && n == 2:
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "the farewell too"}
		}
	}
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull requests", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
	if !slices.Equal(w.phases, []string{"fix", "review", "fix", "review", "fix", "review", "test"}) {
		t.Errorf("after the comment: %v", w.phases)
	}
	if n := w.count(`SELECT count(*) FROM runs r JOIN review_findings f ON f.id = ANY(r.finding_ids)
		WHERE r.task_id = $1 AND r.phase = 'fix' AND f.title = 'The rename misses the farewell'`, w.wi); n != 2 {
		t.Errorf("the finding was sent to %d fixes, want both after it was raised", n)
	}
}

// One of two pull requests is merged while their fix is checked: the
// merged pull request stays merged, and the checked work gets a follow-up
// pull request rather than being discarded or merged without a person.
func TestAPullRequestMergedWhileItsFixIsCheckedIsNotMoved(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "addressed for both"}
		}
	}
	var mainAfterMerge string
	var pullsAtTest int
	w.onTest = func() {
		w.web.Merge(1)
		w.sync()
		mainAfterMerge = w.web.SHA("main")
		pullsAtTest = len(w.web.Pulls())
	}
	w.comment("Please rename the greeting.")
	w.until("the checked fix on target and a follow-up pull request in web", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && len(w.web.Pulls()) == 2 && w.taskStatus(w.wi) == "review"
	})
	if p := w.web.Pull(1); p.State != "closed" || p.MergedAt == nil {
		t.Errorf("the original pull request is no longer merged: %+v", p)
	}
	followup := w.web.Pull(2)
	head := w.web.SHA(followup.Head)
	if followup.State != "open" || followup.MergedAt != nil || head == "" || head == w.webBefore || followup.Base != "main" {
		t.Fatalf("the checked fix has no open follow-up: %+v at %s", followup, head)
	}
	if at := w.web.SHA("main"); mainAfterMerge == "" || at != mainAfterMerge || at == head {
		t.Errorf("main moved from %s to %s without a person merging the follow-up at %s", mainAfterMerge, at, head)
	}
	if shown := w.shownBeforeTheGate(); len(shown) > 0 || pullsAtTest != 1 {
		t.Errorf("unchecked work exposed: target heads %v, web pull requests at test %d", shown, pullsAtTest)
	}
	for i, sha := range w.seenWeb {
		if sha != w.webBefore {
			t.Errorf("web's branch was at %s as %s started", sha, w.phases[i])
		}
	}
	if !slices.Equal(w.phases, []string{"fix", "review", "test"}) {
		t.Errorf("after the comment: %v, want the fix, its review and the test", w.phases)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND review_range->>'of' = $2 AND base_refs->>'web' = $3`, w.wi, delivery.RangePullRequestFix, head); n != 1 {
		t.Errorf("%d completed reviews of the follow-up head", n)
	}
	if n := w.count(`SELECT count(*) FROM acceptance_verdicts WHERE task_id = $1 AND heads->>'web' = $2
		AND status = 'pass'`, w.wi, head); n != 2 {
		t.Errorf("%d passing criteria at the follow-up head, want both", n)
	}
	if r := w.escalationReason(w.wi); r == delivery.ReasonPullRequestMoved {
		t.Errorf("escalated %s for a merged pull request", r)
	}
}

// A person's push after the promotion's GET but before its PATCH returns
// 422: the escalation names their SHA, not the candidate's or the old head.
func TestAPushBeforeTheRefPatchEscalatesWithThePersonsHead(t *testing.T) {
	w := newPRFixWorld(t)
	var raced atomic.Bool
	var theirs atomic.Value
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) && raced.CompareAndSwap(false, true) {
				theirs.Store(w.gh.CommitOnTop(w.branch, "A person's racing push"))
			}
			return 0
		}
	})
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	person, _ := theirs.Load().(string)
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	var detail map[string]any
	if err := w.owner.QueryRow(context.Background(), `SELECT payload->'detail' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if person == "" || w.gh.SHA(w.branch) != person || detail["found"] != person || detail["expected"] != w.before {
		t.Errorf("person's head %q, branch %s, escalation %v", person, w.gh.SHA(w.branch), detail)
	}
}

func TestADeletionBeforeTheRefPatchKeepsTheCheckedCandidate(t *testing.T) {
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusNotFound} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			w := newPRFixWorld(t)
			var raced atomic.Bool
			var creates atomic.Int32
			var checked string
			w.onTest = func() {
				checked = w.candidate()
				w.gh.Merge(1)
				w.sync()
			}
			w.gh.Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if raced.Load() && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/refs") {
						creates.Add(1)
					}
					if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) && raced.CompareAndSwap(false, true) {
						if err := w.gh.DeleteBranch(w.branch); err != nil {
							t.Errorf("delete source branch: %v", err)
						}
						if status == http.StatusNotFound {
							return status
						}
					}
					return 0
				}
			})
			w.comment("Please rename the greeting.")
			w.until("promotion settled", func() bool {
				w.sync()
				return raced.Load() && (w.taskStatus(w.wi) == "awaiting_input" || len(w.gh.Pulls()) > 1)
			})
			if at := w.gh.SHA(w.branch); at != "" {
				t.Errorf("deleted source branch recreated at %s", at)
			}
			if n := creates.Load(); n != 0 {
				t.Errorf("%d create-ref fallbacks during promotion", n)
			}
			if at := w.candidate(); checked == "" || at != checked {
				t.Errorf("checked candidate %q was not retained: %q", checked, at)
			}
			if pulls := w.gh.Pulls(); len(pulls) != 1 {
				t.Errorf("created a follow-up after source deletion: %v", pulls)
			}
			if p := w.gh.Pull(1); p.State != "closed" || p.MergedAt == nil {
				t.Errorf("original pull request no longer merged: %+v", p)
			}
			if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
				t.Fatalf("escalated %q\n%s", r, w.describeRuns())
			}
			var detail map[string]any
			if err := w.owner.QueryRow(context.Background(), `SELECT payload->'detail' FROM events WHERE task_id = $1
				AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&detail); err != nil {
				t.Fatal(err)
			}
			if detail["found"] != "" || detail["expected"] != w.before || detail["candidate"] != checked ||
				detail["branch"] != delivery.CandidateBranch(w.wi, 1) || detail["repo"] != "target" {
				t.Errorf("missing-ref refusal = %v", detail)
			}
		})
	}
}

// GitHub rate-limits the promotion's move: the step waits and moves the
// branch after, without counting the refusal against its attempts.
func TestARateLimitedPromotionWaitsAndMoves(t *testing.T) {
	w := newPRFixWorld(t)
	var limited atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) && limited.Add(1) == 1 {
				return http.StatusTooManyRequests
			}
			return 0
		}
	})
	w.comment("Please rename the greeting.")
	var attempt int
	var waiting bool
	settled := func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT attempt, wait_until > now() FROM workflow_runs WHERE task_id = $1`,
			w.wi).Scan(&attempt, &waiting)
		return attempt > 0 || waiting
	}
	w.until("the rate limit", func() bool { w.sync(); return limited.Load() >= 1 && settled() })
	if attempt != 0 || !waiting {
		t.Errorf("after a rate limit: attempt %d, waiting %v; want a wait, not a failure", attempt, waiting)
	}
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
}
