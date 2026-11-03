package orchestrator_test

// Every fix reaches its pull request directly: the boot sweep moves every
// delivery still on the candidate path to direct fixes (delivery.ConvertCandidates),
// whatever state its candidate is in. Through the real workflow, API and
// fake lux and GitHub.

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
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
	var got map[string]string
	_ = json.Unmarshal([]byte(w.state("heads")), &got)
	if !maps.Equal(got, from) {
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
	if got := w.str(`SELECT state->'escalation'->>'step' || '/' || COALESCE(state->'escalation'->>'at', '') FROM workflow_runs WHERE task_id = $1`, w.wi); got != "prFix/prFix" {
		t.Errorf("escalation step/at %s, want prFix/prFix", got)
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

// Every shape a delivery with an open candidate can be in when the sweep
// runs, under one rule (ConvertCandidateTx): the candidate goes, Retries
// moves on so the redo's Runs are keyed apart from every earlier one's,
// and the fix is redone from the pull request's head (prFix for a pull
// request fix's candidate, the loop's writer for a loop's): at once when
// it runs or waits on its conductor's decision (dropped), on the person's
// answer, whatever resuming one, when it is parked on an escalation.
// Whatever the shape: a fix Run started after the sweep publishes to the
// pull request's head, no Run from before the sweep is waited on, and the
// workflow never completes under a running task.
func TestConvertCandidatesRedoesEveryShapeWithAFreshFix(t *testing.T) {
	type shape struct {
		name   string
		loop   bool
		step   string // "" running as midCandidateReview left it
		point  string // conductorDecision's
		reason string // decide's
		action string
		note   string
	}
	shapes := []shape{
		{name: "running/pr_fix"},
		{name: "running/loop", loop: true},
		{name: "conductorDecision/after_fix", loop: true, step: "conductorDecision", point: delivery.PointFixed},
		{name: "conductorDecision/after_review", loop: true, step: "conductorDecision", point: delivery.PointReviewed},
	}
	for _, d := range []struct {
		reason  string
		actions []string
	}{
		{"stuck", []string{"retry", "accept"}},
		{"exhausted", []string{"retry", "accept"}},
		{delivery.ReasonNotConverging, []string{"retry", "accept", "amend"}},
		{delivery.ReasonChecksUnverified, []string{"retry", delivery.ActionWaive}},
		{delivery.ReasonTestBlocked, []string{"retry", delivery.ActionWaive}},
	} {
		for _, a := range d.actions {
			note := ""
			if delivery.NeedsNote(d.reason, a) {
				note = "The greeting may stay in English."
			}
			// A loop's writer escalates stuck, exhausted and not converging;
			// a test or a held check, a pull request fix's candidate too.
			loop := d.reason != delivery.ReasonChecksUnverified && d.reason != delivery.ReasonTestBlocked
			shapes = append(shapes, shape{name: "decide/" + d.reason + "/" + a, loop: loop, step: "decide",
				reason: d.reason, action: a, note: note})
		}
	}
	for _, c := range shapes {
		t.Run(c.name, func(t *testing.T) {
			w, ana, review, from := midCandidateReview(t)
			wake := "" // the untaken decision's wake ConvertInFlight records
			if c.loop {
				mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{candidate,loop}', 'true') WHERE task_id = $1`, w.wi)
			}
			switch c.step {
			case "conductorDecision":
				mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'conductorDecision', status = 'waiting', wake_at = NULL,
					awaiting_signals = '["conductor.decision"]',
					state = state || jsonb_build_object('decision', jsonb_build_object('point', $2::text, 'policy', 'review'))
					WHERE task_id = $1`, w.wi, c.point)
				// As boot runs them: the quiet conductor's sweep first, which
				// records the untaken decision's wake.
				w.convert()
				wake = w.str(`SELECT COALESCE(max(key), '') FROM conductor_wakes WHERE task_id = $1 AND key LIKE '%:decision:%'
					AND delivered_at IS NULL`, w.wi)
				if wake == "" {
					t.Fatalf("no pending decision wake after ConvertInFlight")
				}
			case "decide":
				at, step := "awaitFix", "fix"
				if !c.loop {
					at, step = "awaitReview", "review"
				}
				mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'decide', status = 'waiting', wake_at = NULL,
					awaiting_signals = '["human.decision"]', state = state || jsonb_build_object('testRunId', $5::text,
					'escalation', jsonb_build_object('reason', $2::text, 'step', $3::text, 'at', $4::text,
					  'atSignals', '["phase.finished"]'::jsonb, 'detail', jsonb_build_object('runId', $5::text)))
					WHERE task_id = $1`, w.wi, c.reason, step, at, review)
				mustExec(t, w.owner, `UPDATE tasks SET status = 'awaiting_input' WHERE id = $1`, w.wi)
			}
			before := w.str(`SELECT clock_timestamp()::text`)
			if n := w.convertCandidates(); n.Converted != 1 {
				t.Fatalf("swept %+v", n)
			}
			w.candidateDropped(from)
			if w.state("decision") != "" {
				t.Errorf("the conductor's decision about the candidate is still there: %s", w.state("decision"))
			}
			if c.step == "conductorDecision" {
				// The decision's wake is settled with it: no turn is started
				// for a decision that no longer exists.
				if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = $2
					AND delivered_at IS NULL`, w.wi, wake); n != 0 {
					t.Errorf("the cleared decision's wake %s is still pending", wake)
				}
			}
			if c.step == "decide" {
				if got := w.step(); got != "decide" {
					t.Errorf("step %s, want it left at decide", got)
				}
				if code, body := w.decide(ana, w.wi, c.action, c.note); code != 200 {
					t.Fatalf("%s: %d %v", c.action, code, body)
				}
			}
			w.directlyFixedAgain(review)
			// The fix's own review: a Run after the sweep, not the one from
			// before it whose key a reset Iteration would match.
			w.until("the new fix's own review finished", func() bool {
				if wake != "" {
					// Quiet now (ConvertInFlight): the redo's own decisions
					// lapse to their defaults.
					w.lapse(w.wi)
				}
				if w.count(`SELECT count(*) FROM workflow_runs wf JOIN tasks t ON t.id = wf.task_id
					WHERE wf.task_id = $1 AND wf.status = 'completed' AND t.status = 'running'`, w.wi) != 0 {
					t.Fatalf("the workflow completed with its task running\n%s", w.describeRuns())
				}
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
					AND created_at > (SELECT min(created_at) FROM runs WHERE task_id = $1 AND phase = 'fix' AND created_at > $2::timestamptz)`, w.wi, before) > 0
			})
			w.until("the delivery past the new fix's review", func() bool {
				if wake != "" {
					w.lapse(w.wi)
				}
				var pending []string
				_ = json.Unmarshal([]byte(w.state("pendingRunIds")), &pending)
				for _, id := range pending {
					if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND created_at <= $2::timestamptz`, id, before) != 0 {
						return false
					}
				}
				return w.step() != "awaitReview" || len(pending) > 0
			})
			if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND created_at <= $2::timestamptz
				AND status NOT IN ('aborted', 'completed', 'failed')`, w.wi, before); n != 0 {
				t.Errorf("%d Runs from before the sweep still live\n%s", n, w.describeRuns())
			}
			if n := w.count(`SELECT count(*) FROM workflow_runs wf JOIN tasks t ON t.id = wf.task_id
				WHERE wf.task_id = $1 AND wf.status = 'completed' AND t.status IN ('running', 'review')`, w.wi); n != 0 {
				t.Errorf("the workflow completed while its task goes on: step %s\n%s", w.step(), w.describeRuns())
			}
			if n := w.count(`SELECT count(*) FROM conductor_wakes cw WHERE cw.task_id = $1 AND cw.key = $2
				AND (cw.conductor_run_id IS NOT NULL OR EXISTS (SELECT 1 FROM conductor_wake_attempts a WHERE a.wake_id = cw.id))`, w.wi, wake); n != 0 {
				t.Errorf("a conductor turn was started for the cleared decision's wake %s", wake)
			}
		})
	}
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

// Selection: a delivery frozen before pullRequestFixes was a key (most
// live ones) still fixes through a candidate: the sweep makes it direct,
// and touches nothing else (no Retries, no step).
func TestConvertCandidatesMakesAKeylessDeliveryDirect(t *testing.T) {
	w := candidateWorld(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state #- '{policy,pullRequestFixes}' WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v, want the keyless delivery converted", n)
	}
	if got := w.state("policy"); !strings.Contains(got, `"pullRequestFixes": "direct"`) {
		t.Errorf("policy %s, want direct", got)
	}
	if got := w.step(); got != "awaitPullRequest" || w.state("retries") != "" {
		t.Errorf("step %s, retries %q: want it left waiting, untouched", got, w.state("retries"))
	}
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before
	})
	if w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) != "" {
		t.Errorf("a candidate branch was written")
	}
}

// Selection: an attempt started over is not the task's delivery any more.
// Stopped with its candidate kept, then superseded by a start over, it is
// left as it is: no notice, and the live attempt's kept Run stays kept.
func TestConvertCandidatesLeavesASupersededAttempt(t *testing.T) {
	w, ana, _, _ := midCandidateReview(t)
	w.stop(ana, w.wi, map[string]any{"note": "hold on"})
	w.until("stopped", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'aborted')`, w.wi) == 1
	})
	old := w.str(`SELECT id FROM workflow_runs WHERE task_id = $1`, w.wi)
	w.pickUp(ana, w.wi, map[string]any{"action": "restart"})
	w.until("the next attempt", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND id <> $2`, w.wi, old) == 1
	})
	// As a person's stop at decide leaves it (State.Stopped), recoverable
	// but for the start over.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"stopped":{"reason":"stuck","step":"fix"}}' WHERE id = $1`, old)
	if w.count(`SELECT count(*) FROM workflow_runs WHERE id = $1 AND state ? 'candidate' AND (state ? 'stopped' OR state ? 'halt')`, old) != 1 {
		t.Fatalf("the stopped attempt kept no candidate: nothing to pin: %s", w.str(`SELECT state::text FROM workflow_runs WHERE id = $1`, old))
	}
	// A Run of the live attempt kept for its next round.
	w.until("a Run of the next attempt", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2 AND status IN ('aborted', 'failed', 'completed')`, w.wi) > 0
	})
	kept := w.str(`SELECT max(id) FROM runs WHERE task_id = $1 AND attempt = 2 AND status IN ('aborted', 'failed', 'completed')`, w.wi)
	mustExec(t, w.owner, `UPDATE runs SET keep = true, kept_until = now() + interval '1 hour' WHERE id = $1`, kept)
	before := w.str(`SELECT state::text FROM workflow_runs WHERE id = $1`, old)
	w.convertCandidates()
	if after := w.str(`SELECT state::text FROM workflow_runs WHERE id = $1`, old); after != before {
		t.Errorf("the superseded attempt was rewritten")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND keep AND kept_until > now()`, kept); n != 1 {
		t.Errorf("the live attempt's kept Run %s was let go", kept)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi); n != 0 {
		t.Errorf("%d notices for a superseded attempt", n)
	}
}

// Selection: a task closed with its candidate open has ended: not
// selected, and nothing tells Chat dude fixes it again.
func TestConvertCandidatesLeavesAClosedTask(t *testing.T) {
	w, ana, _, _ := midCandidateReview(t)
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	w.until("closed", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'aborted')`, w.wi) == 1
	})
	if w.state("candidate") == "" || w.state("stopped") != "" || w.state("halt") != "" {
		t.Fatalf("closed: candidate %q stopped %q halt %q: nothing to pin", w.state("candidate"), w.state("stopped"), w.state("halt"))
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("swept %+v, want nothing", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi); n != 0 {
		t.Errorf("%d notices for a closed task", n)
	}
}
