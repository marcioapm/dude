package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// WorkflowType is the delivery workflow's type name, shared with the rows it
// has already written.
const WorkflowType = "task.delivery"

// Signals the workflow parks on.
const (
	// A phase Run reached a terminal state. Payload: {runId, status}.
	SignalPhaseFinished = "phase.finished"
	// A pull request change the classifier judged worth acting on.
	SignalPRFeedback = "pr.feedback"
	// A person answered an escalation.
	SignalHumanDecision = "human.decision"
)

// State is everything the workflow remembers between steps.
type State struct {
	TaskID    string `json:"taskId"`
	ProjectID string `json:"projectId"`
	Policy    Policy `json:"policy"`

	// The Run whose output the next phase builds on.
	HeadRunID string `json:"headRunId,omitempty"`
	// The task's branch, the same name in every repository it changes.
	Branch string `json:"branch,omitempty"`
	// Where the work stands in each repository it changed, by name: what
	// the next phase checks out. A repository not here starts from its
	// default branch.
	Heads map[string]string `json:"heads,omitempty"`
	// What changed, across repositories, as <repo>/<path> — what picks the
	// reviewers.
	ChangedPaths []string `json:"changedPaths,omitempty"`

	// Phase Runs being waited for.
	PendingRunIDs []string `json:"pendingRunIds,omitempty"`
	// Review → fix cycles spent.
	Iteration int `json:"iteration,omitempty"`
	// PR feedback → fix cycles spent in this review round: a person's new
	// review (not a comment, not CI failing again) starts another.
	PRIteration int `json:"prIteration,omitempty"`
	// Fixes each pull request has had, by repository (a task has one pull
	// request per repository): bounded by the organization's fix rounds per
	// pull request. A fix counts against every pull request whose feedback
	// it addressed, and no other.
	PRFixes map[string]int `json:"prFixesByRepo,omitempty"`
	// Names each PR fix's Run. Only grows, so a round starting over never
	// finds an earlier fix.
	PRFixKey int `json:"prFixKey,omitempty"`
	// The pull request feedback being fixed.
	PRFeedback []forge.ActionableFeedback `json:"prFeedback,omitempty"`
	// The pull requests opened, one per repository changed.
	PullRequestIDs []string `json:"pullRequestIds,omitempty"`
	// Why the workflow stopped for a person, while it waits on one.
	Escalation *Escalation `json:"escalation,omitempty"`
	// How often a person sent it back to try again: each retry's Runs are
	// new ones, not the ones that stopped.
	Retries int `json:"retries,omitempty"`
	// Fix attempts per finding a person granted beyond the policy's, by
	// sending a stuck review back to try again.
	ExtraFixAttempts int `json:"extraFixAttempts,omitempty"`

	// Which attempt at the task this delivery is: each start over is a new
	// one, on a branch of its own. Zero from before attempts were kept here
	// (the attempt is then the task's highest).
	Attempt int `json:"attempt,omitempty"`
	// The escalation a person stopped the delivery at, kept so the task can
	// be picked back up from where it stopped.
	Stopped *Escalation `json:"stopped,omitempty"`
	// A person picking a stopped task back up (Recover), while the workflow
	// carries it out.
	Recover *Recover `json:"recover,omitempty"`

	// Who takes the decisions: DeciderPolicy ("" from before the conductor
	// decided anything) or DeciderConductor. Written from outside the steps
	// (Chat, the hand-back), so a step's transition keeps the row's.
	Decider string `json:"decider,omitempty"`
	// Decisions were handed back to the policy on this delivery: a later
	// message in Chat does not take them over again.
	HandedBack bool `json:"handedBack,omitempty"`
	// The decision the workflow is parked on for the conductor.
	Decision *Pending `json:"decision,omitempty"`
	// Decisions taken so far: what makes each one's Runs and wake its own.
	Decisions int `json:"decisions,omitempty"`
	// The conductor's direction for the step it sent the workflow to.
	Directed *Directed `json:"directed,omitempty"`
	// Open the pull requests as drafts: the person answered Draft.
	Draft bool `json:"draft,omitempty"`
	// The person's Open or Draft, taken by the conductor (or confirmed at a
	// hand-back): under the conductor, the pull requests open only with it.
	GateOpened bool `json:"gateOpened,omitempty"`
	// The conductor entered the pull request gate on this delivery: the
	// opening needs an authorization at the current heads (gateHeld), or
	// an Open or Draft at them, whoever decides by then.
	GateRequired bool `json:"gateRequired,omitempty"`
	// The heads the gate was authorized at, and as a draft or not: written
	// with GateOpened, outside the steps (AuthorizeGateTx) or in a step's
	// transition (authorizeAtCommitTx). GateOpened and Draft are latched
	// and outlive a head change; this decides.
	GateAt *GateAt `json:"gateAt,omitempty"`
	// The step read an answer authorizing the gate: its transition records
	// it (recheck). Never persisted.
	authorizeGate bool
	// Escalations told to the conductor: each one's wake its own.
	Escalations int `json:"escalations,omitempty"`
	// A decision point the policy took in the step committing (next): set
	// only between the step and its transition, which re-checks it.
	Routed *Routed `json:"routed,omitempty"`
}

// Recover is a person picking a stopped task back up: resume the Runs that
// stopped, at the step that waited on them, or try that step again.
type Recover struct {
	// "resume" or "retry". (Starting over is a new delivery, not this one.)
	Action string `json:"action"`
	// Resume: the step that waits on the Runs taken back up.
	At string `json:"at,omitempty"`
	// Retry: the step to run again. Why it stopped (State.Stopped, nil for
	// an abort) says what budget it gets back.
	Step string `json:"step,omitempty"`
}

type Escalation struct {
	Reason string `json:"reason"`
	Detail any    `json:"detail,omitempty"`
	// The step to go back to on "retry": the one that stopped.
	Step string `json:"step,omitempty"`
	// The step that was waiting on the Run that failed, for "resume": it
	// waits on it again once it is taken back up. "" when no Run failed.
	At string `json:"at,omitempty"`
	// A person's decision, once made: set as it is taken, so a second is
	// refused before the workflow has acted on the first.
	Decided *HumanDecision `json:"decided,omitempty"`
}

// Actions says what a person may do about it: go back and try again,
// where there is a step to go back to and trying again can help; go on
// past findings a review got stuck on; take what was merged as the task;
// wait on pull requests still open; or stop.
func (e *Escalation) Actions() []string {
	var out []string
	switch e.Reason {
	case "pull_request_closed":
		out = append(out, "done")
		if open, _ := e.detail("open"); open > 0 {
			out = append(out, "wait")
		}
	case "stuck", "exhausted":
		out = append(out, "retry", "accept")
	case "pull_request_conflict", "ci_stuck":
		// A person resolves the conflict, or sees to CI, on GitHub; dude
		// waits on the pull request again.
		out = append(out, "wait")
	default:
		// A Run that failed is kept a while (phases.keep): the same agent
		// can carry on where it stopped. Whether it still is, the API checks.
		if e.At != "" && e.RunID() != "" {
			out = append(out, "resume")
		}
		if e.Step != "" {
			out = append(out, "retry")
		}
	}
	return append(out, "stop")
}

// RunID is the Run the escalation is about, "" when it names none.
func (e *Escalation) RunID() string {
	var d struct {
		RunID string `json:"runId"`
	}
	e.decode(&d)
	return d.RunID
}

// detail reads a number the escalation's detail carries, as JSON left it.
func (e *Escalation) detail(key string) (float64, bool) {
	var m map[string]any
	if !e.decode(&m) {
		return 0, false
	}
	n, ok := m[key].(float64)
	return n, ok
}

// decode reads the detail into out, as it reads once stored.
func (e *Escalation) decode(out any) bool {
	b, _ := json.Marshal(e.Detail)
	return json.Unmarshal(b, out) == nil
}

// spent: the pull requests (by repository) a pr_loop_exhausted escalation
// found past their budget.
func (e *Escalation) spent() []string {
	var d struct {
		Spent []string `json:"spent"`
	}
	e.decode(&d)
	return d.Spent
}

// HumanDecision is a person's answer to an escalation (SignalHumanDecision),
// kept on it once taken (Escalation.Decided).
type HumanDecision struct {
	// "resume" the Run that failed; "retry" the step that stopped; "accept"
	// the findings a review got stuck on and go on; "done" — what is merged
	// is the task; "wait" on the pull requests still open; "stop".
	Action string `json:"action"`
	// What they said, for the agents from here on.
	Note string `json:"note,omitempty"`
}

// BranchFor is the task's branch — the one its pull requests are opened
// from, the same name in each repository. Phase Runs never push to it directly; each pushes its own branch and
// dude fast-forwards this one.
func BranchFor(taskID string, attempt int) string {
	return fmt.Sprintf("dude/%s/attempt-%d", taskID, attempt)
}

// Workflow builds the delivery workflow's definition.
//
//	implement → review ⟲ fix → simplify → [test] → PR → PR feedback ⟲ fix → done
//
// Two properties it exists to guarantee: every loop ends on a bound declared
// in policy, never on a model deciding it is finished; and waking an agent is
// a decision, not a reflex — an approval or a green check wakes nobody.
func Workflow(s *Store, forges Forges) *workflow.Definition {
	w := &steps{s: s, forges: forges}
	return &workflow.Definition{
		Type:        WorkflowType,
		InitialStep: "implement",
		Owned:       []string{"decider", "handedBack", "gateAt"},
		// The gate's authorization: set by the conductor's decision, or by
		// a hand-back while a step runs.
		Latched: []string{"draft", "gateOpened"},
		Recheck: w.recheck,
		Steps: map[string]workflow.Step{
			"implement":         w.implement,
			"implementRun":      w.implementRun,
			"reviewExit":        w.reviewExit,
			"conductorDecision": w.conductorDecision,
			"awaitImplement":    w.awaitImplement,
			"review":            w.review,
			"awaitReview":       w.awaitReview,
			"fix":               w.fix,
			"awaitFix":          w.awaitFix,
			"simplify":          w.simplify,
			"awaitSimplify":     w.awaitSimplify,
			"test":              w.test,
			"awaitTest":         w.awaitTest,
			"openPullRequest":   w.openPullRequest,
			"awaitPullRequest":  w.awaitPullRequest,
			"prFix":             w.prFix,
			"awaitPRFix":        w.awaitPRFix,
			"decide":            w.decide,
			"recover":           w.recover,
		},
	}
}

type steps struct {
	s      *Store
	forges Forges
}

func load(sc workflow.StepContext) (*State, error) {
	var st State
	if err := json.Unmarshal(sc.State, &st); err != nil {
		return nil, fmt.Errorf("decode delivery state: %w", err)
	}
	return &st, nil
}

// settle folds finished-phase signals into the set still being waited for.
// Signals are consumed once, so a step parked on several Runs must record
// each arrival in state rather than read them again later.
func settle(st *State, sc workflow.StepContext) []string {
	finished := map[string]bool{}
	for _, sig := range sc.Signals {
		if sig.Name != SignalPhaseFinished {
			continue
		}
		var p struct {
			RunID string `json:"runId"`
		}
		if json.Unmarshal(sig.Payload, &p) == nil {
			finished[p.RunID] = true
		}
	}
	var pending []string
	for _, id := range st.PendingRunIDs {
		if !finished[id] {
			pending = append(pending, id)
		}
	}
	return pending
}

func park(next string, st *State, pending []string) workflow.Result {
	st.PendingRunIDs = pending
	return workflow.Result{Next: next, State: st, AwaitSignals: []string{SignalPhaseFinished}}
}

// key makes phase-Run creation idempotent per workflow step and iteration —
// and per retry, so a step a person sent back makes new Runs rather than
// finding the ones that stopped.
func key(sc workflow.StepContext, st *State, parts ...any) string {
	if st.Retries > 0 {
		parts = append(parts, ":retry:", st.Retries)
	}
	return fmt.Sprint(append([]any{sc.WorkflowRunID}, parts...)...)
}

// phase creates a phase Run starting from where the work stands: each
// repository at its head, or its default branch if nothing changed it yet.
func (w *steps) phase(ctx context.Context, sc workflow.StepContext, st *State, phase, k string, extra func(*PhaseRun)) (string, error) {
	in := PhaseRun{TaskID: st.TaskID, Phase: phase, BaseRefs: st.Heads, ParentRunID: st.HeadRunID, Key: k, Attempt: st.Attempt}
	if extra != nil {
		extra(&in)
	}
	return w.s.CreatePhaseRun(ctx, sc.OrganizationID, in)
}

func (w *steps) implement(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	// The conductor plans with the person before anything is built: the
	// first implementer is its call. A person's "try again" is not.
	if st.conducted() && st.Retries == 0 {
		if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "planning with the conductor"); err != nil {
			return workflow.Result{}, err
		}
		return w.next(ctx, sc, st, PointStart, "implementRun", "")
	}
	return w.implementRun(ctx, sc)
}

// implementRun starts the implementer: at once under the policy, at the
// conductor's word under the conductor.
func (w *steps) implementRun(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "implementing"); err != nil {
		return workflow.Result{}, err
	}
	k := key(sc, st, ":implement")
	d := directed(st, PhaseImplement)
	if d != nil {
		k = conductorKey(sc, st, d, ":implement")
	}
	runID, err := w.phase(ctx, sc, st, PhaseImplement, k, d.apply)
	if err != nil {
		return workflow.Result{}, err
	}
	st.Iteration = 0
	return park("awaitImplement", st, []string{runID}), nil
}

func (w *steps) awaitImplement(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if pending := settle(st, sc); len(pending) > 0 {
		return park("awaitImplement", st, pending), nil
	}
	runID := st.PendingRunIDs[0]
	out, err := w.s.PhaseOutcome(ctx, sc.OrganizationID, runID)
	if err != nil {
		return workflow.Result{}, err
	}
	if !out.Succeeded {
		return w.escalate(ctx, sc, st, "implement_failed", map[string]any{"runId": runID, "error": out.Error})
	}
	// An implementer that changed nothing has not done the work, and there
	// is nothing for a reviewer to look at — unless the work changes no
	// code, when what it published is the work.
	writable, err := w.s.HasWritableRepository(ctx, sc.OrganizationID, st.TaskID)
	if err != nil {
		return workflow.Result{}, err
	}
	if len(out.ChangedPaths) == 0 && (writable || !out.Published) {
		return w.escalate(ctx, sc, st, "no_changes", map[string]any{"runId": runID})
	}
	st.HeadRunID, st.Heads, st.ChangedPaths, st.PendingRunIDs = runID, out.advance(st.Heads), out.ChangedPaths, nil
	return w.next(ctx, sc, st, PointImplemented, "review", out.line(runID, "implement"))
}

// review fans reviewers out over the current head. Parallel Runs rather than
// one long prompt: wall-clock once instead of N times, and a security
// reviewer that finds nothing has not polluted the correctness reviewer.
func (w *steps) review(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "agent review"); err != nil {
		return workflow.Result{}, err
	}
	categories := ReviewersFor(st.Policy, st.ChangedPaths)
	d := directed(st, PhaseReview)
	if d != nil && len(d.Categories) > 0 {
		categories = d.Categories
	}
	// A re-review judges what the fixer was sent: the open findings of its
	// category that a fix has attempted.
	toJudge, err := w.s.AttemptedFindings(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	var runIDs []string
	for _, c := range categories {
		k := key(sc, st, ":review:", st.Iteration, ":", c)
		if d != nil {
			k = conductorKey(sc, st, d, ":review:", st.Iteration, ":", c)
		}
		id, err := w.phase(ctx, sc, st, PhaseReview, k,
			func(p *PhaseRun) {
				p.Category, p.BlockingSeverities, p.FindingIDs = c, st.Policy.BlockingSeverities, toJudge[c]
				d.apply(p)
			})
		if err != nil {
			return workflow.Result{}, err
		}
		runIDs = append(runIDs, id)
	}
	if err := w.s.Emit(ctx, sc.OrganizationID, st, EvRunCreated,
		map[string]any{"phase": "review", "reviewers": categories, "iteration": st.Iteration}); err != nil {
		return workflow.Result{}, err
	}
	return park("awaitReview", st, runIDs), nil
}

func (w *steps) awaitReview(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	// Every reviewer must report before the findings are a complete set;
	// acting on part of it would send the fixer back for the rest at once.
	if pending := settle(st, sc); len(pending) > 0 {
		return park("awaitReview", st, pending), nil
	}
	findings, err := w.s.Findings(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	st.Iteration++
	st.PendingRunIDs = nil
	exit := w.loopExit(st, findings)
	if exit == nil || exit.Reason == "clear" {
		// Past a bound it escalates as the policy does; short of one, the
		// round's findings are the conductor's to triage, if it decides as
		// the transition commits (recheck).
		res, err := w.afterLoop(ctx, sc, st, exit)
		st.Routed = &Routed{Point: PointReviewed, Policy: "reviewExit", Next: res.Next, Line: findingsLine(st, findings)}
		return res, err
	}
	return w.afterLoop(ctx, sc, st, exit)
}

// loopExit is the review loop's exit for the findings, with the fix
// attempts a person granted beyond the policy's.
func (w *steps) loopExit(st *State, findings []FindingState) *LoopExit {
	policy := st.Policy
	policy.MaxAttemptsPerFinding += st.ExtraFixAttempts
	return Exit(policy, findings, st.Iteration)
}

// afterLoop goes where the review loop's exit says.
func (w *steps) afterLoop(ctx context.Context, sc workflow.StepContext, st *State, exit *LoopExit) (workflow.Result, error) {
	switch {
	case exit == nil:
		return workflow.Result{Next: "fix", State: st}, nil
	case exit.Reason == "clear":
		if st.Policy.Simplify {
			return workflow.Result{Next: "simplify", State: st}, nil
		}
		return workflow.Result{Next: "test", State: st}, nil
	}
	return w.escalate(ctx, sc, st, exit.Reason, exit)
}

// reviewExit is "what the policy would do" after a review round the
// conductor triaged: the exit for the findings as they are now — some may
// have been dismissed since.
func (w *steps) reviewExit(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	findings, err := w.s.Findings(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	return w.afterLoop(ctx, sc, st, w.loopExit(st, findings))
}

// findingsLine is a review round's findings in a note: how many are open,
// by severity, and the round. Counts only, never a finding's text.
func findingsLine(st *State, findings []FindingState) string {
	bySeverity := map[string]int{}
	open := 0
	for _, f := range findings {
		if f.Status == "open" {
			open++
			bySeverity[f.Severity]++
		}
	}
	var parts []string
	for _, s := range []string{"blocking", "high", "medium", "low", "note"} {
		if n := bySeverity[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	line := fmt.Sprintf("Review round %d of %d done: %d open findings", st.Iteration, st.Policy.MaxReviewIterations, open)
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	return line + "."
}

// fix: one fix Run for every unresolved finding. Findings overlap, a fixer
// that sees them all can resolve several with one change, and N fixers racing
// on one branch would conflict.
func (w *steps) fix(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "fixing review findings"); err != nil {
		return workflow.Result{}, err
	}
	findings, err := w.s.Findings(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	var open []string
	for _, f := range findings {
		if f.Status == "open" {
			open = append(open, f.ID)
		}
	}
	k := key(sc, st, ":fix:", st.Iteration)
	d := directed(st, PhaseFix)
	if d != nil {
		k = conductorKey(sc, st, d, ":fix:", st.Iteration)
		if len(d.FindingIDs) > 0 {
			// Only those of the conductor's that are still open.
			open = slices.DeleteFunc(slices.Clone(d.FindingIDs), func(id string) bool { return !slices.Contains(open, id) })
		}
	}
	// Counted once per fix step, however often the step is replayed: the
	// Run's creation key doubles as the marker that it was counted.
	existing, err := w.s.runByKey(ctx, sc.OrganizationID, st.TaskID, k)
	if err != nil {
		return workflow.Result{}, err
	}
	if existing == "" {
		if err := w.s.MarkAttempted(ctx, sc.OrganizationID, open); err != nil {
			return workflow.Result{}, err
		}
	}
	runID, err := w.phase(ctx, sc, st, PhaseFix, k, func(p *PhaseRun) { p.FindingIDs = open; d.apply(p) })
	if err != nil {
		return workflow.Result{}, err
	}
	return park("awaitFix", st, []string{runID}), nil
}

func (w *steps) awaitFix(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if pending := settle(st, sc); len(pending) > 0 {
		return park("awaitFix", st, pending), nil
	}
	runID := st.PendingRunIDs[0]
	out, err := w.s.PhaseOutcome(ctx, sc.OrganizationID, runID)
	if err != nil {
		return workflow.Result{}, err
	}
	if !out.Succeeded {
		return w.escalate(ctx, sc, st, "fix_failed", map[string]any{"runId": runID, "error": out.Error})
	}
	st.HeadRunID, st.PendingRunIDs, st.Heads, st.ChangedPaths = runID, nil, out.advance(st.Heads), out.ChangedPaths
	return w.next(ctx, sc, st, PointFixed, "review", out.line(runID, "fix"))
}

// simplify runs once blocking findings are clear (plan §11.4). It may
// commit — removing complexity is a change — but must not change behaviour.
func (w *steps) simplify(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "simplifying"); err != nil {
		return workflow.Result{}, err
	}
	k := key(sc, st, ":simplify")
	d := directed(st, PhaseSimplify)
	if d != nil {
		k = conductorKey(sc, st, d, ":simplify")
	}
	runID, err := w.phase(ctx, sc, st, PhaseSimplify, k, d.apply)
	if err != nil {
		return workflow.Result{}, err
	}
	return park("awaitSimplify", st, []string{runID}), nil
}

func (w *steps) awaitSimplify(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if pending := settle(st, sc); len(pending) > 0 {
		return park("awaitSimplify", st, pending), nil
	}
	runID := st.PendingRunIDs[0]
	out, err := w.s.PhaseOutcome(ctx, sc.OrganizationID, runID)
	if err != nil {
		return workflow.Result{}, err
	}
	st.PendingRunIDs = nil
	// A failed or empty simplification is not a failure of the task: the
	// code was simple enough already. Carry on with what we had.
	if out.Succeeded && len(out.Heads) > 0 {
		st.HeadRunID, st.Heads = runID, out.advance(st.Heads)
	}
	return workflow.Result{Next: "test", State: st}, nil
}

func (w *steps) test(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	d := directed(st, PhaseTest)
	if !st.Policy.Test && (d == nil || d.Phase != PhaseTest) {
		return w.next(ctx, sc, st, PointBeforePR, "openPullRequest", "")
	}
	k := key(sc, st, ":test")
	if d != nil {
		k = conductorKey(sc, st, d, ":test")
	}
	runID, err := w.phase(ctx, sc, st, PhaseTest, k, d.apply)
	if err != nil {
		return workflow.Result{}, err
	}
	return park("awaitTest", st, []string{runID}), nil
}

func (w *steps) awaitTest(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if pending := settle(st, sc); len(pending) > 0 {
		return park("awaitTest", st, pending), nil
	}
	runID := st.PendingRunIDs[0]
	out, err := w.s.PhaseOutcome(ctx, sc.OrganizationID, runID)
	if err != nil {
		return workflow.Result{}, err
	}
	if !out.Succeeded {
		// A tester that found a real problem must not be papered over by
		// opening the PR anyway.
		return w.escalate(ctx, sc, st, "test_failed", map[string]any{"runId": runID, "error": out.Error})
	}
	st.PendingRunIDs = nil
	return w.next(ctx, sc, st, PointBeforePR, "openPullRequest", out.line(runID, "test"))
}

func (w *steps) openPullRequest(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	// The gate holds at the opening itself: a delivery taken over after the
	// policy chose to open waits for the person's answer like any other, and
	// one handed back after the conductor entered the gate opens only with
	// the person's Open or Draft at the heads it opens.
	if len(st.PullRequestIDs) == 0 && (st.conducted() || st.GateRequired) && !st.gateHeld() {
		if st.conducted() {
			return w.next(ctx, sc, st, PointBeforePR, "openPullRequest", "")
		}
		ok, err := w.gateAuthorized(ctx, sc, st)
		if err != nil {
			return workflow.Result{}, err
		}
		if !ok {
			return w.parkAtGate(ctx, sc, st, true)
		}
		if st.authorizeGate {
			// Opened by the next run of this step, on the authorization
			// its transition records.
			return workflow.Result{Next: "openPullRequest", State: st}, nil
		}
	}
	if st.GateRequired && st.GateAt != nil {
		st.Draft = st.GateAt.Draft
	}
	prIDs, err := w.s.OpenPullRequests(ctx, sc.OrganizationID, st, w.forges)
	if err != nil {
		return workflow.Result{}, err
	}
	st.PullRequestIDs, st.PRIteration = prIDs, 0
	if len(prIDs) == 0 {
		// Nothing changed in any repository: the work is what the agents
		// published. A person reads it and says when it is done.
		return workflow.Result{State: st}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "ready to read")
	}
	// Waiting on people now: the agents are done until someone asks.
	reason := "pull request open"
	if len(prIDs) > 1 {
		reason = fmt.Sprintf("%d pull requests open", len(prIDs))
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", reason); err != nil {
		return workflow.Result{}, err
	}
	return waitForPR(st), nil
}

// awaitPullRequest wakes a fixer only when there is something to fix: the
// classifier has already decided a change is actionable before it becomes a
// signal, so an approval or a green check never gets here.
func (w *steps) awaitPullRequest(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	// Every signal of the batch is read before anything is decided: they are
	// consumed together, so one PR merging must not drop feedback on another
	// that arrived with it.
	var actionable []forge.ActionableFeedback
	ended, readiness := false, false
	var stop *forge.Signal // a conflict, or CI stuck: a person's to see to
	for _, sig := range sc.Signals {
		if sig.Name != SignalPRFeedback {
			continue
		}
		var s forge.Signal
		if json.Unmarshal(sig.Payload, &s) != nil {
			continue
		}
		switch s.Kind {
		case "terminal":
			ended = true
		case "readiness":
			readiness = true
		case "conflict", "ci_stuck":
			stop = &s
		default:
			actionable = append(actionable, s.Feedback...)
			if s.Conflict {
				stop = &forge.Signal{Kind: "conflict", Repo: s.Repo, Number: s.Number}
			}
		}
	}
	if ended {
		res, finished, err := w.pullRequestEnded(ctx, sc, st)
		if err != nil || finished || len(actionable) == 0 && stop == nil {
			return res, err
		}
	}
	// A person's review is a new round: the fixes before it do not count
	// against it. A comment is not: a conversation of one-line requests,
	// each fixed, is the loop the per-round bound is for.
	if slices.ContainsFunc(actionable, forge.ActionableFeedback.IsReview) {
		st.PRIteration = 0
	}
	if stop != nil {
		// Signals wait while the workflow is busy elsewhere (a fix, a
		// person deciding): one that is no longer true — the conflict
		// resolved, CI passed — is not a reason to stop now.
		if still, err := w.stillStuck(ctx, sc, st, stop); err != nil {
			return workflow.Result{}, err
		} else if !still {
			stop = nil
		}
	}
	if stop != nil {
		// Kept for after: a person who resolves the conflict and waits
		// again has the feedback that arrived with it fixed then.
		st.PRFeedback = append(st.PRFeedback, actionable...)
		reason := "pull_request_conflict"
		if stop.Kind == "ci_stuck" {
			reason = "ci_stuck"
		}
		return w.escalate(ctx, sc, st, reason, map[string]any{"repo": stop.Repo, "number": stop.Number})
	}
	waitAgain := waitForPR(st)
	if len(actionable) == 0 {
		if readiness {
			states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
			if err != nil {
				return workflow.Result{}, err
			}
			return waitAgain, w.weighReadiness(ctx, sc, st, states)
		}
		return waitAgain, nil
	}
	st.PRFeedback = actionable
	return w.next(ctx, sc, st, PointPRFeedback, "prFix", feedbackLine(actionable))
}

// feedbackLine is pull request feedback in a note: how much, of what kind,
// on which pull requests. Never its words.
func feedbackLine(feedback []forge.ActionableFeedback) string {
	kinds := map[string]int{}
	for _, f := range feedback {
		kind := f.Source
		if len(f.Checks) > 0 {
			kind = "failing check"
		}
		kinds[kind]++
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(kinds)) {
		parts = append(parts, fmt.Sprintf("%d %s", kinds[k], k))
	}
	return fmt.Sprintf("%d actionable items (%s): read them with pull_requests.", len(feedback), strings.Join(parts, ", "))
}

// stillStuck says whether what a conflict or ci_stuck signal reported is
// still so, as the pull request was last read.
func (w *steps) stillStuck(ctx context.Context, sc workflow.StepContext, st *State, stop *forge.Signal) (bool, error) {
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return false, err
	}
	for _, p := range states {
		if p.Repo != stop.Repo || p.Number != stop.Number || p.State == forge.StateMerged || p.State == forge.StateClosed {
			continue
		}
		if stop.Kind == "ci_stuck" {
			return p.Checks == forge.ChecksPending, nil
		}
		return p.Mergeable == forge.MergeConflicting, nil
	}
	return false, nil
}

// prFix sends the pull requests' feedback to a fixer — kept in state, so
// a fix a person sends back to try again gets the same feedback.
func (w *steps) prFix(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		return workflow.Result{}, err
	}
	total := forge.DefaultSettings().FixRoundsPerPR
	if gh != nil {
		total = gh.Settings.FixRoundsPerPR
	}
	st.PRIteration++
	// A workflow from before rounds counted its fixes in PRIteration only.
	st.PRFixKey = max(st.PRFixKey, st.PRIteration-1) + 1
	var spent []string // pull requests past the organization's budget
	for _, repo := range feedbackRepos(st.PRFeedback) {
		if st.PRFixes == nil {
			st.PRFixes = map[string]int{}
		}
		st.PRFixes[repo]++
		if total > 0 && st.PRFixes[repo] > total {
			spent = append(spent, repo)
		}
	}
	if st.PRIteration > st.Policy.MaxPRFixIterations || len(spent) > 0 {
		return w.escalate(ctx, sc, st, "pr_loop_exhausted", map[string]any{"iterations": st.PRIteration,
			"perRound": st.Policy.MaxPRFixIterations, "total": total, "spent": db.NonNil(spent)})
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "addressing pull request feedback"); err != nil {
		return workflow.Result{}, err
	}
	k := key(sc, st, ":prfix:", st.PRFixKey)
	existing, err := w.s.runByKey(ctx, sc.OrganizationID, st.TaskID, k)
	if err != nil {
		return workflow.Result{}, err
	}
	if existing == "" && gh != nil {
		// What GitHub has, not what dude last pushed: a person may have
		// pushed since, and a fix from dude's head would fail to
		// fast-forward over theirs — or undo it.
		if err := w.fromPullRequestHeads(ctx, sc, st, gh); err != nil {
			return workflow.Result{}, err
		}
	}
	// Several comments arriving together cost one fix Run, not one each.
	d := directed(st, PhaseFix)
	runID, err := w.phase(ctx, sc, st, PhaseFix, k, func(p *PhaseRun) { p.PRFeedback = st.PRFeedback; d.apply(p) })
	if err != nil {
		return workflow.Result{}, err
	}
	return park("awaitPRFix", st, []string{runID}), nil
}

// feedbackRepos names the pull requests (by repository) feedback is on.
func feedbackRepos(feedback []forge.ActionableFeedback) []string {
	var out []string
	for _, f := range feedback {
		if !slices.Contains(out, f.Repo) {
			out = append(out, f.Repo)
		}
	}
	return out
}

// fromPullRequestHeads starts the fix from each open pull request's head
// as GitHub has it, and gives failing checks the end of their logs.
func (w *steps) fromPullRequestHeads(ctx context.Context, sc workflow.StepContext, st *State, gh *forge.GitHub) error {
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return err
	}
	slugs := map[string]string{}
	for _, p := range states {
		if p.Slug == "" || p.State == forge.StateMerged || p.State == forge.StateClosed {
			continue
		}
		slugs[p.Repo] = p.Slug
		head, err := gh.Head(ctx, p.Slug, p.Number)
		if err != nil {
			return err
		}
		if head != "" && head != st.Heads[p.Repo] {
			if st.Heads == nil {
				st.Heads = map[string]string{}
			}
			st.Heads[p.Repo] = head
		}
	}
	for i, f := range st.PRFeedback {
		for j, c := range f.Checks {
			if c.RunID == 0 || c.Log != "" || slugs[f.Repo] == "" {
				continue
			}
			// Best effort: the check's name and link are enough to start.
			if log, err := gh.CheckOutput(ctx, slugs[f.Repo], c.RunID, checkLogLimit); err == nil {
				st.PRFeedback[i].Checks[j].Log = log
			}
		}
	}
	return nil
}

// checkLogLimit: how much of a failing check's output a fixer is given.
const checkLogLimit = 4000

func (w *steps) awaitPRFix(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if pending := settle(st, sc); len(pending) > 0 {
		return park("awaitPRFix", st, pending), nil
	}
	runID := st.PendingRunIDs[0]
	out, err := w.s.PhaseOutcome(ctx, sc.OrganizationID, runID)
	if err != nil {
		return workflow.Result{}, err
	}
	if !out.Succeeded {
		return w.escalate(ctx, sc, st, "pr_fix_failed", map[string]any{"runId": runID, "error": out.Error})
	}
	st.HeadRunID, st.PendingRunIDs, st.Heads, st.PRFeedback = runID, nil, out.advance(st.Heads), nil
	// The fix may have changed a repository no pull request is open for yet:
	// that one gets its own, alongside the others.
	prIDs, err := w.s.OpenPullRequests(ctx, sc.OrganizationID, st, w.forges)
	if err != nil {
		return workflow.Result{}, err
	}
	st.PullRequestIDs = prIDs
	// The fast-forward updated the PRs; they are back with the reviewers.
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "pull request updated"); err != nil {
		return workflow.Result{}, err
	}
	// What happened to them while the fix ran was signalled to a step not
	// listening for it: one may have been merged or closed, or be approved
	// with green checks already. Weighed now rather than waited for.
	res, _, err := w.pullRequestEnded(ctx, sc, st)
	return res, err
}

// pullRequestEnded: one of the task's pull requests was merged or
// closed. The task is done when every one is merged. One closed without
// merging is someone deciding not to take that part: with nothing else open
// that ends the task (aborted); with siblings still open it is a
// decision for a person, not something to guess.
//
// finished says the task has left the pull request loop (done,
// aborted, or waiting on a person); otherwise the others are still open.
func (w *steps) pullRequestEnded(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, bool, error) {
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return workflow.Result{}, true, err
	}
	var open, merged, closed int
	for _, s := range states {
		switch s.State {
		case forge.StateMerged:
			merged++
		case forge.StateClosed:
			closed++
		default:
			open++
		}
	}
	waitAgain := waitForPR(st)
	switch {
	case len(states) == 0:
		return workflow.Result{}, true, fmt.Errorf("no pull requests recorded for %s", st.TaskID)
	case closed == 0 && open > 0:
		// The rest are still open: what is merged is taken, so whether the
		// task is ready depends on those.
		return waitAgain, false, w.weighReadiness(ctx, sc, st, states)
	case closed == 0:
		return workflow.Result{}, true, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "done", "pull requests merged")
	case open == 0 && merged == 0:
		// Closed without merging is someone deciding not to take the
		// change: an abort of the task, not a failure of it.
		return workflow.Result{}, true, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "aborted", "pull request closed without merging")
	}
	// Part taken, part refused, or part still open: a person decides.
	res, err := w.escalate(ctx, sc, st, "pull_request_closed", map[string]any{"merged": merged, "closed": closed, "open": open})
	return res, true, err
}

// weighReadiness moves the task to ready to merge when every pull
// request still open is approved with its checks passing (forge.Ready),
// and back to review when one no longer is. The factory never merges:
// ready to merge is what a person is told, and merging is theirs.
func (w *steps) weighReadiness(ctx context.Context, sc workflow.StepContext, st *State, states []PullRequestState) error {
	ready, open := true, 0
	for _, s := range states {
		if s.State == forge.StateMerged || s.State == forge.StateClosed {
			continue
		}
		open++
		// Always about the pull request's current head: publishing a fix
		// resets its checks (phases.publish), and a sync records a head
		// someone else pushed.
		ready = ready && forge.Ready(s.Status)
	}
	if open > 0 && ready {
		// The move and its event in one transaction: an event lost to a
		// retry would never be written again (the status has moved).
		return w.s.ReadyToMerge(ctx, sc.OrganizationID, st, open)
	}
	return w.s.SetTaskStatusFrom(ctx, sc.OrganizationID, st, "ready_to_merge", "review", "no longer ready to merge")
}

// escalate stops and asks for a person, and waits for their decision
// (decide). Nothing goes on by itself: no agent works, and no timer
// resumes it.
func (w *steps) escalate(ctx context.Context, sc workflow.StepContext, st *State, reason string, detail any) (workflow.Result, error) {
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "awaiting_input", reason); err != nil {
		return workflow.Result{}, err
	}
	st.Escalation = &Escalation{Reason: reason, Detail: detail, Step: RetryStep[sc.Step]}
	if id := st.Escalation.RunID(); id != "" && strings.HasSuffix(reason, "_failed") {
		// A Run failed, and lux keeps it: the step waiting on it can wait on
		// it again, resumed.
		kept, err := w.s.Kept(ctx, sc.OrganizationID, id)
		if err != nil {
			return workflow.Result{}, err
		}
		if kept {
			st.Escalation.At = sc.Step
		}
	}
	st.PendingRunIDs = nil
	if err := w.s.Emit(ctx, sc.OrganizationID, st, EvQuestionAsked,
		map[string]any{"kind": "escalation", "reason": reason, "detail": detail, "actions": st.Escalation.Actions()}); err != nil {
		return workflow.Result{}, err
	}
	if st.conducted() {
		// The person still decides; the conductor is told, to explain and
		// propose in Chat. Once per escalation, however often replayed.
		st.Decision, st.Directed = nil, nil
		st.Escalations++
		line := fmt.Sprintf("Escalated to a person: %s. Its actions: %s. Only the person decides; explain and propose.",
			strings.ReplaceAll(reason, "_", " "), strings.Join(st.Escalation.Actions(), ", "))
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			_, err := RecordWakeTx(ctx, tx, sc.OrganizationID, st.TaskID, "escalation",
				fmt.Sprintf("%s:escalation:%d", sc.WorkflowRunID, st.Escalations), line)
			return err
		}); err != nil {
			return workflow.Result{}, err
		}
	}
	return workflow.Result{Next: "decide", State: st, AwaitSignals: []string{SignalHumanDecision}}, nil
}

// RetryStep is where "try again" goes back to, from the step that stopped.
var RetryStep = map[string]string{
	"awaitImplement": "implement",
	// A review loop that gave up tries again with another round of fixes.
	"awaitReview": "fix",
	"awaitFix":    "fix",
	"awaitTest":   "test",
	"prFix":       "prFix",
	"awaitPRFix":  "prFix",
}

// decide carries out a person's decision on an escalation.
func (w *steps) decide(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	// The decision is on the escalation, taken with the request; the signal
	// only wakes this step to carry it out.
	if st.Escalation == nil || st.Escalation.Decided == nil {
		return workflow.Result{Next: "decide", State: st, AwaitSignals: []string{SignalHumanDecision}}, nil
	}
	e, d := st.Escalation, st.Escalation.Decided
	if !slices.Contains(e.Actions(), d.Action) {
		// Checked when it was taken; a decision that cannot be carried out
		// must not leave the task looking busy.
		return workflow.Result{}, fmt.Errorf("escalation %s cannot %s", e.Reason, d.Action)
	}
	st.Escalation = nil
	switch d.Action {
	case "stop":
		// Kept, so the task can be picked back up where it stopped.
		e.Decided = nil
		st.Stopped = e
		return workflow.Result{State: st}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "aborted", "a person stopped it")
	case "done":
		return workflow.Result{State: st}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "done", "a person took what was merged")
	case "wait":
		if len(st.PRFeedback) > 0 && (e.Reason == "pull_request_conflict" || e.Reason == "ci_stuck") {
			// Feedback arrived with the conflict: fixed now it is resolved.
			return workflow.Result{Next: "prFix", State: st}, nil
		}
		return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "a person is waiting on the rest")
	}
	// Going on: working again, whichever step takes it up.
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "a person decided: "+d.Action); err != nil {
		return workflow.Result{}, err
	}
	switch d.Action {
	case "accept":
		// The findings it was stuck on ship as they are: a person's call.
		if err := w.s.AcceptFindings(ctx, sc.OrganizationID, st); err != nil {
			return workflow.Result{}, err
		}
		if st.Policy.Simplify {
			return workflow.Result{Next: "simplify", State: st}, nil
		}
		return workflow.Result{Next: "test", State: st}, nil
	}
	if d.Action == "resume" {
		// The Run that failed was taken back up with the decision (the API
		// does it, where it can say if it no longer can): the step that
		// waited on it waits on it again.
		return park(e.At, st, []string{e.RunID()}), nil
	}
	return retry(st, e.Step, e), nil
}

// retry runs a step that stopped again, afresh — new Runs, a new budget
// for the loop it gave up on. e is the escalation it stopped at; nil for
// a step a person aborted.
func retry(st *State, step string, e *Escalation) workflow.Result {
	st.Retries++
	switch step {
	case "fix":
		// A new budget for the loop: its rounds, and each finding's fixes.
		st.Iteration = 0
		if e != nil && e.Reason == "stuck" {
			st.ExtraFixAttempts += st.Policy.MaxAttemptsPerFinding
		}
	case "prFix":
		// The same fix again, not the next: what the stopped one counted
		// is given back. A budget spent is a new one.
		st.PRIteration--
		for _, repo := range feedbackRepos(st.PRFeedback) {
			if st.PRFixes[repo] > 0 {
				st.PRFixes[repo]--
			}
		}
		if e != nil && e.Reason == "pr_loop_exhausted" {
			st.PRIteration = 0
			for _, repo := range e.spent() {
				st.PRFixes[repo] = 0
			}
		}
	}
	st.PendingRunIDs = nil
	return workflow.Result{Next: step, State: st}
}

// AbortedRetryStep is where trying again goes back to when a person
// aborted the work at a step: the step that made the Runs it was waiting on.
var AbortedRetryStep = map[string]string{
	"awaitImplement": "implement",
	"awaitReview":    "review",
	"awaitFix":       "fix",
	"awaitSimplify":  "simplify",
	"awaitTest":      "test",
	"awaitPRFix":     "prFix",
}

// recover carries out a person picking a stopped task back up (State.
// Recover, set with the request, which also took the Runs back up for a
// resume): the step that waited on the Runs waits on them again, or the
// step that made them runs again.
func (w *steps) recover(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	rc := st.Recover
	if rc == nil {
		return workflow.Result{}, fmt.Errorf("nothing to recover")
	}
	e := st.Stopped
	st.Recover, st.Stopped = nil, nil
	if rc.Action == "resume" {
		return park(rc.At, st, st.PendingRunIDs), nil
	}
	return retry(st, rc.Step, e), nil
}

// waitForPR waits on the task's pull requests: what the forge says of
// them, or a person's decision.
func waitForPR(st *State) workflow.Result {
	return workflow.Result{Next: "awaitPullRequest", State: st, AwaitSignals: []string{SignalPRFeedback, SignalHumanDecision}}
}
