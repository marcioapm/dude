package delivery

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
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

// How a conducted delivery's conductor speaks (State.Voice): quiet, it
// decides by Deliver's defaults and leaves the pull request to Deliver's
// own rules, talking only when something needs a person; chatty, it talks
// the work through with its people. "" reads as chatty: a conductor before
// voices existed talked.
const (
	VoiceQuiet  = "quiet"
	VoiceChatty = "chatty"
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
	// A delivery that took over existing work, before any of its Runs: the
	// work is there to review at its start, and to carry on after it.
	Adopted bool `json:"adopted,omitempty"`
	// Taken by the conductor (decide, start_phase), set as it is taken so
	// a second is refused before the workflow acts on the first.
	Taken *Taken `json:"taken,omitempty"`
	// The review the conductor queued behind the fix this decision follows
	// (State.Pipeline), not started: an exception, or wake after_fix.
	// "next" runs it.
	Queued *Directed `json:"queued,omitempty"`
	// Under a quiet conductor: when the wait for its decision began, and
	// when Deliver's defaults take it (QuietDecisionWait).
	Since    *time.Time `json:"since,omitempty"`
	DecideBy *time.Time `json:"decideBy,omitempty"`
}

// QuietDecisionWait is how long a quiet conductor has to take a decision
// its delivery parked on: past it, dude goes on by Deliver's defaults
// (conductorDecision). A constant; tests shorten it (Store.DecisionWait).
const QuietDecisionWait = 10 * time.Minute

// MaxDefaulted is how many decisions in a row a quiet delivery takes by
// Deliver's defaults, its conductor not deciding, before it stops for a
// person (ReasonConductorUnavailable).
const MaxDefaulted = 3

// ReasonConductorUnavailable: a quiet conductor left MaxDefaulted decisions
// in a row to Deliver's defaults. The person waits for it again (retry),
// hands the delivery to Deliver, whose defaults and bounds finish it
// (accept: decider policy), or stops.
const ReasonConductorUnavailable = "conductor_unavailable"

// EvDecisionDefaulted: dude took Deliver's defaults at a decision a quiet
// conductor did not take. Payload: {point, policy, reason (deadline,
// no_model, run_failed, accepted), count (in a row)}.
const EvDecisionDefaulted = "conductor.decision_defaulted"

// Why dude took Deliver's defaults at a quiet conductor's decision.
const (
	DefaultedDeadline  = "deadline"
	DefaultedNoModel   = "no_model"
	DefaultedRunFailed = "run_failed"
	DefaultedAccepted  = "accepted"
)

// DefaultedLine is the Chat notice of a decision taken by Deliver's
// defaults.
func DefaultedLine(reason string) string {
	switch reason {
	case DefaultedNoModel:
		return "dude went on by Deliver's defaults: the conductor has no model to decide with."
	case DefaultedRunFailed:
		return "dude went on by Deliver's defaults: the conductor's Run failed before it decided."
	case DefaultedAccepted:
		return "dude went on by Deliver's defaults, as the person accepted."
	}
	return fmt.Sprintf("dude went on by Deliver's defaults: the conductor did not decide within %d minutes.",
		int(QuietDecisionWait/time.Minute))
}

// decisionWait is how long a quiet conductor has to decide.
func (s *Store) decisionWait() time.Duration {
	if s.DecisionWait > 0 {
		return s.DecisionWait
	}
	return QuietDecisionWait
}

// quietFactsTx is what a quiet conductor's decision is weighed by: now, the
// database's clock (what the runtime claims a parked step's wake by,
// wake_at <= now()), and whether the task's conductor has no model to
// decide with: its role's tier as the syncer resolves it for the
// conductor's spec (ResolveRole, TierFor), where errNoModel fails it.
func quietFactsTx(ctx context.Context, tx pgx.Tx, taskID string) (now time.Time, noModel bool, err error) {
	var project, org json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT now(), p.agent_models, o.default_agent_models FROM tasks t
		JOIN projects p ON p.id = t.project_id JOIN organizations o ON o.id = p.organization_id
		WHERE t.id = $1`, taskID).Scan(&now, &project, &org); err != nil {
		return now, false, err
	}
	_, no, err := TierFor(ctx, tx, RoleConductor, ResolveRole(RoleConductor, project, org))
	return now, no != "", err
}

// decisionWakeKey keys the reason that wakes the conductor for the
// delivery's decision number n.
func decisionWakeKey(wfID string, n int) string { return fmt.Sprintf("%s:decision:%d", wfID, n) }

// quietWait starts the wait for a quiet conductor's decision at now, the
// database's clock (quietFactsTx): its deadline, now (no model: Deliver's
// defaults at once) or after the wait. The runtime wakes the step by the
// database's clock too, so a host clock behind it cannot have the step
// claimed again and again before a deadline it has not reached.
func (w *steps) quietWait(st *State, noModel bool, now time.Time) workflow.Result {
	now = now.UTC()
	by := now.Add(w.s.decisionWait())
	if noModel {
		by = now
	}
	st.Decision.Since, st.Decision.DecideBy = &now, &by
	return w.parkQuiet(st)
}

// parkQuiet parks on a quiet conductor's decision until its deadline.
func (w *steps) parkQuiet(st *State) workflow.Result {
	return workflow.Result{Next: "conductorDecision", State: st, AwaitSignals: []string{SignalConductorDecision},
		SleepUntil: *st.Decision.DecideBy}
}

// waitsQuietly says the delivery waits on a quiet conductor's decision not
// taken, and restarts its wait: a person's pick-up or decision gives the
// conductor its whole time again.
func (st *State) waitsQuietly() bool {
	d := st.Decision
	if !st.quiet() || d == nil || d.Taken != nil || st.quietAt(d.Point) {
		return false
	}
	d.Since, d.DecideBy = nil, nil
	return true
}

// Taken is a conductor's decision.
type Taken struct {
	// "next": what the policy would do; "start_phase"; "open_pull_request";
	// "wait" (on the pull requests, dropping their feedback).
	Action string `json:"action"`
	// start_phase: the phase, and what it is limited to.
	Phase      string   `json:"phase,omitempty"`
	Categories []string `json:"categories,omitempty"`
	FindingIDs []string `json:"findingIds,omitempty"`
	Note       string   `json:"note,omitempty"`
	// open_pull_request: a draft, as the person answered; the person's
	// earlier answer it cites (OpenAuthority), when not the gate's.
	Draft      bool         `json:"draft,omitempty"`
	Authorized *CitedAnswer `json:"authorized,omitempty"`
	// start_phase fix or simplify: the review queued behind it (then).
	Then *Pipeline `json:"then,omitempty"`
	// start_phase review, fix or simplify: the round's scope and bar.
	Round *RoundAsk `json:"round,omitempty"`
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
	// A fix or simplify's queued review (Taken.Then); the round's scope
	// and bar (Taken.Round).
	Then  *Pipeline `json:"then,omitempty"`
	Round *RoundAsk `json:"round,omitempty"`
	By    string    `json:"by"`
}

// The wakes a fix-then-review may ask for (Then.Wake).
const (
	WakeAfterReview = "after_review"
	WakeAfterFix    = "after_fix"
)

// The scopes a round may ask for (RoundAsk.Scope): the whole branch, or
// what the round's rules give (round 1 whole, later ones the fix's commits).
const (
	ScopeWhole = "whole"
	ScopeFix   = "fix"
)

// Then is start_phase's then: the review dude starts as soon as a fix (or
// simplify) has committed, without a decision between.
type Then struct {
	Review []string `json:"review"`
	Wake   string   `json:"wake,omitempty"`
	Note   string   `json:"note,omitempty"`
}

// RoundAsk is start_phase's round: the scope of the review round, and the
// conductor's bar for it, quoted to the fixer and the reviewers.
type RoundAsk struct {
	Scope string `json:"scope,omitempty"`
	Bar   string `json:"bar,omitempty"`
}

// Pipeline is the conductor's fix-then-review in flight: the review queued
// behind the fix it started, carried out by the fix's await step without a
// decision (State.Pipeline). Fix is the fix Run, once created.
type Pipeline struct {
	Review []string `json:"review"`
	Wake   string   `json:"wake"`
	Note   string   `json:"note,omitempty"`
	Scope  string   `json:"scope,omitempty"`
	Bar    string   `json:"bar,omitempty"`
	By     string   `json:"by"`
	Fix    string   `json:"fix,omitempty"`
}

// round is the round its queued review is asked with; nil for none.
func (p *Pipeline) round() *RoundAsk {
	if p.Scope == "" && p.Bar == "" {
		return nil
	}
	return &RoundAsk{Scope: p.Scope, Bar: p.Bar}
}

// queued is the review a pipeline queued, directed as the conductor asked.
func (p *Pipeline) queued() *Directed {
	return &Directed{Phase: PhaseReview, Categories: slices.Clone(p.Review), Note: p.Note, Round: p.round(), By: p.By}
}

// conducted says whether the conductor takes the delivery's decisions.
func (st *State) conducted() bool { return st.Decider == DeciderConductor }

// quiet says the conductor decides the delivery quietly: it takes no turn
// at the start, and the pull request opens by Deliver's rules.
func (st *State) quiet() bool { return st.conducted() && st.Voice == VoiceQuiet }

// chatty says the conductor decides the delivery and talks it through:
// it takes its turn at every decision point, the pull request's too.
func (st *State) chatty() bool { return st.conducted() && !st.quiet() }

// voice is the conductor's voice, "" when no conductor decides.
func (st *State) voice() string {
	switch {
	case st.quiet():
		return VoiceQuiet
	case st.chatty():
		return VoiceChatty
	}
	return ""
}

// quietAt says a quiet conductor takes no turn at point: the start, whose
// implementer starts at once, and the pull request, which Deliver's own
// rules open. The policy's step is taken there, as with no conductor.
func (st *State) quietAt(point string) bool {
	return st.quiet() && (point == PointStart || point == PointBeforePR)
}

// deliverOpens says Deliver's rules hold the pull request gate: no
// conductor decides, or a quiet one does.
func (st *State) deliverOpens() bool { return !st.chatty() }

// Phases is what the conductor may start at a decision point.
func (d *Pending) Phases() []string {
	switch d.Point {
	case PointStart:
		if d.Adopted {
			return []string{PhaseImplement, PhaseReview}
		}
		return []string{PhaseImplement}
	case PointPRFeedback:
		if d.Policy == "awaitPullRequest" {
			// Nothing left to act on (checksPassedSinceTx): as at the wait.
			return prWaitPhases
		}
		return []string{PhaseFix}
	case PointImplemented:
		if d.Adopted {
			return []string{PhaseImplement, PhaseReview, PhaseFix, PhaseSimplify, PhaseTest}
		}
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
var pointLabel = func() map[string]string {
	out := map[string]string{}
	for _, p := range []string{PointStart, PointImplemented, PointReviewed, PointFixed, PointBeforePR, PointPRFeedback} {
		out[p] = render(mustTemplate("wakes/facts/point-"+p), nil)
	}
	return out
}()

// next goes to step, as the policy decides, or under decider conductor
// parks for the conductor's decision at point, saying line (bounded facts).
// Whoever decides is read as the transition commits, not as the step was
// claimed: the step returns the policy's step marked (Routed), and the
// transition's checkpoint (recheck) parks it for the conductor there, its
// wake and event in the transition's transaction. A conductor that was
// deciding as the step entered the pull request gate leaves it required.
func (w *steps) next(ctx context.Context, sc workflow.StepContext, st *State, point, step, line string) (workflow.Result, error) {
	if st.chatty() && point == PointBeforePR {
		st.GateRequired, st.ChoiceGate = true, false
	}
	st.Routed = &Routed{Point: point, Policy: step, Next: step, Line: line}
	return workflow.Result{Next: step, State: st}, nil
}

// pipeline is the fix-then-review a fix or simplify directed so starts
// (State.Pipeline); nil for none.
func (d *Directed) pipeline() *Pipeline {
	if d == nil || d.Then == nil {
		return nil
	}
	// Then carries the round's scope and bar already (pipelineOf).
	p := *d.Then
	p.Review, p.By = slices.Clone(p.Review), d.By
	return &p
}

// What stops a pipeline at its fix, telling the conductor at once
// (pipelineStop): the review it queued does not start.
const (
	pipelineFailed  = "failed"
	pipelineNothing = "nothing"
	pipelineOutcome = "outcome"
)

// pipelineStop is why a pipeline's fix (or simplifier) stops it, "" for
// none: it failed (a simplifier: a fix's failure escalates first), it
// committed nothing, or its writer reports itself partial or blocked.
func pipelineStop(out Outcome) string {
	switch {
	case !out.Succeeded:
		return pipelineFailed
	case len(out.Heads) == 0:
		return pipelineNothing
	case out.WriterOutcome == OutcomePartial || out.WriterOutcome == OutcomeBlocked:
		return pipelineOutcome
	}
	return ""
}

// pipelineStopped is the fact a stopped pipeline's wake says; queued: the
// review is offered at the decision it parks on.
func pipelineStopped(p *Pipeline, stop string, out Outcome, queued bool) string {
	return render(pipelineStoppedTmpl, struct {
		Case, Error, Outcome, Note string
		Review                     []string
		Queued                     bool
	}{stop, out.Error, out.WriterOutcome, out.WriterOutcomeNote, p.Review, queued})
}

// pipelineGoesOn carries out the conductor's pipeline at its fix's (or
// simplifier's) end: with no exception and wake after_review, its queued
// review, directed, with no decision between; else a decision after the
// fix, whose "next" runs the queued review (Pending.Queued), the fact
// that stopped it said. ok is false with no pipeline: the step goes on as
// it would. policy is where the step goes when no conductor decides.
func (w *steps) pipelineGoesOn(ctx context.Context, sc workflow.StepContext, st *State, phase, runID string, out Outcome, policy string) (workflow.Result, bool, error) {
	p := st.Pipeline
	if p == nil {
		return workflow.Result{}, false, nil
	}
	st.Pipeline = nil
	stop := pipelineStop(out)
	if stop == "" && p.Wake != WakeAfterFix {
		st.Directed = p.queued()
		return workflow.Result{Next: "review", State: st}, true, nil
	}
	line := out.line(runID, phase) + " "
	queued := p.queued()
	if stop == pipelineNothing && policy != "review" {
		// Nothing changed: a review whose every reviewer completed at these
		// heads already would cover no change. Dropped, and said; next goes
		// where the policy goes (the test, or the pull request). Not where
		// the policy reviews (after a fix): there next would start a review
		// anyway, the policy's, so it runs the one queued, as before.
		covered, err := w.reviewedAtHeads(ctx, sc, st, p.Review)
		if err != nil {
			return workflow.Result{}, true, err
		}
		if covered {
			queued = nil
		}
	}
	if stop != "" && queued == nil {
		line += render(pipelineCoveredTmpl, struct{ Review []string }{p.Review})
	} else if stop != "" {
		line += pipelineStopped(p, stop, out, true)
	} else {
		line += render(pipelineQueuedTmpl, struct{ Review []string }{p.Review})
	}
	if len(st.FixRange) > 0 && len(out.Heads) > 0 {
		line += render(pipelineUnreviewedTmpl, nil)
	}
	res, err := w.next(ctx, sc, st, PointFixed, policy, line)
	// A round's wake: its facts are not a decision's 300 characters.
	st.Routed.Queued, st.Routed.Kind = queued, WakeRound
	return res, true, err
}

// reviewedAtHeads says each of the categories has a completed review Run of
// the attempt that started at the delivery's heads: a review of them now
// would cover no change since.
func (w *steps) reviewedAtHeads(ctx context.Context, sc workflow.StepContext, st *State, categories []string) (bool, error) {
	if len(st.Heads) == 0 || len(categories) == 0 {
		return false, nil
	}
	heads, err := json.Marshal(st.Heads)
	if err != nil {
		return false, err
	}
	var covered bool
	err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM unnest($3::text[]) c WHERE NOT EXISTS (
				SELECT 1 FROM runs k WHERE k.task_id = $1 AND k.attempt = $2 AND k.phase = 'review' AND k.kind = 'agent'
				AND k.status = 'completed' AND k.category = c AND k.base_refs = $4::jsonb))`,
			st.TaskID, st.Attempt, categories, heads).Scan(&covered)
	})
	return covered, err
}

// Routed is a decision point the policy took in a step: the step it chose
// (Next), and what the conductor would have been asked (Point, Policy:
// where its "next" goes).
type Routed struct {
	Point  string `json:"point"`
	Policy string `json:"policy"`
	Next   string `json:"next"`
	Line   string `json:"line,omitempty"`
	// The review a pipeline queued, offered at the decision (Pending.Queued).
	Queued *Directed `json:"queued,omitempty"`
	// The wake's kind: "" is a decision's; WakeRound a conducted round's.
	Kind string `json:"kind,omitempty"`
}

// recheck is the delivery's decision checkpoint (workflow.Definition.
// Recheck): in the transition's transaction, a decision point the step
// reached is the conductor's when the row, locked now, says the conductor
// decides — whether it did when the step was claimed, or took over while
// the step's mechanics ran — and the policy's otherwise. A gate
// authorization the step asked for (gateAuthorized) is recorded here too,
// and a park at the hold of the task's Ask me first reads the choice again
// (choiceReleasedAtCommitTx).
func (w *steps) recheck(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, res workflow.Result) (workflow.Result, bool, error) {
	if st, ok := res.State.(*State); ok && sc.Step == "decide" && res.Next == "decide" && st.Escalation != nil && st.Escalation.Decided == nil {
		// The step's delivery lock ended before the runtime transaction. An
		// acknowledged decision or ask_person in that gap owns this checkpoint,
		// not the pending snapshot. No task lock is needed to preserve it.
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id=$1 FOR NO KEY UPDATE`, sc.WorkflowRunID).Scan(&raw); err != nil {
			return res, false, err
		}
		var current State
		if err := json.Unmarshal(raw, &current); err != nil {
			return res, false, err
		}
		if current.Escalation != nil && current.Escalation.Decided != nil {
			// Run the accepted decision next even if its signal was already
			// among this tick's claimed signals and will be consumed on commit.
			return workflow.Result{Next: "decide", State: &current}, true, nil
		}
		wait, err := w.escalationWaitTx(ctx, tx, &current)
		return wait, true, err
	}
	if sc.Step != "decide" {
		var raw []byte
		// NO KEY UPDATE: it conflicts with LoadDelivery's FOR UPDATE, so a
		// handover (SetDeciderTx) in flight is read committed, but lets a
		// signal insert's FK KEY SHARE through: a writer holding the task
		// that signals the delivery (EndConductorWait) cannot cycle with a
		// park at Ask me first that waits on the task
		// (choiceReleasedAtCommitTx).
		if err := tx.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id=$1 FOR NO KEY UPDATE`, sc.WorkflowRunID).Scan(&raw); err != nil {
			return res, false, err
		}
		var current State
		if err := json.Unmarshal(raw, &current); err != nil {
			return res, false, err
		}
		if e := current.Escalation; e != nil && e.Reason == ReasonChecksUnverified {
			return w.escalationWait(&current), true, nil
		}
		// Handed to the conductor while the step escalated: told as escalate
		// tells a conducted delivery's escalation.
		if st, ok := res.State.(*State); ok && res.Next == "decide" && st.Escalation != nil && current.conducted() && !st.conducted() {
			st.Decider = current.Decider
			st.Decision, st.Directed = nil, nil
			if _, err := tellEscalationTx(ctx, tx, sc.OrganizationID, sc.WorkflowRunID, st); err != nil {
				return res, false, err
			}
			return res, true, nil
		}
	}
	st, ok := res.State.(*State)
	if !ok {
		return res, false, nil
	}
	if st.acceptDefaults {
		return acceptAtCommitTx(ctx, tx, sc, st, res)
	}
	if st.Routed == nil && !st.authorizeGate {
		return choiceReleasedAtCommitTx(ctx, tx, sc, st, res)
	}
	if st.authorizeGate {
		st.authorizeGate = false
		if err := authorizeAtCommitTx(ctx, tx, sc, st); err != nil {
			return res, false, err
		}
		if st.Routed == nil {
			return res, true, nil
		}
	}
	r := st.Routed
	st.Routed = nil
	var decider, voice string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(state->>'decider', ''), COALESCE(state->>'voice', '') FROM workflow_runs
		WHERE id = $1 FOR UPDATE`, sc.WorkflowRunID).Scan(&decider, &voice); err != nil {
		return res, false, err
	}
	st.Decider, st.Voice = decider, voice
	// A quiet conductor takes no turn at the start, and Deliver's own rules
	// open the pull request: the policy's step, as with no conductor.
	if decider != DeciderConductor || res.Next != r.Next || st.quietAt(r.Point) {
		if res.Next == "openPullRequest" && !st.GateRequired {
			if err := FreezePublicationTx(ctx, tx, st.TaskID, st.Branch); err != nil {
				return res, false, err
			}
		}
		return res, true, nil
	}
	// A quiet conductor with no model to decide with: Deliver's defaults
	// at once, its reason settled, so no conductor Run is started for it.
	var noModel bool
	var now time.Time
	if st.quiet() {
		var err error
		if now, noModel, err = quietFactsTx(ctx, tx, st.TaskID); err != nil {
			return res, false, err
		}
	}
	out, err := toConductorKindTx(ctx, tx, sc, st, r.Point, r.Policy, r.Line, cmp.Or(r.Kind, "decision"), noModel)
	if err == nil && r.Queued != nil {
		st.Decision.Queued = r.Queued
	}
	if err == nil && st.quiet() {
		out = w.quietWait(st, noModel, now)
	}
	return out, true, err
}

// acceptAtCommitTx is accept on conductor_unavailable, in its transition
// (recheck): the row locked as recheck locks it, the decision is taken by
// Deliver's defaults with its notice (defaultedTx) when the row says the
// policy decides, the policy's step next; when a Chat took the delivery
// over before the commit, nothing is taken or said, and the decision is
// the conductor's (conductorDecision parks it). The step has no conductor
// decision points: conductor_unavailable stops only where a quiet
// conductor decides (not quietAt), so its policy step is Deliver's as is.
func acceptAtCommitTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, res workflow.Result) (workflow.Result, bool, error) {
	st.acceptDefaults = false
	var decider, voice string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(state->>'decider', ''), COALESCE(state->>'voice', '') FROM workflow_runs
		WHERE id = $1 FOR UPDATE`, sc.WorkflowRunID).Scan(&decider, &voice); err != nil {
		return res, false, err
	}
	st.Decider, st.Voice = decider, voice
	if st.conducted() {
		return workflow.Result{Next: "conductorDecision", State: st}, true, nil
	}
	if err := defaultedTx(ctx, tx, sc, st, DefaultedAccepted); err != nil {
		return res, false, err
	}
	st.Decision = nil
	st.Decisions++
	return res, true, nil
}

// decisionWaitingNote is the reason to wake the conductor for a decision
// at point, with line's facts.
func decisionWaitingNote(point, line string) string {
	return render(decisionWaitingTmpl, struct{ Point, Line string }{pointLabel[point], line})
}

// toConductorTx parks the workflow on a decision for the conductor, and
// records the reason to wake it, in the transition's transaction. Keyed on
// the decision's number, so a step replayed after a crash records it once.
// The start wakes nobody: only Talk it through decides it, whose message
// in Chat is the conductor's turn already.
func toConductorTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, point, policy, line string) (workflow.Result, error) {
	return toConductorKindTx(ctx, tx, sc, st, point, policy, line, "decision", false)
}

// toConductorKindTx is toConductorTx recording its wake as kind; settled
// records it heard already (a quiet conductor with no model: nobody to
// wake).
func toConductorKindTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, point, policy, line, kind string, settled bool) (workflow.Result, error) {
	st.Directed, st.PendingRunIDs, st.Routed = nil, nil, nil
	st.Decision = &Pending{Point: point, Policy: policy,
		Adopted: st.Source != nil && st.HeadRunID == "" && (point == PointStart || point == PointImplemented)}
	if point == PointBeforePR {
		st.GateRequired, st.ChoiceGate = true, false
	}
	if point == PointBeforePR {
		// A person's answer that opens it and still holds: said, so the
		// conductor cites it rather than ask again.
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT state->'openAuthority' FROM workflow_runs WHERE id = $1`, sc.WorkflowRunID).Scan(&raw); err != nil {
			return workflow.Result{}, err
		}
		var a *OpenAuthority
		if len(raw) > 0 && json.Unmarshal(raw, &a) == nil && a != nil {
			st.OpenAuthority = a
			if facts, err := authorityFactsTx(ctx, tx, sc.WorkflowRunID, st); err != nil {
				return workflow.Result{}, err
			} else if ok, why := OpenAuthorityValid(a, facts); ok {
				line = strings.TrimSpace(line + " " + authorityLine(a))
			} else if a.Voided == nil {
				// Found not to hold: void for good.
				if err := voidTx(ctx, tx, sc.OrganizationID, st.TaskID, a.QuestionID, why); err != nil {
					return workflow.Result{}, err
				}
			}
		}
	}
	note := decisionWaitingNote(point, line)
	added, err := recordWakeTx(ctx, tx, sc.OrganizationID, st.TaskID, kind,
		decisionWakeKey(sc.WorkflowRunID, st.Decisions), note, point == PointStart || settled)
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
	// Under a quiet conductor, a start or Deliver's hold before the pull
	// request (handed back quietly while parked there) is Deliver's too:
	// the implementer starts, and the person's Open or Draft opens it, with
	// no conductor turn.
	if d.Taken == nil && (!st.conducted() || st.quietAt(d.Point)) {
		// A gate the conductor entered stays one handed back: Deliver goes
		// on only with the person's Open or Draft, or their confirmation.
		if d.Point == PointBeforePR && st.GateRequired && !st.gateHeld() {
			// Its head the conductor's untested commit: reviewed first.
			if res, diverted, err := w.reviewUntested(ctx, sc, st); diverted || err != nil {
				if diverted {
					st.Decision = nil
					st.Decisions++
				}
				return res, err
			}
			ok, err := w.gateAuthorized(ctx, sc, st)
			if err != nil {
				return workflow.Result{}, err
			}
			if !ok {
				// The task's Ask me first, changed since it held: the
				// opening step reads the choice again.
				if released, err := w.choiceReleased(ctx, sc, st); err != nil || released {
					return workflow.Result{Next: "openPullRequest", State: st}, err
				}
				if humanFeedback(sc) {
					if res, fixing, err := w.feedbackAtGate(ctx, sc, st); fixing || err != nil {
						return res, err
					}
				}
				return w.parkAtGate(ctx, sc, st, false)
			}
		}
		st.Decision = nil
		st.Decisions++
		return workflow.Result{Next: d.Policy, State: st}, nil
	}
	if d.Taken == nil && st.quiet() {
		return w.quietDecision(ctx, sc, st)
	}
	if d.Taken == nil {
		// Chatty, the decision has no deadline: a quiet wait it had is
		// over, and one handed back quietly later starts afresh.
		d.Since, d.DecideBy = nil, nil
		return workflow.Result{Next: "conductorDecision", State: st, AwaitSignals: []string{SignalConductorDecision}}, nil
	}
	t := d.Taken
	st.Decision = nil
	st.Decisions++
	switch t.Action {
	case "next":
		if d.Point == PointPRFeedback && d.Policy == "awaitPullRequest" && st.Integration == nil {
			// The checks it was for passed since (checksPassedSinceTx):
			// no fix to start; the pull requests are waited on again and
			// their gate weighed now, which says what it still needs.
			st.PRFeedback = nil
			st.reweighNow()
			return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "pull request open")
		}
		if q := d.Queued; q != nil {
			// The review the conductor queued, with what it says now.
			// What is cut to fit is then.note: decide next's words, the
			// latest, and the round's bar (apply) stay whole.
			reserve := 0
			if bar := roundBar(q.Round); bar != "" {
				reserve = utf8.RuneCountInString(bar) + 2
			}
			q.Note = joinNotes(q.Note, t.Note, reserve)
			q.By = t.By
			st.Directed = q
			return workflow.Result{Next: "review", State: st}, nil
		}
		st.Directed = &Directed{Note: t.Note, By: t.By}
		return workflow.Result{Next: d.Policy, State: st}, nil
	case "open_pull_request":
		st.Draft, st.GateOpened = t.Draft, true
		return workflow.Result{Next: "openPullRequest", State: st}, nil
	case "wait":
		st.PRFeedback = nil
		if i := st.Integration; i != nil && !i.Published && i.RunID == "" {
			// A conflict told to the conductor, left as it is: no integration was started for it. Its
			// reading is forgotten, as openPullRequest forgets one, so
			// the next sync that still reads it conflicting is news and
			// tells the conductor again, once per wait.
			st.Integration = nil
			if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE pull_requests pr SET mergeable_state = 'unknown', updated_at = now()
					FROM repositories r WHERE r.id = pr.repository_id AND pr.id = ANY($1) AND r.name = $2
					AND pr.number = $3 AND pr.state IN ('open', 'draft')`, st.PullRequestIDs, i.Repo, i.Number)
				return err
			}); err != nil {
				return workflow.Result{}, err
			}
		}
		return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "the conductor is waiting on the pull request")
	case "start_phase":
		st.Directed = &Directed{Phase: t.Phase, Categories: t.Categories, FindingIDs: t.FindingIDs, Note: t.Note,
			Then: t.Then, Round: t.Round, By: t.By}
		// Started while the delivery waited on its pull requests: it waits
		// on them again once this is done (startAtPRWait).
		st.ActingOnPR = d.Policy == "awaitPullRequest"
		return workflow.Result{Next: phaseStep(t.Phase, d.Point), State: st}, nil
	}
	return workflow.Result{}, fmt.Errorf("decision %q cannot be carried out", t.Action)
}

// quietDecision is a quiet conductor's decision not taken yet: waited on
// until its deadline (Pending.DecideBy), then Deliver's defaults (the
// policy's step, as a hand-back takes it) with a notice in Chat — at once
// when the conductor has no model, or its Run failed since the wait
// began. MaxDefaulted in a row (defaultsInARowTx) stops for a person
// instead. The conductor's decide is serialised with this by the row: it
// is refused unless the delivery waits (parked), and the step runs
// claimed, status running.
func (w *steps) quietDecision(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, error) {
	d := st.Decision
	var noModel, failed, said, retrying bool
	var now time.Time
	var inARow int
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
		if now, noModel, err = quietFactsTx(ctx, tx, st.TaskID); err != nil || d.Since == nil {
			return err
		}
		if inARow, said, err = defaultsInARowTx(ctx, tx, st.TaskID, sc.WorkflowRunID, st.Decisions); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT COALESCE((SELECT status = 'failed' AND ended_at > $2 FROM runs
			WHERE task_id = $1 AND role = 'conductor' ORDER BY created_at DESC LIMIT 1), false),
			EXISTS (SELECT 1 FROM tasks t WHERE t.id = $1 AND `+ConductorRetrying+`)`, st.TaskID, *d.Since).Scan(&failed, &retrying)
	}); err != nil {
		return workflow.Result{}, err
	}
	if d.DecideBy == nil {
		// Parked before its wait was kept (an escalation's resume, a
		// retry): the wait starts now.
		return w.quietWait(st, noModel, now), nil
	}
	reason := defaultedReason(d, noModel, failed, now)
	if reason == DefaultedDeadline {
		// The conductor waits on a person: its deadline is suspended while
		// the question is open, and restarts in full once it is settled.
		var held bool
		var settled *time.Time
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			var err error
			held, settled, err = conductorAsksTx(ctx, tx, st.TaskID)
			return err
		}); err != nil {
			return workflow.Result{}, err
		}
		switch {
		case held:
			res := w.parkQuiet(st)
			res.SleepUntil = now.UTC().Add(askingRecheck)
			return res, nil
		case settled != nil && now.Before(settled.Add(w.s.decisionWait())):
			// Its full window again, from the person's answer.
			at, by := settled.UTC(), settled.UTC().Add(w.s.decisionWait())
			d.Since, d.DecideBy = &at, &by
			return w.parkQuiet(st), nil
		}
	}
	if reason == "" {
		return w.parkQuiet(st), nil
	}
	if reason == DefaultedDeadline && retrying {
		// The conductor is waiting out a temporary model error to retry
		// the turn that would decide: its deadline does not default the
		// decision while the retry is pending; looked at again shortly.
		res := w.parkQuiet(st)
		res.SleepUntil = now.UTC().Add(retryingRecheck)
		return res, nil
	}
	if !said && inARow >= MaxDefaulted {
		res, err := w.escalate(ctx, sc, st, ReasonConductorUnavailable, map[string]any{"point": d.Point, "defaulted": inARow,
			"reason": reason})
		// The decision stays: retry waits on it again, accept hands it
		// to Deliver.
		st.Decision = d
		st.Escalation.Step = "conductorDecision"
		return res, err
	}
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		return defaultedTx(ctx, tx, sc, st, reason)
	}); err != nil {
		return workflow.Result{}, err
	}
	st.Decision = nil
	st.Decisions++
	return workflow.Result{Next: d.Policy, State: st}, nil
}

// ConductorRetrying (SQL, over tasks t): the task's live conductor is
// retrying a turn that failed on a temporary model error (phases'
// retryTurn): running, no control pending, a retry scheduled (deliver_after)
// or its retried turn running, and its count of retries (harness_state
// turnRetries) not reset. A paused, pausing or stopping conductor is not
// retrying, whatever count it was left with. While it is, its quiet
// decision's deadline does not default the decision (quietDecision):
// skipped rather than extended, because how long the retries take is not
// known when the first is scheduled (each failure may ask for its own
// wait), and once they end — the turn done, or the bound reached and the
// Run failed or parked as before — the deadline applies as it stood.
const ConductorRetrying = `EXISTS (SELECT 1 FROM runs cr WHERE cr.task_id = t.id AND cr.role = 'conductor' AND cr.kind = 'agent'
	AND cr.phase IS NULL AND cr.status = 'running' AND cr.control = 'none'
	AND COALESCE((cr.harness_state->>'turnRetries')::int, 0) > 0
	AND EXISTS (SELECT 1 FROM directives rd WHERE rd.run_id = cr.id AND ` + retryLive + `))`

// askingRecheck is how often a quiet decision whose conductor waits on a
// person (conductorAsksTx) looks again whether the question is settled.
const askingRecheck = 30 * time.Second

// conductorAsksTx says whether the task's live conductor waits on a person:
// a question of its Run, addressed to a person, still open. Else settled
// is when the latest such question asked by a conductor of the task was
// settled since (answered, or withdrawn with a question.closed event), nil
// for none: the quiet decision's deadline restarts in full from then. One
// its Run's end cancelled gives none: the deadline applies as it stood. Neither suspends
// nor counts toward MaxDefaulted: no decision is defaulted meanwhile.
func conductorAsksTx(ctx context.Context, tx pgx.Tx, taskID string) (held bool, settled *time.Time, err error) {
	err = tx.QueryRow(ctx, conductorAsksSQL, taskID).Scan(&held, &settled)
	return held, settled, err
}

// conductorAsksSQL is conductorAsksTx's statement ($1 the task). A
// question settled before the wait began gives a window already over. The
// task's question.closed events are read once, aggregated by question, and
// joined: not once per question (it runs every askingRecheck while held).
// A question its Run's end cancelled (runs_cancel_open_questions,
// migration 015) has neither an answer nor a question.closed event: it
// gives no settle time, and the deadline applies as it stood. The CASE on
// an open question of an ended Run does not apply on the supported path
// (every status change of a Run fires that trigger); kept for a question
// left open on a Run whose end did not pass through it (a row written
// ended, a question added after the end), whose end is then its settle
// time rather than none.
const conductorAsksSQL = `SELECT
		EXISTS (SELECT 1 FROM questions q JOIN runs r ON r.id = q.run_id
			WHERE q.task_id = $1 AND q.status = 'open' AND q.addressee = 'person' AND ` + LiveConductor + `
			AND NOT COALESCE(` + Ending + `, false)),
		(SELECT max(GREATEST(q.answered_at, c.at, CASE WHEN q.status = 'open' THEN r.ended_at END))
			FROM questions q JOIN runs r ON r.id = q.run_id AND r.role = 'conductor'
			LEFT JOIN (SELECT e.payload->>'questionId' AS qid, max(e.occurred_at) AS at FROM events e
				WHERE e.task_id = $1 AND e.event_type = '` + EvQuestionClosed + `' GROUP BY 1) c ON c.qid = q.id
			WHERE q.task_id = $1 AND q.addressee = 'person' AND q.asked_at <= now())`

// retryingRecheck is how soon a quiet decision whose deadline passed while
// its conductor retries (ConductorRetrying) is looked at again.
const retryingRecheck = 30 * time.Second

// defaultsInARowTx is how many decisions in a row the task's delivery
// (workflow wfID) left to Deliver's defaults: its decision_defaulted
// events since the last thing that gave the decisions back to someone —
// who decides or how the conductor speaks changed (decider_changed: a
// Chat takeover, a hand-back, accept), the conductor decided, or a person
// asked to wait for it again (conductor_unavailable's retry). Read from the
// ledger, not kept in the state, so a step that ran across one of these
// has no count of its own to write back. said: decision n's default is
// recorded already (a step replayed), counted in inARow.
func defaultsInARowTx(ctx context.Context, tx pgx.Tx, taskID, wfID string, n int) (inARow int, said bool, err error) {
	err = tx.QueryRow(ctx, defaultsInARowSQL, taskID, wfID, n, ReasonConductorUnavailable).Scan(&inARow, &said)
	return inARow, said, err
}

// defaultsInARowSQL is defaultsInARowTx's one statement, on
// events_task_decisions_idx (migration 238: task_id, cursor, over these
// event types alone): the newest reset, the defaults since it, the replay
// check among the defaults. The types are literals, so the planner proves
// the index's predicate; a task.decided that is not conductor_unavailable's
// retry is filtered out here. The reset is the max of the task's resets
// read whole (OFFSET 0 keeps it a subquery): a LIMIT 1 the planner may
// read backwards on the primary key, betting a reset is near the end,
// which walks every event since it.
const defaultsInARowSQL = `SELECT
		count(*) FILTER (WHERE cursor > COALESCE((SELECT max(cursor) FROM (SELECT cursor FROM events WHERE task_id = $1
			AND event_type IN ('` + EvDeciderChanged + `', '` + EvDecisionTaken + `', '` + EvTaskDecided + `')
			AND (event_type <> '` + EvTaskDecided + `' OR payload->>'reason' = $4 AND payload->>'action' = 'retry')
			OFFSET 0) resets), 0)),
		COALESCE(bool_or((payload->>'decision')::int = $3), false)
	FROM events WHERE task_id = $1 AND event_type = '` + EvDecisionDefaulted + `' AND payload->>'workflowId' = $2`

// defaultedTx records that Deliver's defaults took the delivery's pending
// decision, for reason, and says so in Chat: once per decision, however
// often its step is replayed.
func defaultedTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, reason string) error {
	d := st.Decision
	inARow, said, err := defaultsInARowTx(ctx, tx, st.TaskID, sc.WorkflowRunID, st.Decisions)
	if err != nil || said {
		return err
	}
	if err := emitTx(ctx, tx, sc.OrganizationID, st, EvDecisionDefaulted, map[string]any{"point": d.Point,
		"policy": d.Policy, "reason": reason, "count": inARow + 1, "workflowId": sc.WorkflowRunID, "decision": st.Decisions}); err != nil {
		return err
	}
	return emitTx(ctx, tx, sc.OrganizationID, st, EvChatNotice, map[string]any{"about": "decision_defaulted",
		"reason": reason, "text": DefaultedLine(reason)})
}

// defaultedReason is why Deliver's defaults take a quiet conductor's
// decision d now (the database's clock, as its deadline was set by), ""
// while it still waits for the conductor.
func defaultedReason(d *Pending, noModel, failed bool, now time.Time) string {
	switch {
	case noModel:
		return DefaultedNoModel
	case failed:
		return DefaultedRunFailed
	case !now.Before(*d.DecideBy):
		return DefaultedDeadline
	}
	return ""
}

// gateAuthorized says whether a gate the conductor entered may open under
// Deliver: an authorization at the heads now (gateHeld), or the person's
// Open or Draft at them. Both callers first send a head that is the
// conductor's untested commit to a review (reviewUntested). The answer is
// only read here; the step's result asks for it to be recorded
// (authorizeGate), which the transition does under the row lock and the
// step's lease (authorizeAtCommitTx). Until then gateHeld is false: the
// opening waits for the recorded one.
func (w *steps) gateAuthorized(ctx context.Context, sc workflow.StepContext, st *State) (bool, error) {
	if st.gateHeld() {
		return true, nil
	}
	err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		_, err := GateAnswer(ctx, tx, st)
		return err
	})
	var refused Refusal
	if errors.As(err, &refused) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if w.s.GateRead != nil {
		w.s.GateRead()
	}
	st.authorizeGate = true
	return true, nil
}

// authorizeAtCommitTx records the gate authorization a step asked for, in
// its transition's transaction: the row locked, the person's answer read
// again (the latest gate question, at the heads the row holds), so an
// answer given since the step read it wins. A transition whose lease was
// lost fails its guarded UPDATE, rolling this back with it. Heads moved
// since the step's read, or an answer that no longer authorizes, record
// nothing.
func authorizeAtCommitTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT state->'heads' FROM workflow_runs WHERE id = $1 FOR UPDATE`,
		sc.WorkflowRunID).Scan(&raw); err != nil {
		return err
	}
	var heads map[string]string
	_ = json.Unmarshal(raw, &heads)
	if !maps.Equal(nonNilMap(heads), nonNilMap(st.Heads)) {
		return nil
	}
	draft, err := GateAnswer(ctx, tx, st)
	var refused Refusal
	if errors.As(err, &refused) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := AuthorizeGateTx(ctx, tx, sc.WorkflowRunID, st, draft); err != nil {
		return err
	}
	st.GateOpened = true
	if !st.ChoiceGate {
		st.Draft = draft
	}
	return nil
}

// gateHeld says the gate was authorized at the heads the delivery is at
// now. An authorization at other heads does not count, whatever the
// latched GateOpened says.
func (st *State) gateHeld() bool {
	return st.GateOpened && st.GateAt != nil && maps.Equal(nonNilMap(st.GateAt.Heads), nonNilMap(st.Heads))
}

// GateAt is the gate's authorization: the heads it was given at.
type GateAt struct {
	Heads map[string]string `json:"heads"`
	Draft bool              `json:"draft,omitempty"`
}

// AuthorizeGateTx records the gate's authorization at st's heads, in the
// row (gateAt is owned: a step's transition keeps the row's).
func AuthorizeGateTx(ctx context.Context, tx pgx.Tx, wfID string, st *State, draft bool) error {
	if err := FreezePublicationTx(ctx, tx, st.TaskID, st.Branch); err != nil {
		return err
	}
	st.GateAt = &GateAt{Heads: nonNilMap(st.Heads), Draft: draft}
	raw, err := json.Marshal(st.GateAt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{gateAt}', $2::jsonb) WHERE id = $1`, wfID, raw)
	return err
}

// parkAtGate parks a delivery Deliver decides at the pull request gate —
// one the conductor entered, an adopted branch's, or the task's Ask me
// first — asking the person, until they answer Open or Draft or confirm the
// opening at a hand-back. It also waits on the people's feedback, which
// sends the work to a fix (feedbackAtGate). entering: it parks there now,
// which is recorded once, with the heads it is shown at (State.Hold).
func (w *steps) parkAtGate(ctx context.Context, sc workflow.StepContext, st *State, entering bool) (workflow.Result, error) {
	st.Decision = &Pending{Point: PointBeforePR, Policy: "openPullRequest"}
	note := "Deliver waits for the person's Open or Draft before it opens the pull request."
	if entering {
		if err := w.holdAt(ctx, sc, st); err != nil {
			return workflow.Result{}, err
		}
		st.Hold.SentBack = false
	}
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		if err := askGateTx(ctx, tx, sc.OrganizationID, st); err != nil || !entering {
			return err
		}
		// The hold's time is its event's, to the microsecond Postgres keeps.
		now := time.Now().UTC().Truncate(time.Microsecond)
		st.Hold.Since = &now
		_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvDecisionAwaited, OrganizationID: sc.OrganizationID,
			ProjectID: st.ProjectID, TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "workflow",
			Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID, OccurredAt: now,
			Payload: map[string]any{"point": PointBeforePR, "policy": "openPullRequest", "actions": []string{}, "phases": []string{},
				"note": note, "round": st.Hold.Round}})
		return err
	}); err != nil {
		return workflow.Result{}, err
	}
	if (st.adoptedBranch() || st.ChoiceGate) && st.deliverOpens() {
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			_, err := ToPersonTx(ctx, tx, ToPerson{Ref: RunRef{Org: sc.OrganizationID}, State: st, Reason: ReasonReadyForReview,
				Why: WhyPersonGate})
			return err
		}); err != nil {
			return workflow.Result{}, err
		}
	}
	return atGate(st), nil
}

// ReasonReadyForReview is the task's status reason while Deliver holds it
// before its pull request for its owner.
const ReasonReadyForReview = "Ready for your review"

// atGate waits at a gate Deliver holds: the person's answer, or the
// people's feedback (feedbackAtGate).
func atGate(st *State) workflow.Result {
	return workflow.Result{Next: "conductorDecision", State: st, AwaitSignals: []string{SignalConductorDecision, SignalHumanFeedback}}
}

// Hold is where Deliver last held the delivery before its pull request:
// the round (1, then one more after each fix of the people's feedback), the
// heads it is shown at, when it last parked there (Since), and the heads
// the round before was shown at (Previous), with the task's own commits
// since them (not the base's, nor a merge of it) in each repository GitHub
// could compare; and what each repository would open (Repos), measured
// against its base. A rebase or force-push between holds gives the task's
// commits new SHAs, which CommitsSince counts as new. SentBack: the
// people's feedback sent from the hold is being fixed, until the delivery
// parks there again. Cleared once a pull request opens.
type Hold struct {
	Round        int               `json:"round"`
	Heads        map[string]string `json:"heads"`
	Since        *time.Time        `json:"since,omitempty"`
	Previous     map[string]string `json:"previous,omitempty"`
	CommitsSince *int              `json:"commitsSince,omitempty"`
	Repos        []HeldRepo        `json:"repos,omitempty"`
	SentBack     bool              `json:"sentBack,omitempty"`
}

// HeldRepo is one repository a held delivery would open a pull request in:
// its branch into its base, and the lines it changes there (nil when
// GitHub could not compare them).
type HeldRepo struct {
	Name      string `json:"name"`
	Head      string `json:"head"`
	Base      string `json:"base"`
	Additions *int   `json:"additions,omitempty"`
	Deletions *int   `json:"deletions,omitempty"`
}

// holdAt records the hold at st's heads: a new round when they moved since
// the last one. What it reads of GitHub is for the person; a compare it
// cannot make leaves its numbers out.
func (w *steps) holdAt(ctx context.Context, sc workflow.StepContext, st *State) error {
	heads := nonNilMap(st.Heads)
	if st.Hold != nil && maps.Equal(st.Hold.Heads, heads) {
		return nil
	}
	var prev map[string]string
	if st.Hold == nil {
		st.Hold = &Hold{Round: 1, Heads: maps.Clone(heads)}
	} else {
		prev = st.Hold.Heads
		st.Hold = &Hold{Round: st.Hold.Round + 1, Heads: maps.Clone(heads), Previous: prev}
	}
	var repos []Repository
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
		repos, err = TaskRepositories(ctx, tx, st.TaskID)
		return err
	}); err != nil {
		return err
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		gh = nil
	}
	n, counted := 0, prev != nil && gh != nil
	for _, repo := range repos {
		to := heads[repo.Name]
		if to == "" {
			continue
		}
		base := repo.DefaultBranch
		if b := st.Bases[repo.Name]; b != "" {
			base = b
		}
		held := HeldRepo{Name: repo.Name, Head: st.Branch, Base: base}
		if gh != nil {
			cmp, err := gh.CompareCommits(ctx, forge.SlugFromURL(repo.URL), base, to)
			if err == nil && !cmp.Truncated() {
				add, del := 0, 0
				for _, f := range cmp.Files {
					add, del = add+f.Additions, del+f.Deletions
				}
				held.Additions, held.Deletions = &add, &del
			}
			if from := prev[repo.Name]; counted && from != "" && from != to {
				before, beforeErr := gh.CompareCommits(ctx, forge.SlugFromURL(repo.URL), base, from)
				if err != nil || len(cmp.Commits) < cmp.TotalCommits || beforeErr != nil || len(before.Commits) < before.TotalCommits {
					counted = false
				} else {
					n += tasksOwnSince(cmp.Commits, before.Commits)
				}
			}
		}
		st.Hold.Repos = append(st.Hold.Repos, held)
	}
	if counted {
		st.Hold.CommitsSince = &n
	}
	return nil
}

// tasksOwnSince counts the commits of now (base...head, listed whole) that
// are not in then (base...the previous hold's head) and are not a merge of
// the base: a merge with any parent outside now's list, which the base
// holds (syncBeforePR's, a person's Update branch, or main merged with the
// branch as its second parent and pushed to it). A task's first commit has
// its parent in the base too, but it is never a merge.
func tasksOwnSince(now, then []forge.ComparedCommit) int {
	inNow := make(map[string]bool, len(now))
	for _, c := range now {
		inNow[c.SHA] = true
	}
	inThen := make(map[string]bool, len(then))
	for _, c := range then {
		inThen[c.SHA] = true
	}
	n := 0
	for _, c := range now {
		if inThen[c.SHA] {
			continue
		}
		baseMerge := false
		if len(c.Parents) > 1 {
			for _, p := range c.Parents {
				baseMerge = baseMerge || !inNow[p.SHA]
			}
		}
		if !baseMerge {
			n++
		}
	}
	return n
}

// feedbackAtGate takes the people's feedback signalled to a hold Deliver
// keeps before the pull request: with items open, the task goes back to a
// fix with a fresh loop budget, as the reader's wait does (awaitRead), and
// the round's review, simplify and test bring it back to the hold at its
// new heads, which asks again (gateHeld compares heads). Handled already,
// nothing starts.
func (w *steps) feedbackAtGate(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, bool, error) {
	open, err := w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st)
	if err != nil || len(open) == 0 {
		return workflow.Result{}, false, err
	}
	// The work leaves the hold's heads for a fix: the question about them
	// is over, and asked again when the work comes back to the hold.
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		return leaveGateTx(ctx, tx, sc.OrganizationID, st)
	}); err != nil {
		return workflow.Result{}, false, err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", ReasonFixingFeedback); err != nil {
		return workflow.Result{}, false, err
	}
	st.Decision = nil
	st.Decisions++
	st.Retries++
	st.Iteration, st.PendingRunIDs = 0, nil
	if st.Hold != nil {
		st.Hold.SentBack = true
	}
	res, err := w.next(ctx, sc, st, PointReviewed, "fix", feedbackOpenLine(feedbackBeforePR, len(open)))
	return res, true, err
}

// ReasonFixingFeedback is the task's status reason while a fix of the
// feedback its owner sent from the hold runs.
const ReasonFixingFeedback = "fixing your feedback"

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

// apply gives a phase Run the conductor's mark.
func (d *Directed) apply(p *PhaseRun) {
	if d != nil {
		p.ConductorRunID, p.ConductorNote = d.By, d.Note
		if bar := roundBar(d.Round); bar != "" {
			// The conductor's bar for the round, quoted as its words.
			p.ConductorNote = joinNotes(d.Note, bar, 0)
		}
	}
}

// roundBar is the round's bar as a Run's note quotes it, "" for none.
func roundBar(r *RoundAsk) string {
	if r == nil || r.Bar == "" {
		return ""
	}
	return strings.TrimSpace(render(roundBarTmpl, r.Bar))
}

// joinNotes is head then tail, a blank line between, within noteChars less
// reserve: what is cut to fit is head, tail is kept whole. An empty side
// is left out.
func joinNotes(head, tail string, reserve int) string {
	room := noteChars - reserve - utf8.RuneCountInString(tail) - 2
	if head = strings.TrimSpace(head); room <= 0 {
		head = ""
	} else {
		head = clip(head, room)
	}
	return strings.TrimSpace(head + "\n\n" + tail)
}

// RecordWakeTx records a reason to wake the task's conductor, once per key.
// Returns whether it is new.
func RecordWakeTx(ctx context.Context, tx pgx.Tx, org, taskID, kind, key, line string) (bool, error) {
	return recordWakeTx(ctx, tx, org, taskID, kind, key, line, false)
}

// recordWakeTx is RecordWakeTx; settled records the reason as heard
// already, so it marks the key without waking anyone.
func recordWakeTx(ctx context.Context, tx pgx.Tx, org, taskID, kind, key, line string, settled bool) (bool, error) {
	limit := wakeLineChars
	switch kind {
	case WakeStalled:
		// A stall's facts, which the conductor decides on without a tool.
		limit = stalledLineChars
	case WakeWait:
		// A wait's digest: what held and what did not, worst first.
		limit = waitLineChars
	case "escalation":
		// The findings it names and the direction the person must give.
		limit = escalationLineChars
	case WakeRound:
		// A conducted round's findings at the fix bar, each with its facts.
		limit = roundLineChars
	case WakeChecks:
		// A checks report's summary and one question per check.
		limit = checksLineChars
	case WakeRecovered:
		// A refusal's reason and, for a copy gone, the person's warning.
		limit = recoveredLineChars
	}
	text := clip(oneLine(line), limit)
	if kind == "escalation" && strings.HasSuffix(key, ":decided") {
		// A person's words are not a machine-generated fact to summarize.
		text = line
	}
	tag, err := tx.Exec(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, delivered_at)
		VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $7 THEN now() END) ON CONFLICT (task_id, key) DO NOTHING`,
		ids.New("cwk"), org, taskID, kind, key, text, settled)
	return err == nil && tag.RowsAffected() == 1, err
}

// wakeLineChars bounds one reason's line, as the table does; a stall's,
// stalledLineChars; an escalation's, escalationLineChars.
const (
	wakeLineChars       = 300
	stalledLineChars    = 2000
	escalationLineChars = 1200
)

// tellEscalationTx tells the conductor of the escalation the delivery waits
// on a person at: its wake, kind escalation, keyed on its number among those
// told (State.Escalations). Whoever escalated: one the conductor had when it
// escalated (escalate, a held checks report), or one Deliver stopped at,
// handed to the conductor while it waits (SetDeciderTx) or while its step
// escalated (recheck). Once per escalation (Escalation.Told), however often
// it is handed over; a step replayed tells it again with its same key, which
// records nothing new. Returns whether it told it now, st changed.
func tellEscalationTx(ctx context.Context, tx pgx.Tx, org, wfID string, st *State) (bool, error) {
	e := st.Escalation
	if e == nil || e.Decided != nil || e.Told != 0 {
		return false, nil
	}
	st.Escalations++
	e.Told = st.Escalations
	_, err := RecordWakeTx(ctx, tx, org, st.TaskID, "escalation", fmt.Sprintf("%s:escalation:%d", wfID, st.Escalations),
		escalatedLine(e))
	return err == nil, err
}

// WhyAcceptedDefaults is why a delivery was handed to Deliver's policy
// when its person accepted conductor_unavailable: the one delivery no
// conductor decides, by the person's choice (AcceptedDefaultsTx).
const WhyAcceptedDefaults = "the person accepted Deliver's defaults"

// AcceptedDefaultsTx says the task's delivery is Deliver's because its
// person accepted Deliver's defaults: its latest change of decider is that
// accept. Nothing re-conducts it silently — not a Stop's or a pick-up's
// next attempt, nor the boot sweep (ConvertToQuietTx); a person's Chat or
// an explicit hand to the conductor does.
func AcceptedDefaultsTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	var accepted bool
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT payload->>'to' = $2 AND payload->>'why' = $3 FROM events
		WHERE task_id = $1 AND event_type = '`+EvDeciderChanged+`' ORDER BY cursor DESC LIMIT 1), false)`,
		taskID, DeciderPolicy, WhyAcceptedDefaults).Scan(&accepted)
	return accepted, err
}

// PolicyWhyTx is why Deliver's policy, not a quiet conductor, decides the
// task's delivery from here (a Start over's, the boot sweep's): its person
// accepted Deliver's defaults (WhyAcceptedDefaults), else its conductor has
// no model (WhyNoConductorModel); "" when a quiet conductor does.
func PolicyWhyTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	if accepted, err := AcceptedDefaultsTx(ctx, tx, taskID); err != nil || accepted {
		return WhyAcceptedDefaults, err
	}
	if noModel, err := ConductorNoModelTx(ctx, tx, taskID); err != nil || noModel {
		return WhyNoConductorModel, err
	}
	return "", nil
}

// SetDeciderTx changes who decides a task's delivery, and under the
// conductor how it speaks (voice: VoiceQuiet, VoiceChatty; "" keeps the
// delivery's), recording it once: a no-op when both already are. Returns
// whether it changed. A conductor whose delivery changes voice is told
// (voiceToldTx): its instruction file says the voice it was submitted with.
func SetDeciderTx(ctx context.Context, tx pgx.Tx, org, wfID string, st *State, to, voice, why, actorType, actorID string) (bool, error) {
	from, fromVoice := st.Decider, st.voice()
	if from == "" {
		from = DeciderPolicy
	}
	if to != DeciderConductor {
		voice = ""
	} else if voice == "" {
		voice = cmp.Or(st.Voice, VoiceChatty)
	}
	if from == to && (to != DeciderConductor || fromVoice == voice) {
		return false, nil
	}
	// Not while the conductor's publish moves the task branch: the caller
	// holds the delivery's row (LoadDelivery), which the reservation takes.
	if err := RefuseWhileMovingTx(ctx, tx, st.TaskID); err != nil {
		return false, err
	}
	st.Decider = to
	st.HadConductor = st.HadConductor || to == DeciderConductor
	if voice != "" {
		st.Voice = voice
	}
	// Handed to the conductor while it waits on a person at an escalation:
	// the conductor is told of it, as of one it had when it escalated.
	told := false
	if to == DeciderConductor {
		var err error
		if told, err = tellEscalationTx(ctx, tx, org, wfID, st); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = (jsonb_set(state, '{decider}', to_jsonb($2::text))
			|| CASE WHEN $2 = 'conductor' THEN jsonb_build_object('hadConductor', true, 'voice', $3::text) ELSE '{}'::jsonb END)
			WHERE id = $1`, wfID, to, voice); err != nil {
		return false, err
	}
	if told {
		// The fields the telling changed, beside the decider: the
		// escalation (now told) and its number. Its decision and direction
		// stay: Directed is the conductor's alone, and a Pending decision
		// here is Deliver's own hold, which the escalation resumes to.
		raw, err := json.Marshal(st.Escalation)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = state
				|| jsonb_build_object('escalation', $2::jsonb, 'escalations', $3::int) WHERE id = $1`,
			wfID, raw, st.Escalations); err != nil {
			return false, err
		}
	}
	if fromVoice != "" && voice != "" && fromVoice != voice {
		if err := voiceToldTx(ctx, tx, org, st.TaskID, voice); err != nil {
			return false, err
		}
	}
	payload := map[string]any{"from": from, "to": to, "why": why}
	if voice != "" {
		payload["voice"] = voice
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvDeciderChanged, OrganizationID: org, ProjectID: st.ProjectID,
		TaskID: st.TaskID, ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID,
		Payload: payload})
	return true, err
}

// voiceWakePrefix keys the reason that tells a conductor its delivery's
// voice changed: held news (heldWakePrefix), as a merge's is (retest).
const voiceWakePrefix = heldWakePrefix + "voice:"

// voiceToldTx tells the conductor its delivery's voice, which its
// instruction file says only as it was submitted: one line, recorded
// as a reason in the same transaction as the change, as an escalation
// handed to it is told (tellEscalationTx). It is news, not a call to act:
// told at once to a conductor between turns, else in its next note, never
// spending a turn by itself. A change not yet told replaces the one before
// it: only the latest voice is said.
func voiceToldTx(ctx context.Context, tx pgx.Tx, org, taskID, voice string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM conductor_wakes WHERE task_id = $1 AND key LIKE $2 AND delivered_at IS NULL`,
		taskID, voiceWakePrefix+"%"); err != nil {
		return err
	}
	_, err := RecordWakeTx(ctx, tx, org, taskID, "decision", voiceWakePrefix+ids.New("v"), VoiceChangedLine(voice))
	return err
}

// VoiceChangedLine is the fact a conductor is told when its delivery's
// voice changes.
func VoiceChangedLine(voice string) string {
	return render(voiceChangedTmpl, struct{ Quiet bool }{voice == VoiceQuiet})
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
