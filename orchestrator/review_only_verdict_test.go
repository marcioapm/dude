package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Infrastructure failures get one fresh lens, not a missing review.
func TestAReviewOnlyFailedLensRetriesOnce(t *testing.T) {
	for _, c := range []struct {
		name, reason          string
		again, stopped, moved bool
		want                  int
	}{
		{"setup", "The setup script of greet did not finish within 15m0s.", false, false, false, 2},
		{"twice", "The setup script of greet failed with exit code 1", true, false, false, 2},
		{"budget", "the agent's turn failed: Reached maximum budget ($8)", false, false, false, 2},
		{"stopped", "The setup script of greet failed with exit code 1", false, true, false, 1},
		{"moved", "The setup script of greet did not finish within 15m0s.", false, false, true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.gh.SetChecks("success")
			w.hangingReviews(taskCall)
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
			w.startDelivery(wi)
			w.until("review lens started", func() bool {
				w.quietConductorDecides(wi)
				return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND status='running'`, wi) > 0
			})
			runs := w.ids(`SELECT id FROM runs WHERE task_id=$1 AND phase='review' ORDER BY created_at`, wi)
			old := runs[0]
			var category string
			if err := w.owner.QueryRow(context.Background(), `SELECT category FROM runs WHERE id=$1`, old).Scan(&category); err != nil {
				t.Fatal(err)
			}
			if c.moved {
				w.gh.CommitFiles("feature/greet", "new head", map[string]string{"new.md": "new\n"})
				w.sync()
			}
			status, control := "failed", "none"
			if c.stopped {
				status, control = "aborted", "abort"
			}
			mustExec(t, w.owner, `UPDATE runs SET status=$2::run_status, error=$3, control=$4, control_reason=CASE WHEN $2='aborted' THEN 'stopped by conductor' END, ended_at=now() WHERE id=$1`, old, status, c.reason, control)
			w.until("retry or verdict", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND category=$2`, wi, category) > 1 || w.waitingOnPR(wi)
			})
			if got := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND category=$2`, wi, category); got != c.want {
				t.Fatalf("lens runs=%d want %d", got, c.want)
			}
			if got := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND category<>$2`, wi, category); got != 0 {
				t.Fatalf("retried %d unrelated lenses", got)
			}
			if c.want == 2 {
				// Replay the durable wait after creation: it must keep this Run.
				mustExec(t, w.owner, `UPDATE workflow_runs SET status='running', awaiting_signals='[]' WHERE task_id=$1 AND step='awaitReview'`, wi)
				w.advanced()
				result := "completed"
				if c.again {
					result = "failed"
				}
				mustExec(t, w.owner, `UPDATE runs SET status=$3::run_status, error=CASE WHEN $3='failed' THEN $4 END, ended_at=now() WHERE task_id=$1 AND phase='review' AND id<>$2`, wi, old, result, c.reason)
			}
			w.until("final verdict", func() bool { return w.waitingOnPR(wi) })
			w.advanced()
			if got := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND category=$2`, wi, category); got != c.want {
				t.Fatalf("after resume: %d runs want %d", got, c.want)
			}
			if !c.moved {
				reviews := w.dudeReviews(wi, n)
				if len(reviews) != 1 {
					t.Fatalf("reviews=%+v", reviews)
				}
				if c.again || c.stopped {
					if reviews[0].State != "COMMENTED" || !strings.Contains(reviews[0].Body, category) || !strings.Contains(reviews[0].Body, c.reason) {
						t.Fatalf("incomplete: %+v", reviews[0])
					}
				} else if reviews[0].State != "APPROVED" {
					t.Fatalf("complete: %+v", reviews[0])
				}
			}
		})
	}
}

func TestAReviewOnlyIncompleteVerdictSeparatesCheckedFindings(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	w.hangingReviews(taskCall)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.startDelivery(wi)
	w.until("lens in flight", func() bool {
		w.quietConductorDecides(wi)
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND status='running'`, wi) > 0
	})
	ids := w.ids(`SELECT id FROM runs WHERE task_id=$1 AND phase='review'`, wi)
	old := ids[0]
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, category, base_refs)
		SELECT 'old_'||id, organization_id, project_id, task_id, attempt, phase, role, kind, 'completed', category, '{}'::jsonb FROM runs WHERE id=$1`, old)
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, status)
		SELECT $2||id, organization_id, task_id, 'old_'||id, category, 'high', $3, $4::finding_status FROM runs WHERE id=$1`, old, "unchecked_", "Unchecked deadline", "open")
	for _, c := range []struct{ id, title, status string }{{"fixed_", "Fixed deadline", "resolved"}, {"still_", "Confirmed problem", "open"}} {
		mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, status)
			SELECT $2||id, organization_id, task_id, 'old_'||id, 'security', 'high', $3, $4::finding_status FROM runs WHERE id=$1`, old, c.id, c.title, c.status)
	}
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, category, base_refs, finding_ids, verdicts)
		SELECT 'judged_'||id, organization_id, project_id, task_id, attempt, phase, role, kind, 'completed', 'security', base_refs,
		ARRAY['unchecked_'||id, 'fixed_'||id, 'still_'||id],
		jsonb_build_object('unchecked_'||id,'fixed','fixed_'||id,'fixed','still_'||id,'still') FROM runs WHERE id=$1`, old)
	mustExec(t, w.owner, `UPDATE runs SET status='failed', error='review harness crashed', ended_at=now() WHERE id=$1`, old)
	w.until("incomplete verdict posted", func() bool { return w.waitingOnPR(wi) })
	reviews := w.dudeReviews(wi, n)
	if len(reviews) != 1 || reviews[0].State != "COMMENTED" {
		t.Fatalf("reviews=%+v", reviews)
	}
	body := reviews[0].Body
	for _, want := range []string{"Could not complete the review", "**Fixed at ", "Fixed deadline", "Confirmed problem", "**Not re-checked at "} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	unchecked := strings.Split(body, "**Not re-checked at ")
	if len(unchecked) != 2 || !strings.Contains(unchecked[1], "Unchecked deadline") || strings.Contains(unchecked[0], "Unchecked deadline") || strings.Contains(unchecked[1], "(high)") || strings.Contains(body, "open finding(s)") {
		t.Fatalf("unchecked assertion:\n%s", body)
	}
}

// cleanLenses has every lens find nothing.
func (w *world) cleanLenses() {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			return fakelux.Behaviour{Reply: "No findings."}
		}
		return scripted(spec)
	}
}

// reviewOnly starts a review-only task on alice's pull request, its lenses
// finding nothing; its task and number.
func (w *world) reviewOnly() (string, int) {
	w.t.Helper()
	w.cleanLenses()
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.startDelivery(wi)
	return wi, n
}

// dudeReviews are pull request n's reviews carrying the task's ReviewMarker.
func (w *world) dudeReviews(wi string, n int) []fakegithub.Review {
	var out []fakegithub.Review
	for _, r := range w.gh.Pull(n).Reviews {
		if strings.Contains(r.Body, "<!-- dude:review task="+wi+" ") {
			out = append(out, r)
		}
	}
	return out
}

// writers counts the task's writer Runs: none may ever start.
func (w *world) writers(wi string) int {
	return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix', 'simplify')`, wi)
}

// withDeliveries has the world's deliveries run on store (its timings),
// with the world's database and pull request sync.
func (w *world) withDeliveries(store *delivery.Store) {
	store.DB, store.SyncPullRequest = w.app, w.prs.Sync
	w.runtime.Register(delivery.Workflow(store, forge.Resolver{DB: w.app}))
}

// G1c: a review-only task runs its lenses at the pull request's head,
// never starts a writer, and posts exactly one review pinned to that head;
// with nothing found and CI green, an approval. It then waits, no agent
// running, and is not ready to merge: dude delivered nothing.
func TestAReviewOnlyTaskPostsOneReviewAtTheHead(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	wi, n := w.reviewOnly()
	head := w.gh.SHA("feature/greet")
	w.agentsDone(wi, "dude's review posted", func() bool { return len(w.dudeReviews(wi, n)) > 0 && w.waitingOnPR(wi) })
	got := w.dudeReviews(wi, n)
	if len(got) != 1 || got[0].State != "APPROVED" || got[0].CommitID != head {
		t.Fatalf("dude's reviews %+v, want one APPROVED at %s", got, head)
	}
	if !strings.Contains(got[0].Body, "Approved at "+head[:7]) {
		t.Errorf("the review's body: %q", got[0].Body)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'`, wi); r == 0 {
		t.Error("no lens reviewed it")
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writer Runs on a review-only task", c)
	}
	if e := w.events(wi, delivery.EvReviewSubmitted); e != 1 {
		t.Errorf("%d review_submitted events", e)
	}
	if s := w.taskStatus(wi); s != "review" {
		t.Errorf("the task is %s, want review", s)
	}
	// One status comment, dude's own: never feedback.
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "Approved at") {
		t.Fatalf("status comments %+v", c)
	}
	// Synced again: neither its review nor its status comment is anyone's feedback.
	w.sync()
	w.advanced()
	if f := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type IN ('pull_request.commented', 'pull_request.reviewed')
		AND payload->>'body' LIKE '%<!-- dude:%' AND payload->>'own' IS DISTINCT FROM 'true'`, wi); f != 0 {
		t.Errorf("%d of dude's own posts recorded as feedback", f)
	}
	// Merged: done.
	w.gh.Merge(n)
	w.until("done", func() bool { w.sync(); w.advanced(); return w.taskStatus(wi) == "done" })
}

// G4: GitHub took the review and its answer was lost. The claim stays;
// the next run finds the review by its marker (FindOwnReview) and records
// it recovered: still exactly one review, never resubmitted.
func TestALostReviewAnswerIsRecoveredNotResubmitted(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	w.gh.ReviewTimeoutOnce = true
	wi, n := w.reviewOnly()
	w.agentsDone(wi, "the review recorded", func() bool { return w.events(wi, delivery.EvReviewSubmitted) == 1 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); len(got) != 1 {
		t.Fatalf("%d reviews, want exactly one", len(got))
	}
	if r := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'recovered' = 'true'`,
		wi, delivery.EvReviewSubmitted); r != 1 {
		t.Fatal("the review was not recorded as recovered")
	}
	if c := w.events(wi, delivery.EvReviewClaimed); c != 1 {
		t.Errorf("%d claims, want the one kept", c)
	}
}

// G2: a new head pushed before the verdict: no review is posted at all,
// and nothing is left to post later.
func TestANewHeadBeforeTheVerdictPostsNothing(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.cleanLenses()
	scripted := w.lux.Decide
	pushed := false
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "review" && !pushed {
			pushed = true
			w.gh.CommitFiles("feature/greet", "alice's later change", map[string]string{"later.md": "later\n"})
		}
		return scripted(spec)
	}
	w.startDelivery(wi)
	w.agentsDone(wi, "the verdict abandoned", func() bool { return w.events(wi, delivery.EvReviewAbandoned) > 0 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); len(got) != 0 {
		t.Fatalf("a review was posted for a head the pull request no longer shows: %+v", got)
	}
	w.sync()
	w.advanced()
	if got := w.dudeReviews(wi, n); len(got) != 0 {
		t.Fatalf("a zombie review: %+v", got)
	}
}

// G5: CI pending at the reviewed head defers the verdict with no agent
// Run; CI's signal then posts it.
func TestPendingCIDefersTheVerdictUntilTheChecksSignal(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi, n := w.reviewOnly()
	w.agentsDone(wi, "deferred", func() bool { return w.events(wi, delivery.EvVerdictDeferred) > 0 && w.waitingOnPR(wi) })
	runs := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi)
	if got := w.dudeReviews(wi, n); len(got) != 0 {
		t.Fatalf("posted while CI is pending: %+v", got)
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "waiting for CI") {
		t.Fatalf("status comment %+v", c)
	}
	w.gh.SetChecks("success")
	w.until("approved once CI passed", func() bool { w.sync(); w.advanced(); return len(w.dudeReviews(wi, n)) == 1 })
	if got := w.dudeReviews(wi, n); got[0].State != "APPROVED" {
		t.Fatalf("after CI passed: %+v", got)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi); r != runs {
		t.Errorf("%d Runs made while deferred", r-runs)
	}
	// Edited, never duplicated.
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "Approved at") {
		t.Fatalf("status comments %+v", c)
	}
}

// The cap: a verdict never defers past it, even while the gate's grace
// never runs out; it comments that CI could not be confirmed.
func TestADeferredVerdictStopsAtItsCap(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	w.withDeliveries(&delivery.Store{VerdictDeferCap: time.Nanosecond})
	wi, n := w.reviewOnly()
	w.agentsDone(wi, "commented at the cap", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	got := w.dudeReviews(wi, n)
	if got[0].State != "COMMENTED" || !strings.Contains(got[0].Body, "could not confirm CI") {
		t.Fatalf("at the cap: %+v", got)
	}
}

// A review the account would post on its own pull request: GitHub refuses
// APPROVE, and dude posts its verdict as a COMMENT, recorded downgraded.
func TestAnOwnPullRequestsApprovalIsDowngradedToAComment(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	n := w.gh.OpenAsPerson(fakegithub.Login, "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.cleanLenses()
	w.startDelivery(wi)
	w.agentsDone(wi, "posted", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); got[0].State != "COMMENTED" {
		t.Fatalf("%+v", got)
	}
	if e := w.events(wi, delivery.EvReviewDowngraded); e != 1 {
		t.Fatalf("%d downgrades", e)
	}
}

// Decision 2: CI failing at the reviewed head. The conductor is woken
// first (a decision at pull request feedback, offered review and test
// only); judged unrelated (its "next"), the review is posted without
// approving; once CI passes at that head, the approval follows.
func TestRedCIAtTheHeadIsTheConductorsBeforeAnyApproval(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("failure")
	wi, n := w.reviewOnly()
	w.agentsDone(wi, "commented without approving", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	if d := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_awaited'
		AND payload->>'point' = $2`, wi, delivery.PointPRFeedback); d != 1 {
		t.Fatalf("%d decisions about the failing checks, want 1", d)
	}
	got := w.dudeReviews(wi, n)
	if got[0].State != "COMMENTED" || !strings.Contains(got[0].Body, "not approved while CI fails") {
		t.Fatalf("with red CI: %+v", got)
	}
	w.gh.SetChecks("success")
	w.until("approved once green at the head", func() bool { w.sync(); w.advanced(); return len(w.dudeReviews(wi, n)) == 2 })
	if got := w.dudeReviews(wi, n); got[1].State != "APPROVED" || got[1].CommitID != got[0].CommitID {
		t.Fatalf("after CI passed: %+v", got)
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writers", c)
	}
}
