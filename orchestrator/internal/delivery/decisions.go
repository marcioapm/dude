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
	// open_pull_request: a draft, as the person answered.
	Draft bool `json:"draft,omitempty"`
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

// ConductedLoop says the review loop is the conductor's: the conductor
// decides the delivery now. A delivery that was in flight before the
// conducted loop existed gets it on its next round too. One a conductor
// had (HadConductor), handed back to Deliver, keeps it: the fix bar it had
// does not drop with the conductor. A delivery handed back before
// HadConductor existed has no bit, and goes back to the project's bar.
func (st *State) ConductedLoop() bool { return st.conducted() || st.HadConductor }

// Phases is what the conductor may start at a decision point.
func (d *Pending) Phases() []string {
	switch d.Point {
	case PointStart:
		if d.Adopted {
			return []string{PhaseImplement, PhaseReview}
		}
		return []string{PhaseImplement}
	case PointPRFeedback:
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
	if st.conducted() && point == PointBeforePR {
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
	p := *d.Then
	p.Review, p.By, p.Fix = slices.Clone(p.Review), d.By, ""
	if d.Round != nil {
		p.Scope, p.Bar = d.Round.Scope, d.Round.Bar
	}
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
	if stop != "" {
		line += pipelineStopped(p, stop, out, true)
	} else {
		line += render(pipelineQueuedTmpl, struct{ Review []string }{p.Review})
	}
	if len(st.FixRange) > 0 && len(out.Heads) > 0 {
		line += render(pipelineUnreviewedTmpl, nil)
	}
	res, err := w.next(ctx, sc, st, PointFixed, policy, line)
	// A round's wake: its facts are not a decision's 300 characters.
	st.Routed.Queued, st.Routed.Kind = p.queued(), WakeRound
	return res, true, err
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
	if sc.Step != "decide" {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id=$1`, sc.WorkflowRunID).Scan(&raw); err != nil {
			return res, false, err
		}
		var current State
		if err := json.Unmarshal(raw, &current); err != nil {
			return res, false, err
		}
		if e := current.Escalation; e != nil && e.Reason == ReasonChecksUnverified {
			return workflow.Result{Next: "decide", State: &current, AwaitSignals: []string{SignalHumanDecision}}, true, nil
		}
	}
	st, ok := res.State.(*State)
	if !ok {
		return res, false, nil
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
	var decider string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(state->>'decider', '') FROM workflow_runs WHERE id = $1 FOR UPDATE`,
		sc.WorkflowRunID).Scan(&decider); err != nil {
		return res, false, err
	}
	st.Decider = decider
	if decider != DeciderConductor || res.Next != r.Next {
		if res.Next == "openPullRequest" && !st.GateRequired {
			if err := FreezePublicationTx(ctx, tx, st.TaskID, st.Branch); err != nil {
				return res, false, err
			}
		}
		return res, true, nil
	}
	out, err := toConductorKindTx(ctx, tx, sc, st, r.Point, r.Policy, r.Line, cmp.Or(r.Kind, "decision"))
	if err == nil && r.Queued != nil {
		st.Decision.Queued = r.Queued
	}
	return out, true, err
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
	return toConductorKindTx(ctx, tx, sc, st, point, policy, line, "decision")
}

// toConductorKindTx is toConductorTx recording its wake as kind.
func toConductorKindTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, point, policy, line, kind string) (workflow.Result, error) {
	st.Directed, st.PendingRunIDs, st.Routed = nil, nil, nil
	st.Decision = &Pending{Point: point, Policy: policy,
		Adopted: st.Source != nil && st.HeadRunID == "" && (point == PointStart || point == PointImplemented)}
	if point == PointBeforePR {
		st.GateRequired, st.ChoiceGate = true, false
	}
	note := decisionWaitingNote(point, line)
	added, err := recordWakeTx(ctx, tx, sc.OrganizationID, st.TaskID, kind,
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
	if d.Taken == nil {
		return workflow.Result{Next: "conductorDecision", State: st, AwaitSignals: []string{SignalConductorDecision}}, nil
	}
	t := d.Taken
	st.Decision = nil
	st.Decisions++
	switch t.Action {
	case "next":
		if q := d.Queued; q != nil {
			// The review the conductor queued, with what it says now.
			q.Note = clip(strings.TrimSpace(strings.Join([]string{q.Note, t.Note}, "\n\n")), noteChars)
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
			// A conflict told to the conductor (pullRequestFixes direct),
			// left as it is: no integration was started for it. Its
			// reading is forgotten, as openPullRequest forgets one, so
			// the next sync that still reads it conflicting is news and
			// tells the conductor again, once per wait.
			st.Integration = nil
			if st.Policy.DirectFixes() {
				if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE pull_requests pr SET mergeable_state = 'unknown', updated_at = now()
						FROM repositories r WHERE r.id = pr.repository_id AND pr.id = ANY($1) AND r.name = $2
						AND pr.number = $3 AND pr.state IN ('open', 'draft')`, st.PullRequestIDs, i.Repo, i.Number)
					return err
				}); err != nil {
					return workflow.Result{}, err
				}
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
	if (st.adoptedBranch() || st.ChoiceGate) && !st.conducted() {
		if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "awaiting_input", ReasonReadyForReview); err != nil {
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
		if d.Round != nil && d.Round.Bar != "" {
			// The conductor's bar for the round, quoted as its words.
			p.ConductorNote = clip(strings.TrimSpace(d.Note+"\n\n"+render(roundBarTmpl, d.Round.Bar)), noteChars)
		}
	}
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
	// Not while the conductor's publish moves the task branch: the caller
	// holds the delivery's row (LoadDelivery), which the reservation takes.
	if err := RefuseWhileMovingTx(ctx, tx, st.TaskID); err != nil {
		return false, err
	}
	st.Decider = to
	st.HadConductor = st.HadConductor || to == DeciderConductor
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{decider}', to_jsonb($2::text))
			|| CASE WHEN $2 = 'conductor' THEN '{"hadConductor": true}'::jsonb ELSE '{}'::jsonb END WHERE id = $1`,
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
