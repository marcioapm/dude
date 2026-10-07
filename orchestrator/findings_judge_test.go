package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// storybookFinding is a blocking finding whose category is the reviewer's
// own word, not a reviewer flavour, as real reviewers write them.
const storybookFinding = "---\n" +
	"severity: blocking\n" +
	"category: storybook\n" +
	"file: FACTORY.md\n" +
	"line: 1\n" +
	"title: The stories fail to load the MSW data\n" +
	"description: The decorator never sets the data the stories fetch.\n"

// The end-to-end run's stuck finding: a correctness reviewer reports a
// finding labelled storybook, the fixer fixes it, and the next round's
// reviewer, shown it, judges it fixed. It is resolved, never counts toward
// stuck, and the delivery reaches its pull request. The finding is stored
// under the reporting Run's category.
func TestAFindingInAReviewersOwnCategoryIsJudgedByTheNextRound(t *testing.T) {
	w := newWorld(t)
	// A real model, so the reviewer's spec carries the prompt it is shown.
	w.onModel("reviewer", "llm-review")
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		workload, _ := spec["workload"].(map[string]any)
		prompt := fmt.Sprint(workload["prompt"])
		switch labels["dude.phase"] {
		case "review":
			reviews++
			if reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + storybookFinding + "```\n"}
			}
			if strings.Contains(prompt, "The stories fail to load the MSW data") {
				return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
			}
			return fakelux.Behaviour{Reply: "Nothing new."}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Nothing to simplify."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request, or a stop", func() bool {
		return len(w.gh.Pulls()) == 1 || w.taskStatus(wi) == "awaiting_input"
	})
	if s := w.escalationReason(wi); s != "" {
		t.Fatalf("the delivery stopped for a person: %s\n%s", s, w.describeFindings(wi))
	}
	var category, status string
	var attempts int
	if err := w.owner.QueryRow(context.Background(), `SELECT category, status::text, fix_attempts FROM review_findings
		WHERE task_id = $1`, wi).Scan(&category, &status, &attempts); err != nil {
		t.Fatal(err)
	}
	if category != "correctness" || status != "resolved" || attempts != 1 {
		t.Errorf("the finding is %s, %s after %d fixes; want correctness, resolved after 1", category, status, attempts)
	}
	var topic string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(topic, '') FROM review_findings WHERE task_id = $1`, wi).Scan(&topic)
	if topic != "storybook" {
		t.Errorf("the reviewer's own word is kept as %q, want storybook", topic)
	}
}

// describeFindings lists the task's findings and its review Runs, for a
// failure's message.
func (w *world) describeFindings(task string) string {
	rows, _ := w.owner.Query(context.Background(), `SELECT 'finding ' || category || ' ' || COALESCE(topic, '-') || ' ' ||
			status::text || ' attempts=' || fix_attempts FROM review_findings WHERE task_id = $1
		UNION ALL SELECT 'review ' || COALESCE(category, '') || ' shown=' || finding_ids::text FROM runs
		WHERE task_id = $1 AND phase = 'review'`, task)
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		b.WriteString(s + "\n")
	}
	return b.String()
}

// 082 backfills a finding's category from the review Run that reported it,
// keeping the reviewer's own word as its topic; one already its Run's, or
// with no Run, is left as it was.
func TestTheFindingCategoryBackfill(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "082")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_u', 'U', 'u')`)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_u', 'org_u', 'P', 'p', 'P')`)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_u', 'org_u', 'prj_u', 1, 'T')`)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category)
		VALUES ('run_c', 'org_u', 'prj_u', 'wi_u', 1, 'completed', 'review', 'reviewer', 'correctness'),
		       ('run_f', 'org_u', 'prj_u', 'wi_u', 1, 'completed', 'review', 'reviewer', 'frontend')`)
	mustExec(t, owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title)
		VALUES ('f_story', 'org_u', 'wi_u', 'run_c', 'storybook', 'high', 'a'),
		       ('f_state', 'org_u', 'wi_u', 'run_f', 'state-consistency', 'high', 'b'),
		       ('f_same', 'org_u', 'wi_u', 'run_c', 'correctness', 'high', 'c'),
		       ('f_none', 'org_u', 'wi_u', NULL, 'accessibility', 'high', 'd')`)
	apply()
	want := map[string][2]string{
		"f_story": {"correctness", "storybook"},
		"f_state": {"frontend", "state-consistency"},
		"f_same":  {"correctness", ""},
		"f_none":  {"accessibility", ""},
	}
	for id, w := range want {
		var category, topic string
		if err := owner.QueryRow(context.Background(), `SELECT category, COALESCE(topic, '') FROM review_findings WHERE id = $1`, id).
			Scan(&category, &topic); err != nil {
			t.Fatal(err)
		}
		if category != w[0] || topic != w[1] {
			t.Errorf("%s: %s / %q, want %s / %q", id, category, topic, w[0], w[1])
		}
	}
}

// A round the conductor scopes to one reviewer still judges every finding a
// fix attempted, whichever reviewer raised it: frontend's goes to the
// security reviewer the round has.
func TestAScopedRoundJudgesEveryAttemptedFinding(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	var a, run string
	_ = w.owner.QueryRow(context.Background(), `SELECT id, run_id FROM review_findings WHERE task_id = $1`, task).Scan(&a, &run)
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title)
		SELECT 'fnd_fe_'||$1, organization_id, task_id, run_id, 'frontend', 'high', 'Frontend one'
		FROM review_findings WHERE id = $2`, task, a)
	w.must(task, "start_phase", `{"phase":"fix"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "start_phase", `{"phase":"review","categories":["security"]}`)
	w.until("the scoped review", func() bool { return w.phaseRuns(task, "review") == 2 })
	var shown []string
	_ = w.owner.QueryRow(context.Background(), `SELECT finding_ids FROM runs WHERE task_id = $1 AND phase = 'review'
		AND category = 'security'`, task).Scan(&shown)
	if !slices.Contains(shown, a) || !slices.Contains(shown, "fnd_fe_"+task) {
		t.Errorf("the security reviewer was shown %v, want %s and fnd_fe_%s", shown, a, task)
	}
}

// A finding of a reviewer the round lacks goes to the policy's required
// reviewer the round has, not to the round's first: frontend's, in a round
// of security then correctness (correctness required), is correctness's,
// and its reviewer judges it.
func TestARoundsRequiredReviewerJudgesAFindingWithoutItsOwn(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	// Re-reviews judge every finding they are shown fixed (the scripted
	// reviewer names only F1).
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "review" && fmt.Sprint(labels["dude.run"]) != w.firstReview() {
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n  F2: fixed\n```\n"}
		}
		return scripted(spec)
	}
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	var a string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM review_findings WHERE task_id = $1`, task).Scan(&a)
	fe := "fnd_fe_" + task
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title)
		SELECT $1, organization_id, task_id, run_id, 'frontend', 'high', 'Frontend one' FROM review_findings WHERE id = $2`, fe, a)
	w.must(task, "start_phase", `{"phase":"fix"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	if n := w.count(`SELECT count(*) FROM review_findings WHERE id = $1 AND fix_attempts > 0`, fe); n != 1 {
		t.Fatalf("the fix did not attempt the frontend finding:\n%s", w.describeFindings(task))
	}
	w.must(task, "start_phase", `{"phase":"review","categories":["security","correctness"]}`)
	w.until("the round's reviews", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	shownTo := func(category string) []string {
		var shown []string
		_ = w.owner.QueryRow(context.Background(), `SELECT finding_ids FROM runs WHERE task_id = $1 AND phase = 'review'
			AND category = $2 ORDER BY created_at DESC LIMIT 1`, task, category).Scan(&shown)
		return shown
	}
	if got := shownTo("correctness"); !slices.Contains(got, fe) {
		t.Errorf("the correctness reviewer was shown %v, want %s\n%s", got, fe, w.describeFindings(task))
	}
	if got := shownTo("security"); slices.Contains(got, fe) {
		t.Errorf("the round's first reviewer, security, was shown %s", fe)
	}
	// Judged by correctness's Run: its verdict resolved the finding.
	if n := w.count(`SELECT count(*) FROM review_findings f WHERE f.id = $1 AND f.status = 'resolved'`, fe); n != 1 {
		t.Errorf("the frontend finding was not judged resolved:\n%s", w.describeFindings(task))
	}
}
