package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// reviewOnlyConducted starts a review-only task on alice's pull request
// with the conductor's tools served and the lenses filing the scripted
// finding, talked through: parked at its start; task and number.
func (w *world) reviewOnlyConducted() (string, int) {
	w.t.Helper()
	w.withTools()
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	status, out := w.call("/internal/tasks/"+wi+"/talk", map[string]any{})
	if status != 201 {
		w.t.Fatalf("talk: %d %v", status, out)
	}
	w.until("its first decision", func() bool { return w.decisionAt(wi) != "" })
	w.t.Logf("first decision at %s", w.decisionAt(wi))
	return wi, n
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

// openFinding is one of the task's open findings.
func (w *world) openFinding(wi string) string {
	w.t.Helper()
	ids := w.ids(`SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open' ORDER BY created_at, id`, wi)
	if len(ids) == 0 {
		w.t.Fatal("no open finding")
	}
	return ids[0]
}

// T1-T4 on a review-only task: a dismissal states its grounds; after the
// round, ask_person is not offered and next posts dude's review; a
// directed round still runs every lens the verdict requires; the
// conductor posts no review of its own on the pull request.
func TestAReviewOnlyConductorReviewsItDoesNotTriageAway(t *testing.T) {
	w := conducting(t)
	w.gh.SetChecks("success")
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"requiredReviewers":["correctness","tests","performance"]}'
		WHERE id = $1`, w.project)
	wi, n := w.reviewOnlyConducted()
	// T3: the conductor directs correctness alone; the policy's other
	// required lenses run too.
	w.must(wi, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the round's lenses", func() bool {
		w.advanced()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	got := w.ids(`SELECT DISTINCT category FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY 1`, wi)
	for _, lens := range []string{"correctness", "performance", "tests"} {
		if !strings.Contains(strings.Join(got, ","), lens) {
			t.Fatalf("the round ran %v: %s was required", got, lens)
		}
	}
	w.untilDecision(wi, delivery.PointReviewed)
	if a := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_awaited'
		AND payload->>'point' = $2 AND payload->>'note' LIKE '%posts dude''s review%' AND payload->>'actions' = '["next"]'`,
		wi, delivery.PointReviewed); a != 1 {
		t.Fatalf("%d decisions after the round saying next posts dude's review, offering only next", a)
	}
	f := w.openFinding(wi)

	// T4: no top-level comment of the conductor's.
	w.refused(wi, "reply_on_pull_request", fmt.Sprintf(`{"pr":"%d","text":"My review: changes requested."}`, n),
		"dude's review and its status comment are the output")

	// T1: grounds, or refused.
	for _, reason := range []string{"Valid finding, but not fixed here", "no fixer runs on this task", "wrong:"} {
		w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":%q}`, f, reason), "wrong")
	}
	w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"duplicate: fnd_nope"}`, f), "fnd_nope is not another open finding")
	w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"duplicate: %s"}`, f, f), "is not another open finding")
	if s := w.ids(`SELECT status::text FROM review_findings WHERE id = $1`, f); len(s) != 1 || s[0] != "open" {
		t.Fatalf("refused dismissals left the finding %v", s)
	}

	// T2: no fix-or-not question after a review-only round.
	w.refused(wi, "decide", `{"action":"ask_person","note":"Stop here, or fix it?"}`, `"ask_person" is not a decision`)

	// T1: wrong, with its evidence, is accepted and recorded.
	w.must(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"wrong: FACTORY.md records it at line 3"}`, f))
	if g := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->'grounds'->>'kind' = 'wrong' AND payload->'grounds'->>'evidence' = 'FACTORY.md records it at line 3'`,
		wi, delivery.EvFindingResolved); g != 1 {
		t.Fatalf("%d dismissals recorded with their grounds", g)
	}

	// T1: a duplicate names another open finding, so a set of duplicates
	// always leaves one open and counted: not the dismissed one, and not
	// back round a cycle.
	open := w.ids(`SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open' ORDER BY created_at, id`, wi)
	if len(open) < 2 {
		t.Fatalf("open findings %v, want two to duplicate", open)
	}
	a, b := open[0], open[1]
	w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"duplicate: %s"}`, a, f), "is not another open finding")
	w.must(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"Duplicate: %s."}`, a, b))
	if g := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->'grounds'->>'kind' = 'duplicate' AND payload->'grounds'->>'duplicateOf' = $3`,
		wi, delivery.EvFindingResolved, b); g != 1 {
		t.Fatalf("%d duplicate dismissals recorded with their grounds", g)
	}
	w.refused(wi, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"duplicate: %s"}`, b, a), "is not another open finding")
	if s := w.ids(`SELECT status::text FROM review_findings WHERE id = $1`, b); len(s) != 1 || s[0] != "open" {
		t.Fatalf("a duplicate of a dismissed finding left it %v", s)
	}

	// T2: next posts the verdict: the review is complete (not row 2).
	w.must(wi, "decide", `{"action":"next"}`)
	w.agentsDone(wi, "dude's review", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	r := w.dudeReviews(wi, n)[0]
	if strings.Contains(r.Body, "could not complete") || r.State == "APPROVED" && w.count(`SELECT count(*) FROM review_findings
		WHERE task_id = $1 AND status = 'open'`, wi) > 0 {
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
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"requiredReviewers":["correctness"],
		"conditionalReviewers":[{"category":"database","whenPathsMatch":["migrations/**"]}]}' WHERE id = $1`, w.project)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	if status, out := w.call("/internal/tasks/"+wi+"/talk", map[string]any{}); status != 201 {
		t.Fatalf("talk: %d %v", status, out)
	}
	w.until("its first decision", func() bool { return w.decisionAt(wi) != "" })
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
// task's conductor, at the same points, is as before: a free-text
// dismissal dismisses, ask_person is offered after a review round, its
// directed lenses alone run, and a top-level reply posts.
func TestAWritingDeliveryKeepsItsConductorsFreedoms(t *testing.T) {
	w := conducting(t)
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"requiredReviewers":["correctness","tests","performance"]}'
		WHERE id = $1`, w.project)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	if got := w.ids(`SELECT DISTINCT category FROM runs WHERE task_id = $1 AND phase = 'review'`, task); len(got) != 1 || got[0] != "correctness" {
		t.Fatalf("a writing task's directed round ran %v, want its correctness alone", got)
	}
	if a := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_awaited'
		AND payload->>'point' = $2 AND payload->>'actions' = '["next", "ask_person"]' AND payload->>'note' NOT LIKE '%posts dude''s review%'`,
		task, delivery.PointReviewed); a != 1 {
		t.Fatalf("%d decisions after a writing task's round offering next and ask_person, as before", a)
	}
	f := w.openFinding(task)
	w.must(task, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"Valid finding, but not fixed here"}`, f))
	if g := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'grounds'`,
		task, delivery.EvFindingResolved); g != 0 {
		t.Fatalf("a writing task's dismissal recorded grounds")
	}
	w.must(task, "decide", `{"action":"ask_person","note":"Fix the rest, or stop?"}`)

	reviewing := w.reviewing()
	if status, out := w.chat(reviewing, "Tell the reviewers where it stands."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.must(reviewing, "reply_on_pull_request", `{"pr":"1","text":"Pushed the fix; CI is running."}`)
}

// T5: red CI at a review-only task's head wakes its conductor, which may
// rerun the failed checks: neither the writer guard nor the pull request
// actions' review-only refusal stops it.
func TestAReviewOnlyConductorRerunsUnrelatedRedCI(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("run:failure")
	w.withTools()
	w.cleanLenses()
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.startDelivery(wi)
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
