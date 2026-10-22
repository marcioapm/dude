package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// EarlierQuestion is a reviewer's question (review_questions) as the next
// reviewer of its category is shown it. Status is open or answered; a
// withdrawn one is not shown. ByPerson: answered, and not by the conductor's
// Run (a person whose row is gone still answered as a person).
type EarlierQuestion struct {
	Question, FindingTitle, Status, Answer string
	ByPerson                               bool
}

// EarlierQuestionsTx is what runID is shown of earlier reviewers' questions:
// when it is a review, every question of its task's attempt raised by a
// review Run of its category, oldest first; none for any other phase, so no
// fixer, implementer, simplifier or tester is ever given one.
func EarlierQuestionsTx(ctx context.Context, tx pgx.Tx, runID string) ([]EarlierQuestion, error) {
	rows, err := tx.Query(ctx, `SELECT q.question, COALESCE(f.title, ''), q.status, COALESCE(q.answer, ''), q.status = 'answered' AND q.answered_by_run IS NULL
		FROM runs r
		JOIN runs asker ON asker.task_id = r.task_id AND asker.attempt = r.attempt AND asker.phase = 'review'
		-- q.category is the asker's, "" read as correctness (recordFindingsTx).
		JOIN review_questions q ON q.run_id = asker.id AND q.category = COALESCE(NULLIF(r.category, ''), 'correctness')
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

// SignalReviewQuestion wakes a delivery held by open reviewer questions
// (holdForQuestions) once one is answered or withdrawn: the held step runs
// again and goes on when none is left open. Its own signal, because the
// others' steps read theirs as something else to do: human.decision as an
// escalation's decision, conductor.decision as the conductor's, and
// human.feedback as people's feedback to fix.
const SignalReviewQuestion = "review.question_settled"

// The task's status reasons while a reviewer's question holds the pull
// request, and once the last is settled.
const (
	reasonQuestionsOpen    = "a reviewer's question waits on an answer"
	reasonQuestionsSettled = "reviewers' questions settled"
)

// holdForQuestions parks step while the delivery's attempt has an open
// reviewer question: the pull request is not opened, nor a checked fix
// promoted to it, past a question nobody answered. The task waits on a
// person; an answer or a withdrawal, the conductor's included, signals the
// step to run again. People's feedback wakes it too: it goes to a fixer
// first, as the step would send it.
func (w *steps) holdForQuestions(ctx context.Context, sc workflow.StepContext, st *State, step string) (workflow.Result, bool, error) {
	var open []OpenReviewQuestion
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
		open, err = openReviewQuestionsTx(ctx, tx, st.TaskID, st.Attempt, nil)
		return err
	}); err != nil {
		return workflow.Result{}, false, err
	}
	if len(open) == 0 {
		if st.QuestionsHeld {
			// Released: the task works again until the step says otherwise.
			st.QuestionsHeld = false
			return workflow.Result{}, false, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", reasonQuestionsSettled)
		}
		return workflow.Result{}, false, nil
	}
	st.QuestionsHeld = true
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "awaiting_input", reasonQuestionsOpen); err != nil {
		return workflow.Result{}, false, err
	}
	return workflow.Result{Next: step, State: st, AwaitSignals: []string{SignalReviewQuestion, SignalHumanFeedback}}, true, nil
}

// ConductAnswerReviewQuestion is answer_review_question: the task's live
// conductor, while it takes the decisions, answers one of its task's open
// reviewer questions from a fact it established, or withdraws it with a
// reason; never recorded as a person's decision.
func ConductAnswerReviewQuestion(ctx context.Context, tx pgx.Tx, ref RunRef, id, answer string, withdraw bool, reason string) (SettledReviewQuestion, error) {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return SettledReviewQuestion{}, err
	}
	d, err := ReadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return SettledReviewQuestion{}, err
	}
	switch {
	case d != nil && Ended(d.TaskStatus):
		return SettledReviewQuestion{}, refusef(rThisTaskYouAreReadOnly, d.TaskStatus)
	case d == nil || !d.Live():
		return SettledReviewQuestion{}, refuseText(rThisTaskHasNoDeliveryIn)
	case !d.State.conducted():
		return SettledReviewQuestion{}, refuseText(rDeliverTakesThisTaskDecisionsNot)
	}
	if withdraw == (strings.TrimSpace(answer) != "") {
		return SettledReviewQuestion{}, refuseText(rAnswerOrWithdrawReviewQuestion)
	}
	by := ReviewQuestionBy{Run: ref.RunID, TaskID: ref.TaskID}
	var out SettledReviewQuestion
	if withdraw {
		out, err = WithdrawReviewQuestionTx(ctx, tx, ref.Org, id, reason, by)
	} else {
		out, err = AnswerReviewQuestionTx(ctx, tx, ref.Org, id, answer, by)
	}
	var e ReviewQuestionError
	if errors.As(err, &e) {
		return out, refuseText(e.Msg)
	}
	return out, err
}

// ReviewQuestionError is an answer or withdrawal refused: Kind not_found,
// conflict (the question is not open) or bad_request, as the API answers it.
type ReviewQuestionError struct{ Kind, Msg string }

func (e ReviewQuestionError) Error() string { return e.Msg }

// ReviewQuestionBy is who settles a reviewer's question: a person (Person
// may be "" for a principal that is none), or the task's conductor (Run).
type ReviewQuestionBy struct {
	Person, ActorType, ActorID string
	// The conductor's Run; the question must then be TaskID's.
	Run, TaskID string
}

func (b ReviewQuestionBy) conductor() bool { return b.Run != "" }

// SettledReviewQuestion is a question as an answer or withdrawal left it.
type SettledReviewQuestion struct {
	ID, TaskID, ProjectID, Status string
}

// AnswerReviewQuestionTx answers an open reviewer question. A person's
// answer is also one of the task's decisions (RecordDecisionTx), which every
// later phase, the fixer included, is told; the conductor's is not.
func AnswerReviewQuestionTx(ctx context.Context, tx pgx.Tx, org, id, answer string, by ReviewQuestionBy) (SettledReviewQuestion, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return SettledReviewQuestion{}, ReviewQuestionError{"bad_request", mReviewQuestionAnswerRequired}
	}
	return settleReviewQuestionTx(ctx, tx, org, id, "answered", answer, by)
}

// WithdrawReviewQuestionTx withdraws an open reviewer question as moot, with
// the reason, which the event keeps. No decision is recorded.
func WithdrawReviewQuestionTx(ctx context.Context, tx pgx.Tx, org, id, reason string, by ReviewQuestionBy) (SettledReviewQuestion, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return SettledReviewQuestion{}, ReviewQuestionError{"bad_request", mReviewQuestionReasonRequired}
	}
	return settleReviewQuestionTx(ctx, tx, org, id, "withdrawn", reason, by)
}

// settleReviewQuestionTx moves an open question to status; only an open row
// changes, so a second answer, or an answer to a withdrawn one, is a
// conflict that writes nothing. text is the answer, or the withdrawal's
// reason.
func settleReviewQuestionTx(ctx context.Context, tx pgx.Tx, org, id, status, text string, by ReviewQuestionBy) (SettledReviewQuestion, error) {
	out := SettledReviewQuestion{ID: id, Status: status}
	var question string
	err := tx.QueryRow(ctx, `UPDATE review_questions q SET status = $2,
			answer = CASE WHEN $2 = 'answered' THEN $3 END,
			answered_by_person = CASE WHEN $5 = '' THEN NULLIF($4, '') END,
			answered_by_run = NULLIF($5, ''), answered_at = now()
		FROM tasks t WHERE q.id = $1 AND q.status = 'open' AND t.id = q.task_id AND ($6 = '' OR q.task_id = $6)
		RETURNING q.task_id, t.project_id, q.question`, id, status, text, by.Person, by.Run, by.TaskID).
		Scan(&out.TaskID, &out.ProjectID, &question)
	if db.IsNotFound(err) {
		var current string
		err := tx.QueryRow(ctx, `SELECT status FROM review_questions WHERE id = $1 AND ($2 = '' OR task_id = $2)`, id, by.TaskID).
			Scan(&current)
		if db.IsNotFound(err) {
			return out, ReviewQuestionError{"not_found", fmt.Sprintf(mReviewQuestionNotFound, id)}
		}
		if err != nil {
			return out, err
		}
		return out, ReviewQuestionError{"conflict", fmt.Sprintf(mReviewQuestionNotOpen, id, current)}
	}
	if err != nil {
		return out, err
	}
	payload := map[string]any{"questionId": id, "by": "person"}
	if by.conductor() {
		payload["by"] = "conductor"
	}
	typ := EvReviewQuestionAnswered
	if status == "answered" {
		payload["answer"] = text
		if !by.conductor() {
			if err := RecordDecisionTx(ctx, tx, org, out.TaskID, question, text); err != nil {
				return out, err
			}
		}
	} else {
		typ, payload["reason"] = EvReviewQuestionWithdrawn, text
	}
	ev := ledger.Event{Type: typ, OrganizationID: org, ProjectID: out.ProjectID, TaskID: out.TaskID,
		ActorType: by.ActorType, ActorID: by.ActorID, Source: ledger.SourceOrchestrator, CorrelationID: out.TaskID, Payload: payload}
	if by.conductor() {
		ev.RunID, ev.ActorType, ev.ActorID = by.Run, ledger.ActorAgent, by.Run
	}
	if _, err := ledger.Append(ctx, tx, ev); err != nil {
		return out, err
	}
	d, err := ReadDelivery(ctx, tx, out.TaskID)
	if err != nil || d == nil || !d.Live() {
		return out, err
	}
	// A step held by open questions reads them again (holdForQuestions).
	if err := workflow.SignalTx(ctx, tx, org, d.WorkflowID, SignalReviewQuestion, map[string]any{"questionId": id},
		"review-question:"+id); err != nil {
		return out, err
	}
	if !by.conductor() && d.State.conducted() {
		// The conductor decides whether a review should weigh it.
		line := reviewQuestionAnsweredLine(id, text, status == "withdrawn")
		if _, err := RecordWakeTx(ctx, tx, org, out.TaskID, "question", "review-question:"+id+":answered", line); err != nil {
			return out, err
		}
	}
	return out, nil
}

// OpenReviewQuestion is an open reviewer question as the conductor is told
// of it: in a wake when it is raised, and in its briefing.
type OpenReviewQuestion struct {
	ID, RunID, Category, NeededFrom, Question string
}

// line is the question in the conductor's briefing; wakeLine, in the wake
// that tells the conductor it was raised.
func (q OpenReviewQuestion) line() string     { return render(reviewQuestionLineTmpl, q) }
func (q OpenReviewQuestion) wakeLine() string { return render(reviewQuestionWakeTmpl, q) }

// reviewQuestionAnsweredLine wakes the conductor with a person's answer to
// question id, or its withdrawal (answer is then the reason, not shown).
func reviewQuestionAnsweredLine(id, answer string, withdrawn bool) string {
	return render(reviewQuestionAnsweredTmpl, struct {
		ID, Answer string
		Withdrawn  bool
	}{id, answer, withdrawn})
}

// openReviewQuestionsTx is the task's open reviewer questions of attempt,
// oldest first (0: the task's highest attempt, as State.Attempt reads); of
// the review Runs runIDs only when given.
func openReviewQuestionsTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int, runIDs []string) ([]OpenReviewQuestion, error) {
	rows, err := tx.Query(ctx, `SELECT q.id, COALESCE(q.run_id, ''), q.category, q.needed_from, q.question
		FROM review_questions q JOIN runs r ON r.id = q.run_id
		WHERE q.task_id = $1 AND q.status = 'open'
		  AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1 AND phase IS NOT NULL))
		  AND ($3::text[] IS NULL OR q.run_id = ANY($3))
		ORDER BY q.created_at, q.run_id, q.n`, taskID, attempt, runIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[OpenReviewQuestion])
}

// recordReviewQuestionWakesTx records one wake for the conductor per open
// question the review Runs asked, keyed on the question: a step replayed
// records each once.
func recordReviewQuestionWakesTx(ctx context.Context, tx pgx.Tx, org, taskID string, runIDs []string) error {
	qs, err := openReviewQuestionsTx(ctx, tx, taskID, 0, runIDs)
	if err != nil {
		return err
	}
	for _, q := range qs {
		if _, err := RecordWakeTx(ctx, tx, org, taskID, "question", "review-question:"+q.ID, q.wakeLine()); err != nil {
			return err
		}
	}
	return nil
}
