package delivery

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ConductorQuestionInsertSQL inserts only a conductor-addressed row. The
// structural person-route guard pins both its column and literal recipient.
const ConductorQuestionInsertSQL = `INSERT INTO questions(id,organization_id,task_id,run_id,prompt,options,items,addressee) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,'conductor')`

// AskConductorTx routes a normalized writer question without putting the task
// on hold for a person. The bool reports the actual recipient, including the
// exact existing ToPersonTx fallback for policy or unavailable conductors.
func AskConductorTx(ctx context.Context, tx pgx.Tx, ref RunRef, items []QuestionItem) (string, bool, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return "", false, err
	}
	d, err := LoadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return "", false, err
	}
	if err := LockTaskTx(ctx, tx, ref.TaskID); err != nil {
		return "", false, err
	}
	halted, err := HaltedTx(ctx, tx, ref.TaskID)
	if err != nil {
		return "", false, err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id=$1`, ref.TaskID).Scan(&status); err != nil {
		return "", false, err
	}
	if halted || Ended(status) {
		return "", false, refuseText(rQuestionRequiresCurrentRun)
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=$1 AND task_id=$2 AND status IN ('pending','scheduled','starting','running') AND attempt=task_attempt($2))`, ref.RunID, ref.TaskID).Scan(&current); err != nil {
		return "", false, err
	}
	if !current {
		return "", false, refuseText(rQuestionRequiresCurrentRun)
	}
	if open, err := HasOpenQuestion(ctx, tx, ref.RunID); err != nil {
		return "", false, err
	} else if open {
		return "", false, refuseText(rQuestionRequiresCurrentRun)
	}
	unavailable, err := conductorUnavailableTx(ctx, tx, ref.TaskID, nil)
	if err != nil {
		return "", false, err
	}
	if d == nil || !d.Live() || !d.State.conducted() || unavailable {
		id, err := ToPersonTx(ctx, tx, ToPerson{Ref: ref, Items: items, Why: WhyNoConductor})
		return id, false, err
	}
	id := ids.New(ids.Question)
	prompt, options := AskPrompt(items), AskOptions(items)
	raw, _ := json.Marshal(items)
	opts, _ := json.Marshal(options)
	if _, err := tx.Exec(ctx, ConductorQuestionInsertSQL, id, ref.Org, ref.TaskID, ref.RunID, prompt, opts, raw); err != nil {
		return "", false, err
	}
	var phase string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(phase::text,'agent') FROM runs WHERE id=$1`, ref.RunID).Scan(&phase); err != nil {
		return "", false, err
	}
	// ExternalText fences the bounded excerpt; the complete items are in run_chat.
	line := render(mustTemplate("wakes/writer-asks"), struct{ Phase, RunID, ID, Prompt string }{phase, ref.RunID, id, ExternalText(ref.RunID, prompt)})
	if _, err := RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, "question", "question:"+id, line); err != nil {
		return "", false, err
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvQuestionRouted, ledger.ActorAgent, map[string]any{"kind": "agent", "questionId": id, "prompt": prompt, "options": options, "items": items, "addressee": "conductor"}))
	return id, true, err
}

// ConductAnswerQuestion settles only a current writer's conductor-addressed
// question. Chat, delivery and task locks precede question/run locks, as on
// the owner's answer path. Closing the row and queuing its directive are atomic.
func ConductAnswerQuestion(ctx context.Context, tx pgx.Tx, ref RunRef, id, answer string, answers []ItemAnswer) (string, error) {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return "", err
	}
	d, err := LoadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	if err := LockTaskTx(ctx, tx, ref.TaskID); err != nil {
		return "", err
	}
	halted, err := HaltedTx(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	if d == nil || !d.Live() || !d.State.conducted() || Ended(d.TaskStatus) || halted {
		return "", refuseText(rQuestionRequiresCurrentRun)
	}
	var writer, prompt string
	var items []QuestionItem
	err = tx.QueryRow(ctx, `SELECT q.run_id,q.prompt,q.items FROM questions q JOIN runs r ON r.id=q.run_id
 WHERE q.id=$1 AND q.task_id=$2 AND q.organization_id=$3 AND q.status='open' AND q.addressee='conductor'
 AND r.task_id=q.task_id AND r.attempt=task_attempt(q.task_id) AND r.role<>'conductor'
 AND r.status IN ('running','paused')
 FOR UPDATE OF q,r`, id, ref.TaskID, ref.Org).Scan(&writer, &prompt, &items)
	if err != nil {
		if db.IsNotFound(err) {
			return "", refuseText(rQuestionRequiresCurrentRun)
		}
		return "", err
	}
	if answers != nil && strings.TrimSpace(answer) != "" {
		return "", refuseText(rQuestionRequiresCurrentRun)
	}
	if answers == nil {
		answers = []ItemAnswer{{Text: answer}}
	}
	a, err := CheckAnswers(items, answers, "")
	if err != nil {
		return "", refuseText(err.Error())
	}
	raw, _ := json.Marshal(a.Answers)
	if _, err := tx.Exec(ctx, `UPDATE questions SET status='answered',answer=$2,answers=$3::jsonb,answered_at=now(),answered_by_run=$4 WHERE id=$1 AND status='open'`, id, a.Text, raw, ref.RunID); err != nil {
		return "", err
	}
	writerRef := ref
	writerRef.RunID = writer
	directive, _, err := QueueDirective(ctx, tx, writerRef, Directive{Text: "Your conductor answered.\n" + AnswerDirective(prompt, items, a, "Your conductor", false), Scope: "run"})
	if err != nil {
		return "", err
	}
	event := writerRef.Event("question.answered", ledger.ActorAgent, map[string]any{"questionId": id, "answer": a.Text, "answers": a.Answers, "directiveId": directive, "by": "conductor", "askingRunId": writer})
	event.ActorID = ref.RunID
	_, err = ledger.Append(ctx, tx, event)
	return directive, err
}
