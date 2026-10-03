package delivery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Who takes a delivery's decisions (State.Decider): the policy's fixed
// rules (Deliver), or the task's conductor, in Chat with its people.
const (
	DeciderPolicy    = "policy"
	DeciderConductor = "conductor"
)

// SignalConductorDecision wakes a workflow parked on a decision once its
// conductor took it, or decisions went back to the policy. The decision is
// in the workflow's state (Decision.Taken); the signal only wakes it.
const SignalConductorDecision = "conductor.decision"

// The decision points: where the policy applies a fixed rule, and under
// decider conductor the workflow parks for the conductor instead.
const (
	PointStart       = "start"
	PointImplemented = "after_implement"
	PointReviewed    = "after_review"
	PointFixed       = "after_fix"
	PointBeforePR    = "before_pull_request"
	PointPRFeedback  = "pull_request_feedback"
)

// Events about who decides and what was decided.
const (
	// Who decides changed. Payload: {from, to, why}.
	EvDeciderChanged = "task.decider_changed"
	// The workflow parked on a decision for the conductor. Payload: {point,
	// policy (what the policy would do), actions, phases}.
	EvDecisionAwaited = "conductor.decision_awaited"
	// The conductor took the decision the workflow waited on. Payload:
	// {point, action, phase?, categories?, findingIds?, note?, draft?}.
	EvDecisionTaken = "conductor.decided"
	// dude woke the task's conductor with a note. Payload: {text, reasons,
	// directiveId}.
	EvConductorWoken = "conductor.woken"
)

// Pending is the decision a workflow parked for its conductor, and once
// taken, what was decided.
type Pending struct {
	Point string `json:"point"`
	// The step the policy would take: where "next" goes, and where the
	// workflow goes if decisions return to the policy.
	Policy string `json:"policy"`
	// Taken by the conductor (decide, start_phase), set as it is taken so
	// a second is refused before the workflow acts on the first.
	Taken *Taken `json:"taken,omitempty"`
}

// Taken is a conductor's decision.
type Taken struct {
	// "next": what the policy would do; "start_phase"; "open_pull_request";
	// "wait" (on the pull requests, leaving their feedback).
	Action string `json:"action"`
	// start_phase: the phase, and what it is limited to.
	Phase      string   `json:"phase,omitempty"`
	Categories []string `json:"categories,omitempty"`
	FindingIDs []string `json:"findingIds,omitempty"`
	Note       string   `json:"note,omitempty"`
	// open_pull_request: a draft, as the person answered.
	Draft bool `json:"draft,omitempty"`
	// The conductor that took it.
	By string `json:"by"`
}

// Directed is what the step after a conductor's decision creates its Runs
// with: the conductor's, limited as it said.
type Directed struct {
	Phase      string   `json:"phase,omitempty"`
	Categories []string `json:"categories,omitempty"`
	FindingIDs []string `json:"findingIds,omitempty"`
	Note       string   `json:"note,omitempty"`
	By         string   `json:"by"`
}

// conducted says whether the conductor takes the delivery's decisions.
func (st *State) conducted() bool { return st.Decider == DeciderConductor }

// Phases is what the conductor may start at a decision point.
func (d *Pending) Phases() []string {
	switch d.Point {
	case PointStart:
		return []string{PhaseImplement}
	case PointPRFeedback:
		return []string{PhaseFix}
	}
	return []string{PhaseReview, PhaseFix, PhaseSimplify, PhaseTest}
}

// Actions is what decide may say at a decision point.
func (d *Pending) Actions() []string {
	switch d.Point {
	case PointBeforePR:
		return []string{"ask_person", "open_pull_request"}
	case PointPRFeedback:
		return []string{"next", "ask_person", "wait"}
	}
	return []string{"next", "ask_person"}
}

// pointLabel is how a decision point reads in a note.
var pointLabel = map[string]string{
	PointStart:       "start: nothing has run yet",
	PointImplemented: "after implement",
	PointReviewed:    "after a review round",
	PointFixed:       "after a fix",
	PointBeforePR:    "before the pull request",
	PointPRFeedback:  "pull request feedback",
}

// next goes to step, as the policy decides, or under decider conductor
// parks for the conductor's decision at point, saying line (bounded facts).
// Whoever decides is read as the transition commits, not as the step was
// claimed: the step returns the policy's step marked (Routed), and the
// transition's checkpoint (recheck) parks it for the conductor there, its
// wake and event in the transition's transaction. A conductor that was
// deciding as the step entered the pull request gate leaves it required.
func (w *steps) next(ctx context.Context, sc workflow.StepContext, st *State, point, step, line string) (workflow.Result, error) {
	if st.conducted() && point == PointBeforePR {
		st.GateRequired = true
	}
	st.Routed = &Routed{Point: point, Policy: step, Next: step, Line: line}
	return workflow.Result{Next: step, State: st}, nil
}

// Routed is a decision point the policy took in a step: the step it chose
// (Next), and what the conductor would have been asked (Point, Policy:
// where its "next" goes).
type Routed struct {
	Point  string `json:"point"`
	Policy string `json:"policy"`
	Next   string `json:"next"`
	Line   string `json:"line,omitempty"`
}

// recheck is the delivery's decision checkpoint (workflow.Definition.
// Recheck): in the transition's transaction, a decision point the step
// reached is the conductor's when the row, locked now, says the conductor
// decides — whether it did when the step was claimed, or took over while
// the step's mechanics ran — and the policy's otherwise.
func (w *steps) recheck(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, res workflow.Result) (workflow.Result, bool, error) {
	st, ok := res.State.(*State)
	if !ok || st.Routed == nil {
		return res, false, nil
	}
	r := st.Routed
	st.Routed = nil
	var decider string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(state->>'decider', '') FROM workflow_runs WHERE id = $1 FOR UPDATE`,
		sc.WorkflowRunID).Scan(&decider); err != nil {
		return res, false, err
	}
	st.Decider = decider
	if decider != DeciderConductor || res.Next != r.Next {
		return res, true, nil
	}
	out, err := toConductorTx(ctx, tx, sc, st, r.Point, r.Policy, r.Line)
	return out, true, err
}

// toConductorTx parks the workflow on a decision for the conductor, and
// records the reason to wake it, in the transition's transaction. Keyed on
// the decision's number, so a step replayed after a crash records it once.
// The start wakes nobody: only Talk it through decides it, whose message
// in Chat is the conductor's turn already.
func toConductorTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, point, policy, line string) (workflow.Result, error) {
	st.Directed, st.PendingRunIDs, st.Routed = nil, nil, nil
	st.Decision = &Pending{Point: point, Policy: policy}
	if point == PointBeforePR {
		st.GateRequired = true
	}
	note := fmt.Sprintf("Decision waiting: %s.", pointLabel[point])
	if line != "" {
		note += " " + line
	}
	added, err := recordWakeTx(ctx, tx, sc.OrganizationID, st.TaskID, "decision",
		fmt.Sprintf("%s:decision:%d", sc.WorkflowRunID, st.Decisions), note, point == PointStart)
	if err == nil && added {
		err = emitTx(ctx, tx, sc.OrganizationID, st, EvDecisionAwaited, map[string]any{"point": point, "policy": policy,
			"actions": st.Decision.Actions(), "phases": st.Decision.Phases(), "note": note})
	}
	if err != nil {
		return workflow.Result{}, err
	}
	return workflow.Result{Next: "conductorDecision", State: st, AwaitSignals: []string{SignalConductorDecision}}, nil
}

// conductorDecision carries out the decision the workflow parked on: the
// conductor's, once taken — even if decisions went back to the policy
// since, which takes the next one; else, handed back, what the policy
// would have done.
func (w *steps) conductorDecision(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	d := st.Decision
	if d == nil {
		return workflow.Result{}, fmt.Errorf("no decision to carry out")
	}
	if d.Taken == nil && !st.conducted() {
		// A gate the conductor entered stays one handed back: Deliver goes
		// on only with the person's Open or Draft, or their confirmation.
		if d.Point == PointBeforePR && st.GateRequired && !st.GateOpened {
			ok, err := w.gateAuthorized(ctx, sc, st)
			if err != nil {
				return workflow.Result{}, err
			}
			if !ok {
				return w.parkAtGate(ctx, sc, st, false)
			}
		}
		st.Decision = nil
		st.Decisions++
		return workflow.Result{Next: d.Policy, State: st}, nil
	}
	if d.Taken == nil {
		return workflow.Result{Next: "conductorDecision", State: st, AwaitSignals: []string{SignalConductorDecision}}, nil
	}
	t := d.Taken
	st.Decision = nil
	st.Decisions++
	switch t.Action {
	case "next":
		st.Directed = &Directed{Note: t.Note, By: t.By}
		return workflow.Result{Next: d.Policy, State: st}, nil
	case "open_pull_request":
		st.Draft, st.GateOpened = t.Draft, true
		return workflow.Result{Next: "openPullRequest", State: st}, nil
	case "wait":
		st.PRFeedback = nil
		return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "the conductor is waiting on the pull request")
	case "start_phase":
		st.Directed = &Directed{Phase: t.Phase, Categories: t.Categories, FindingIDs: t.FindingIDs, Note: t.Note, By: t.By}
		return workflow.Result{Next: phaseStep(t.Phase, d.Point), State: st}, nil
	}
	return workflow.Result{}, fmt.Errorf("decision %q cannot be carried out", t.Action)
}

// gateAuthorized says whether a gate the conductor entered may open under
// Deliver: the person's Open or Draft at the heads now (taken into the
// state, Draft as the draft), or a confirmed hand-back (GateOpened).
func (w *steps) gateAuthorized(ctx context.Context, sc workflow.StepContext, st *State) (bool, error) {
	if st.GateOpened {
		return true, nil
	}
	var draft bool
	err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		var err error
		draft, err = gate(ctx, tx, st)
		return err
	})
	var refused Refusal
	if errors.As(err, &refused) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	st.Draft, st.GateOpened = st.Draft || draft, true
	return true, nil
}

// parkAtGate parks a delivery Deliver decides at the pull request gate the
// conductor entered, asking the person, until they answer Open or Draft or
// confirm the opening at a hand-back. entering: it parks there now, which
// is recorded once.
func (w *steps) parkAtGate(ctx context.Context, sc workflow.StepContext, st *State, entering bool) (workflow.Result, error) {
	st.Decision = &Pending{Point: PointBeforePR, Policy: "openPullRequest"}
	note := "Deliver waits for the person's Open or Draft before it opens the pull request."
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		if err := askGateTx(ctx, tx, sc.OrganizationID, st); err != nil || !entering {
			return err
		}
		return emitTx(ctx, tx, sc.OrganizationID, st, EvDecisionAwaited, map[string]any{"point": PointBeforePR,
			"policy": "openPullRequest", "actions": []string{}, "phases": []string{}, "note": note})
	}); err != nil {
		return workflow.Result{}, err
	}
	return workflow.Result{Next: "conductorDecision", State: st, AwaitSignals: []string{SignalConductorDecision}}, nil
}

// phaseStep is the step that creates a phase's Runs; a fix of pull request
// feedback is the pull request's fix.
func phaseStep(phase, point string) string {
	switch phase {
	case PhaseImplement:
		return "implementRun"
	case PhaseFix:
		if point == PointPRFeedback {
			return "prFix"
		}
	}
	return phase
}

// directed reads, and spends, the conductor's direction for a step that
// creates phase Runs: nil for one the policy chose.
func directed(st *State, phase string) *Directed {
	d := st.Directed
	if d == nil || d.Phase != "" && d.Phase != phase {
		return nil
	}
	st.Directed = nil
	return d
}

// conductorKey makes a directed Run's creation key: each decision's own,
// so a phase the conductor starts twice is two Runs.
func conductorKey(sc workflow.StepContext, st *State, d *Directed, parts ...any) string {
	return key(sc, st, append([]any{":c", st.Decisions}, parts...)...)
}

// conductorRun gives a phase Run the conductor's mark.
func (d *Directed) apply(p *PhaseRun) {
	if d != nil {
		p.ConductorRunID, p.ConductorNote = d.By, d.Note
	}
}

// wakeWindow and the rest: see the syncer's delivery of wakes.

// RecordWakeTx records a reason to wake the task's conductor, once per key.
// Returns whether it is new.
func RecordWakeTx(ctx context.Context, tx pgx.Tx, org, taskID, kind, key, line string) (bool, error) {
	return recordWakeTx(ctx, tx, org, taskID, kind, key, line, false)
}

// recordWakeTx is RecordWakeTx; settled records the reason as heard
// already, so it marks the key without waking anyone.
func recordWakeTx(ctx context.Context, tx pgx.Tx, org, taskID, kind, key, line string, settled bool) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, delivered_at)
		VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $7 THEN now() END) ON CONFLICT (task_id, key) DO NOTHING`,
		ids.New("cwk"), org, taskID, kind, key, clip(oneLine(line), wakeLineChars), settled)
	return err == nil && tag.RowsAffected() == 1, err
}

// wakeLineChars bounds one reason's line, as the table does.
const wakeLineChars = 300

// SetDeciderTx changes who decides a task's delivery, recording it once:
// a no-op when it already is. Returns whether it changed.
func SetDeciderTx(ctx context.Context, tx pgx.Tx, org, wfID string, st *State, to, why, actorType, actorID string) (bool, error) {
	from := st.Decider
	if from == "" {
		from = DeciderPolicy
	}
	if from == to {
		return false, nil
	}
	st.Decider = to
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{decider}', to_jsonb($2::text)) WHERE id = $1`,
		wfID, to); err != nil {
		return false, err
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvDeciderChanged, OrganizationID: org, ProjectID: st.ProjectID,
		TaskID: st.TaskID, ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID,
		Payload: map[string]any{"from": from, "to": to, "why": why}})
	return true, err
}

// reviewCategories is every reviewer a conductor may name.
func reviewCategories() []string { return slices.Clone(reviewerOrder) }

// listOr names a list in a message.
func listOr(items []string) string {
	if len(items) == 0 {
		return "nothing"
	}
	return strings.Join(items, ", ")
}
