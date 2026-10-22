package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// A question a model wrote in the finding format too: a title and a
// blocking severity beside its question.
const mixedQuestion = "---\nquestion: Do invoices close in UTC or in the organisation's zone?\n" +
	"title: Month boundary unknown\nseverity: blocking\n" +
	"why_it_matters: The finding is blocking only if months close in UTC.\n" +
	"evidence_checked: Read the export and its configuration; neither names a zone.\n" +
	"needed_from: product\n"

const onlyQuestion = "question: Is a retry expected after a timeout?\nwhy_it_matters: W.\nevidence_checked: E.\nneeded_from: operations\n"

// reviewQuestionRow is one stored question as a test reads it.
type reviewQuestionRow struct {
	RunID, Category, FindingID, Question, Status, Answer string
	N                                                    int
}

func (w *world) reviewQuestions(task string) []reviewQuestionRow {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT COALESCE(run_id, ''), category, COALESCE(finding_id, ''), question,
			status, COALESCE(answer, ''), n
		FROM review_questions WHERE task_id = $1 ORDER BY created_at, run_id, n`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[reviewQuestionRow])
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

// A reviewer's reply with one blocking finding and a question that also
// carries a title and a blocking severity: one finding is stored and sent
// to the one fixer, and the question is stored as a question. Were it read
// as a finding, the fixer would be sent two.
func TestAQuestionInTheFindingFormatIsNeverSentToAFixer(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" && strings.Contains(b.Reply, "One problem") {
			b.Reply += "\n```yaml\n" + mixedQuestion + "```\n"
		}
		return b
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request, or a stop", func() bool {
		return len(w.gh.Pulls()) == 1 || w.taskStatus(wi) == "awaiting_input"
	})
	if s := w.escalationReason(wi); s != "" {
		t.Fatalf("the delivery stopped for a person: %s", s)
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1`, wi); n != 1 {
		t.Errorf("%d findings stored, want the one", n)
	}
	rows, err := w.owner.Query(context.Background(), `SELECT cardinality(finding_ids) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sent) != "[1]" {
		t.Errorf("fix Runs were sent %v findings each, want one fix sent one", sent)
	}
	qs := w.reviewQuestions(wi)
	if len(qs) != 1 || qs[0].Category != "correctness" || qs[0].Status != "open" || qs[0].N != 1 || qs[0].FindingID != "" {
		t.Errorf("questions = %+v, want the one, open, about no finding", qs)
	}
}

// A reply with a question and no finding holds nothing: the delivery goes
// on through the simplifier to its pull request with no fixer, and the
// question is stored. Its review Run reported again (a retried finish)
// stores it once and writes no second event, and keeps an answer given
// between the two reports.
func TestAQuestionAloneHoldsNothingAndIsStoredOnce(t *testing.T) {
	w := newWorld(t)
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "Nothing wrong; one thing I could not establish.\n\n```yaml\n" + onlyQuestion + "```\n"}
			}
			return fakelux.Behaviour{Reply: "Nothing new."}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		case "simplify":
			return fakelux.Behaviour{Commit: map[string]string{"SIMPLE.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "simplify"}
		}
		return fakelux.Behaviour{Reply: "Done."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request, or a stop", func() bool {
		return len(w.gh.Pulls()) == 1 || w.taskStatus(wi) == "awaiting_input"
	})
	if s := w.escalationReason(wi); s != "" {
		t.Fatalf("the delivery stopped for a person: %s", s)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi); n != 0 {
		t.Errorf("%d fix Runs, want none", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi); n != 1 {
		t.Errorf("%d simplify Runs, want one", n)
	}
	qs := w.reviewQuestions(wi)
	if len(qs) != 1 || qs[0].Question != "Is a retry expected after a timeout?" || qs[0].Status != "open" {
		t.Fatalf("questions = %+v, want the one, open", qs)
	}
	if n := w.events(wi, delivery.EvReviewQuestionRaised); n != 1 {
		t.Errorf("%d %s events, want one", n, delivery.EvReviewQuestionRaised)
	}

	ctx := context.Background()
	run := qs[0].RunID
	mustExec(t, w.owner, `UPDATE review_questions SET status = 'answered', answer = 'Yes, once.', answered_at = now() WHERE run_id = $1`, run)
	var reply string
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) (err error) {
		reply, err = delivery.RunReplyTx(ctx, tx, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := phases.RecordFindings(ctx, w.app, w.org, run, delivery.ParseFindings(reply), nil, delivery.ParseQuestions(reply)); err != nil {
		t.Fatal(err)
	}
	again := w.reviewQuestions(wi)
	if len(again) != 1 || again[0].Status != "answered" || again[0].Answer != "Yes, once." {
		t.Errorf("after a second report the questions are %+v, want the one, answered as it was", again)
	}
	if n := w.events(wi, delivery.EvReviewQuestionRaised); n != 1 {
		t.Errorf("%d %s events after a second report, want still one", n, delivery.EvReviewQuestionRaised)
	}

	// A tester's question document is not stored: the tester reports
	// blocked for what it cannot establish.
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		SELECT 'run_tq_' || $1, organization_id, project_id, id, 1, 'completed', 'test', 'qa_browser', 'agent' FROM tasks WHERE id = $1`, wi)
	if err := phases.RecordFindings(ctx, w.app, w.org, "run_tq_"+wi, nil, nil, delivery.ParseQuestions(onlyQuestion)); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM review_questions WHERE run_id = $1`, "run_tq_"+wi); n != 0 {
		t.Errorf("a tester's question was stored (%d rows)", n)
	}
}
