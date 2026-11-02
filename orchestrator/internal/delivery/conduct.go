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
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// The conductor's decisions, as its tools take them (agenttools): each in
// the caller's transaction, on the task's delivery locked for it, refused
// with a Refusal the conductor can act on.

// Refusal is a conductor's request declined, with what to do instead.
type Refusal struct {
	Msg string
	// A start refused as blocked (path 4): the caller records it
	// (task.start_refused) after the refused call rolls back.
	Blocked *BlockedError
	// A start refused as archived or closed (path 4), recorded likewise.
	Lifecycle string
	// An event the caller records, by the calling Run, after the refused
	// call rolls back: what the refusal changed (a rerun's claim released).
	Event *RefusalEvent
}

// RefusalEvent is a Refusal's event: its type and payload.
type RefusalEvent struct {
	Type    string
	Payload map[string]any
}

func (r Refusal) Error() string { return r.Msg }

func refusef(format string, a ...any) error { return Refusal{Msg: fmt.Sprintf(format, a...)} }

// refuseText is refusef with a message that has no verbs to fill.
func refuseText(msg string) error { return Refusal{Msg: msg} }

// IsRefusal says whether err is a Refusal.
func IsRefusal(err error) bool { return errors.As(err, new(Refusal)) }

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
	return scanDelivery(tx.QueryRow(ctx, `SELECT w.id, w.status::text, w.step, w.state, t.status::text
		FROM tasks t JOIN workflow_runs w ON w.task_id = t.id AND w.workflow_type = $2
		WHERE t.id = $1 ORDER BY w.created_at DESC LIMIT 1`+lock, taskID, WorkflowType))
}

// loadDeliveryByID is LoadDelivery for one delivery of the task, latest or
// not; nil when the task has none by that id.
func loadDeliveryByID(ctx context.Context, tx pgx.Tx, taskID, workflowID string) (*Delivery, error) {
	return scanDelivery(tx.QueryRow(ctx, `SELECT w.id, w.status::text, w.step, w.state, t.status::text
		FROM tasks t JOIN workflow_runs w ON w.task_id = t.id AND w.workflow_type = $3
		WHERE t.id = $1 AND w.id = $2 FOR UPDATE OF w`, taskID, workflowID, WorkflowType))
}

func scanDelivery(row pgx.Row) (*Delivery, error) {
	var d Delivery
	var raw []byte
	err := row.Scan(&d.WorkflowID, &d.Status, &d.Step, &raw, &d.TaskStatus)
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
	if err := liveConductor(ctx, tx, ref); err != nil {
		return nil, err
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
	if Ended(status) && d != nil && d.State.Decision != nil && d.State.Decision.Point == PointStart {
		// Path 4 on a task a person closed or archived while it waited at
		// its start: the rule's words, recorded as a refused start.
		if err := refuseLifecycleStart(ctx, tx, taskID); err != nil {
			return nil, err
		}
	}
	switch {
	case Ended(status):
		return nil, refusef(rThisTaskYouAreReadOnly, status)
	case d == nil || !d.Live():
		return nil, refuseText(rThisTaskHasNoDeliveryIn)
	case !d.State.conducted():
		return nil, refuseText(rDeliverTakesThisTaskDecisionsNot)
	case d.Step == "conductorDecision" && d.State.Decision != nil && d.State.Decision.Taken != nil:
		return nil, refusef(rThisDecisionWasTakenAlreadyYou, d.State.Decision.Taken.Action)
	case d.Step == "decide" && d.State.Escalation != nil:
		return nil, refusef(rDeliveryStoppedForPersonAtEscalation, strings.ReplaceAll(d.State.Escalation.Reason, "_", " "))
	case d.Status != "waiting" || d.Step != "conductorDecision" || d.State.Decision == nil:
		return nil, refusef(rDeliveryNotWaitingOnDecisionNow, d.Step)
	}
	return d, nil
}

// liveConductor refuses unless the caller is the task's live conductor. The
// task's Chat lock, which ending and replacing a conductor take, serialises
// the check with them: a call from a conductor superseded since it was
// authenticated is refused.
func liveConductor(ctx context.Context, tx pgx.Tx, ref RunRef) error {
	return liveConductorOr(ctx, tx, ref, rYouAreNoLongerThisTask)
}

// liveConductorOr is liveConductor refusing with refusal, a tool's own
// words for "another took over".
func liveConductorOr(ctx context.Context, tx pgx.Tx, ref RunRef, refusal string) error {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
		AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
		return err
	}
	if !live {
		return refuseText(refusal)
	}
	return nil
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
	if t.Then != nil {
		then := map[string]any{"review": t.Then.Review, "wake": t.Then.Wake}
		if t.Then.Note != "" {
			then["note"] = t.Then.Note
		}
		payload["then"] = then
	}
	if t.Round != nil {
		round := map[string]any{}
		if t.Round.Scope != "" {
			round["scope"] = t.Round.Scope
		}
		if t.Round.Bar != "" {
			round["bar"] = t.Round.Bar
		}
		payload["round"] = round
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
	// fix or simplify: the review to start once it has committed.
	Then *Then
	// review, fix or simplify: the round's scope and bar.
	Round *RoundAsk
}

// barChars bounds the conductor's bar for a round.
const barChars = 1000

// pipelineOf checks start_phase's then and round, and is what the decision
// carries of them: then only with fix or simplify, naming known reviewers;
// round with review, fix or simplify.
func pipelineOf(in StartPhase) (*Pipeline, *RoundAsk, error) {
	var round *RoundAsk
	if in.Round != nil {
		switch in.Phase {
		case PhaseReview, PhaseFix, PhaseSimplify:
		default:
			return nil, nil, refusef(rRoundIsForReviewFixOrSimplify, in.Phase)
		}
		scope := strings.TrimSpace(in.Round.Scope)
		if scope != "" && scope != ScopeWhole && scope != ScopeFix {
			return nil, nil, refusef(rRoundScopeIsOneOf, scope)
		}
		round = &RoundAsk{Scope: scope, Bar: clip(strings.TrimSpace(in.Round.Bar), barChars)}
		if round.Scope == "" && round.Bar == "" {
			round = nil
		}
	}
	if in.Then == nil {
		return nil, round, nil
	}
	if in.Phase != PhaseFix && in.Phase != PhaseSimplify {
		return nil, nil, refusef(rThenIsForFixOrSimplify, in.Phase)
	}
	if len(in.Then.Review) == 0 {
		return nil, nil, refuseText(rThenNamesTheReview)
	}
	for _, c := range in.Then.Review {
		if !slices.Contains(reviewCategories(), c) {
			return nil, nil, refusef(rNoReviewerCategoriesAre, c, listOr(reviewCategories()))
		}
	}
	wake := strings.TrimSpace(in.Then.Wake)
	switch wake {
	case "":
		wake = WakeAfterReview
	case WakeAfterReview, WakeAfterFix:
	default:
		return nil, nil, refusef(rThenWakeIsOneOf, wake)
	}
	p := &Pipeline{Review: slices.Clone(in.Then.Review), Wake: wake,
		Note: clip(strings.TrimSpace(in.Then.Note), noteChars)}
	if round != nil {
		p.Scope, p.Bar = round.Scope, round.Bar
	}
	return p, round, nil
}

// ConductStartPhase takes start_phase as the decision the delivery waits on.
func ConductStartPhase(ctx context.Context, tx pgx.Tx, ref RunRef, in StartPhase) (string, error) {
	d, err := parked(ctx, tx, ref)
	if errors.As(err, new(Refusal)) {
		// Not at a decision: it may be waiting on its pull requests.
		if w, werr := ReadDelivery(ctx, tx, ref.TaskID); werr != nil {
			return "", werr
		} else if w != nil && w.waitingOnPR() && w.State.conducted() && !Ended(w.TaskStatus) {
			return startAtPRWait(ctx, tx, ref, w, in)
		}
	}
	if err != nil {
		return "", err
	}
	p, st := d.State.Decision, &d.State
	if !slices.Contains(p.Phases(), in.Phase) {
		return "", refusef(rCannotStartAtThisDecisionCan, in.Phase, pointLabel[p.Point], listOr(p.Phases()))
	}
	if p.Point == PointStart {
		if err := refuseBlockedStart(ctx, tx, ref, st); err != nil {
			return "", err
		}
	}
	if Publishes[in.Phase] {
		if err := RefuseWhileMovingTx(ctx, tx, ref.TaskID); errors.Is(err, ErrPublishMoving) {
			return "", refusef(rYourPublishMovingTaskBranchNow, in.Phase)
		} else if err != nil {
			return "", err
		}
		if st.Policy.DirectFixes() && p.Point != PointPRFeedback {
			// No second writer past an unreviewed push of dude's on the
			// pull request (I-3), as after a fix with then.wake after_fix.
			// Not at the pull request's feedback (or conflict): there the
			// fix it starts reviews that push first (prFix's reviewFirst).
			if err := refusePullRequestOpenTx(ctx, tx, ref.TaskID, d); err != nil {
				return "", err
			}
		}
	}
	if len(in.Categories) > 0 && in.Phase != PhaseReview || len(in.FindingIDs) > 0 && in.Phase != PhaseFix {
		return "", refuseText(rCategoriesAreForReviewFindingsFor)
	}
	then, round, err := pipelineOf(in)
	if err != nil {
		return "", err
	}
	switch in.Phase {
	case PhaseReview:
		for _, c := range in.Categories {
			if !slices.Contains(reviewCategories(), c) {
				return "", refusef(rNoReviewerCategoriesAre, c, listOr(reviewCategories()))
			}
		}
	case PhaseFix:
		if p.Point == PointPRFeedback {
			if err := prFixBound(ctx, tx, st); err != nil {
				return "", err
			}
			break
		}
		ids, err := fixable(ctx, tx, st, in.FindingIDs)
		if err != nil {
			return "", err
		}
		in.FindingIDs = ids
	}
	if err := take(ctx, tx, ref, d, Taken{Action: "start_phase", Phase: in.Phase, Categories: in.Categories,
		FindingIDs: in.FindingIDs, Note: clip(strings.TrimSpace(in.Note), noteChars), Then: then, Round: round}); err != nil {
		return "", err
	}
	return render(conductorStartsTmpl, in.Phase), nil
}

// noteChars bounds what the conductor asks of a Run, as runs.conductor_note does.
const noteChars = 4000

// fixable is the findings a fix may be sent, of those named (all open ones
// for none): of this attempt and open. No counter refuses one: how many
// rounds or fixes a finding gets is the conductor's judgement, with the
// round wake's facts (playbook).
func fixable(ctx context.Context, tx pgx.Tx, st *State, named []string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, status::text FROM review_findings
		WHERE task_id = $1 AND `+thisAttempt+` ORDER BY created_at`, st.TaskID, st.Attempt)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (f FindingState, err error) {
		err = row.Scan(&f.ID, &f.Status)
		return
	})
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
		// A fix for the people's feedback alone: every open item goes with it.
		var people int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM feedback_items f WHERE `+openItems, st.TaskID, st.Attempt).
			Scan(&people); err != nil {
			return nil, err
		}
		if people > 0 {
			return nil, nil
		}
		return nil, refuseText(rNoOpenFindingOrFeedbackItem)
	}
	for _, id := range named {
		f, ok := byID[id]
		switch {
		case !ok:
			return nil, refusef(rNotFindingThisTaskDeliveryFindings, id)
		case f.Status != "open":
			return nil, refusef(rNotOpen, id, f.Status)
		}
	}
	return named, nil
}

// prFixBound refuses a pull request fix past the policy's rounds per
// review, or a pull request's fix rounds in all, as prFix would escalate.
func prFixBound(ctx context.Context, tx pgx.Tx, st *State) error {
	return prFixBoundFor(ctx, tx, st, feedbackRepos(st.PRFeedback))
}

// prFixBoundFor is prFixBound for a fix of the pull requests in repos.
func prFixBoundFor(ctx context.Context, tx pgx.Tx, st *State, repos []string) error {
	if st.RoundFixesSpent() {
		return refusef(rPullRequestFixesAreAtTheir,
			st.PRIteration, st.RoundFixLimit())
	}
	settings, err := GitHubSettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	total := settings.FixRoundsPerPR
	for _, repo := range repos {
		if st.PullRequestFixesSpent(repo, total) {
			return refusef(rPullRequestInHasHadFix,
				repo, st.PRFixes[repo])
		}
	}
	return nil
}

// Before the pull request, the conductor asks this, with these answers
// (results/gate-choices, one a line, Open and Draft first); only Open or
// Draft, at the head it was asked at, opens it.
const GateQuestion = "Open the pull request?"

var (
	GateChoices = gateChoices(render(mustTemplate("results/gate-choices"), nil))
	GateOpen    = GateChoices[0]
	GateDraft   = GateChoices[1]
)

// gateChoices are the gate's answers, one a line: Open, Draft, then the
// rest. Fewer than two panics at init.
func gateChoices(text string) []string {
	choices := strings.Split(text, "\n")
	if len(choices) < 2 {
		panic("agents/results/gate-choices.md needs Open and Draft, one a line")
	}
	return choices
}

// What decide answers the conductor once the delivery goes on, or once it
// has asked the person.
var (
	conductorGoesOn    = render(mustTemplate("contracts/conductor-goes-on"), nil)
	conductorAsked     = render(mustTemplate("contracts/conductor-asked"), nil)
	conductorAskedGate = render(mustTemplate("contracts/conductor-asked-gate"), nil)
)

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
		return nil, refusef(rNotDecisionAtOneOrStart, action, pointLabel[p.Point], listOr(p.Actions()))
	}
	switch action {
	case "ask_person":
		return ask(ctx, tx, ref, d, note)
	case "next":
		if p.Point == PointStart {
			if err := refuseBlockedStart(ctx, tx, ref, st); err != nil {
				return nil, err
			}
		}
		if p.Policy == "prFix" {
			if err := prFixBound(ctx, tx, st); err != nil {
				return nil, err
			}
		}
	case "open_pull_request":
		if err := untestedRefusal(ctx, tx, st); err != nil {
			return nil, err
		}
		if n, err := BlockingFeedbackTx(ctx, tx, st.TaskID, st.Attempt); err != nil {
			return nil, err
		} else if n > 0 {
			return nil, refusef(rFeedbackItemsFromPeopleAreNot, n)
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
		return map[string]any{"decided": action, "next": render(conductorOpensTmpl, draft)}, nil
	}
	if err := take(ctx, tx, ref, d, Taken{Action: action, Note: clip(note, noteChars)}); err != nil {
		return nil, err
	}
	return map[string]any{"decided": action, "next": conductorGoesOn}, nil
}

// ask is decide(ask_person): a question for the person, on the
// conductor's Run, while the delivery keeps waiting. Before the pull
// request it is the gate's question, at the task's head.
func ask(ctx context.Context, tx pgx.Tx, ref RunRef, d *Delivery, note string) (map[string]any, error) {
	if open, err := HasOpenQuestion(ctx, tx, ref.RunID); err != nil {
		return nil, err
	} else if open {
		return nil, refuseText(rYouAlreadyHaveQuestionWaitingFor)
	}
	if d.State.Decision.Point != PointBeforePR {
		if note == "" {
			return nil, refuseText(rSayWhatAskPersonInNote)
		}
		id, err := AskTx(ctx, tx, ref, clip(note, noteChars), nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"questionId": id, "next": conductorAsked}, nil
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
		"next": conductorAskedGate}, nil
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
	if err != nil || d == nil {
		return err
	}
	// An Open or Draft answer is the person's publication request: its author is frozen now.
	if _, err := GateAnswer(ctx, tx, &d.State); err == nil {
		if err := FreezePublicationTx(ctx, tx, d.State.TaskID, d.State.Branch); err != nil {
			return err
		}
	} else {
		var refused Refusal
		if !errors.As(err, &refused) {
			return err
		}
	}
	if !d.AtGate() {
		return nil
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
		return false, refusef(rPersonHasNotBeenAskedDecide, GateQuestion)
	}
	if err != nil {
		return false, err
	}
	var heads map[string]string
	_ = json.Unmarshal(raw, &heads)
	switch said := strings.ToLower(strings.TrimSpace(answer)); {
	case status != "answered":
		return false, refusef(rPersonHasNotAnsweredYetEnd, GateQuestion)
	case !maps.Equal(heads, nonNilMap(st.Heads)):
		return false, refuseText(rPersonAnsweredAtEarlierHeadWork)
	case said == strings.ToLower(GateOpen):
		return false, nil
	case said == strings.ToLower(GateDraft):
		return true, nil
	default:
		return false, refusef(rPersonAnsweredNotOpenOrDraft, clip(oneLine(answer), 80))
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
		return refuseText(rSayWhyDismissedReasonShownWith)
	}
	var title, status string
	err := tx.QueryRow(ctx, `SELECT title, status::text FROM review_findings WHERE id = $1 AND task_id = $2 FOR UPDATE`,
		findingID, ref.TaskID).Scan(&title, &status)
	if db.IsNotFound(err) {
		return refusef(rNotFindingThisTaskFindingsLists, findingID)
	}
	if err != nil {
		return err
	}
	if status != "open" {
		return refusef(rAlready, findingID, status)
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

// EvReviewQuestionRaised is a reviewer's question stored (review_questions),
// once per question.
const EvReviewQuestionRaised = "review.question_raised"

// A reviewer's open question answered or withdrawn, once per transition.
// Payload: {questionId, by: person|conductor, answer} or {..., reason}.
const (
	EvReviewQuestionAnswered  = "review.question_answered"
	EvReviewQuestionWithdrawn = "review.question_withdrawn"
)

// DismissedByConductor begins the resolution note of a finding the
// conductor dismissed: accepted, by the conductor and not a person.
const DismissedByConductor = "Dismissed by the conductor: "

// AcceptedBy says who left an accepted finding as it is, from its
// resolution note: the conductor, with its reason, or a person.
func AcceptedBy(note string) string {
	reason, conductor := strings.CutPrefix(note, DismissedByConductor)
	return render(findingAcceptedTmpl, struct {
		Conductor bool
		Reason    string
	}{conductor, reason})
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
		return refuseText(rImplementerHasStartedOnThisAttempt)
	}
	var goal string
	var criteria []string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT goal, acceptance_criteria FROM tasks WHERE id = $1 FOR NO KEY UPDATE`, ref.TaskID).
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
		return refuseText(rGiveGoalAcceptanceCriteriaOrBoth)
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
			return refusef(rNothingWasSaved, refused.Message)
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
