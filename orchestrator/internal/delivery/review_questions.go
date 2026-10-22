package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// EarlierQuestion is a reviewer's question (review_questions) as the next
// reviewer of its category is shown it. Status is open or answered; a
// withdrawn one is not shown. ByPerson: a person answered, not the conductor.
type EarlierQuestion struct {
	Question, FindingTitle, Status, Answer string
	ByPerson                               bool
}

// EarlierQuestionsTx is what runID is shown of earlier reviewers' questions:
// when it is a review, every question of its task's attempt raised by a
// review Run of its category, oldest first; none for any other phase, so no
// fixer, implementer, simplifier or tester is ever given one.
func EarlierQuestionsTx(ctx context.Context, tx pgx.Tx, runID string) ([]EarlierQuestion, error) {
	rows, err := tx.Query(ctx, `SELECT q.question, COALESCE(f.title, ''), q.status, COALESCE(q.answer, ''), q.answered_by_person IS NOT NULL
		FROM runs r
		JOIN runs asker ON asker.task_id = r.task_id AND asker.attempt = r.attempt AND asker.phase = 'review'
		  AND COALESCE(asker.category, '') = COALESCE(r.category, '')
		JOIN review_questions q ON q.run_id = asker.id AND q.category = COALESCE(r.category, '')
		LEFT JOIN review_findings f ON f.id = q.finding_id
		WHERE r.id = $1 AND r.phase = 'review' AND q.status <> 'withdrawn'
		ORDER BY q.created_at, asker.id, q.n`, runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[EarlierQuestion])
}

// earlierQuestions renders contracts/earlier-questions; "" for none.
func earlierQuestions(questions []EarlierQuestion) string {
	if len(questions) == 0 {
		return ""
	}
	return render(earlierQuestionsTmpl, questions)
}
