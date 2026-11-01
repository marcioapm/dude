package orchestrator_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A conducted direct delivery's pull request fix: the conductor decides
// on the comment, then the fix's narrow loop runs — its review's blocking
// finding fixed and reviewed again — and ends at prFixDone. Each review
// round of the loop is the conductor's (step 3): it is asked after each
// round, and never after a fix, and its decide next goes on as the
// policy's rules would. (The name is from before step 3, when the loop ran
// with no decision; kept, as the test's id.) A fresh reviewer (a large
// delta) keeps the loop's range. Neither an older open blocking finding
// outside the range nor a person's open feedback item holds the loop's
// exit.
//
// Each of the loop's direct-mode switches (fixLoop at awaitFix,
// withFeedback, loopsOwn, KeepRange) reverted to checkingCandidate turns
// this red; gateReviewers' is the next test's.
func TestDirectAConductedNarrowLoopRunsWithoutTheConductor(t *testing.T) {
	w := conducting(t)
	w.toolsWithSync()
	w.onModel("reviewer", "llm-reviewer")
	task := w.prOpen(w.task(), prAt{direct: true, test: true})
	setPolicy(w, task, `{"resumeRounds":true}`)
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	ctx := context.Background()
	// An older blocking finding, open, of a review before the pull request.
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description, status)
		SELECT 'fnd_old_'||$1, organization_id, task_id, run_id, category, 'blocking', 'Old', 'old', 'open'
		FROM review_findings WHERE task_id = $1 ORDER BY created_at LIMIT 1`, task)
	// A person's feedback item, open, that no fix will answer.
	ana := w.person("Ana")
	mustExec(t, w.owner, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		SELECT 'fbb_'||$1, $2, $1, 1, person_id, 'k' FROM api_keys WHERE id = $3`, task, w.org, ana)
	mustExec(t, w.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind,
			status, submitted_at)
		SELECT 'fbi_'||$1, $2, $1, 1, 'fbb_'||$1, person_id, 'Log at info.', 'general', 'open', now() FROM api_keys WHERE id = $3`, task, w.org, ana)

	var large atomic.Bool
	w.gh.CompareAnswer = func(base, head string, answer map[string]any) {
		if large.Load() {
			answer["files"] = []any{map[string]any{"filename": "big.go", "status": "modified",
				"additions": delivery.MaxResumeDeltaLines, "deletions": 1}}
		}
	}
	var mu sync.Mutex
	var phases []string
	counts := map[string]int{}
	var armed atomic.Bool
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		phase, _ := labels["dude.phase"].(string)
		b := scripted(spec)
		if !armed.Load() || phase == "" {
			return b
		}
		mu.Lock()
		phases = append(phases, phase)
		counts[phase]++
		n := counts[phase]
		mu.Unlock()
		switch {
		case phase == "review" && n == 1:
			b.Reply = "One problem:\n\n" + reviewFinding("The fix leaves the greeting untranslated")
		case phase == "fix" && n == 2:
			large.Store(true)
		case phase == "review" && n == 2:
			large.Store(false)
			b.Reply = "Reviewed the fix.\n\n" + fakeagent.Verdict
		}
		return b
	}
	seen := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(phases) }

	armed.Store(true)
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("feedback to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	var from int64
	_ = w.owner.QueryRow(ctx, `SELECT max(cursor) FROM events WHERE task_id = $1`, task).Scan(&from)
	w.must(task, "decide", `{"action":"next"}`)
	stopped := func() string {
		if at := w.decisionAt(task); at == delivery.PointFixed {
			return at
		}
		return w.escalationReason(task)
	}
	w.until("the fix's loop done", func() bool {
		w.sync()
		w.nextAtRound(task)
		return stopped() != "" || len(seen()) >= 4 && w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
			AND NOT state ? 'fixRange' AND (step = 'awaitPullRequest' OR state->'decision'->>'point' = $2)`, task, delivery.PointPRFeedback) == 1
	})
	if s := stopped(); s != "" {
		t.Fatalf("the loop stopped at %q after %v\n%s", s, seen(), w.describeRuns())
	}
	if got := seen(); !slices.Equal(got, []string{"fix", "review", "fix", "review"}) {
		t.Fatalf("after the decision: %v, want [fix review fix review]\n%s", got, w.describeRuns())
	}
	if got := slices.DeleteFunc(w.awaited(task, from), func(p string) bool { return p == delivery.PointPRFeedback }); !slices.Equal(got,
		[]string{delivery.PointReviewed, delivery.PointReviewed}) {
		t.Errorf("the conductor was asked at %v during the loop, want after each of its two rounds", got)
	}
	// Both reviews over the round's push, the fresh one too.
	reviews := w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2 ORDER BY created_at`,
		task, delivery.RangePullRequestFix)
	if len(reviews) != 2 {
		t.Fatalf("%d reviews over the fix's range, want 2\n%s", len(reviews), w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'fresh' = 'large_delta'`,
		reviews[1]); n != 1 {
		t.Errorf("the second review was not fresh for a large delta: the KeepRange premise does not hold")
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE id = 'fnd_old_'||$1 AND status = 'open'`, task); n != 1 {
		t.Errorf("the older finding is no longer open: the premise does not hold")
	}
	if n := w.count(`SELECT count(*) FROM feedback_items WHERE id = 'fbi_'||$1 AND status IN ('open', 'delivered')`, task); n != 1 {
		t.Errorf("the feedback item is no longer open: the premise does not hold")
	}
}

// A direct fix that changed only paths outside the product (CI
// configuration) picks no reviewer by the policy's round rules: its push
// is on the pull request, so the gate's reviewers review it all the same.
func TestDirectAFixOutsideTheProductIsStillReviewed(t *testing.T) {
	w := newPRFixWorldDirect(t)
	setPolicy(w.world, w.wi, `{"alwaysReviewers":[]}`)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" {
			b.Commit = map[string]string{".github/workflows/ci.yml": "on: push\n"}
		}
	}
	w.comment("Please run CI on push.")
	w.until("the fix on the pull request and the delivery waiting", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.waiting()
	})
	fixed := w.gh.SHA(w.branch)
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND review_range->>'of' = $2 AND base_refs->>'target' = $3`, w.wi, delivery.RangePullRequestFix, fixed); n == 0 {
		t.Fatalf("the fix's push %s reached the wait with no review\n%s", fixed, w.describeRuns())
	}
}
