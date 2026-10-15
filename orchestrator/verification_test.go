package orchestrator_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// testing starts a delivery with the tester on, played by model, on a
// task of two criteria.
func (w *world) testing(model string) string {
	w.t.Helper()
	w.onModel("qa_browser", model)
	wi := w.task()
	mustExec(w.t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(context.Background(), tx, wi)
	}); err != nil {
		w.t.Fatal(err)
	}
	p := scriptedPolicy()
	p.Test = true
	w.start(wi, p)
	return wi
}

// verdicts is the task's latest test's verdict, criterion by criterion:
// status, and a person's decision after a slash.
func (w *world) verdicts(wi string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT status || COALESCE('/' || decision, '') FROM acceptance_verdicts
		WHERE run_id = (SELECT run_id FROM acceptance_verdicts WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1)
		ORDER BY criterion`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// decide is a person's decision on the task's escalation, from the banner.
func (w *world) decide(as, wi, action, note string) (int, map[string]any) {
	w.t.Helper()
	return w.callAs(as, "/internal/tasks/"+wi+"/decide", map[string]any{"action": action, "note": note})
}

// people makes the task's owner, an admin and a member, by key.
func (w *world) people(wi string) (owner, admin, member string) {
	owner, admin, member = w.person("Ana"), w.person("Cy"), w.person("Bo")
	mustExec(w.t, w.owner, `UPDATE people SET role = CASE WHEN name = 'Cy' THEN 'admin' ELSE 'member' END
		WHERE organization_id = $1`, w.org)
	w.assignOwner(wi, owner)
	return
}

func TestATestThatPassesEveryCriterionWithEvidenceOpensThePullRequest(t *testing.T) {
	w := newWorld(t)
	wi := w.testing("fake/scripted")
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	if got := w.verdicts(wi); strings.Join(got, ",") != "pass,pass" {
		t.Fatalf("verdicts = %v, want both passed", got)
	}
	// Bound to what was tested: the commit the tester checked out, which
	// is the branch's head the pull request opened at.
	var heads, evidence string
	_ = w.owner.QueryRow(context.Background(), `SELECT v.heads->>'target', array_to_string(v.evidence, ',')
		FROM acceptance_verdicts v JOIN runs r ON r.id = v.run_id AND r.phase = 'test'
		WHERE v.task_id = $1 AND v.criterion = 1`, wi).Scan(&heads, &evidence)
	pr := w.gh.Pull(1)
	if head := w.branchHead(wi); heads == "" || heads != head {
		t.Errorf("tested at %q, the pull request is at %q", heads, head)
	}
	if evidence != fakeagent.Evidence {
		t.Errorf("evidence = %q", evidence)
	}
	for _, want := range []string{"## Verification", "Tested at `" + heads[:7] + "`.", "- **pass** — it greets",
		"- **pass** — it says goodbye"} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("the pull request's body has no %q:\n%s", want, pr.Body)
		}
	}
	if strings.Contains(pr.Body, "Stale") {
		t.Errorf("a verdict at the pull request's head is not stale:\n%s", pr.Body)
	}
}

func TestAFailedCriterionGoesToTheFixerAndTheTesterJudgesItsFinding(t *testing.T) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestFailOnceModel)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	// review ⟲ fix, simplify, test (fails) → fix → review → simplify → test.
	if n := w.phaseRuns(wi, "test"); n != 2 {
		t.Errorf("%d tests, want 2", n)
	}
	if n := w.phaseRuns(wi, "fix"); n != 2 {
		t.Errorf("%d fixes, want the review's and the test's", n)
	}
	if n := w.phaseRuns(wi, "review"); n != 3 {
		t.Errorf("%d reviews, want the fix after the test reviewed too", n)
	}
	if n := w.phaseRuns(wi, "simplify"); n != 2 {
		t.Errorf("%d simplifications, want one after each review loop", n)
	}
	// The tester's finding went to the fixer, and the second test — not a
	// reviewer — judged it fixed.
	var status, resolvedBy, phase string
	var attempts int
	_ = w.owner.QueryRow(context.Background(), `SELECT f.status::text, COALESCE(f.resolved_by_run_id, ''),
			COALESCE((SELECT phase::text FROM runs WHERE id = f.resolved_by_run_id), ''), f.fix_attempts
		FROM review_findings f JOIN runs r ON r.id = f.run_id AND r.phase = 'test'
		WHERE f.task_id = $1 AND f.title = 'Criterion 1 is not met'`, wi).Scan(&status, &resolvedBy, &phase, &attempts)
	if status != "resolved" || phase != "test" || attempts != 1 {
		t.Errorf("the tester's finding: %s by a %s Run after %d fixes, want resolved by the next test after 1", status, phase, attempts)
	}
	var reviewerShown int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM runs r, review_findings f
		WHERE r.task_id = $1 AND r.phase = 'review' AND f.id = ANY(r.finding_ids) AND f.title = 'Criterion 1 is not met'`, wi).Scan(&reviewerShown)
	if reviewerShown != 0 {
		t.Errorf("a reviewer was given the tester's finding to judge")
	}
	var shown int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM runs r, review_findings f
		WHERE r.task_id = $1 AND r.phase = 'test' AND f.id = ANY(r.finding_ids) AND f.title = 'Criterion 1 is not met'`, wi).Scan(&shown)
	if shown != 1 {
		t.Errorf("%d tests were shown the earlier finding to judge, want the second", shown)
	}
	if got := w.verdicts(wi); strings.Join(got, ",") != "pass,pass" {
		t.Errorf("verdicts = %v", got)
	}
}

// branchHead is the commit the task's branch is at on the fake GitHub.
func (w *world) branchHead(wi string) string {
	out, _ := exec.Command("git", "-C", w.gh.Repo, "rev-parse", "refs/heads/"+delivery.BranchFor(wi, 1)).Output()
	return strings.TrimSpace(string(out))
}

func TestACriterionThatKeepsFailingIsAPersonsToAccept(t *testing.T) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestFailModel)
	owner, _, _ := w.people(wi)
	w.until("the gate to stop", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != delivery.ReasonTestFailedCriteria {
		t.Fatalf("escalated %q, want %s\n%s", r, delivery.ReasonTestFailedCriteria, w.describeRuns())
	}
	if n := w.phaseRuns(wi, "test"); n != 3 {
		t.Errorf("%d tests, want 3: the first and one after each of 2 fix rounds", n)
	}
	// One more fix round, sent the tester's finding again; failing still,
	// it is back with the person.
	var failing string
	_ = w.owner.QueryRow(context.Background(), `SELECT f.id FROM review_findings f JOIN runs r ON r.id = f.run_id AND r.phase = 'test'
		WHERE f.task_id = $1 AND f.title = 'Criterion 1 is not met'`, wi).Scan(&failing)
	fixes, escalated := w.phaseRuns(wi, "fix"), w.count(`SELECT count(*) FROM events WHERE task_id = $1
		AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, wi)
	if code, body := w.decide(owner, wi, "retry", ""); code != 200 {
		t.Fatalf("the owner's retry: %d %v", code, body)
	}
	w.until("the gate to stop again", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
			AND payload->>'kind' = 'escalation'`, wi) == escalated+1 && w.taskStatus(wi) == "awaiting_input"
	})
	if n := w.phaseRuns(wi, "fix"); n != fixes+1 {
		t.Errorf("%d fixes after the retry, want one more", n-fixes)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND $2 = ANY(finding_ids)
		AND created_at = (SELECT max(created_at) FROM runs WHERE task_id = $1 AND phase = 'fix')`, wi, failing); n != 1 {
		t.Errorf("the retry's fix was not given the tester's finding %s\n%s", failing, w.describeRuns())
	}
	if r := w.escalationReason(wi); r != delivery.ReasonTestFailedCriteria {
		t.Fatalf("escalated %q after the retry, want %s", r, delivery.ReasonTestFailedCriteria)
	}
	if n := w.count(`SELECT (payload->'detail'->>'rounds')::int FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'kind' = 'escalation' ORDER BY cursor DESC LIMIT 1`, wi); n != delivery.MaxTestFixRounds+1 {
		t.Errorf("escalated after %d test fix rounds, want the retry's counted: %d", n, delivery.MaxTestFixRounds+1)
	}
	if code, body := w.decide(owner, wi, "accept", "  "); code != 400 || !strings.Contains(errMessage(body), "needs a note") {
		t.Fatalf("accept without a note: %d %v", code, body)
	}
	if code, body := w.decide(owner, wi, "accept", "Goodbye ships next week."); code != 200 {
		t.Fatalf("the owner's accept: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.verdicts(wi); strings.Join(got, ",") != "fail/accepted,pass" {
		t.Errorf("verdicts = %v, want the failure accepted", got)
	}
	var by, note string
	_ = w.owner.QueryRow(context.Background(), `SELECT p.name, v.decision_note FROM acceptance_verdicts v JOIN people p ON p.id = v.decided_by
		WHERE v.task_id = $1 AND v.decision = 'accepted'`, wi).Scan(&by, &note)
	if by != "Ana" || note != "Goodbye ships next week." {
		t.Errorf("accepted by %q: %q", by, note)
	}
	if body := w.gh.Pull(1).Body; !strings.Contains(body, "- **fail, accepted by Ana: Goodbye ships next week.** — it greets") {
		t.Errorf("the pull request does not say who accepted what:\n%s", body)
	}
}

func errMessage(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	m, _ := e["message"].(string)
	return m
}

func TestATesterThatCannotRunTheAppIsBlockedNotFixed(t *testing.T) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestBlockedModel)
	owner, admin, member := w.people(wi)
	w.until("the gate to stop", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != delivery.ReasonTestBlocked {
		t.Fatalf("escalated %q, want %s", r, delivery.ReasonTestBlocked)
	}
	fixes := w.phaseRuns(wi, "fix")
	if fixes != 1 {
		t.Errorf("%d fixes, want only the review loop's: a blocked test is no defect", fixes)
	}

	// Try again: the tester runs again on the same heads.
	if code, body := w.decide(owner, wi, "retry", "Start it with make dev."); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	w.until("a second test to stop", func() bool {
		return w.phaseRuns(wi, "test") == 2 && w.taskStatus(wi) == "awaiting_input"
	})
	var bases int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(DISTINCT base_refs::text) FROM runs WHERE task_id = $1 AND phase = 'test'`, wi).Scan(&bases)
	if bases != 1 || w.phaseRuns(wi, "fix") != fixes {
		t.Errorf("the retry tested %d heads and made %d fixes", bases, w.phaseRuns(wi, "fix")-fixes)
	}
	// The first test's environment finding is the second's to say again.
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'open' AND title LIKE 'Test environment:%'`, wi); n != 1 {
		t.Errorf("%d open environment findings, want the latest test's", n)
	}

	if code, body := w.decide(member, wi, "waive", "It works on my machine."); code != 403 {
		t.Fatalf("a member's waiver: %d %v", code, body)
	}
	if code, body := w.decide(admin, wi, "waive", ""); code != 400 {
		t.Fatalf("a waiver without a reason: %d %v", code, body)
	}
	if code, body := w.decide(member, wi, "retry", ""); code != 403 {
		t.Fatalf("a member's retry, still the owner's: %d %v", code, body)
	}
	// An admin's say is the waiver alone.
	for _, action := range []string{"retry", "stop"} {
		if code, body := w.decide(admin, wi, action, "As admin."); code != 403 {
			t.Fatalf("an admin's %s, still the owner's: %d %v", action, code, body)
		}
	}
	// With its owner gone the task is nobody's, and a waiver still an
	// admin's only.
	mustExec(t, w.owner, `UPDATE people SET removed_at = now() WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, owner)
	if code, body := w.decide(member, wi, "waive", "Nobody owns it."); code != 403 {
		t.Fatalf("a member's waiver on an ownerless task: %d %v", code, body)
	}
	if code, body := w.decide(admin, wi, "waive", "Checked by hand on staging."); code != 200 {
		t.Fatalf("an admin's waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.verdicts(wi); strings.Join(got, ",") != "blocked/waived,blocked/waived" {
		t.Errorf("verdicts = %v", got)
	}
	if body := w.gh.Pull(1).Body; !strings.Contains(body, "blocked, waived by Cy: Checked by hand on staging.") {
		t.Errorf("the pull request does not carry the waiver:\n%s", body)
	}
}

func TestAPassCitingAFileNobodyPublishedIsUnverified(t *testing.T) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestUnverifiedModel)
	owner, _, _ := w.people(wi)
	w.until("the gate to stop", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != delivery.ReasonTestBlocked {
		t.Fatalf("escalated %q, want %s", r, delivery.ReasonTestBlocked)
	}
	if got := w.verdicts(wi); strings.Join(got, ",") != "unverified,unverified" {
		t.Errorf("verdicts = %v", got)
	}
	if len(w.gh.Pulls()) != 0 || w.phaseRuns(wi, "fix") != 1 {
		t.Errorf("an unverified pass opened a pull request or sent a fix")
	}
	if code, body := w.decide(owner, wi, "waive", "Seen by hand."); code != 200 {
		t.Fatalf("the owner's waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.verdicts(wi); strings.Join(got, ",") != "unverified/waived,unverified/waived" {
		t.Errorf("verdicts = %v, want each unverified pass waived", got)
	}
}

func TestATestWithNoVerdictIsBlocked(t *testing.T) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestSilentModel)
	w.until("the gate to stop", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != delivery.ReasonTestBlocked {
		t.Fatalf("escalated %q, want %s", r, delivery.ReasonTestBlocked)
	}
	if got := w.verdicts(wi); strings.Join(got, ",") != "blocked,blocked" {
		t.Errorf("verdicts = %v, want every criterion blocked", got)
	}
}

// A test that could not run the app has not judged what an earlier test
// found: that finding, still open, is a check to make, not a fix to send.
// So is one that judged no criterion at all.
func TestABlockedRetestOfAnOpenFailureIsBlockedNotFixed(t *testing.T) {
	for _, retest := range []string{fakeagent.TestBlockedModel, fakeagent.TestSilentModel} {
		t.Run(retest, func(t *testing.T) { blockedRetest(t, retest) })
	}
}

func blockedRetest(t *testing.T, retest string) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestFailModel)
	// The first test failed and its fix round began: the next test cannot
	// start the app, or judges nothing.
	w.until("the test's fix round", func() bool { return w.phaseRuns(wi, "fix") == 2 })
	w.onModel("qa_browser", retest)
	w.until("the gate to stop", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != delivery.ReasonTestBlocked {
		t.Fatalf("escalated %q, want %s\n%s", r, delivery.ReasonTestBlocked, w.describeRuns())
	}
	if n := w.phaseRuns(wi, "test"); n != 2 {
		t.Errorf("%d tests, want 2", n)
	}
	if n := w.phaseRuns(wi, "fix"); n != 2 {
		t.Errorf("%d fixes, want the review's and the first test's: the blocked test sent none", n)
	}
	if got := w.verdicts(wi); strings.Join(got, ",") != "blocked,blocked" {
		t.Errorf("verdicts = %v", got)
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'open' AND title = 'Criterion 1 is not met'`, wi); n != 1 {
		t.Errorf("the first test's finding is not left open for a test that can check it")
	}
}

// A criterion the tester failed without saying why gets dude's own
// finding: the fixer is given it, and the passing re-test resolves it.
func TestAFailureTheTesterGaveNoFindingForStillReachesTheFixer(t *testing.T) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestFailQuietModel)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	var id, status string
	_ = w.owner.QueryRow(context.Background(), `SELECT f.id, f.status::text FROM review_findings f
		JOIN runs r ON r.id = f.run_id AND r.phase = 'test'
		WHERE f.task_id = $1 AND f.title = 'Acceptance criterion 1 fails: it greets'`, wi).Scan(&id, &status)
	if id == "" {
		t.Fatalf("no finding for the failed criterion\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND $2 = ANY(finding_ids)`, wi, id); n != 1 {
		t.Errorf("%d fixes were given the failed criterion's finding, want 1", n)
	}
	if status != "resolved" {
		t.Errorf("the failed criterion's finding is %s after the passing test, want resolved", status)
	}
	if got := w.verdicts(wi); strings.Join(got, ",") != "pass,pass" {
		t.Errorf("verdicts = %v", got)
	}
}
