package delivery

import (
	"context"
	"encoding/json"
	"fmt"

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
	// PR feedback → fix cycles spent.
	PRIteration int `json:"prIteration,omitempty"`
	// The pull requests opened, one per repository changed.
	PullRequestIDs []string `json:"pullRequestIds,omitempty"`
	// Why the workflow stopped, when it stopped early.
	Escalation *Escalation `json:"escalation,omitempty"`
}

type Escalation struct {
	Reason string `json:"reason"`
	Detail any    `json:"detail,omitempty"`
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
		Steps: map[string]workflow.Step{
			"implement":        w.implement,
			"awaitImplement":   w.awaitImplement,
			"review":           w.review,
			"awaitReview":      w.awaitReview,
			"fix":              w.fix,
			"awaitFix":         w.awaitFix,
			"simplify":         w.simplify,
			"awaitSimplify":    w.awaitSimplify,
			"test":             w.test,
			"awaitTest":        w.awaitTest,
			"openPullRequest":  w.openPullRequest,
			"awaitPullRequest": w.awaitPullRequest,
			"awaitPRFix":       w.awaitPRFix,
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

// key makes phase-Run creation idempotent per workflow step and iteration.
func key(sc workflow.StepContext, parts ...any) string {
	return fmt.Sprint(append([]any{sc.WorkflowRunID}, parts...)...)
}

// phase creates a phase Run starting from where the work stands: each
// repository at its head, or its default branch if nothing changed it yet.
func (w *steps) phase(ctx context.Context, sc workflow.StepContext, st *State, phase, k string, extra func(*PhaseRun)) (string, error) {
	in := PhaseRun{TaskID: st.TaskID, Phase: phase, BaseRefs: st.Heads, ParentRunID: st.HeadRunID, Key: k}
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
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "implementing"); err != nil {
		return workflow.Result{}, err
	}
	runID, err := w.phase(ctx, sc, st, PhaseImplement, key(sc, ":implement"), nil)
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
	return workflow.Result{Next: "review", State: st}, nil
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
	// A re-review judges what the fixer was sent: the open findings of its
	// category that a fix has attempted.
	toJudge, err := w.s.AttemptedFindings(ctx, sc.OrganizationID, st.TaskID)
	if err != nil {
		return workflow.Result{}, err
	}
	var runIDs []string
	for _, c := range categories {
		id, err := w.phase(ctx, sc, st, PhaseReview, key(sc, ":review:", st.Iteration, ":", c),
			func(p *PhaseRun) {
				p.Category, p.BlockingSeverities, p.FindingIDs = c, st.Policy.BlockingSeverities, toJudge[c]
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
	findings, err := w.s.Findings(ctx, sc.OrganizationID, st.TaskID)
	if err != nil {
		return workflow.Result{}, err
	}
	st.Iteration++
	st.PendingRunIDs = nil
	exit := Exit(st.Policy, findings, st.Iteration)
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
	findings, err := w.s.Findings(ctx, sc.OrganizationID, st.TaskID)
	if err != nil {
		return workflow.Result{}, err
	}
	var open []string
	for _, f := range findings {
		if f.Status == "open" {
			open = append(open, f.ID)
		}
	}
	k := key(sc, ":fix:", st.Iteration)
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
	runID, err := w.phase(ctx, sc, st, PhaseFix, k, func(p *PhaseRun) { p.FindingIDs = open })
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
	return workflow.Result{Next: "review", State: st}, nil
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
	runID, err := w.phase(ctx, sc, st, PhaseSimplify, key(sc, ":simplify"), nil)
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
	if !st.Policy.Test {
		return workflow.Result{Next: "openPullRequest", State: st}, nil
	}
	runID, err := w.phase(ctx, sc, st, PhaseTest, key(sc, ":test"), nil)
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
	return workflow.Result{Next: "openPullRequest", State: st}, nil
}

func (w *steps) openPullRequest(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
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
		default:
			actionable = append(actionable, s.Feedback...)
		}
	}
	if ended {
		res, finished, err := w.pullRequestEnded(ctx, sc, st)
		if err != nil || finished || len(actionable) == 0 {
			return res, err
		}
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
	st.PRIteration++
	if st.PRIteration > st.Policy.MaxPRFixIterations {
		return w.escalate(ctx, sc, st, "pr_loop_exhausted", map[string]any{"iterations": st.PRIteration})
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "addressing pull request feedback"); err != nil {
		return workflow.Result{}, err
	}
	// Several comments arriving together cost one fix Run, not one each.
	runID, err := w.phase(ctx, sc, st, PhaseFix, key(sc, ":prfix:", st.PRIteration),
		func(p *PhaseRun) { p.PRFeedback = actionable })
	if err != nil {
		return workflow.Result{}, err
	}
	return park("awaitPRFix", st, []string{runID}), nil
}

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
	st.HeadRunID, st.PendingRunIDs, st.Heads = runID, nil, out.advance(st.Heads)
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
		ready = ready && forge.Ready(s.Review, s.Checks)
	}
	if open > 0 && ready {
		// The move and its event in one transaction: an event lost to a
		// retry would never be written again (the status has moved).
		return w.s.ReadyToMerge(ctx, sc.OrganizationID, st, open)
	}
	return w.s.SetTaskStatusFrom(ctx, sc.OrganizationID, st, "ready_to_merge", "review", "no longer approved with checks passing")
}

// escalate stops and asks for a person. Terminal rather than parked: holding
// the workflow open pretending to make progress would hide that it needs a
// decision.
func (w *steps) escalate(ctx context.Context, sc workflow.StepContext, st *State, reason string, detail any) (workflow.Result, error) {
	if err := w.s.Emit(ctx, sc.OrganizationID, st, EvQuestionAsked,
		map[string]any{"kind": "escalation", "reason": reason, "detail": detail}); err != nil {
		return workflow.Result{}, err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "awaiting_input", reason); err != nil {
		return workflow.Result{}, err
	}
	st.Escalation = &Escalation{Reason: reason, Detail: detail}
	st.PendingRunIDs = nil
	return workflow.Result{State: st}, nil
}

// waitForPR waits on the task's pull requests: what the forge says of
// them, or a person's decision.
func waitForPR(st *State) workflow.Result {
	return workflow.Result{Next: "awaitPullRequest", State: st, AwaitSignals: []string{SignalPRFeedback, SignalHumanDecision}}
}
