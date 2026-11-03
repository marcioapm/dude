package orchestrator_test

// Everyone direct now (step 2, PR B1): the boot sweep moves every delivery
// still on the candidate path to direct fixes (delivery.ConvertCandidates),
// whatever state its candidate is in. Through the real workflow, API and
// fake lux and GitHub.

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// convertCandidates runs the sweep once, and returns its counts.
func (w *world) convertCandidates() delivery.CandidatesConverted {
	w.t.Helper()
	n, err := delivery.ConvertCandidates(context.Background(), w.app)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// candidateWorld is a pull request fix world whose delivery froze
// candidate fixes, as every delivery did before direct ones.
func candidateWorld(t *testing.T) *prFixWorld {
	t.Helper()
	w := newPRFixWorld(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{policy,pullRequestFixes}', '"candidate"') WHERE task_id = $1`, w.wi)
	return w
}

// midCandidateReview is a candidate world whose person's item (fbi_x) a
// fixer resolved on the candidate, now under its candidate's review, which
// hangs: returns the hanging review Run and the candidate's From.
func midCandidateReview(t *testing.T) (*prFixWorld, string, string, map[string]string) {
	t.Helper()
	w := candidateWorld(t)
	ana := w.person("Ana")
	w.assignOwner(w.wi, ana)
	w.resolveAsItPublishes(w.wi, "fbi_x", itsNewHead, nil)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			b.Hang = true
		}
	}
	w.armed.Store(true)
	if code, body := w.feedback(ana, w.wi, "fbi_x", "Log the retry count at info, not debug."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	var review string
	w.until("the candidate's review at work", func() bool {
		review = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'
			AND lux_state = 'running'`, w.wi)
		return review != ""
	})
	var c delivery.Candidate
	if err := json.Unmarshal([]byte(w.state("candidate")), &c); err != nil || c.Branch != delivery.CandidateBranch(w.wi, 1) {
		t.Fatalf("no candidate under review: %q %v\n%s", w.state("candidate"), err, w.describeRuns())
	}
	if status, _ := w.resolution("fbi_x"); status != "resolved" {
		t.Fatalf("fbi_x is %s, want resolved on the candidate", status)
	}
	if w.gh.SHA(w.branch) != w.before {
		t.Fatalf("the pull request moved before promotion")
	}
	return w, ana, review, c.From
}

// heads is the delivery's heads.
func (w *prFixWorld) heads() map[string]string {
	var h map[string]string
	_ = json.Unmarshal([]byte(w.state("heads")), &h)
	return h
}

func (w *prFixWorld) step() string {
	return w.str(`SELECT step FROM workflow_runs WHERE task_id = $1`, w.wi)
}

// directlyFixedAgain waits for the fix the sweep sent the delivery to: a
// fix Run with no branch of its own, given fbi_x, published to the pull
// request's head; the candidate branch still on GitHub.
func (w *prFixWorld) directlyFixedAgain(after string) {
	w.t.Helper()
	candidate := w.gh.SHA(delivery.CandidateBranch(w.wi, 1))
	var fix string
	w.until("the direct fix published", func() bool {
		w.sync()
		fix = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'
			AND COALESCE(branch, $2) = $2 AND created_at > (SELECT created_at FROM runs WHERE id = $3)`, w.wi, w.branch, after)
		return fix != "" && w.gh.SHA(w.branch) != w.before
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND 'fbi_x' = ANY(feedback_item_ids)`, fix); n != 1 {
		w.t.Errorf("the new fixer %s is not given the reopened item", fix)
	}
	if got := w.str(`SELECT COALESCE(heads->'target'->>'sha', '') FROM runs WHERE id = $1`, fix); got == "" || got != w.gh.SHA(w.branch) {
		w.t.Errorf("the pull request's head %s is not the new fix's %q", w.gh.SHA(w.branch), got)
	}
	if candidate == "" || w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) != candidate {
		w.t.Errorf("the candidate branch went: was %q, now %q", candidate, w.gh.SHA(delivery.CandidateBranch(w.wi, 1)))
	}
	if w.state("candidate") != "" {
		w.t.Errorf("a candidate again: %s", w.state("candidate"))
	}
}

// candidateDropped checks what the sweep did to a delivery with a candidate:
// no candidate, its heads back where the candidate started, direct, the
// item reopened, and one notice in Chat.
func (w *prFixWorld) candidateDropped(from map[string]string) {
	w.t.Helper()
	if w.state("candidate") != "" {
		w.t.Errorf("the candidate is still in state: %s", w.state("candidate"))
	}
	if got := w.heads(); !maps.Equal(got, from) {
		w.t.Errorf("heads %v, want the candidate's from %v", got, from)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'policy'->>'pullRequestFixes' = 'direct'`, w.wi); n != 1 {
		w.t.Errorf("the policy is not direct: %s", w.state("policy"))
	}
	if status, _ := w.resolution("fbi_x"); status != "open" {
		w.t.Errorf("fbi_x is %s, want reopened", status)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'candidate_converted'`, w.wi); n != 1 {
		w.t.Errorf("%d notices of the conversion, want 1", n)
	}
}

// State B (§3.3's world test): a delivery mid-candidate-review. Its review
// is aborted, the candidate gone, its heads where the candidate started,
// and its fix is redone directly: the new fix Run publishes to the pull
// request's head, with the reopened item; the candidate branch stays.
func TestConvertCandidatesRedoesAFixUnderReviewDirectly(t *testing.T) {
	w, _, review, from := midCandidateReview(t)
	if n := w.convertCandidates(); n.Converted != 1 || n.Moving != 0 || n.Left != 0 {
		t.Fatalf("swept %+v, want one converted", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND NOT keep`, review); n != 1 {
		t.Errorf("the candidate's review was not aborted\n%s", w.describeRuns())
	}
	if got := w.step(); got != "prFix" {
		t.Errorf("step %s, want prFix", got)
	}
	w.candidateDropped(from)
	w.directlyFixedAgain(review)
	// Converted once: a second sweep finds nothing.
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
}

// Two sweeps at once (two orchestrators booting) convert the delivery once.
func TestConvertCandidatesTwiceAtOnceConvertsOnce(t *testing.T) {
	w, _, review, from := midCandidateReview(t)
	var wg sync.WaitGroup
	var a, b delivery.CandidatesConverted
	var errA, errB error
	wg.Add(2)
	go func() { defer wg.Done(); a, errA = delivery.ConvertCandidates(context.Background(), w.app) }()
	go func() { defer wg.Done(); b, errB = delivery.ConvertCandidates(context.Background(), w.app) }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a.Converted+b.Converted != 1 {
		t.Errorf("converted %d and %d, want once", a.Converted, b.Converted)
	}
	w.candidateDropped(from)
	w.directlyFixedAgain(review)
}

// State A: a candidate delivery with no candidate open (waiting on its pull
// request) is made direct, and nothing else: its next fix publishes to the
// pull request.
func TestConvertCandidatesMakesADeliveryWithNoCandidateDirect(t *testing.T) {
	w := candidateWorld(t)
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v", n)
	}
	if got := w.step(); got != "awaitPullRequest" {
		t.Errorf("step %s, want it left waiting", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi); n != 0 {
		t.Errorf("a notice for a delivery with no candidate")
	}
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before
	})
	if _, branch, _ := w.fixRun(); branch != "" && branch != w.branch {
		t.Errorf("the fix published to %q", branch)
	}
	if w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) != "" {
		t.Errorf("a candidate branch was written")
	}
}

// State C: a candidate being promoted is left for the next sweep, counted
// moving, untouched.
func TestConvertCandidatesLeavesAPromotionMoving(t *testing.T) {
	w := candidateWorld(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'promoteCandidate', wake_at = now() + interval '1 hour',
		state = state || jsonb_build_object('candidate', jsonb_build_object('round', 1, 'branch', $2::text, 'from', state->'heads'))
		WHERE task_id = $1`, w.wi, delivery.CandidateBranch(w.wi, 1))
	before := w.str(`SELECT state::text FROM workflow_runs WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n.Moving != 1 || n.Converted != 0 {
		t.Fatalf("swept %+v, want one moving", n)
	}
	if after := w.str(`SELECT state::text FROM workflow_runs WHERE task_id = $1`, w.wi); after != before || w.step() != "promoteCandidate" {
		t.Errorf("a moving delivery changed:\n%s\n%s", before, after)
	}
	// Mid-refresh after a loop candidate's promotion: also moving.
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'refreshPromoted',
		state = state - 'candidate' || '{"promotedRefresh":{"repos":["target"],"next":"beforePR"}}' WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n.Moving != 1 || n.Converted != 0 {
		t.Fatalf("swept %+v, want one moving", n)
	}
}

// State D: parked on a person with a candidate open. The candidate goes,
// the escalation stays, and a retry fixes again directly from the pull
// request.
func TestConvertCandidatesLeavesAParkedDeliveryParkedAndItsRetryDirect(t *testing.T) {
	w, ana, review, from := midCandidateReview(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'decide', status = 'waiting', wake_at = NULL,
		awaiting_signals = '["human.decision"]', state = state || '{"escalation":{"reason":"review_failed","step":"review","at":"awaitReview"}}'
		WHERE task_id = $1`, w.wi)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'awaiting_input' WHERE id = $1`, w.wi)
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v", n)
	}
	if got := w.step(); got != "decide" {
		t.Errorf("step %s, want it left at decide", got)
	}
	if got := w.str(`SELECT state->'escalation'->>'step' || '/' || COALESCE(state->'escalation'->>'at', '') FROM workflow_runs WHERE task_id = $1`, w.wi); got != "prFix/" {
		t.Errorf("escalation step/at %s, want prFix/", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, review); n != 1 {
		t.Errorf("the candidate's review was not aborted")
	}
	w.candidateDropped(from)
	if code, body := w.decide(ana, w.wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	w.directlyFixedAgain(review)
}

// State F: stopped with its candidate kept for a recovery. The candidate
// goes, the kept Runs are let go, and a retry fixes again directly.
func TestConvertCandidatesLetsAStoppedDeliveryRetryDirectly(t *testing.T) {
	w, ana, review, from := midCandidateReview(t)
	w.stop(ana, w.wi, map[string]any{"note": "hold on"})
	w.until("stopped", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'aborted')`, w.wi) == 1
	})
	if w.state("candidate") == "" {
		t.Fatalf("the stop dropped the candidate")
	}
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND keep`, w.wi); n != 0 {
		t.Errorf("%d Runs still kept", n)
	}
	w.candidateDropped(from)
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/recover", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.directlyFixedAgain(review)
}

// State G: a hand-over left on a delivery is dropped with the rest.
func TestConvertCandidatesDropsAHandOver(t *testing.T) {
	w := candidateWorld(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"policy":{"pullRequestFixes":"direct"}}'::jsonb
		|| jsonb_build_object('policy', (state->'policy') || '{"pullRequestFixes":"direct"}',
		  'handedOver', '{"target:abc:def":{"repo":"target","head":"abc","base":"def"}}'::jsonb) WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v", n)
	}
	if w.state("handedOver") != "" {
		t.Errorf("the hand-over is still there: %s", w.state("handedOver"))
	}
	if got := w.step(); got != "awaitPullRequest" {
		t.Errorf("step %s", got)
	}
}
