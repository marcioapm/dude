package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// An escalation is a person's to decide, from the banner or by answering
// the conductor's question about it; a free answer of the task's owner to
// that question hands this one escalation to the conductor
// (decide_escalation). Every way ends in DecideEscalationTx.

// EscalationError is an escalation decision refused: Kind not_found,
// conflict, bad_request or not_kept, as the API answers it.
type EscalationError struct{ Kind, Msg string }

func (e EscalationError) Error() string { return e.Msg }

func escalationErr(kind, format string, a ...any) error {
	return EscalationError{kind, fmt.Sprintf(format, a...)}
}

// EscalationKey names the n-th escalation of a delivery: what its
// conductor's question records (questions.escalation).
func EscalationKey(wfID string, n int) string { return fmt.Sprintf("%s:%d", wfID, n) }

// EscalationDecision is a decision on a task's escalation, and who took it.
type EscalationDecision struct {
	Action, Note string
	// Who took it, for the ledger: the person (banner, answer), or the
	// conductor's Run.
	ActorType, ActorID string
	// Taken on the owner's answer to the conductor's question: the
	// question, and the person who answered.
	QuestionID, AnsweredBy string
	// Taken by the conductor, on that answer: its Run.
	Conductor string
	// Checks who may decide, once the escalation and action are known to
	// be valid; nil when the caller checked already.
	Authorize func() error
}

// LockEscalationTx takes the locks every path that reads or changes an
// escalation's decision or its question takes, in this order: the task's
// row, alone, then its latest delivery, read in a statement of its own
// after the task lock was granted, so a decision committed while this one
// waited is seen. Questions are locked only after both. Ownership changes
// take the same task row (control-plane setTaskPeople, passOnTasks), so an
// owner read under this lock is the current one. The delivery is nil when
// the task has none.
func LockEscalationTx(ctx context.Context, tx pgx.Tx, taskID string) (projectID string, d *Delivery, err error) {
	if err := LockTaskTx(ctx, tx, taskID, &projectID); err != nil {
		return "", nil, err
	}
	d, err = LoadDelivery(ctx, tx, taskID)
	return projectID, d, err
}

// LockTaskTx locks the task's row for the rest of tx: the first lock of an
// escalation path (LockEscalationTx). NO KEY UPDATE: it excludes every other
// writer of the row (status, ownership's FOR UPDATE) but not the KEY SHARE an
// insert of a child row takes, which the syncer takes holding a Run's row.
// projectID, if given, is set.
func LockTaskTx(ctx context.Context, tx pgx.Tx, taskID string, projectID *string) error {
	var p string
	err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1 FOR NO KEY UPDATE`, taskID).Scan(&p)
	if db.IsNotFound(err) {
		return escalationErr("not_found", "task %s not found", taskID)
	}
	if projectID != nil {
		*projectID = p
	}
	return err
}

// DecideEscalationTx takes a decision on the task's escalation, in tx, and
// signals the workflow to carry it out: the action checked against what the
// escalation offers, a resume taking the failed Run back up, the decision
// kept on the escalation (a second is refused), its note one of the task's
// decisions, the conductor's open question about it closed. Who may decide
// is the caller's to check (Authorize), under the locks taken here.
func DecideEscalationTx(ctx context.Context, tx pgx.Tx, org, taskID string, in EscalationDecision) error {
	projectID, d, err := LockEscalationTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if d == nil || d.TaskStatus != "awaiting_input" || d.State.Escalation == nil || d.State.Escalation.Decided != nil {
		return escalationErr("conflict", "delivery of task %s is not waiting for a decision", taskID)
	}
	st := d.State
	e := st.Escalation
	wfID := d.WorkflowID
	if d.Status == "completed" && e.Step == "" {
		// Stopped before a stop waited for a decision: it ended there,
		// and never said which step to go back to. Its step says, where
		// trying again from it can work: a pull request fix from then
		// kept none of the feedback it was fixing.
		if step := RetryStep[d.Step]; step != "prFix" {
			e.Step = step
		}
	}
	if !slices.Contains(e.Actions(), in.Action) {
		return escalationErr("bad_request", "%q is not a way to go on after %s; one of %s",
			in.Action, e.Reason, strings.Join(e.Actions(), ", "))
	}
	if in.Authorize != nil {
		if err := in.Authorize(); err != nil {
			return err
		}
	}
	note := strings.TrimSpace(in.Note)
	if in.Action == "resume" {
		// The Run that failed, taken back up now; the workflow waits on it.
		if err := ResumeKeptTx(ctx, tx, org, projectID, taskID, []string{e.RunID()}, note); err != nil {
			return err
		}
	} else if in.Action != "stop" {
		// Gone on past it: nothing will resume the Run that failed. (Stop
		// keeps it: the task can still be picked back up.)
		if err := ReleaseKeptTx(ctx, tx, taskID); err != nil {
			return err
		}
	}
	// Taken now, in the workflow's own state: a second decision is refused
	// from here on, before the workflow has acted on this one. One that
	// ended (from before decisions) is reopened to wait for it.
	e.Decided = &HumanDecision{Action: in.Action, Note: note, QuestionID: in.QuestionID, AnsweredBy: in.AnsweredBy,
		Conductor: in.Conductor}
	next, _ := json.Marshal(st)
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, status = CASE WHEN status = 'completed'
			THEN 'waiting'::workflow_run_status ELSE status END, step = 'decide',
			awaiting_signals = '["human.decision"]'::jsonb, wake_at = NULL, last_error = NULL
		WHERE id = $1`, wfID, next); err != nil {
		return err
	}
	if note != "" {
		if err := RecordDecisionTx(ctx, tx, org, taskID,
			"Delivery stopped ("+strings.ReplaceAll(e.Reason, "_", " ")+"). How should it go on?", note); err != nil {
			return err
		}
	}
	if err := closeEscalationQuestionsTx(ctx, tx, org, projectID, taskID, EscalationKey(wfID, st.Escalations), in); err != nil {
		return err
	}
	// A decided escalation waits on no one; the workflow then moves the
	// task as the decision says.
	if err := EndConductorWait(ctx, tx, org, projectID, taskID); err != nil {
		return err
	}
	payload := map[string]any{"reason": e.Reason, "action": in.Action, "note": note, "by": "person"}
	if in.QuestionID != "" {
		payload["questionId"], payload["answeredBy"] = in.QuestionID, in.AnsweredBy
	}
	if in.Conductor != "" {
		payload["by"] = "conductor"
	}
	if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvTaskDecided, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, ActorType: in.ActorType, ActorID: in.ActorID, Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: payload}); err != nil {
		return err
	}
	// Wakes the workflow to carry it out; the decision is in its state.
	return workflow.SignalTx(ctx, tx, org, wfID, SignalHumanDecision, nil, "")
}

// Events about escalations decided.
const (
	// A person (or the conductor, on the owner's answer) decided an
	// escalation. Payload: {reason, action, note, by, questionId?, answeredBy?}.
	EvTaskDecided = "task.decided"
	// A question was closed unanswered because what it asked was settled
	// elsewhere. Payload: {questionId, by: "decision", action}.
	EvQuestionClosed = "question.closed"
)

// closeEscalationQuestionsTx closes the conductor's questions about the
// escalation still open once it is decided — the banner, used while one
// was open — and tells the conductor, so no card is left waiting on an
// escalation that is over. A decision that came from an answer tells it
// what that answer decided.
func closeEscalationQuestionsTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID, key string, in EscalationDecision) error {
	rows, err := tx.Query(ctx, `UPDATE questions SET status = 'cancelled' WHERE task_id = $1 AND escalation = $2
		AND status = 'open' RETURNING id, run_id`, taskID, key)
	if err != nil {
		return err
	}
	closed, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, RunID string }])
	if err != nil {
		return err
	}
	for _, q := range closed {
		ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: q.RunID}
		if _, err := ledger.Append(ctx, tx, ref.Event(EvQuestionClosed, ledger.ActorSystem,
			map[string]any{"questionId": q.ID, "by": "decision", "action": in.Action})); err != nil {
			return err
		}
	}
	var line string
	switch {
	case in.Conductor != "":
		return nil
	case in.QuestionID != "":
		line = fmt.Sprintf("The owner's answer to your question %s decided the escalation: %s. The delivery goes on.",
			in.QuestionID, in.Action)
	case len(closed) > 0:
		line = fmt.Sprintf("The person decided the escalation on the banner: %s. Your question %s about it is closed. "+
			"The delivery goes on.", in.Action, closed[0].ID)
	default:
		return nil
	}
	_, err = RecordWakeTx(ctx, tx, org, taskID, "escalation", key+":decided", line)
	return err
}

// EscalationAnswerTx decides the task's escalation when the owner's answer
// to the conductor's question about it picks one of its choices: the
// action the choice names, the answer and the conductor's proposal its
// note. A free answer, or one to an escalation no longer waiting, decides
// nothing: it reaches the conductor, which may then decide_escalation.
func EscalationAnswerTx(ctx context.Context, tx pgx.Tx, org, taskID, questionID, actorType, actorID, person string) error {
	// The answer routes took the task's row before the question
	// (LockEscalationTx's order); taken again here, it is held already.
	if err := LockTaskTx(ctx, tx, taskID, nil); err != nil {
		return err
	}
	var key, prompt, answer string
	var options, actions []string
	var rawOpts, rawActions []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(escalation, ''), prompt, COALESCE(answer, ''), options, COALESCE(actions, '[]')
		FROM questions WHERE id = $1`, questionID).Scan(&key, &prompt, &answer, &rawOpts, &rawActions)
	if err != nil || key == "" {
		return err
	}
	_ = json.Unmarshal(rawOpts, &options)
	_ = json.Unmarshal(rawActions, &actions)
	i := slices.IndexFunc(options, func(o string) bool { return strings.EqualFold(strings.TrimSpace(o), strings.TrimSpace(answer)) })
	if i < 0 || i >= len(actions) {
		return nil
	}
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil || d == nil || !waitingOn(d, key) {
		return err
	}
	return DecideEscalationTx(ctx, tx, org, taskID, EscalationDecision{Action: actions[i],
		Note:      escalationNote(prompt, answer, ""),
		ActorType: actorType, ActorID: actorID, QuestionID: questionID, AnsweredBy: person})
}

// waitingOn says the delivery waits at decide on the escalation key names,
// undecided.
func waitingOn(d *Delivery, key string) bool {
	e := d.State.Escalation
	return d.Live() && d.Step == "decide" && e != nil && e.Decided == nil && EscalationKey(d.WorkflowID, d.State.Escalations) == key
}

// escalationNote is the note a decision on the conductor's question
// carries: what the conductor proposed, what the owner said, and the
// conductor's own word when it decided.
func escalationNote(proposal, answer, conductorNote string) string {
	parts := []string{"The conductor proposed: " + clip(oneLine(proposal), 1500), "The owner answered: " + clip(oneLine(answer), 1000)}
	if n := strings.TrimSpace(conductorNote); n != "" {
		parts = append(parts, "The conductor decided: "+clip(oneLine(n), 1000))
	}
	return strings.Join(parts, "\n")
}

// EscalationQuestion is what ask_person records for a conductor while its
// task's escalation waits on a person: the escalation it asks about and the
// action each choice stands for. "" (no escalation) for any other question;
// refused when choices do not each name one of the escalation's actions, and
// while an escalation is decided but not yet carried out. Checked under the
// escalation's locks (LockEscalationTx), held until the question is
// inserted in the same tx, so no decision lands between the check and it.
func EscalationQuestion(ctx context.Context, tx pgx.Tx, ref RunRef, choices, actions []string) (string, error) {
	_, d, err := LockEscalationTx(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	if d != nil && d.State.conducted() && d.Live() && d.State.Escalation != nil && d.State.Escalation.Decided != nil {
		return "", refusef("the escalation (%s) was just decided %s: %s. Ask nothing about it; the delivery carries it "+
			"out, and you are woken at the next decision", strings.ReplaceAll(d.State.Escalation.Reason, "_", " "),
			decidedBy(d.State.Escalation.Decided), d.State.Escalation.Decided.Action)
	}
	waiting := d != nil && d.State.conducted() && d.Live() && d.Step == "decide" && d.State.Escalation != nil
	if !waiting {
		if len(actions) > 0 {
			return "", refusef("actions are for a question about an escalation waiting on a person; there is none")
		}
		return "", nil
	}
	offered := d.State.Escalation.Actions()
	if len(choices) == 0 || len(actions) != len(choices) {
		return "", refusef("the task's escalation waits on a person: this question is its question. Offer choices, and "+
			"actions naming what each stands for, one per choice, each one of %s; the owner picking one decides the "+
			"escalation, as the banner does", listOr(offered))
	}
	for _, a := range actions {
		if !slices.Contains(offered, a) {
			return "", refusef("%q is not one of this escalation's actions: %s", a, listOr(offered))
		}
	}
	return EscalationKey(d.WorkflowID, d.State.Escalations), nil
}

// decidedBy says who took a decision, for a refusal.
func decidedBy(h *HumanDecision) string {
	switch {
	case h.Conductor != "":
		return "by the conductor " + h.Conductor + " (decide_escalation)"
	case h.QuestionID != "":
		return "by the owner's answer to question " + h.QuestionID
	}
	return "by a person on the banner"
}

// ConductDecideEscalation is decide_escalation: the conductor decides the
// escalation the delivery waits on, only on the task's owner's free answer
// to its question about this escalation.
func ConductDecideEscalation(ctx context.Context, tx pgx.Tx, ref RunRef, action, note string) (map[string]any, error) {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return nil, err
	}
	_, d, err := LockEscalationTx(ctx, tx, ref.TaskID)
	if err != nil {
		return nil, err
	}
	const banner = "a person decides it on the banner, or by answering your question about it (ask_person, the " +
		"escalation's actions as choices)"
	if d == nil || !d.State.conducted() || d.Step != "decide" || d.State.Escalation == nil || d.State.Escalation.Decided != nil || !d.Live() {
		return nil, refusef("no escalation waits on a decision now: decide_escalation is only for one the owner handed you "+
			"with a free answer; %s", banner)
	}
	key := EscalationKey(d.WorkflowID, d.State.Escalations)
	var qID, prompt, answer, by, asker string
	err = tx.QueryRow(ctx, `SELECT q.id, q.prompt, COALESCE(q.answer, ''), COALESCE(q.answered_by_person, ''), q.run_id
		FROM questions q WHERE q.task_id = $1 AND q.escalation = $2 AND q.status = 'answered'
		ORDER BY q.answered_at DESC, q.id DESC LIMIT 1`, ref.TaskID, key).Scan(&qID, &prompt, &answer, &by, &asker)
	if db.IsNotFound(err) {
		return nil, refusef("the owner has not answered your question about this escalation: only a person decides it. "+
			"Ask them, or end your turn; %s", banner)
	}
	if err != nil {
		return nil, err
	}
	// The answer handed the escalation to the conductor that asked: one
	// that took over since has not been heard by the owner.
	if asker != ref.RunID {
		return nil, refusef("the owner's answer (question %s) was to an earlier conductor's question, not yours: ask them "+
			"again yourself (ask_person, the escalation's actions as choices); %s", qID, banner)
	}
	owner, err := TaskOwner(ctx, tx, ref.TaskID)
	if err != nil {
		return nil, err
	}
	if owner != "" && by != owner {
		return nil, refusef("the answer to your question %s is not the task's owner's: only the owner hands you an "+
			"escalation; %s", qID, banner)
	}
	if !slices.Contains(d.State.Escalation.Actions(), action) {
		return nil, refusef("%q is not a way to go on after %s; one of %s", action, d.State.Escalation.Reason,
			listOr(d.State.Escalation.Actions()))
	}
	err = DecideEscalationTx(ctx, tx, ref.Org, ref.TaskID, EscalationDecision{Action: action,
		Note:      escalationNote(prompt, answer, note),
		ActorType: ledger.ActorAgent, ActorID: ref.RunID, QuestionID: qID, AnsweredBy: by, Conductor: ref.RunID})
	if e, ok := err.(EscalationError); ok {
		return nil, refusef("%s", e.Msg)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"decided": action, "questionId": qID,
		"next": "The delivery carries it out now, as the banner's would. You are woken at the next decision."}, nil
}

// TaskOwner is the task's owner: its first active person, "" for none.
func TaskOwner(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT p.id FROM task_people tp JOIN people p ON p.id = tp.person_id
		WHERE tp.task_id = $1 AND p.removed_at IS NULL ORDER BY tp.position, tp.person_id LIMIT 1`, taskID).Scan(&id)
	if db.IsNotFound(err) {
		return "", nil
	}
	return id, err
}

// ResumeKeptTx takes kept Runs back up: paused, for the syncer to resume as
// it resumes any (phases.Syncer.whilePaused), the agent's conversation and
// workspace as they stopped. Their next message is the note, if any, and —
// what it asks for having changed since they stopped — the task as it is
// now: an agent that goes on works to the task it has in mind.
func ResumeKeptTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, runIDs []string, note string) error {
	var title, goal string
	var criteria []byte
	var edited bool
	if err := tx.QueryRow(ctx, `SELECT title, goal, acceptance_criteria, EXISTS (SELECT 1 FROM events e
			WHERE e.task_id = t.id AND e.event_type = 'task.updated'
			  AND (e.payload ? 'title' OR e.payload ? 'goal' OR e.payload ? 'acceptanceCriteria')
			  AND e.occurred_at > (SELECT min(ended_at) FROM runs WHERE id = ANY($2)))
		FROM tasks t WHERE t.id = $1`, taskID, runIDs).Scan(&title, &goal, &criteria, &edited); err != nil {
		return err
	}
	if edited {
		note = strings.TrimSpace(changedTask(title, goal, criteria) + "\n\n" + note)
	}
	// Its push, if it had asked for one, is asked for again when its turn
	// ends: what it pushed then is not what it will have done by then.
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'paused', control = 'resume', control_requested_at = now(),
			control_reason = 'picked back up', lux_stop_reason = 'pause', keep = false, kept_until = NULL,
			ended_at = NULL, error = NULL, phase_notified_at = NULL, dude_pause = NULL, finishes = finishes + 1,
			push_request_id = NULL, push_result = NULL, turn_done_at = NULL, next_attempt_at = NULL
		WHERE id = ANY($1) AND `+KeptRun, runIDs)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(runIDs) {
		return escalationErr("not_kept", "what stopped is no longer kept to resume; try again or start over")
	}
	if note == "" {
		return nil
	}
	for _, id := range runIDs {
		if _, _, err := QueueDirective(ctx, tx, RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: id},
			Directive{Text: note, Scope: "run"}); err != nil {
			return err
		}
	}
	return nil
}

// changedTask tells a resumed agent what its task asks for now.
func changedTask(title, goal string, rawCriteria []byte) string {
	var criteria []string
	_ = json.Unmarshal(rawCriteria, &criteria)
	// A resume carries no images, so the changed text names them only.
	text := fmt.Sprintf("While you were stopped, the task was changed. Work to it as it is now.\n\n**%s**\n\n%s", title,
		strings.TrimSpace(ImagesAsText(goal)))
	if len(criteria) > 0 {
		text += "\n\nAcceptance criteria:\n" + CriteriaList(CriteriaImagesAsText(criteria))
	}
	return text
}

// ReleaseKeptTx lets a task's stopped Runs go — a retry or a start over
// took their work up afresh, and nothing will resume them — so the syncer
// cancels them now, kept already or not yet, rather than when their time
// is up.
func ReleaseKeptTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET keep = false, kept_until = now()
		WHERE task_id = $1 AND status IN ('aborted', 'failed') AND keep`, taskID)
	return err
}
