package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// A re-review's verdicts are kept on its Run, a still as much as a fixed:
// the finding is judged still after the first fix and fixed after the
// second, and each review Run reads back what it said of it. The finding
// keeps the criterion and caused_by_fix it was raised with, as do two
// low ones beside it: one saying caused_by_fix no, one not saying.
func TestEachReviewRunKeepsItsVerdicts(t *testing.T) {
	w := newWorld(t)
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			switch reviews++; reviews {
			case 1:
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + "caused_by_fix: yes\n" +
					"---\nseverity: low\ncategory: correctness\ntitle: Said not caused by the fix\ndescription: D.\ncaused_by_fix: no\n" +
					"---\nseverity: low\ncategory: correctness\ntitle: Silent on the fix\ndescription: D.\n```\n"}
			case 2:
				return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
			case 3:
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
		t.Fatalf("the delivery stopped for a person: %s", s)
	}
	const title = "FACTORY.md does not record the fix"
	var finding, acceptance string
	if err := w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(acceptance, '') FROM review_findings
		WHERE task_id = $1 AND title = $2`, wi, title).Scan(&finding, &acceptance); err != nil {
		t.Fatal(err)
	}
	if acceptance != fakeagent.FindingAcceptance {
		t.Errorf("the finding was stored with acceptance %q, want %q", acceptance, fakeagent.FindingAcceptance)
	}
	causedRows, err := w.owner.Query(context.Background(), `SELECT title, caused_by_fix FROM review_findings WHERE task_id = $1`, wi)
	if err != nil {
		t.Fatal(err)
	}
	type causedRow struct {
		Title  string
		Caused bool
	}
	causedList, err := pgx.CollectRows(causedRows, pgx.RowToStructByPos[causedRow])
	if err != nil {
		t.Fatal(err)
	}
	caused := map[string]bool{}
	for _, r := range causedList {
		caused[r.Title] = r.Caused
	}
	wantCaused := map[string]bool{title: true, "Said not caused by the fix": false, "Silent on the fix": false}
	if fmt.Sprint(caused) != fmt.Sprint(wantCaused) {
		t.Errorf("caused_by_fix by title = %v, want %v", caused, wantCaused)
	}
	rows, err := w.owner.Query(context.Background(), `SELECT verdicts FROM runs WHERE task_id = $1 AND phase = 'review'
		ORDER BY created_at, id`, wi)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[map[string]string])
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{{}, {finding: "still"}, {finding: "fixed"}}
	if len(got) < len(want) {
		t.Fatalf("review Runs' verdicts = %v, want at least %v", got, want)
	}
	for i, v := range want {
		if fmt.Sprint(got[i]) != fmt.Sprint(v) {
			t.Errorf("review %d's verdicts = %v, want %v", i+1, got[i], v)
		}
	}
}

// A review Run reporting again (a retried report) keeps only its latest
// verdicts: one dropped from the report is gone, and a report with none
// leaves an empty map, never NULL.
func TestAReportedAgainReviewKeepsOnlyItsLatestVerdicts(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	ctx := context.Background()
	var run string
	if err := w.owner.QueryRow(ctx, `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review'
		ORDER BY created_at, id LIMIT 1`, wi).Scan(&run); err != nil {
		t.Fatal(err)
	}
	for _, report := range []struct {
		verdicts map[string]bool
		want     string
	}{
		{map[string]bool{"a": false, "b": false}, `{"a": "still", "b": "still"}`},
		{map[string]bool{"b": false}, `{"b": "still"}`},
		{nil, `{}`},
	} {
		if err := phases.RecordFindings(ctx, w.app, w.org, run, nil, report.verdicts); err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := w.owner.QueryRow(ctx, `SELECT COALESCE(verdicts::text, 'NULL') FROM runs WHERE id = $1`, run).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != report.want {
			t.Errorf("after reporting %v the Run's verdicts are %s, want %s", report.verdicts, stored, report.want)
		}
	}
}

// A reviewer's finding without an acceptance criterion: it is stored with
// none (NULL), the fixer is given no criterion for it, and the delivery
// completes as it does with one.
func TestAFindingWithoutACriterionIsFixedAsBefore(t *testing.T) {
	w := newWorld(t)
	// The scripted agent throughout, its reviewer's finding stated without
	// the acceptance line. A new fake model would need a migration (the
	// model_tiers_model_check list); the reply is all that differs.
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		b.Reply = strings.Replace(b.Reply, "acceptance: "+fakeagent.FindingAcceptance+"\n", "", 1)
		return b
	}
	w.onModel("fixer", "llm-fix")
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request, or a stop", func() bool {
		return len(w.gh.Pulls()) == 1 || w.taskStatus(wi) == "awaiting_input"
	})
	if s := w.escalationReason(wi); s != "" {
		t.Fatalf("the delivery stopped for a person: %s", s)
	}
	rows, err := w.owner.Query(context.Background(), `SELECT COALESCE(quote_literal(acceptance), 'NULL') FROM review_findings
		WHERE task_id = $1`, wi)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0] != "NULL" {
		t.Errorf("findings' acceptance = %v, want one NULL", stored)
	}
	fixes := w.luxRunsOf("fix")
	if len(fixes) != 1 {
		t.Fatalf("%d fixers, want one", len(fixes))
	}
	prompt := fixes[0].Prompt()
	if !strings.Contains(prompt, "FACTORY.md does not record the fix") {
		t.Fatalf("the fixer was not given the finding:\n%s", prompt)
	}
	if strings.Contains(prompt, "Done when:") || strings.Contains(prompt, fakeagent.FindingAcceptance) {
		t.Errorf("the fixer was given a criterion for a finding without one:\n%s", prompt)
	}
}

// A finding's criterion reaches whoever acts on it next: the fixer, and
// the re-reviewer judging it, resumed (its round message) or fresh (its
// first message). The re-review, whose parent is the fix, is told that
// fix's commits; the first review (parent: the implementer) and the
// simplifier's range review (parent: the simplifier) are told none.
func TestTheCriterionAndTheLastFixReachTheNextRounds(t *testing.T) {
	for _, resume := range []bool{true, false} {
		t.Run(fmt.Sprintf("resume=%v", resume), func(t *testing.T) {
			world, policy := newWorld, scriptedPolicy()
			if resume {
				world, policy = resuming, resumingPolicy()
			}
			// Real models' names, so lux is given each prompt itself; the
			// scripted agent plays them all the same.
			w := world(t)
			w.onModel("reviewer", "llm-review")
			w.onModel("fixer", "llm-fix")
			wi := w.task()
			w.names(wi, w.repoID)
			w.start(wi, policy)
			w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

			fixes := w.luxRunsOf("fix")
			if len(fixes) != 1 {
				t.Fatalf("%d fixers, want one", len(fixes))
			}
			if !strings.Contains(fixes[0].Prompt(), fakeagent.FindingAcceptance) {
				t.Errorf("the fixer was not given the criterion:\n%s", fixes[0].Prompt())
			}
			// What the fix and the simplifier each published: base..head.
			published := func(phase string) string {
				var r string
				if err := w.owner.QueryRow(context.Background(), `SELECT h.value->>'base' || '..' || (h.value->>'sha')
					FROM runs r, jsonb_each(r.heads) h WHERE r.task_id = $1 AND r.phase = $2`, wi, phase).Scan(&r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			implementRange, fixRange, simplifyRange := published("implement"), published("fix"), published("simplify")
			if reviews := w.reviewRows(wi); len(reviews) != 3 || (reviews[1].How == delivery.ContinuesResume) != resume {
				t.Fatalf("review Runs %+v; want three, the re-review resumed: %v", reviews, resume)
			}

			// What each review round was given, in order: the first round,
			// the re-review after the fix, the simplifier's range review.
			var rounds []string
			for _, lr := range w.luxRunsOf("review") {
				rounds = append(rounds, lr.Prompt())
				rounds = append(rounds, w.lux.Inputs(lr.ID)...)
			}
			if len(rounds) != 3 {
				t.Fatalf("%d review rounds' messages, want 3: %q", len(rounds), rounds)
			}
			if strings.Contains(rounds[0], implementRange) {
				t.Errorf("the first review, after the implementer, is told a last fix:\n%s", rounds[0])
			}
			if !strings.Contains(rounds[1], fakeagent.FindingAcceptance) {
				t.Errorf("the re-review was not given the criterion:\n%s", rounds[1])
			}
			if !strings.Contains(rounds[1], fixRange) {
				t.Errorf("the re-review after the fix is not told its commits %s:\n%s", fixRange, rounds[1])
			}
			if strings.Contains(rounds[2], fixRange) || strings.Contains(rounds[2], simplifyRange) {
				t.Errorf("the simplifier's range review is told a last fix (%s or %s):\n%s", fixRange, simplifyRange, rounds[2])
			}
		})
	}
}
