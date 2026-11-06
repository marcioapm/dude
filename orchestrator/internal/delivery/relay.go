package delivery

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// The relay (design §1.4): the conductor asks the person a writer's
// question for it (ask_person relay), and the person's answer answers both.

// openWriterQuestion (SQL; $1 question, $2 task, $3 organization) is a
// writer's question still with its conductor: open, addressed to it, asked
// by a current writer Run of the task. ConductAnswerQuestion and a relay
// take the same.
const openWriterQuestion = `SELECT q.run_id, q.prompt, q.items FROM questions q JOIN runs r ON r.id=q.run_id
 WHERE q.id=$1 AND q.task_id=$2 AND q.organization_id=$3 AND q.status='open' AND q.addressee='conductor'
 AND r.task_id=q.task_id AND r.attempt=task_attempt(q.task_id) AND r.role<>'conductor'
 AND r.status IN ('running','paused')
 FOR UPDATE OF q,r`

// CheckRelayTx validates the conductor's ask_person relay of writer
// question id, before it asks: the caller is the task's live conductor and
// id is a writer's question still with it (openWriterQuestion), not relayed
// already by a question still open. Takes Chat → delivery → task →
// question → Run, as an answer does (LockAnswerTx).
//
// A second relay of a question whose relay is still open is refused rather
// than answered with the first: the person already has it, and a second
// question would ask them twice.
func CheckRelayTx(ctx context.Context, tx pgx.Tx, ref RunRef, id string) error {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return err
	}
	d, err := LockAnswerTx(ctx, tx, ref.TaskID)
	if err != nil {
		return err
	}
	halted, err := HaltedTx(ctx, tx, ref.TaskID)
	if err != nil {
		return err
	}
	if d == nil || !d.Live() || !d.State.conducted() || Ended(d.TaskStatus) || halted {
		return refusef(rRelayRequiresOpenWriterQuestion, id)
	}
	var writer, prompt string
	var items []QuestionItem
	if err := tx.QueryRow(ctx, openWriterQuestion, id, ref.TaskID, ref.Org).Scan(&writer, &prompt, &items); db.IsNotFound(err) {
		return refusef(rRelayRequiresOpenWriterQuestion, id)
	} else if err != nil {
		return err
	}
	var open string
	err = tx.QueryRow(ctx, `SELECT id FROM questions WHERE relays=$1 AND status='open' LIMIT 1`, id).Scan(&open)
	if err == nil {
		return refusef(rRelayAlreadyOpen, id, open)
	}
	if !db.IsNotFound(err) {
		return err
	}
	return nil
}

// RelayOutcome is what a person's answer to a relay did to the writer's
// question it relays: Forwarded, or left as it was (Status), because it was
// answered, cancelled or handed over meanwhile.
type RelayOutcome struct {
	QuestionID  string `json:"questionId"`
	Forwarded   bool   `json:"forwarded"`
	Status      string `json:"status,omitempty"`
	DirectiveID string `json:"directiveId,omitempty"`
}

// ForwardRelayTx answers the writer's question that question relayID
// relays (if any) with the person's answer a to it, in the transaction
// that answered the relay: the writer's question answered as the person's
// (answered_by, answered_by_person; no answered_by_run, so it is a human
// decision for PeopleDecisionsTx), its Run told once, question.answered on
// it. A writer's question no longer open with the conductor is left as it
// is (status 'open' guard): nil outcome when relayID relays nothing. The
// caller holds LockAnswerTx's locks and the relay question's row.
func ForwardRelayTx(ctx context.Context, tx pgx.Tx, ref RunRef, relayID string, relayItems []QuestionItem, a Answered,
	actor Writer) (*RelayOutcome, error) {
	var writerQ string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(relays,'') FROM questions WHERE id=$1`, relayID).Scan(&writerQ); err != nil || writerQ == "" {
		return nil, err
	}
	out := &RelayOutcome{QuestionID: writerQ}
	var writer, prompt, status, addressee string
	var items []QuestionItem
	if err := tx.QueryRow(ctx, `SELECT run_id, prompt, items, status::text, addressee FROM questions WHERE id=$1 FOR UPDATE`,
		writerQ).Scan(&writer, &prompt, &items, &status, &addressee); err != nil {
		return nil, err
	}
	if status != "open" || addressee != HolderConductor {
		out.Status = status
		if status == "open" {
			out.Status = "handed_over"
		}
		return out, nil
	}
	wa, err := CheckAnswers(items, relayedAnswers(items, relayItems, a), "")
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(wa.Answers)
	tag, err := tx.Exec(ctx, `UPDATE questions SET status='answered', answer=$2, answers=$3::jsonb, answered_at=now(),
		answered_by=(SELECT id FROM users WHERE id=$4), answered_by_person=NULLIF($5,'')
		WHERE id=$1 AND status='open' AND addressee='conductor'`, writerQ, wa.Text, raw, actor.ActorID, actor.Person)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		out.Status = "answered"
		return out, nil
	}
	name, err := PersonName(ctx, tx, actor.Person)
	if err != nil {
		return nil, err
	}
	writerRef := ref
	writerRef.RunID = writer
	text := render(relayedTmpl, name) + "\n" + AnswerDirective(prompt, items, wa, name, false)
	directive, _, err := QueueDirective(ctx, tx, writerRef, Directive{Text: text, Scope: "run"})
	if err != nil {
		return nil, err
	}
	ev := writerRef.Event("question.answered", actor.ActorType, map[string]any{"questionId": writerQ, "answer": wa.Text,
		"answers": wa.Answers, "directiveId": directive, "by": "person", "relayedBy": relayID})
	ev.ActorID = actor.ActorID
	if _, err := ledger.Append(ctx, tx, ev); err != nil {
		return nil, err
	}
	out.Forwarded, out.Status, out.DirectiveID = true, "answered", directive
	return out, nil
}

// relayedAnswers are the person's answers a to the relay, as answers to
// the writer's items: item by item when the relay asks as many questions
// (each in words: the relay's choices are the conductor's, not the
// writer's), else the whole answer for each. An answer with no words (the
// person answered with an image only, which goes to the conductor's Run
// with its answer) is forwarded as relayedImageOnly, so the writer is not
// left asked.
func relayedAnswers(items, relayItems []QuestionItem, a Answered) []ItemAnswer {
	out := make([]ItemAnswer, len(items))
	for i := range out {
		text := a.Text
		if len(items) == len(relayItems) && len(a.Answers) == len(relayItems) {
			text = itemAnswerText(relayItems[i], a.Answers[i], asTyped)
		}
		if strings.TrimSpace(text) == "" {
			text = relayedImageOnly
		}
		out[i] = ItemAnswer{Text: clipRunes(text, AnswerTextMax)}
	}
	return out
}

// relayedImageOnly is the writer's answer when the person answered the
// relay with an image and no words: the image is the conductor's to pass on.
const relayedImageOnly = "(the person answered with an image, which your conductor has)"

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// CancelRelaysTx withdraws the person's open questions that relay writer
// question id, once it is settled some other way (the conductor or the
// owner answered it directly): the person is not left asked a dead
// question. Puts the task back if nothing else waits on a person.
func CancelRelaysTx(ctx context.Context, tx pgx.Tx, ref RunRef, id, by string) error {
	rows, err := tx.Query(ctx, `UPDATE questions SET status='cancelled' WHERE relays=$1 AND status='open' RETURNING id, run_id`, id)
	if err != nil {
		return err
	}
	closed, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, RunID string }])
	if err != nil || len(closed) == 0 {
		return err
	}
	for _, q := range closed {
		r := ref
		r.RunID = q.RunID
		if _, err := ledger.Append(ctx, tx, r.Event(EvQuestionClosed, ledger.ActorSystem,
			map[string]any{"questionId": q.ID, "by": by, "relays": id, "reason": "the question it relays was answered"})); err != nil {
			return err
		}
	}
	return EndCancelledWaitTx(ctx, tx, ref.Org, ref.ProjectID, ref.TaskID)
}

// ConductorQuestionsDue (SQL, a FROM clause over questions q; $1 the wait
// in seconds) are writers' questions with their conductor that dude hands
// to the person (§1.7): open and conductor-addressed (the partial index
// questions_conductor_open_idx), relayed by no open question (the person
// has it already), and asked longer ago than the wait, or since asked the
// task's latest conductor failed, or its quiet conductor has no model.
//
// Reviewer questions and repository requests use their own held-ask sweep
// (ReviewQuestionsDueSQL and RepositoryRequestsDueSQL), not writer relays.
const ConductorQuestionsDue = `questions q CROSS JOIN LATERAL (SELECT q.task_id) qn(task_id)
	WHERE q.status = 'open' AND q.addressee = 'conductor' AND q.task_id IS NOT NULL
	  AND NOT EXISTS (SELECT 1 FROM questions rq WHERE rq.relays = q.id AND rq.status = 'open')
	  AND (q.asked_at <= now() - make_interval(secs => $1)
	    OR COALESCE((SELECT c.status = 'failed' AND c.ended_at >= q.asked_at FROM runs c
	        WHERE c.task_id = q.task_id AND c.role = 'conductor' ORDER BY c.created_at DESC, c.id DESC LIMIT 1), false)
	    OR ` + quietNoModel + `)`

// HandOverDueTx hands writer question id to the person if it is still due
// (ConductorQuestionsDue, read again under the locks): Chat → delivery →
// task → question, then ToPersonTx(WhyHandover). False when it is not due
// any more, or was handed over already.
func HandOverDueTx(ctx context.Context, tx pgx.Tx, org, id string, wait time.Duration) (bool, error) {
	var taskID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(task_id,'') FROM questions WHERE id=$1`, id).Scan(&taskID); err != nil || taskID == "" {
		if db.IsNotFound(err) {
			err = nil
		}
		return false, err
	}
	if err := LockChat(ctx, tx, taskID); err != nil {
		return false, err
	}
	projectID, _, err := LockEscalationTx(ctx, tx, taskID)
	if err != nil {
		return false, err
	}
	var late, failed bool
	err = tx.QueryRow(ctx, `SELECT q.asked_at <= now() - make_interval(secs => $2),
		COALESCE((SELECT c.status = 'failed' AND c.ended_at >= q.asked_at FROM runs c
			WHERE c.task_id = q.task_id AND c.role = 'conductor' ORDER BY c.created_at DESC, c.id DESC LIMIT 1), false)
		FROM `+ConductorQuestionsDue+` AND q.id = $3 FOR UPDATE OF q`, wait.Seconds(), wait.Seconds(), id).Scan(&late, &failed)
	if db.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	why := "its conductor has no model to run on"
	switch {
	case failed:
		why = "its conductor failed"
	case late:
		why = "its conductor did not answer in time"
	}
	got, err := ToPersonTx(ctx, tx, ToPerson{Ref: RunRef{Org: org, ProjectID: projectID, TaskID: taskID}, Why: WhyHandover,
		HandOver: id, Reason: why})
	return got != "", err
}

// ConductorTakesTx says a conducted task's conductor takes what would
// otherwise wait on a person, as a writer's question does (AskConductorTx):
// its delivery is live and conducted, and a conductor is available.
func ConductorTakesTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return false, err
	}
	return conductorHeldTx(ctx, tx, taskID, d)
}

// RecordIdleParkTx is a conducted task's idle park of Run r (the agent
// went quiet and did not answer a nudge): the conductor's stalled wake,
// keyed per park, instead of the task waiting on a person (A12). It steers
// the Run (which resumes it: ConductSteerFeedback) or restarts it; one it
// leaves parked is handed to the person (IdleParksDueSQL).
func RecordIdleParkTx(ctx context.Context, tx pgx.Tx, r RunRef, phase string, parkedAt time.Time) error {
	line := render(idleParkedTmpl, struct{ Phase, RunID string }{phase, r.RunID})
	_, err := RecordWakeTx(ctx, tx, r.Org, r.TaskID, WakeStalled, "stalled:idle:"+r.RunID+":"+parkedAt.UTC().Format(time.RFC3339Nano), line)
	return err
}

// reasonIdleHandover is the task's status reason while a conducted idle
// park its conductor left waits on the person; EndConductorWait puts the
// task back once nothing waits on a person.
const reasonIdleHandover = "the agent went quiet: handed to you from the conductor"

// idleParked (SQL, a FROM clause) is each phase Run of a conducted task
// dude parked as idle that nobody has taken up (no resume asked, the task
// not waiting on a person), as q with task_id and created_at the park's
// start (its latest run.parked), for heldDue.
const idleParked = `(SELECT r.id, r.organization_id, r.task_id,
		(SELECT e.occurred_at FROM events e WHERE e.run_id = r.id AND e.event_type IN ('run.parked', 'run.unparked')
		 ORDER BY e.cursor DESC LIMIT 1) AS created_at
	FROM runs r JOIN tasks t ON t.id = r.task_id
	WHERE r.status = 'paused' AND r.dude_pause = 'idle' AND r.control = 'none' AND r.phase IS NOT NULL
	  AND r.source_kind IS NULL AND r.role <> 'conductor'
	  AND t.status NOT IN ('awaiting_input', 'done', 'failed', 'aborted')) q`

// IdleParksDueSQL (SQL, $1 the wait in seconds) selects the idle-parked
// phase Runs dude hands to the person now (heldDue: the conductor left it
// past the wait, failed since, has no model, or no longer decides): id,
// organization, task.
const IdleParksDueSQL = `SELECT q.id, q.organization_id, q.task_id FROM ` + idleParked + `
	WHERE q.created_at IS NOT NULL AND ` + heldDue + ` ORDER BY q.created_at, q.id`

// HandOverIdleParkTx hands Run runID's idle park its conductor left to the
// person, if it is still due under the locks (Chat → delivery → task, then
// the Run): the task waits on them (awaiting_input), as an idle park of a
// task no conductor decides does. False when it is not due any more.
func HandOverIdleParkTx(ctx context.Context, tx pgx.Tx, org, taskID, runID string, wait time.Duration) (bool, error) {
	d, err := LockAnswerTx(ctx, tx, taskID)
	if err != nil || d == nil {
		return false, err
	}
	var projectID string
	err = tx.QueryRow(ctx, `SELECT r.project_id FROM runs r WHERE r.id = $1 FOR UPDATE`, runID).Scan(&projectID)
	if db.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var due bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+idleParked+` WHERE q.id = $2 AND q.task_id = $3
		AND q.created_at IS NOT NULL AND `+heldDue+`)`, wait.Seconds(), runID, taskID).Scan(&due); err != nil || !due {
		return false, err
	}
	var before string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, taskID).Scan(&before); err != nil {
		return false, err
	}
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: runID}
	if _, err = ToPersonTx(ctx, tx, ToPerson{Ref: ref, Why: WhyHandover, Reason: reasonIdleHandover}); err != nil {
		return false, err
	}
	// The status the resume puts back, as an idle park's own run.parked
	// keeps it for a task no conductor decides (phases.whilePaused).
	_, err = ledger.Append(ctx, tx, ref.Event(EvIdleHandedOver, ledger.ActorSystem,
		map[string]any{"reason": "idle", "taskStatus": before, "handedOverFrom": HolderConductor}))
	return err == nil, err
}

// EvIdleHandedOver is a conducted idle park its conductor left, handed to
// the person (HandOverIdleParkTx); its taskStatus is what a resume puts
// the task back to.
const EvIdleHandedOver = "run.idle_handed_over"

// RelayNotEscalation refuses a relay on the conductor's question about its
// task's escalation.
func RelayNotEscalation() error { return refuseText(rRelayNotEscalation) }

// RelayOutcomeLine tells the conductor, after the person's answer to its
// relay, what became of the writer's question: "" for no relay.
func RelayOutcomeLine(o *RelayOutcome) string {
	if o == nil {
		return ""
	}
	return "\n\n" + render(relayOutcomeTmpl, o)
}
