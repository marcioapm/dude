package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// The conductor's decisions, as its tools take them (agenttools): each in
// the caller's transaction, on the task's delivery locked for it, refused
// with a Refusal the conductor can act on.

// Refusal is a conductor's request declined, with what to do instead.
type Refusal struct{ Msg string }

func (r Refusal) Error() string { return r.Msg }

func refusef(format string, a ...any) error { return Refusal{fmt.Sprintf(format, a...)} }

// Delivery is a task's latest delivery; LoadDelivery locks it for a decision.
type Delivery struct {
	WorkflowID, Status, Step string
	State                    State
	TaskStatus               string
}

// LoadDelivery reads the task's latest delivery, its row locked for the
// rest of tx; nil when the task has none.
func LoadDelivery(ctx context.Context, tx pgx.Tx, taskID string) (*Delivery, error) {
	return readDelivery(ctx, tx, taskID, " FOR UPDATE OF w")
}

// ReadDelivery is LoadDelivery without the lock, to read.
func ReadDelivery(ctx context.Context, tx pgx.Tx, taskID string) (*Delivery, error) {
	return readDelivery(ctx, tx, taskID, "")
}

func readDelivery(ctx context.Context, tx pgx.Tx, taskID, lock string) (*Delivery, error) {
	var d Delivery
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT w.id, w.status::text, w.step, w.state, t.status::text
		FROM tasks t JOIN workflow_runs w ON w.task_id = t.id AND w.workflow_type = $2
		WHERE t.id = $1 ORDER BY w.created_at DESC LIMIT 1`+lock, taskID, WorkflowType).
		Scan(&d.WorkflowID, &d.Status, &d.Step, &raw, &d.TaskStatus)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &d.State); err != nil {
		return nil, fmt.Errorf("decode delivery state: %w", err)
	}
	return &d, nil
}

// Live says whether the delivery is still running or waiting.
func (d *Delivery) Live() bool { return d.Status == "running" || d.Status == "waiting" }

// Ended says the task was merged or closed (or stopped): its conductor
// reads and answers, and starts nothing.
func Ended(taskStatus string) bool {
	return taskStatus == "done" || taskStatus == "aborted" || taskStatus == "failed"
}

// parked loads the task's delivery and refuses unless the caller is the
// task's live conductor, its conductor takes the delivery's decisions,
// and the delivery waits on one now. The task's Chat lock, which ending
// and replacing a conductor take, serialises the check with them: a call
// from a conductor superseded since it was authenticated is refused.
func parked(ctx context.Context, tx pgx.Tx, ref RunRef) (*Delivery, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return nil, err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
		AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, refusef("you are no longer this task's conductor: another took over from you. Decide nothing")
	}
	taskID := ref.TaskID
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	var status string
	if d == nil {
		if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, taskID).Scan(&status); err != nil {
			return nil, err
		}
	} else {
		status = d.TaskStatus
	}
	switch {
	case Ended(status):
		return nil, refusef("this task is %s: you are read-only now and start nothing. Offer to record the change as a "+
			"follow-up task (create_task), linked to this one, and create it once the person agrees", status)
	case d == nil || !d.Live():
		return nil, refusef("this task has no delivery in progress to decide for")
	case !d.State.conducted():
		return nil, refusef("Deliver takes this task's decisions, not you: you are read-only. A person hands them to you " +
			"by writing in Chat; until then, answer and advise")
	case d.Step == "conductorDecision" && d.State.Decision != nil && d.State.Decision.Taken != nil:
		return nil, refusef("this decision was taken already (%s); you are woken at the next one", d.State.Decision.Taken.Action)
	case d.Status != "waiting" || d.Step != "conductorDecision" || d.State.Decision == nil:
		return nil, refusef("the delivery is not waiting on a decision now (it is at %s): you are woken when it is", d.Step)
	}
	return d, nil
}

// take records the conductor's decision in the delivery's state and wakes
// the workflow to carry it out.
func take(ctx context.Context, tx pgx.Tx, ref RunRef, d *Delivery, t Taken) error {
	t.By = ref.RunID
	d.State.Decision.Taken = &t
	raw, err := json.Marshal(d.State.Decision)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{decision}', $2::jsonb) WHERE id = $1`,
		d.WorkflowID, raw); err != nil {
		return err
	}
	payload := map[string]any{"point": d.State.Decision.Point, "action": t.Action}
	for k, v := range map[string]any{"phase": t.Phase, "categories": t.Categories, "findingIds": t.FindingIDs, "note": t.Note} {
		switch v := v.(type) {
		case string:
			if v != "" {
				payload[k] = v
			}
		case []string:
			if len(v) > 0 {
				payload[k] = v
			}
		}
	}
	if t.Action == "open_pull_request" {
		payload["draft"] = t.Draft
	}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvDecisionTaken, ledger.ActorAgent, payload)); err != nil {
		return err
	}
	return workflow.SignalTx(ctx, tx, ref.Org, d.WorkflowID, SignalConductorDecision, map[string]any{"action": t.Action}, "")
}

// StartPhase is start_phase: a phase Run (or a review's fan-out) the
// workflow creates from the task's head, as it creates its own.
type StartPhase struct {
	Phase      string
	Categories []string
	FindingIDs []string
	Note       string
}

// ConductStartPhase takes start_phase as the decision the delivery waits on.
func ConductStartPhase(ctx context.Context, tx pgx.Tx, ref RunRef, in StartPhase) (string, error) {
	d, err := parked(ctx, tx, ref)
	if err != nil {
		return "", err
	}
	p, st := d.State.Decision, &d.State
	if !slices.Contains(p.Phases(), in.Phase) {
		return "", refusef("%s cannot start at this decision (%s); it can start %s", in.Phase, pointLabel[p.Point], listOr(p.Phases()))
	}
	if len(in.Categories) > 0 && in.Phase != PhaseReview || len(in.FindingIDs) > 0 && in.Phase != PhaseFix {
		return "", refusef("categories are for review, findings for fix")
	}
	switch in.Phase {
	case PhaseReview:
		for _, c := range in.Categories {
			if !slices.Contains(reviewCategories(), c) {
				return "", refusef("no reviewer %q: the categories are %s", c, listOr(reviewCategories()))
			}
		}
		if err := reviewBound(st); err != nil {
			return "", err
		}
	case PhaseFix:
		if p.Point == PointPRFeedback {
			if err := prFixBound(ctx, tx, st); err != nil {
				return "", err
			}
			break
		}
		if err := reviewBound(st); err != nil {
			return "", err
		}
		ids, err := fixable(ctx, tx, st, in.FindingIDs)
		if err != nil {
			return "", err
		}
		in.FindingIDs = ids
	}
	if err := take(ctx, tx, ref, d, Taken{Action: "start_phase", Phase: in.Phase, Categories: in.Categories,
		FindingIDs: in.FindingIDs, Note: clip(strings.TrimSpace(in.Note), noteChars)}); err != nil {
		return "", err
	}
	return fmt.Sprintf("The delivery starts %s now, from the task's head. You are woken when it is done.", in.Phase), nil
}

// noteChars bounds what the conductor asks of a Run, as runs.conductor_note does.
const noteChars = 4000

// reviewBound refuses another review round, or a fix, once the policy's
// review iterations are spent, as the policy's loop would escalate.
func reviewBound(st *State) error {
	if st.Iteration >= st.Policy.MaxReviewIterations {
		return refusef("the review loop is at its bound: %d of %d rounds spent. Ask the person how to go on (ask_person)",
			st.Iteration, st.Policy.MaxReviewIterations)
	}
	return nil
}

// fixable is the findings a fix may be sent, of those named (all open ones
// for none): open, of this attempt, and short of the attempts per finding.
func fixable(ctx context.Context, tx pgx.Tx, st *State, named []string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, severity::text, status::text, fix_attempts FROM review_findings
		WHERE task_id = $1 AND `+thisAttempt+` ORDER BY created_at`, st.TaskID, st.Attempt)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowToStructByPos[FindingState])
	if err != nil {
		return nil, err
	}
	byID := map[string]FindingState{}
	var open []string
	for _, f := range all {
		byID[f.ID] = f
		if f.Status == "open" {
			open = append(open, f.ID)
		}
	}
	if len(named) == 0 {
		named = open
	}
	if len(named) == 0 {
		return nil, refusef("no open finding to fix")
	}
	limit := st.Policy.MaxAttemptsPerFinding + st.ExtraFixAttempts
	// The policy's checkpoint: a blocking finding still open after its last
	// allowed fix stops the loop until a review says whether that fix worked,
	// or the person decides. Another fix, of any finding, waits for that.
	for _, f := range all {
		if f.Status == "open" && f.FixAttempts >= limit && slices.Contains(st.Policy.BlockingSeverities, f.Severity) {
			return nil, refusef("%s is still open after %d fix attempts, the bound per finding: re-review (start_phase review) "+
				"or ask the person how to go on (ask_person) before another fix", f.ID, f.FixAttempts)
		}
	}
	for _, id := range named {
		f, ok := byID[id]
		switch {
		case !ok:
			return nil, refusef("%s is not a finding of this task's delivery (findings lists them)", id)
		case f.Status != "open":
			return nil, refusef("%s is %s, not open", id, f.Status)
		case f.FixAttempts >= limit:
			return nil, refusef("%s has had %d fix attempts, the bound per finding: ask the person how to go on (ask_person)",
				id, f.FixAttempts)
		}
	}
	return named, nil
}

// prFixBound refuses a pull request fix past the policy's rounds per
// review, or a pull request's fix rounds in all, as prFix would escalate.
func prFixBound(ctx context.Context, tx pgx.Tx, st *State) error {
	if st.PRIteration >= st.Policy.MaxPRFixIterations {
		return refusef("pull request fixes are at their bound: %d of %d this review round. Ask the person how to go on (ask_person)",
			st.PRIteration, st.Policy.MaxPRFixIterations)
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT settings FROM forge_credentials WHERE forge = 'github' LIMIT 1`).Scan(&raw)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	total := forge.ReadSettings(raw).FixRoundsPerPR
	for _, repo := range feedbackRepos(st.PRFeedback) {
		if total > 0 && st.PRFixes[repo] >= total {
			return refusef("the pull request in %s has had %d fix rounds, the organization's bound: ask the person how to go on (ask_person)",
				repo, st.PRFixes[repo])
		}
	}
	return nil
}

// Before the pull request, the conductor asks this, with these answers;
// only Open or Draft, at the head it was asked at, opens it.
const (
	GateQuestion = "Open the pull request?"
	GateOpen     = "Open"
	GateDraft    = "Draft"
)

// GateChoices are the gate question's answers.
var GateChoices = []string{GateOpen, GateDraft, "Show me the diff", "Another round"}

// ConductDecide is decide: the next step, asking the person, waiting on the
// pull request, or opening it.
func ConductDecide(ctx context.Context, tx pgx.Tx, ref RunRef, action, note string) (map[string]any, error) {
	d, err := parked(ctx, tx, ref)
	if err != nil {
		return nil, err
	}
	p, st := d.State.Decision, &d.State
	note = strings.TrimSpace(note)
	if !slices.Contains(p.Actions(), action) {
		return nil, refusef("%q is not a decision at %s; one of %s, or start_phase", action, pointLabel[p.Point], listOr(p.Actions()))
	}
	switch action {
	case "ask_person":
		return ask(ctx, tx, ref, d, note)
	case "next":
		switch p.Policy {
		case "review":
			if err := reviewBound(st); err != nil {
				return nil, err
			}
		case "prFix":
			if err := prFixBound(ctx, tx, st); err != nil {
				return nil, err
			}
		}
	case "open_pull_request":
		if err := untestedRefusal(ctx, tx, st); err != nil {
			return nil, err
		}
		draft, err := GateAnswer(ctx, tx, st)
		if err != nil {
			return nil, err
		}
		if err := AuthorizeGateTx(ctx, tx, d.WorkflowID, st, draft); err != nil {
			return nil, err
		}
		if err := take(ctx, tx, ref, d, Taken{Action: action, Note: clip(note, noteChars), Draft: draft}); err != nil {
			return nil, err
		}
		what := "a pull request"
		if draft {
			what = "a draft pull request"
		}
		return map[string]any{"decided": action, "next": "The delivery opens " + what + " now."}, nil
	}
	if err := take(ctx, tx, ref, d, Taken{Action: action, Note: clip(note, noteChars)}); err != nil {
		return nil, err
	}
	return map[string]any{"decided": action, "next": "The delivery goes on. You are woken at the next decision."}, nil
}

// ask is decide(ask_person): a question for the person, on the
// conductor's Run, while the delivery keeps waiting. Before the pull
// request it is the gate's question, at the task's head.
func ask(ctx context.Context, tx pgx.Tx, ref RunRef, d *Delivery, note string) (map[string]any, error) {
	if open, err := HasOpenQuestion(ctx, tx, ref.RunID); err != nil {
		return nil, err
	} else if open {
		return nil, refusef("you already have a question waiting for an answer: end your turn and wait for it")
	}
	if d.State.Decision.Point != PointBeforePR {
		if note == "" {
			return nil, refusef("say what to ask the person in the note")
		}
		id, err := AskTx(ctx, tx, ref, clip(note, noteChars), nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"questionId": id, "next": "End your turn now. The person's answer will be your next message."}, nil
	}
	prompt := GateQuestion
	if note != "" {
		prompt = clip(note, noteChars) + "\n\n" + GateQuestion
	}
	id, err := askGate(ctx, tx, ref, &d.State, prompt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"questionId": id, "choices": GateChoices,
		"next": "End your turn now. If the person answers Open or Draft, decide open_pull_request."}, nil
}

// askGate asks the pull request gate's question on the conductor's Run, at
// the heads the delivery is at; refused while the head is a conductor's
// commit nothing has reviewed.
func askGate(ctx context.Context, tx pgx.Tx, ref RunRef, st *State, prompt string) (string, error) {
	if err := untestedRefusal(ctx, tx, st); err != nil {
		return "", err
	}
	id, err := AskTx(ctx, tx, ref, prompt, GateChoices)
	if err != nil {
		return "", err
	}
	heads, _ := json.Marshal(nonNilMap(st.Heads))
	_, err = tx.Exec(ctx, `UPDATE questions SET pr_gate_heads = $2::jsonb WHERE id = $1`, id, heads)
	return id, err
}

// askGateTx asks the gate's question for a delivery Deliver holds at a gate
// the conductor entered, on the task's live conductor: unless one is open
// already, or the task has no live conductor (a confirmed hand-back then
// opens it). Its answer wakes the delivery (GateAnswered).
func askGateTx(ctx context.Context, tx pgx.Tx, org string, st *State) error {
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL
		AND status = 'open')`, st.TaskID).Scan(&open); err != nil || open {
		return err
	}
	// Not asked at an untested head: the step that parked goes to a review.
	if untested, err := UntestedConductorHeadTx(ctx, tx, st.TaskID, st.Heads); err != nil || untested {
		return err
	}
	var runID string
	err := tx.QueryRow(ctx, `SELECT r.id FROM runs r WHERE r.task_id = $1 AND `+LiveConductor+` AND NOT COALESCE(`+Ending+`, false)`,
		st.TaskID).Scan(&runID)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = askGate(ctx, tx, RunRef{Org: org, ProjectID: st.ProjectID, TaskID: st.TaskID, RunID: runID}, st,
		"Deliver is about to open the pull request, and the conductor had asked you first.\n\n"+GateQuestion)
	return err
}

// GateAnswered wakes the task's delivery once the gate's question is
// answered: one Deliver holds at the gate goes on with an Open or Draft.
func GateAnswered(ctx context.Context, tx pgx.Tx, org, questionID string) error {
	var taskID string
	err := tx.QueryRow(ctx, `SELECT task_id FROM questions WHERE id = $1 AND pr_gate_heads IS NOT NULL`, questionID).Scan(&taskID)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil || d == nil || !d.AtGate() {
		return err
	}
	return workflow.SignalTx(ctx, tx, org, d.WorkflowID, SignalConductorDecision, map[string]any{"action": "answered"}, "")
}

// AtGate says the delivery is parked at the pull request gate, untaken.
func (d *Delivery) AtGate() bool {
	p := d.State.Decision
	return d.Step == "conductorDecision" && p != nil && p.Taken == nil && p.Point == PointBeforePR
}

// GateAnswer reads the latest gate question at the current heads. Open and
// Draft authorize the opening; other answers return a Refusal. It writes nothing.
func GateAnswer(ctx context.Context, tx pgx.Tx, st *State) (draft bool, err error) {
	var status, answer string
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT status::text, COALESCE(answer, ''), pr_gate_heads FROM questions
		WHERE task_id = $1 AND pr_gate_heads IS NOT NULL ORDER BY asked_at DESC, id DESC LIMIT 1`, st.TaskID).
		Scan(&status, &answer, &raw)
	if db.IsNotFound(err) {
		return false, refusef("the person has not been asked: decide ask_person first, which asks them %q", GateQuestion)
	}
	if err != nil {
		return false, err
	}
	var heads map[string]string
	_ = json.Unmarshal(raw, &heads)
	switch said := strings.ToLower(strings.TrimSpace(answer)); {
	case status != "answered":
		return false, refusef("the person has not answered %q yet: end your turn and wait", GateQuestion)
	case !maps.Equal(heads, nonNilMap(st.Heads)):
		return false, refusef("the person answered at an earlier head; the work has moved since. Ask again (decide ask_person)")
	case said == strings.ToLower(GateOpen):
		return false, nil
	case said == strings.ToLower(GateDraft):
		return true, nil
	default:
		return false, refusef("the person answered %q, not Open or Draft: do what they asked", clip(oneLine(answer), 80))
	}
}

// ConductDismiss is dismiss_finding: an open finding of the task left as
// it is, with the reason, as a person accepting it is.
func ConductDismiss(ctx context.Context, tx pgx.Tx, ref RunRef, findingID, reason string) error {
	if _, err := parked(ctx, tx, ref); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return refusef("say why it is dismissed: the reason is shown with the finding")
	}
	var title, status string
	err := tx.QueryRow(ctx, `SELECT title, status::text FROM review_findings WHERE id = $1 AND task_id = $2 FOR UPDATE`,
		findingID, ref.TaskID).Scan(&title, &status)
	if db.IsNotFound(err) {
		return refusef("%s is not a finding of this task (findings lists them)", findingID)
	}
	if err != nil {
		return err
	}
	if status != "open" {
		return refusef("%s is %s already", findingID, status)
	}
	note := DismissedByConductor + clip(oneLine(reason), 1000)
	if _, err := tx.Exec(ctx, `UPDATE review_findings SET status = 'accepted', resolution_note = $2, updated_at = now()
		WHERE id = $1`, findingID, note); err != nil {
		return err
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvFindingResolved, ledger.ActorAgent,
		map[string]any{"findingId": findingID, "status": "accepted", "note": note, "title": title, "by": "conductor"}))
	return err
}

// EvFindingResolved is a finding's status changed by hand: a person's, or
// the conductor's dismissal.
const EvFindingResolved = "review.finding_resolved"

// DismissedByConductor begins the resolution note of a finding the
// conductor dismissed: accepted, by the conductor and not a person.
const DismissedByConductor = "Dismissed by the conductor: "

// AcceptedBy says who left an accepted finding as it is, from its
// resolution note: the conductor, with its reason, or a person.
func AcceptedBy(note string) string {
	if reason, ok := strings.CutPrefix(note, DismissedByConductor); ok {
		return "dismissed by the conductor: " + clip(reason, 120)
	}
	return "accepted by a person"
}

// TaskSpec is update_task: a goal, acceptance criteria, or both; nil
// leaves one as it is.
type TaskSpec struct {
	Goal     *string
	Criteria []string
	// Criteria given (an empty list clears them).
	HasCriteria bool
}

// ConductUpdateTask is update_task: what Chat settled, written into the
// task before the implementer is started, so its prompt has it.
func ConductUpdateTask(ctx context.Context, tx pgx.Tx, ref RunRef, in TaskSpec) error {
	d, err := parked(ctx, tx, ref)
	if err != nil {
		return err
	}
	var started bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND phase = 'implement'
		AND attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1)))`,
		ref.TaskID, d.State.Attempt).Scan(&started); err != nil {
		return err
	}
	if started {
		return refusef("an implementer has started on this attempt: the task's text is what it was given. Record what " +
			"changed as a decision with the person (ask_person), or as a follow-up task")
	}
	var goal string
	var criteria []string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT goal, acceptance_criteria FROM tasks WHERE id = $1 FOR UPDATE`, ref.TaskID).
		Scan(&goal, &raw); err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &criteria)
	before := map[string]any{"goal": goal, "acceptanceCriteria": db.NonNil(criteria)}
	after := map[string]any{}
	if in.Goal != nil {
		goal = strings.TrimSpace(*in.Goal)
		after["goal"] = goal
	}
	if in.HasCriteria {
		criteria = in.Criteria
		after["acceptanceCriteria"] = db.NonNil(criteria)
	}
	if len(after) == 0 {
		return refusef("give a goal, acceptance criteria, or both")
	}
	next, _ := json.Marshal(db.NonNil(criteria))
	if _, err := tx.Exec(ctx, `UPDATE tasks SET goal = $2, acceptance_criteria = $3::jsonb, updated_at = now() WHERE id = $1`,
		ref.TaskID, goal, next); err != nil {
		return err
	}
	// The images it shows follow the text, as on a person's edit.
	if err := SyncTaskImagesTx(ctx, tx, ref.TaskID, goal, criteria); err != nil {
		var refused AttachmentError
		if errors.As(err, &refused) {
			return refusef("%s; nothing was saved", refused.Message)
		}
		return err
	}
	// The shape a person's edit records (the fields changed, as they are
	// now), and what they were.
	payload := maps.Clone(after)
	payload["before"], payload["by"] = before, "conductor"
	_, err = ledger.Append(ctx, tx, ref.Event(EvTaskUpdated, ledger.ActorAgent, payload))
	return err
}

// EvTaskUpdated is a task's text changed.
const EvTaskUpdated = "task.updated"
