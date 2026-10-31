package orchestrator_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// newPRFixWorldDirect is newPRFixWorld with the delivery frozen to push
// its pull request fixes directly (pullRequestFixes direct).
func newPRFixWorldDirect(t *testing.T, testers ...string) *prFixWorld {
	return newPRFixWorldAt(t, prAt{test: true, direct: true}, testers...)
}

// fixRun is the round's fix Run of the pull request feedback: its id,
// the branch it published to ("" for the task branch) and its head.
func (w *prFixWorld) fixRun() (id, branch, head string) {
	_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(branch, ''), COALESCE(heads->'target'->>'sha', '') FROM runs
		WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb ORDER BY created_at LIMIT 1`, w.wi).Scan(&id, &branch, &head)
	return id, branch, head
}

// waiting says the delivery is parked on its pull requests again.
func (w *prFixWorld) waiting() bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest'`, w.wi) == 1
}

// A comment on the pull request: the fix publishes to the pull request's
// branch, which CI and its reviewers see at once, and is then reviewed over
// its own push alone; no simplifier, no tester. Its reviewers are asked
// again once, and nothing goes to a candidate branch.
func TestDirectAPullRequestFixLandsOnThePullRequestThenIsReviewedOverItsRange(t *testing.T) {
	w := newPRFixWorldDirect(t)
	// A candidate branch left from before (I-5): a direct delivery neither
	// writes nor deletes one.
	leftover := w.gh.Branch(delivery.CandidateBranch(w.wi, 1), w.branch, "A candidate from before")
	if leftover == "" {
		t.Fatal("making the leftover candidate branch failed")
	}
	w.askingFor("jerv")
	w.gh.Review(1, "jerv", "COMMENTED")
	w.comment("Please rename the greeting.")
	w.until("the fix reviewed and the delivery waiting", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review" && w.waiting()
	})
	if !slices.Equal(w.phases, []string{"fix", "review"}) {
		t.Errorf("after the comment: %v, want the fix and its review", w.phases)
	}
	_, branch, fixHead := w.fixRun()
	if branch != w.branch || fixHead == "" {
		t.Errorf("the fix published to %q at %q, want the pull request's branch %s", branch, fixHead, w.branch)
	}
	if head := w.gh.SHA(w.branch); head != fixHead {
		t.Errorf("the pull request is at %s, the fix at %s", head, fixHead)
	}
	// Its review started with the fix on the pull request already.
	if shown := w.shownBeforeTheGate(); !slices.Equal(shown, []string{"review@" + fixHead}) {
		t.Errorf("the pull request's heads as the fix's checks started: %v", shown)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND branch IS NULL
		AND review_range->>'of' = $2 AND review_range->'base' = jsonb_build_object('target', $3::text) AND base_refs->>'target' = $4`,
		w.wi, delivery.RangePullRequestFix, w.before, fixHead); n != 1 {
		t.Errorf("%d reviews of the fix's push %s..%s\n%s", n, w.before, fixHead, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IN ('simplify', 'test')
		AND created_at > (SELECT min(created_at) FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb)`, w.wi); n != 0 {
		t.Errorf("%d simplifier or tester Runs after the pull request's fix", n)
	}
	if w.candidate() != leftover || len(w.gh.BranchLog(delivery.CandidateBranch(w.wi, 1))) != 0 {
		t.Errorf("the candidate branch was written or deleted: at %q, was %s", w.candidate(), leftover)
	}
	if events := w.rerequests(w.wi); len(events) != 1 || events[0]["head"] != fixHead {
		t.Errorf("re-requests %v, want one at %s", events, fixHead)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 0 {
		t.Errorf("%d promotions recorded", n)
	}
	if got := w.state("fixRange"); got != "" {
		t.Errorf("the fix's range is still in state: %s", got)
	}
}

// The narrow review raises a blocking finding: its fix pushes to the pull
// request too and is reviewed again; the round's reviewers are asked again
// once, after the loop, and the round spends one of the pull request's
// fix rounds however many pushes its loop made.
func TestDirectANarrowReviewFindingIsFixedOnThePullRequestAndReviewedAgain(t *testing.T) {
	w := newPRFixWorldDirect(t)
	w.askingFor("jerv")
	w.gh.Review(1, "jerv", "COMMENTED")
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			b.Reply = "One problem:\n\n" + reviewFinding("The fix leaves the greeting untranslated")
		}
	}
	w.comment("Please rename the greeting.")
	w.until("the loop's second fix reviewed", func() bool {
		w.sync()
		return w.taskStatus(w.wi) == "review" && w.waiting() && len(w.phases) >= 4
	})
	if !slices.Equal(w.phases, []string{"fix", "review", "fix", "review"}) {
		t.Fatalf("after the comment: %v\n%s", w.phases, w.describeRuns())
	}
	// Each fix was on the pull request before the review after it.
	if w.seen[1] == w.before || w.seen[3] == w.seen[1] || w.seen[3] != w.gh.SHA(w.branch) {
		t.Errorf("the pull request's heads as each Run started: %v (before %s, now %s)", w.seen, w.before, w.gh.SHA(w.branch))
	}
	if n := len(w.gh.BranchLog(w.branch)); n < 2 {
		t.Errorf("the pull request's branch moved %d times", n)
	}
	// The second review is of the round's whole push, from where it began.
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
		AND review_range->'base' = jsonb_build_object('target', $3::text)`, w.wi, delivery.RangePullRequestFix, w.before); n != 2 {
		t.Errorf("%d reviews over the round's range from %s\n%s", n, w.before, w.describeRuns())
	}
	if events := w.rerequests(w.wi); len(events) != 1 || events[0]["head"] != w.gh.SHA(w.branch) {
		t.Errorf("re-requests %v, want one after the loop", events)
	}
	if got := w.state("prFixesByRepo"); got != `{"target": 1}` {
		t.Errorf("fix rounds charged %s, want one", got)
	}
	// The loop's fix was sent the narrow review's finding, keyed in the round.
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND cardinality(finding_ids) = 1
		AND creation_key LIKE '%:prfix:1%'`, w.wi); n != 1 {
		t.Errorf("%d loop fixes sent the finding, keyed in the round\n%s", n, w.describeRuns())
	}
}

// A round whose narrow loop pushed twice spends one round of the pull
// request's budget, as one with no loop does: with MaxPRFixIterations 1
// the next comment escalates rather than fixing.
func TestDirectAFixRoundSpendsOneRoundOfTheBudgetWhateverItsLoop(t *testing.T) {
	w := newPRFixWorldDirect(t)
	setPolicy(w.world, w.wi, `{"maxPrFixIterations":1,"maxPrFixReviewIterations":3,"maxAttemptsPerFinding":5}`)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		switch {
		case phase == "review" && n == 1:
			b.Reply = "One problem:\n\n" + reviewFinding("Not translated")
		case phase == "review" && n == 2:
			b.Reply = fakeagent.Verdict + "\nOne more problem:\n\n" + reviewFinding("Not translated either")
		}
	}
	w.comment("Please rename the greeting.")
	w.until("the round's loop done", func() bool {
		w.sync()
		return w.waiting() && len(w.phases) >= 6 || w.taskStatus(w.wi) == "awaiting_input"
	})
	if !slices.Equal(w.phases, []string{"fix", "review", "fix", "review", "fix", "review"}) || !w.waiting() {
		t.Fatalf("after the comment: %v (escalated %q)\n%s", w.phases, w.escalationReason(w.wi), w.describeRuns())
	}
	if got := w.state("prFixesByRepo"); got != `{"target": 1}` {
		t.Errorf("fix rounds charged %s, want one", got)
	}
	if got := w.state("prIteration"); got != "1" {
		t.Errorf("rounds this review round %s, want 1", got)
	}
	w.gh.Comment(1, "alice", "And the farewell.")
	w.personAsked()
	if n := w.fixes(w.wi); n != 1 {
		t.Errorf("%d pull request fixes, want the one the budget allowed", n)
	}
}

// The narrow loop after a direct fix is bounded by maxPrFixReviewIterations
// (2): its second review still blocking escalates, as the review loop does
// before the pull request at its own bound.
func TestDirectTheNarrowLoopEscalatesAtItsBound(t *testing.T) {
	w := newPRFixWorldDirect(t)
	setPolicy(w.world, w.wi, `{"maxAttemptsPerFinding":5}`)
	titles := []string{"First", "Second", "Third"}
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n <= len(titles) {
			b.Reply = "One problem:\n\n" + reviewFinding(titles[n-1])
		}
	}
	w.comment("Please rename the greeting.")
	w.personAsked()
	if !slices.Equal(w.phases, []string{"fix", "review", "fix", "review"}) {
		t.Errorf("after the comment: %v, want two reviews then a person", w.phases)
	}
	if reason := w.escalationReason(w.wi); reason != "exhausted" {
		t.Errorf("escalated %q, want exhausted", reason)
	}
}

// A person's feedback item the fix resolves is verified as the fix
// publishes: it is on the pull request already, so a merge is not held
// for it during the narrow review.
func TestDirectResolutionsAreVerifiedAtPublish(t *testing.T) {
	w := newPRFixWorldDirect(t)
	ana := w.person("Ana")
	w.assignOwner(w.wi, ana)
	ar := w.resolveAsItPublishes(w.wi, "fbi_x", itsNewHead, nil)
	during := make(chan bool, 1)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			_, verified := w.resolution("fbi_x")
			during <- verified
		}
	}
	w.armed.Store(true)
	if code, body := w.feedback(ana, w.wi, "fbi_x", "Log the retry count at info, not debug."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	w.until("the fix reviewed and the delivery waiting", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.waiting()
	})
	if _, commit, answer := ar.state(); !strings.HasPrefix(answer, "200 ") {
		t.Fatalf("feedback_resolve with %s: %s", commit, answer)
	}
	if verified := <-during; !verified {
		t.Errorf("as the narrow review started the item was not verified")
	}
	if status, verified := w.resolution("fbi_x"); status != "resolved" || !verified {
		t.Errorf("the item is %s verified=%v", status, verified)
	}
}

// Checks failing on the fix's first push, signalled after the loop's second
// pushed, start no fix: the pull request shows another head.
func TestDirectChecksFailingOnAnOlderHeadDoNotStartAFix(t *testing.T) {
	w := newPRFixWorldDirect(t)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			b.Reply = "One problem:\n\n" + reviewFinding("The fix leaves the greeting untranslated")
		}
	}
	w.comment("Please rename the greeting.")
	w.until("the loop's second fix reviewed", func() bool {
		w.sync()
		return w.waiting() && len(w.phases) >= 4
	})
	first := w.seen[1]
	w.checksFailedAt(w.wi, first, "first-push")
	for range 3 {
		w.pump()
	}
	if n := w.fixes(w.wi); n != 1 {
		t.Errorf("%d pull request fixes, want only the comment's", n)
	}
	if !w.waiting() {
		t.Errorf("not waiting on the pull request\n%s", w.describeRuns())
	}
}

// A delivery already running when direct fixes shipped froze its policy
// without the key: its next fix goes to a candidate and is promoted, as
// before, whatever every new delivery does.
func TestAnInFlightDeliveryWithoutTheKeyStaysCandidate(t *testing.T) {
	w := newPRFixWorld(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state #- '{policy,pullRequestFixes}' WHERE task_id = $1`, w.wi)
	if strings.Contains(w.state("policy"), "pullRequestFixes") {
		t.Fatalf("the delivery froze the key: %s", w.state("policy"))
	}
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
	if shown := w.shownBeforeTheGate(); len(shown) > 0 {
		t.Errorf("the pull request showed the fix before its review and test: %v", shown)
	}
	if _, branch, _ := w.fixRun(); branch != delivery.CandidateBranch(w.wi, 1) {
		t.Errorf("the fix published to %q, want its candidate", branch)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 1 {
		t.Errorf("%d promotions", n)
	}
}

// A person pushes to the pull request while dude's fix works: the fix's
// publish is no fast-forward, so it fails rather than overwriting their
// commit, and a person is asked. The fix's commits stay on its per-Run
// branch, kept with the Run, and the escalation names that branch.
func TestDirectAPersonsPushDuringTheFixFailsThePublishAndKeepsItsCommits(t *testing.T) {
	w := newPRFixWorldDirect(t)
	var theirs string
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			theirs = w.gh.CommitOnTop(w.branch, "A person's own fix")
		}
	}
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool {
		w.sync()
		return w.taskStatus(w.wi) == "awaiting_input"
	})
	if r := w.escalationReason(w.wi); r != "pr_fix_failed" {
		t.Fatalf("escalated %q, want pr_fix_failed\n%s", r, w.describeRuns())
	}
	if at := w.gh.SHA(w.branch); at != theirs || theirs == "" {
		t.Errorf("the pull request is at %s, the person's commit %s", at, theirs)
	}
	if log := w.gh.Log(w.branch); !slices.Contains(log, "A person's own fix") {
		t.Errorf("the person's commit is gone: %v", log)
	}
	var errText string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(error, '') FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND pr_feedback <> '[]'::jsonb`, w.wi).Scan(&errText)
	if !strings.Contains(errText, "move "+w.branch) {
		t.Errorf("the fix failed with %q, want its move refused", errText)
	}
	if w.candidate() != "" {
		t.Errorf("a candidate branch was written")
	}
	// The fixer's commit is still on its per-Run branch once the Run ended.
	var fixID, pushed string
	_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(push_result->'results'->0->>'commit', '') FROM runs
		WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb`, w.wi).Scan(&fixID, &pushed)
	runBranch := "dude/" + w.wi + "/run-" + fixID
	w.until("the fix's lux Run settled", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason IS NOT NULL`, fixID) == 1
	})
	for range 3 {
		w.pump()
	}
	if at := w.gh.SHA(runBranch); pushed == "" || at != pushed {
		t.Errorf("the fix's per-Run branch %s is at %q, want its commit %q", runBranch, at, pushed)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND keep AND kept_until > now()`, fixID); n != 1 {
		t.Errorf("the fix's Run is not kept")
	}
	if raw, _ := w.escalationOf(w.wi); !strings.Contains(string(raw), runBranch) {
		t.Errorf("the escalation does not name %s: %s", runBranch, raw)
	}
}

// A taken-over pull request's first review loop, in a direct delivery,
// publishes in place: its simplifier's commit is on the person's branch
// before the review of its diff, and no candidate branch is ever made.
func TestDirectAdoptedPullRequestsFirstLoopPublishesInPlace(t *testing.T) {
	w := newWorld(t)
	w.onModel("qa_browser", "fake/scripted")
	// Direct as every project is that does not set it otherwise.
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = '{"requiredReviewers":["correctness"],"test":true,"pullRequestFixes":"direct"}' WHERE id = $1`, w.project)
	scripted := w.lux.Decide
	var atSimplifyReview string
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			reviews++
			if reviews == 2 {
				atSimplifyReview = w.gh.SHA("feature/greet")
			}
			return fakelux.Behaviour{Reply: "Reviewed it; no problems."}
		}
		return scripted(spec)
	}
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet in Portuguese")
	start := w.gh.SHA("feature/greet")
	w.gh.Review(n, "ana", "APPROVED")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReview)
	w.startDelivery(wi)
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(wi) })
	var frozen string
	_ = w.owner.QueryRow(context.Background(), `SELECT state->'policy'->>'pullRequestFixes' FROM workflow_runs WHERE task_id = $1`, wi).Scan(&frozen)
	if frozen != delivery.PullRequestFixesDirect {
		t.Fatalf("the delivery froze pullRequestFixes %q", frozen)
	}
	if got := w.phasesRun(wi); !slices.Equal(got, []string{"review", "simplify", "review", "test"}) {
		t.Fatalf("phases %v\n%s", got, w.describeRuns())
	}
	head := w.gh.SHA("feature/greet")
	if head == start || atSimplifyReview != head {
		t.Errorf("the simplifier's commit was not on the pull request before its review: %s at the review, %s now, %s before", atSimplifyReview, head, start)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND branch LIKE '%-candidate'`, wi); n != 0 {
		t.Errorf("%d Runs on a candidate", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, wi); n != 0 {
		t.Errorf("%d promotions", n)
	}
	for _, b := range []string{delivery.CandidateBranch(wi, 1), "feature/greet-candidate"} {
		if w.gh.SHA(b) != "" || len(w.gh.BranchLog(b)) > 0 {
			t.Errorf("candidate branch %s was written", b)
		}
	}
}

// The fixer of a direct delivery's pull request is told its fix goes to
// the pull request when it publishes; a candidate delivery's, that it is
// checked on a candidate branch first.
func TestDirectTheFixerIsToldItsFixGoesToThePullRequest(t *testing.T) {
	for _, direct := range []bool{true, false} {
		w := newPRFixWorldAt(t, prAt{test: true, direct: direct})
		w.comment("Please rename the greeting.")
		var fix string
		w.until("the fix submitted", func() bool {
			w.sync()
			fix, _, _ = w.fixRun()
			return fix != "" && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id IS NOT NULL`, fix) == 1
		})
		text := w.instructionsOf(fix)
		want := delivery.Instructions("fixer", "", delivery.InstructionsInput{DirectFixes: direct})
		_, role, _ := strings.Cut(want, "# Your role: fixer\n\n")
		role, _, _ = strings.Cut(role, "\n")
		if !strings.Contains(text, role) {
			t.Errorf("direct=%v: the fixer's file lacks its role's line %q", direct, role)
		}
	}
}

// Reviewers and testers never push, whichever way fixes reach the pull
// request: their specs name no push branch.
func TestDirectReviewersAndTestersStillNeverPush(t *testing.T) {
	for _, direct := range []bool{true, false} {
		w := newPRFixWorldAt(t, prAt{test: true, direct: direct})
		w.comment("Please rename the greeting.")
		w.until("the fix's review", func() bool {
			w.sync()
			return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
				AND lux_run_id IS NOT NULL`, w.wi, delivery.RangePullRequestFix) > 0
		})
		for _, r := range w.lux.Runs() {
			var spec lux.Spec
			if json.Unmarshal(r.Spec, &spec) != nil {
				continue
			}
			if p := spec.Labels["dude.phase"]; (p == "review" || p == "test") && spec.Git != nil && spec.Git.Push != nil {
				t.Errorf("direct=%v: a %s Run's spec pushes to %q", direct, p, spec.Git.Push.Branch)
			}
		}
	}
}

// The pull request syncer asks GitHub to update a branch that is behind
// only while the delivery waits on it: never while a direct fix's narrow
// loop runs, which pushes to it. Once the loop is done and the delivery
// waits again, it does.
func TestDirectNoBaseMergeIsAskedDuringAFixLoop(t *testing.T) {
	w := newPRFixWorldDirect(t)
	var hung atomic.Bool
	hung.Store(true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			b.Hang = hung.Load()
		}
	}
	w.comment("Please rename the greeting.")
	w.until("the narrow review running", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'`, w.wi) == 1
	})
	// The base moves: the pull request, at the fix's push, is behind.
	w.gh.AdvanceBase("main", "Base moved")
	w.until("the pull request read as behind", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND mergeable_state = 'behind'`, w.wi) == 1
	})
	for range 3 {
		w.sync()
		w.pump()
	}
	updates := `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action' AND payload->>'action' = 'update-branch'`
	if n := w.count(updates, w.wi); n != 0 {
		t.Errorf("%d update-branch requests during the fix's loop", n)
	}
	if w.waiting() {
		t.Fatalf("the delivery is waiting already\n%s", w.describeRuns())
	}
}

// The factory's delivery defaults, which the settings pages show for a key
// no layer sets, say direct fixes and a narrow loop of 2.
func TestTheFactoryDefaultsSayDirectFixes(t *testing.T) {
	w := newWorld(t)
	status, body := w.get("/internal/delivery-defaults", w.org)
	var p delivery.Policy
	if status != 200 || json.Unmarshal([]byte(body), &p) != nil || p.PullRequestFixes != delivery.PullRequestFixesDirect || p.MaxPRFixReviewIterations != 2 {
		t.Errorf("delivery defaults %d: %s", status, body)
	}
}
