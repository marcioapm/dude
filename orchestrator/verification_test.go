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
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
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
	_, admin, member := w.people(wi)
	w.until("the gate to stop", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != delivery.ReasonTestBlocked {
		t.Fatalf("escalated %q, want %s", r, delivery.ReasonTestBlocked)
	}
	fixes := w.phaseRuns(wi, "fix")
	if fixes != 1 {
		t.Errorf("%d fixes, want only the review loop's: a blocked test is no defect", fixes)
	}

	// Try again: the tester runs again on the same heads.
	if code, body := w.decide(w.ownerOf(wi), wi, "retry", "Start it with make dev."); code != 200 {
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

// ownerOf is the task's owner's key.
func (w *world) ownerOf(wi string) string {
	var key string
	_ = w.owner.QueryRow(context.Background(), `SELECT k.id FROM task_people tp JOIN api_keys k ON k.person_id = tp.person_id
		WHERE tp.task_id = $1 ORDER BY tp.position LIMIT 1`, wi).Scan(&key)
	return key
}

func TestAPassCitingAFileNobodyPublishedIsUnverified(t *testing.T) {
	w := newWorld(t)
	wi := w.testing(fakeagent.TestUnverifiedModel)
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
