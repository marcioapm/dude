package delivery

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// What a Run is shown of earlier reviewers' questions is decided by its
// own row: a review Run is shown its category's questions of its attempt,
// open and answered, never a withdrawn one; a Run of any other phase, even
// one carrying the category, or a review of another category or attempt,
// is shown none.
func TestEarlierQuestionsAreAReviewOfTheSameCategorysOnly(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, kind, created_at)
		VALUES
		('run_r1', $1, 'prj_g', 'wi_g', 1, 'completed', 'review', 'reviewer', 'correctness', 'agent', '2026-01-02 10:00Z'),
		('run_s1', $1, 'prj_g', 'wi_g', 1, 'completed', 'review', 'reviewer', 'security', 'agent', '2026-01-02 10:00Z'),
		('run_old', $1, 'prj_g', 'wi_g', 2, 'completed', 'review', 'reviewer', 'correctness', 'agent', '2026-01-01 10:00Z'),
		('run_f1', $1, 'prj_g', 'wi_g', 1, 'running', 'fix', 'implementer', 'correctness', 'agent', '2026-01-02 11:00Z'),
		('run_t1', $1, 'prj_g', 'wi_g', 1, 'running', 'test', 'qa_browser', 'correctness', 'agent', '2026-01-02 11:00Z'),
		('run_i1', $1, 'prj_g', 'wi_g', 1, 'running', 'implement', 'implementer', 'correctness', 'agent', '2026-01-02 11:00Z'),
		('run_m1', $1, 'prj_g', 'wi_g', 1, 'running', 'simplify', 'simplifier', 'correctness', 'agent', '2026-01-02 11:00Z'),
		('run_r2', $1, 'prj_g', 'wi_g', 1, 'running', 'review', 'reviewer', 'correctness', 'agent', '2026-01-02 11:00Z'),
		('run_s2', $1, 'prj_g', 'wi_g', 1, 'running', 'review', 'reviewer', 'security', 'agent', '2026-01-02 11:00Z'),
		('run_r9', $1, 'prj_g', 'wi_g', 3, 'running', 'review', 'reviewer', 'correctness', 'agent', '2026-01-03 11:00Z')`)
	w.exec(t, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description, suggested_fix)
		VALUES ('fnd_q', $1, 'wi_g', 'run_r1', 'correctness', 'blocking', 'Totals off by one cent', 'D', 'F')`)
	w.exec(t, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, finding_id, question, needed_from,
			status, answer, answered_by_person, answered_by_run, answered_at, created_at)
		VALUES
		('rvq_1', $1, 'wi_g', 'run_r1', 1, 'correctness', 'fnd_q', 'Do invoices close in UTC?', 'product',
			'open', NULL, NULL, NULL, NULL, '2026-01-02 10:30Z'),
		('rvq_2', $1, 'wi_g', 'run_r1', 2, 'correctness', NULL, 'Is a retry expected after a timeout?', 'operations',
			'answered', 'Yes, once, after 30 seconds.', 'per_ana', NULL, '2026-01-02 10:40Z', '2026-01-02 10:31Z'),
		('rvq_3', $1, 'wi_g', 'run_r1', 3, 'correctness', NULL, 'Are refunds in scope?', 'product',
			'answered', 'No: the task names invoices only.', NULL, 'run_f1', '2026-01-02 10:41Z', '2026-01-02 10:32Z'),
		('rvq_4', $1, 'wi_g', 'run_r1', 4, 'correctness', NULL, 'Withdrawn: is the old exporter still called?', '',
			'withdrawn', NULL, NULL, NULL, NULL, '2026-01-02 10:33Z'),
		('rvq_5', $1, 'wi_g', 'run_s1', 1, 'security', NULL, 'Who may download another org''s export?', 'security',
			'open', NULL, NULL, NULL, NULL, '2026-01-02 10:34Z'),
		('rvq_6', $1, 'wi_g', 'run_old', 1, 'correctness', NULL, 'A question of another attempt', '',
			'open', NULL, NULL, NULL, NULL, '2026-01-01 10:30Z')`)

	shown := func(run string) []EarlierQuestion {
		var qs []EarlierQuestion
		w.in(t, func(ctx context.Context, tx pgx.Tx) (_ string, err error) {
			qs, err = EarlierQuestionsTx(ctx, tx, run)
			return "", err
		})
		return qs
	}
	for _, run := range []string{"run_f1", "run_t1", "run_i1", "run_m1", "run_r9"} {
		if qs := shown(run); len(qs) != 0 {
			t.Errorf("%s is shown %+v, want none", run, qs)
		}
	}
	if qs := shown("run_s2"); len(qs) != 1 || qs[0].Question != "Who may download another org's export?" {
		t.Errorf("the security reviewer is shown %+v, want its category's one", qs)
	}
	correctness := shown("run_r2")
	want := []EarlierQuestion{
		{Question: "Do invoices close in UTC?", FindingTitle: "Totals off by one cent", Status: "open"},
		{Question: "Is a retry expected after a timeout?", Status: "answered", Answer: "Yes, once, after 30 seconds.", ByPerson: true},
		{Question: "Are refunds in scope?", Status: "answered", Answer: "No: the task names invoices only."},
	}
	if fmt.Sprint(correctness) != fmt.Sprint(want) {
		t.Fatalf("the correctness reviewer is shown %+v, want %+v", correctness, want)
	}
	golden(t, "earlier-questions", earlierQuestions(correctness))
}
