package orchestrator_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// requireReviewers merges policy (JSON) into the project's delivery policy.
func (w *world) requireReviewers(policy string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || $2::jsonb WHERE id = $1`, w.project, policy)
}

// adoptedConducted adopts alice's pull request in mode and talks it
// through: parked at its first decision; task and number.
func (w *world) adoptedConducted(mode string) (string, int) {
	w.t.Helper()
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", mode)
	if status, out := w.call("/internal/tasks/"+wi+"/talk", map[string]any{}); status != 201 {
		w.t.Fatalf("talk: %d %v", status, out)
	}
	w.until("its first decision", func() bool { return w.decisionAt(wi) != "" })
	return wi, n
}

// reviewLenses is the categories of the task's review Runs.
func (w *world) reviewLenses(wi string) []string {
	return w.ids(`SELECT DISTINCT category FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY 1`, wi)
}

// keepsFreedoms checks a writing task's conductor, after its directed
// correctness round: that lens alone ran, next and ask_person are offered
// with no review-only note, and a free-text dismissal records no grounds.
func (w *world) keepsFreedoms(wi string) {
	w.t.Helper()
	if got := w.reviewLenses(wi); len(got) != 1 || got[0] != "correctness" {
		w.t.Fatalf("a writing task's directed round ran %v, want its correctness alone", got)
	}
	if a := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_awaited'
		AND payload->>'point' = $2 AND payload->>'actions' = '["next", "ask_person"]' AND payload->>'note' NOT LIKE '%posts dude''s review%'`,
		wi, delivery.PointReviewed); a != 1 {
		w.t.Fatalf("%d decisions after a writing task's round offering next and ask_person, as before", a)
	}
	w.must(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"Valid finding, but not fixed here"}`, w.openFindings(wi, 1)[0]))
	if g := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'grounds'`,
		wi, delivery.EvFindingResolved); g != 0 {
		w.t.Fatalf("a writing task's dismissal recorded grounds")
	}
}

// untilDecision runs the task's phase Runs, deciding nothing, until its
// delivery waits on the conductor at point.
func (w *world) untilDecision(wi, point string) {
	w.t.Helper()
	w.until("the decision at "+point, func() bool {
		w.sync()
		w.advanced()
		for _, id := range w.pendingPhaseRuns(wi) {
			w.finished(id)
		}
		return w.decisionAt(wi) == point
	})
}

// openFindings is the task's open findings, oldest first: at least want.
func (w *world) openFindings(wi string, want int) []string {
	w.t.Helper()
	ids := w.ids(`SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open' ORDER BY created_at, id`, wi)
	if len(ids) < want {
		w.t.Fatalf("open findings %v, want %d", ids, want)
	}
	return ids
}

// dismissedWith is how many conductor dismissals of the task recorded
// grounds whose key is value.
func (w *world) dismissedWith(wi, key, value string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->'grounds'->>$3 = $4`,
		wi, delivery.EvFindingResolved, key, value)
}

// T1-T4 on a review-only task: a dismissal states its grounds; after the
// round, ask_person is not offered and next posts dude's review; a
// directed round still runs every lens the verdict requires; the
// conductor posts no review of its own on the pull request.
func TestAReviewOnlyConductorReviewsItDoesNotTriageAway(t *testing.T) {
	w := conducting(t)
	w.gh.SetChecks("success")
	w.requireReviewers(`{"requiredReviewers":["correctness","tests","performance"]}`)
	wi, n := w.adoptedConducted(delivery.ModeReviewOnly)
	// T3: the conductor directs correctness alone; the policy's other
	// required lenses run too.
	w.must(wi, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the round's lenses", func() bool { w.advanced(); return len(w.reviewLenses(wi)) > 0 })
	if got := w.reviewLenses(wi); !slices.Contains(got, "performance") || !slices.Contains(got, "tests") {
		t.Fatalf("the round ran %v: performance and tests were required", got)
	}
	w.untilDecision(wi, delivery.PointReviewed)
	if a := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_awaited'
		AND payload->>'point' = $2 AND payload->>'note' LIKE '%posts dude''s review%' AND payload->>'actions' = '["next"]'`,
		wi, delivery.PointReviewed); a != 1 {
		t.Fatalf("%d decisions after the round saying next posts dude's review, offering only next", a)
	}
	f := w.openFindings(wi, 1)[0]

	// T4: no top-level comment of the conductor's.
	w.refused(wi, "reply_on_pull_request", fmt.Sprintf(`{"pr":"%d","text":"My review: changes requested."}`, n),
		"dude's review and its status comment are the output")
	// T4 refuses only its own top-level post: a reply in a thread someone
	// started still posts there.
	line := w.gh.LineComment(n, "alice", "FACTORY.md", "Why record it here?")
	w.until("alice's line comment read", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'feedbackId' = $3`,
			wi, delivery.EvPullRequestCommented, fmt.Sprintf("line-comment-%d", line)) > 0
	})
	w.must(wi, "reply_on_pull_request", fmt.Sprintf(`{"pr":"%d","text":"The review covers it.","in_reply_to":"line-comment-%d"}`, n, line))
	if lines := w.gh.Pull(n).LineComments; lines[len(lines)-1].InReplyTo != line || !strings.HasPrefix(lines[len(lines)-1].Body, "The review covers it.") {
		t.Fatalf("the thread's reply: %+v", lines[len(lines)-1])
	}

	// T1: grounds, or refused. "wrong:" evidence says something: a letter
	// or digit, and more than a word.
	for reason, want := range map[string]string{
		"Valid finding, but not fixed here": "no reason to dismiss it",
		"no fixer runs on this task":        "no reason to dismiss it",
		"wrong:":                            `Say what shows the finding is wrong after "wrong: "`,
		"WRONG : .":                         `Say what shows the finding is wrong after "wrong: "`,
		"wrong: nope":                       `Say what shows the finding is wrong after "wrong: "`,
		"wrong: ----------------":           `Say what shows the finding is wrong after "wrong: "`,
		"duplicate: fnd_nope":               "fnd_nope is not another open finding",
		"duplicate: " + f:                   "is not another open finding",
	} {
		w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":%q}`, f, reason), want)
	}
	if s := w.ids(`SELECT status::text FROM review_findings WHERE id = $1`, f); len(s) != 1 || s[0] != "open" {
		t.Fatalf("refused dismissals left the finding %v", s)
	}

	// T2: no fix-or-not question after a review-only round.
	w.refused(wi, "decide", `{"action":"ask_person","note":"Stop here, or fix it?"}`, `"ask_person" is not a decision`)

	// T1: wrong, with its evidence, is accepted and recorded.
	w.must(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"wrong: FACTORY.md records it at line 3"}`, f))
	if g := w.dismissedWith(wi, "evidence", "FACTORY.md records it at line 3") + w.dismissedWith(wi, "kind", "wrong"); g != 2 {
		t.Fatalf("%d/2 grounds of the wrong dismissal recorded", g)
	}

	// T1: a duplicate names another open finding, so a set of duplicates
	// always leaves one open: not the dismissed one, nor round a cycle.
	open := w.openFindings(wi, 2)
	a, b := open[0], open[1]
	w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"duplicate: %s"}`, a, f), "is not another open finding")
	w.must(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"Duplicate: %s."}`, a, b))
	if g := w.dismissedWith(wi, "duplicateOf", b) + w.dismissedWith(wi, "kind", "duplicate"); g != 2 {
		t.Fatalf("%d/2 grounds of the duplicate dismissal recorded", g)
	}
	w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"duplicate: %s"}`, b, a), "is not another open finding")

	// T2: next posts the verdict: open findings at the fix bar request
	// changes (T1's outcome is never APPROVE), and the review is complete.
	w.must(wi, "decide", `{"action":"next"}`)
	w.agentsDone(wi, "dude's review", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	if r := w.dudeReviews(wi, n)[0]; r.State != "CHANGES_REQUESTED" || strings.Contains(r.Body, "could not complete") {
		t.Fatalf("the verdict: %s %q", r.State, r.Body)
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writers", c)
	}
}

// T3 in a later round: the author pushes a path a conditional required
// lens matches (database on migrations/**). The conductor's directed round
// at the new head still runs it: the round's lenses are the verdict gate's,
// read from the live compare, not the paths at adoption, and the verdict
// is not "could not complete".
func TestAReviewOnlyRoundAfterAPushRunsTheLensThePushRequires(t *testing.T) {
	w := conducting(t)
	w.gh.SetChecks("success")
	w.cleanLenses()
	w.requireReviewers(`{"requiredReviewers":["correctness"],
		"conditionalReviewers":[{"category":"database","whenPathsMatch":["migrations/**"]}]}`)
	wi, n := w.adoptedConducted(delivery.ModeReviewOnly)
	w.must(wi, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.untilDecision(wi, delivery.PointReviewed)
	w.must(wi, "decide", `{"action":"next"}`)
	w.agentsDone(wi, "the first verdict", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	if c := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND category = 'database'`, wi); c != 0 {
		t.Fatalf("%d database lenses before any migration", c)
	}

	head := w.push("migrations/001_greetings.sql")
	w.must(wi, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.untilDecision(wi, delivery.PointReviewed)
	if c := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND category = 'database'
		AND base_refs->>'target' = $2`, wi, head); c != 1 {
		t.Fatalf("%d database lenses at %s, the push's migration requires one (%v)", c, head,
			w.ids(`SELECT category FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at`, wi))
	}
	w.must(wi, "decide", `{"action":"next"}`)
	w.agentsDone(wi, "the verdict at the push", func() bool {
		return len(reviewsAt(w.dudeReviews(wi, n), head)) == 1 && w.waitingOnPR(wi)
	})
	if r := reviewsAt(w.dudeReviews(wi, n), head)[0]; r.State != "APPROVED" || strings.Contains(r.Body, "could not complete") {
		t.Fatalf("the verdict at the push: %s %q", r.State, r.Body)
	}
}

// The separation: every guard above is review-only's. A conducted writing
// task's conductor, new work or a pull request adopted to review and fix
// or to continue, is as before after its directed round (keepsFreedoms),
// and a top-level reply posts.
func TestAWritingDeliveryKeepsItsConductorsFreedoms(t *testing.T) {
	for _, mode := range []string{"new", delivery.ModeReview, delivery.ModeContinue} {
		t.Run(mode, func(t *testing.T) {
			w := conducting(t)
			w.requireReviewers(`{"requiredReviewers":["correctness","tests","performance"]}`)
			var wi, replier, pr string
			if mode == "new" {
				wi = w.task()
				w.talk(wi)
				w.implemented(wi)
			} else {
				var n int
				wi, n = w.adoptedConducted(mode)
				replier, pr = wi, fmt.Sprint(n)
			}
			w.must(wi, "start_phase", `{"phase":"review","categories":["correctness"]}`)
			w.untilDecision(wi, delivery.PointReviewed)
			w.keepsFreedoms(wi)
			if mode == "new" {
				w.must(wi, "decide", `{"action":"ask_person","note":"Fix the rest, or stop?"}`)
				replier, pr = w.reviewing(), "1"
				if status, out := w.chat(replier, "Tell the reviewers where it stands."); status >= 300 {
					t.Fatalf("chat: %d %v", status, out)
				}
			}
			w.must(replier, "reply_on_pull_request", fmt.Sprintf(`{"pr":%q,"text":"Looking at it now."}`, pr))
		})
	}
}

// T5: red CI at a review-only task's head wakes its conductor, which may
// rerun the failed checks: neither the writer guard nor the pull request
// actions' review-only refusal stops it.
func TestAReviewOnlyConductorRerunsUnrelatedRedCI(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("run:failure")
	w.withTools()
	wi, n := w.reviewOnly()
	w.until("the decision about the failing checks", func() bool {
		w.sync()
		w.advanced()
		for _, id := range w.pendingPhaseRuns(wi) {
			w.finished(id)
		}
		if w.decisionAt(wi) == delivery.PointReviewed {
			w.must(wi, "decide", `{"action":"next"}`)
		}
		return w.decisionAt(wi) == delivery.PointPRFeedback
	})
	w.must(wi, "rerun_failed_checks", fmt.Sprintf(`{"pr":"%d"}`, n))
	if e := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'action' = 'rerun_failed_checks'`,
		wi, delivery.EvPullRequestAction); e == 0 {
		t.Fatal("no rerun recorded")
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writers", c)
	}
}
