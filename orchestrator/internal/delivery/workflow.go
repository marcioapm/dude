package delivery

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/advance"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
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
	// The frozen pull request owner connected GitHub again.
	SignalGitHubAuthorized = "github.authorized"
)

// State is everything the workflow remembers between steps.
type State struct {
	TaskID           string            `json:"taskId"`
	ProjectID        string            `json:"projectId"`
	Policy           Policy            `json:"policy"`
	Integration      *Integration      `json:"integration,omitempty"`
	IntegrationPairs map[string]string `json:"integrationPairs,omitempty"`
	// Integrations whose merge published while GitHub had not said whether
	// the pull request still conflicts, left to the pull request syncer's
	// next reading, by pair.
	HandedOver       map[string]Integration `json:"handedOver,omitempty"`
	PendingConflicts []forge.Signal         `json:"pendingConflicts,omitempty"`

	// The Run whose output the next phase builds on.
	HeadRunID string `json:"headRunId,omitempty"`
	// The task's branch, the same name in every repository it changes.
	Branch string `json:"branch,omitempty"`
	// What the task branch merges into, by repository name, where that is
	// not the repository's default branch: an adopted source's base. Every
	// diff, review and pull request of that repository measures against it.
	Bases map[string]string `json:"bases,omitempty"`
	// The existing pull request or branch the task took over; nil for new
	// work. Its head branch is Branch.
	Source *Source `json:"source,omitempty"`
	// Where the work stands in each repository it changed, by name: what
	// the next phase checks out. A repository not here starts from its
	// default branch.
	Heads map[string]string `json:"heads,omitempty"`
	// What changed, across repositories, as <repo>/<path> — what picks the
	// reviewers.
	ChangedPaths []string `json:"changedPaths,omitempty"`

	// Phase Runs being waited for.
	PendingRunIDs []string `json:"pendingRunIds,omitempty"`
	// The review round's Runs, every one, while it is waited for: each
	// must have finished for the round to be complete. Empty in a round
	// started before it was kept: ReviewRound finds them.
	ReviewRunIDs []string `json:"reviewRunIds,omitempty"`
	// The categories the next review round is limited to: a retry of a
	// round whose reviewers in them failed.
	RetryCategories []string `json:"retryCategories,omitempty"`
	// Review → fix cycles spent.
	Iteration       int       `json:"iteration,omitempty"`
	ConvergenceFrom time.Time `json:"convergenceFrom,omitzero"`
	// PR feedback → fix cycles spent in this review round: a person's new
	// review (not a comment, not CI failing again) starts another.
	PRIteration int `json:"prIteration,omitempty"`
	// Fixes each pull request has had, by repository (a task has one pull
	// request per repository): bounded by the organization's fix rounds per
	// pull request. A round counts once its fix lands with commits, against
	// every open pull request whose repository it changed, and no other.
	PRFixes map[string]int `json:"prFixesByRepo,omitempty"`
	// The last round charged to PRFixes, and the repositories charged.
	PRFixCharged *PRFixCharge `json:"prFixCharged,omitempty"`
	// Names each PR fix's Run. Only grows, so a round starting over never
	// finds an earlier fix.
	PRFixKey int `json:"prFixKey,omitempty"`
	// The pull request feedback being fixed.
	PRFeedback []forge.ActionableFeedback `json:"prFeedback,omitempty"`
	// The pull requests opened, one per repository changed.
	PullRequestIDs []string `json:"pullRequestIds,omitempty"`
	// The conductor started a phase while the delivery waited on its pull
	// requests: once that work is done (a review loop clear, a fix, a
	// test), the delivery waits on them again, with no simplify and no
	// pull request decision in between. A failed test's fix is tested
	// again first (RetestDue).
	ActingOnPR bool `json:"actingOnPr,omitempty"`
	// Why the workflow stopped for a person, while it waits on one.
	Escalation *Escalation `json:"escalation,omitempty"`
	// How often a person sent it back to try again: each retry's Runs are
	// new ones, not the ones that stopped.
	Retries int `json:"retries,omitempty"`
	// Fix attempts per finding a person granted beyond the policy's, by
	// sending a stuck review back to try again.
	ExtraFixAttempts int `json:"extraFixAttempts,omitempty"`
	// Limits people raised for this delivery on the conductor's
	// request_limit, and its requests still waiting on an answer. Written
	// only from outside the steps (Owned).
	LimitGrants   []LimitGrant   `json:"limitGrants,omitempty"`
	LimitRequests []LimitRequest `json:"limitRequests,omitempty"`
	// The test Run whose verdict the gate weighs (testVerdict).
	TestRunID string `json:"testRunId,omitempty"`
	// The attempt's previous test round when this round's heads descend
	// from its checkouts: what a resumed tester may carry criteria from
	// (carryForward). Set at each test step.
	CarryFrom string `json:"carryFrom,omitempty"`
	// Fixes a failed test sent this attempt back for: bounded by
	// MaxTestFixRounds.
	TestFixRounds int `json:"testFixRounds,omitempty"`
	// The tester's findings the next fix is sent, besides the reviewers'
	// open ones: set by a failed test, cleared once the fix is in.
	TestFindingIDs []string `json:"testFindingIds,omitempty"`
	// Where each repository the simplifier changed stood before it: the
	// review loop after it covers only from there (RangeSimplify). Set by
	// awaitSimplify when it published, cleared at the test.
	SimplifyBase map[string]string `json:"simplifyBase,omitempty"`
	// An open pull request's fix being checked on its candidate branch
	// before the pull request shows it (prFix to promoteCandidate).
	Candidate *Candidate `json:"candidate,omitempty"`
	// The after-promotion refresh of the task's open pull requests, while
	// it is retried (refreshPromoted).
	PromotedRefresh *PromotedRefresh `json:"promotedRefresh,omitempty"`
	// A failed test sent a fix round: the next clear review goes to a
	// test, even at the pull request wait or with the policy's tester off.
	// Cleared once that test's Run is created.
	RetestDue bool `json:"retestDue,omitempty"`
	// The policy's choice for the review round being created, which a
	// replay of the step at the same round and heads reuses.
	ReviewChoice *ReviewChoice `json:"reviewChoice,omitempty"`

	// A person started it past its open blockers (Start anyway): the
	// implement step's and the conductor's start guards let it through.
	Overridden bool `json:"overridden,omitempty"`

	// Which attempt at the task this delivery is: each start over is a new
	// one, on a branch of its own. Zero from before attempts were kept here
	// (the attempt is then the task's highest).
	Attempt int `json:"attempt,omitempty"`
	// A step is held by open reviewer questions (holdForQuestions): the
	// task waits on a person until the last is settled.
	QuestionsHeld bool `json:"questionsHeld,omitempty"`
	// The closed pull requests a person chose to wait past (decide, "wait"
	// on pull_request_closed): only a close beyond them is asked about again.
	WaitedPastClosed []string `json:"waitedPastClosed,omitempty"`
	// The escalation a person stopped the delivery at, kept so the task can
	// be picked back up from where it stopped.
	Stopped *Escalation `json:"stopped,omitempty"`
	// A person picking a stopped task back up (Recover), while the workflow
	// carries it out.
	Recover *Recover `json:"recover,omitempty"`
	// A person stopped the whole task (Stop), until it is picked back up.
	Halt *Halt `json:"halt,omitempty"`

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
	// The gate is required because the task's choice before its pull
	// request was Ask me first when Deliver reached it (PullRequestChoiceTx):
	// a change of the choice while held releases it (choiceReleased).
	ChoiceGate bool `json:"choiceGate,omitempty"`
	// Where Deliver last held the delivery before its pull request, for the
	// person reading it: the round and the heads it was shown at.
	Hold *Hold `json:"hold,omitempty"`
	// Authorization heads and draft mode, owned by the row. GateOpened and
	// Draft are latched and outlive a head change; gateHeld checks these heads.
	GateAt *GateAt `json:"gateAt,omitempty"`
	// The step read an answer authorizing the gate: its transition records
	// it (recheck). Never persisted.
	authorizeGate bool
	// The step handed an integration over: the wait after it takes one
	// pass first (closeHandedOver). Never persisted.
	handingOver bool
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

// Halt is a person's Stop of the task: the conductor it stopped, if one was
// live, kept so a resume goes on with it; and the step the delivery waited
// at with no phase Run at work (a decision, the pull requests), which a
// pick-up waits at again.
type Halt struct {
	Conductor string `json:"conductor,omitempty"`
	At        string `json:"at,omitempty"`
}

const ReasonNotConverging = "not_converging"

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
	case ReasonNotConverging:
		out = append(out, "retry", "accept", "amend")
	case "stuck", "exhausted":
		out = append(out, "retry", "accept")
	case ReasonTestFailedCriteria:
		// Another fix round, or ship with the failure: a person's call,
		// with their reason.
		out = append(out, "retry", "accept")
	case ReasonChecksUnverified:
		out = append(out, "retry", ActionWaive)
	case ReasonTestBlocked:
		// Test again (after someone told dude how to run it), or ship what
		// could not be checked, with a reason.
		out = append(out, "retry", ActionWaive)
	case "pull_request_conflict":
		var d struct {
			Integration bool `json:"integration"`
		}
		e.decode(&d)
		if d.Integration {
			out = append(out, "retry")
		}
		out = append(out, "wait")
	case "ci_stuck":
		// A person resolves the conflict, or sees to CI, on GitHub; dude
		// waits on the pull request again.
		out = append(out, "wait")
	case ReasonPullRequestMoved:
		// The same feedback fixed again from what the branch holds now, or
		// back to waiting on the pull request without the fix.
		out = append(out, "retry", "wait")
	default:
		// A Run that failed is kept a while (phases.keep): the same agent
		// can carry on where it stopped. Whether it still is, the API checks.
		if e.At != "" && e.RunID() != "" {
			out = append(out, "resume")
		}
		if e.Step != "" {
			out = append(out, "retry")
		}
		// A review round short of a reviewer may go on without it, as a
		// person's choice.
		if e.Reason == "review_failed" {
			out = append(out, "accept")
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

// RunIDs are the Runs a resume takes back up: every reviewer of a round
// that failed, or the one Run the escalation is about.
func (e *Escalation) RunIDs() []string {
	var d struct {
		RunIDs []string `json:"runIds"`
	}
	if e.decode(&d); len(d.RunIDs) > 0 {
		return d.RunIDs
	}
	if id := e.RunID(); id != "" {
		return []string{id}
	}
	return nil
}

// categories are the review categories whose reviewers failed, for a
// review_failed escalation.
func (e *Escalation) categories() []string {
	var d struct {
		Categories []string `json:"categories"`
	}
	e.decode(&d)
	return d.Categories
}

// findingIDs are the findings a gate escalation is about.
func (e *Escalation) findingIDs() []string {
	var d struct {
		FindingIDs []string `json:"findingIds"`
	}
	e.decode(&d)
	return d.FindingIDs
}

// feedbackIDs are the people's feedback items a stuck or exhausted review
// loop is held by.
func (e *Escalation) feedbackIDs() []string {
	var d struct {
		FeedbackIDs []string `json:"feedbackIds"`
	}
	e.decode(&d)
	return d.FeedbackIDs
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
	At time.Time `json:"at,omitzero"`
	// "resume" the Run that failed; "retry" the step that stopped; "accept"
	// the findings a review got stuck on, or a test's failed criteria, and
	// go on; "waive" the criteria a test could not check; "done" — what is
	// merged is the task; "wait" on the pull requests still open; "stop".
	Action string `json:"action"`
	// What they said, for the agents from here on.
	Note string `json:"note,omitempty"`
	// Taken on the owner's answer to the conductor's question about the
	// escalation: the question, and the person who answered.
	QuestionID string `json:"questionId,omitempty"`
	AnsweredBy string `json:"answeredBy,omitempty"`
	// Taken by the conductor (decide_escalation) on that answer: its Run.
	Conductor string `json:"conductor,omitempty"`
	// The person who decided, when one did: an acceptance resolves the
	// feedback items it names as theirs.
	Person string `json:"person,omitempty"`
	// "attempt": a checks waiver later Runs of the attempt may carry; ""
	// is the Run's alone.
	Scope string `json:"scope,omitempty"`
}

// BranchFor is the task's branch — the one its pull requests are opened
// from, the same name in each repository. Phase Runs never push to it directly; each pushes its own branch and
// dude fast-forwards this one.
func BranchFor(taskID string, attempt int) string {
	return fmt.Sprintf("dude/%s/attempt-%d", taskID, attempt)
}

// CandidateBranch is where a fix for an open pull request's feedback, and
// every fix of it, is taken while it is reviewed and tested: the task
// branch (the pull request's head) is fast-forwarded to it only once the
// gate passes (promoteCandidate).
func CandidateBranch(taskID string, attempt int) string {
	return BranchFor(taskID, attempt) + candidateSuffix
}

const candidateSuffix = "-candidate"

// NotGeneratedCandidateSQL (SQL, over runs aliased run) holds when the
// Run's branch is not its own CandidateBranch: a fix not yet checked.
func NotGeneratedCandidateSQL(run string) string {
	return fmt.Sprintf(`%[1]s.branch <> ('dude/' || %[1]s.task_id || '/attempt-' || %[1]s.attempt || '%[2]s')`, run, candidateSuffix)
}

// Candidate is one round of pull request feedback being fixed on the
// candidate branch.
type Candidate struct {
	// The round's PRFixKey: what keeps its Runs apart from another round's.
	Round  int    `json:"round"`
	Branch string `json:"branch"`
	// The heads the fix started from: each pull request's head as GitHub
	// had it, the rest where the delivery had them. The pull request
	// branch is moved only from these.
	From map[string]string `json:"from"`
	// Where its review range starts per repository the fix changed: From,
	// or where the fix's checkout started in a repository From lacks.
	Base map[string]string `json:"base,omitempty"`
	// The heads at which a person accepted a review round whose reviewers
	// failed: promotion takes them as reviewed (unreviewedHeads).
	Accepted map[string]string `json:"accepted,omitempty"`
	// Opened by a writer of the review loop while a pull request was open
	// (writerCandidate), not by a pull request's fix: once promoted, the
	// delivery goes on as that loop would have (beforePR).
	Loop bool `json:"loop,omitempty"`
	// Set by writerCandidate in the step that opens it, until phase has
	// read the pull requests' heads (or a replay's Run's). Never persisted.
	opening bool
}

// PRFixCharge is a round (its PRFixKey) charged to the pull requests in
// Repos: once, when its fix landed with commits; a retry of that round
// gives it back.
type PRFixCharge struct {
	Round   int      `json:"round"`
	Repos   []string `json:"repos"`
	Pending bool     `json:"pending,omitempty"`
}

// PromotedRefresh is the after-promotion refresh still to do: the
// repositories whose pull request (or adopted verdict comment) is not
// refreshed yet, and the attempts made.
type PromotedRefresh struct {
	Tries int      `json:"tries,omitempty"`
	Repos []string `json:"repos"`
	// The step after it: "" for prFixLanded, "beforePR" past a loop's candidate.
	Next string `json:"next,omitempty"`
}

// promotedRefreshRetries bounds the attempts after the first at refreshing
// a promotion's pull requests; past them the failure is recorded and the
// delivery goes on.
const promotedRefreshRetries = 2

// Workflow builds the delivery workflow's definition.
//
//	implement → review ⟲ fix → simplify → [test] → PR → PR feedback ⟲ fix → done
//
// Two properties it exists to guarantee: every loop ends on a bound declared
// in policy, never on a model deciding it is finished; and waking an agent is
// a decision, not a reflex — an approval or a green check wakes nobody.
func Workflow(s *Store, forges Forges) *workflow.Definition {
	w := &steps{s: s, forges: forges}
	def := &workflow.Definition{
		Type:        WorkflowType,
		InitialStep: "implement",
		Owned:       []string{"decider", "handedBack", "gateAt", "limitGrants", "limitRequests", "roundDelta", "reviewChoice"},
		// The gate's authorization: set by the conductor's decision, or by
		// a hand-back while a step runs.
		Latched: []string{"draft", "gateOpened", "overridden"},
		Recheck: w.awaitEnded,
		Steps: map[string]workflow.Step{
			"implement":         w.implement,
			"adopt":             w.adopt,
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
			"testVerdict":       w.testVerdict,
			"openPullRequest":   w.openPullRequest,
			"awaitPullRequest":  w.awaitPullRequest,
			"awaitRead":         w.awaitRead,
			"prFix":             w.prFix,
			"awaitPRFix":        w.awaitPRFix,
			"promoteCandidate":  w.promoteCandidate,
			"refreshPromoted":   w.refreshPromoted,
			"decide":            w.decide,
			"recover":           w.recover,
		},
	}
	// A pull request's end reaches every step but the pull request wait,
	// which reads it from pr.feedback.
	for name, step := range def.Steps {
		if name != "awaitPullRequest" {
			def.Steps[name] = w.endable(step)
		}
	}
	return def
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
// finding the ones that stopped, and per pull request fix round, whose
// review loop counts its iterations from zero again.
func key(sc workflow.StepContext, st *State, parts ...any) string {
	if st.Candidate != nil {
		parts = append(parts, ":candidate:", st.Candidate.Round)
	}
	if st.Retries > 0 {
		parts = append(parts, ":retry:", st.Retries)
	}
	return fmt.Sprint(append([]any{sc.WorkflowRunID}, parts...)...)
}

// phase creates a phase Run starting from where the work stands: each
// repository at its head, or its default branch if nothing changed it yet.
// While a pull request's fix is checked, what a Run publishes goes to the
// candidate branch.
func (w *steps) phase(ctx context.Context, sc workflow.StepContext, st *State, phase, k string, extra func(*PhaseRun)) (string, error) {
	if c := st.Candidate; c != nil && c.opening {
		if err := w.openCandidate(ctx, sc, st, k); err != nil {
			return "", err
		}
	}
	in := PhaseRun{TaskID: st.TaskID, Phase: phase, WorkflowID: sc.WorkflowRunID, BaseRefs: st.Heads, ParentRunID: st.HeadRunID, Key: k, Attempt: st.Attempt}
	if st.Candidate != nil && Publishes[phase] {
		in.Branch = st.Candidate.Branch
	}
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
	// Existing work has nothing to implement: it is taken over, then reviewed.
	if st.Source != nil && st.Retries == 0 {
		return workflow.Result{Next: "adopt", State: st}, nil
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
	// The backstop for an edge added between the start and this step: a
	// first implementer never starts on a blocked task, and no implementer
	// on a closed or archived one. Parked, not failed.
	parked, err := w.s.parkIfBlocked(ctx, sc.OrganizationID, st, !st.Overridden && st.Retries == 0)
	if err != nil {
		return workflow.Result{}, err
	}
	if parked {
		return workflow.Result{Next: "implementRun", State: st, AwaitSignals: []string{SignalRelationsUnblock},
			SleepUntil: time.Now().Add(blockedRecheck)}, nil
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "implementing"); err != nil {
		return workflow.Result{}, err
	}
	if err := w.writerCandidate(ctx, sc, st, PhaseImplement); err != nil {
		return workflow.Result{}, err
	}
	k := writerKey(sc, st, PhaseImplement)
	d := directed(st, PhaseImplement)
	// Feedback submitted before any work: the implementer is given it.
	feedback, err := w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	runID, err := w.phase(ctx, sc, st, PhaseImplement, k, func(p *PhaseRun) { p.FeedbackItemIDs = feedback; d.apply(p) })
	if errors.Is(err, ErrDeliveryEnded) {
		return workflow.Result{Next: "implementRun", State: st, AwaitSignals: []string{SignalRelationsUnblock},
			SleepUntil: time.Now().Add(blockedRecheck)}, nil
	}
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
	if len(out.ChangedPaths) == 0 && st.Source.Continues() {
		// Work to continue that already met its goal: reviewed as it is,
		// on its unchanged head, by the reviewers its own changes pick.
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			// Once per Run: a replayed step finds it written.
			var said bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type = $2)`,
				runID, EvNothingToAdd).Scan(&said); err != nil || said {
				return err
			}
			_, err := ledger.Append(ctx, tx, ledger.Event{
				Type: EvNothingToAdd, OrganizationID: sc.OrganizationID, ProjectID: st.ProjectID, TaskID: st.TaskID, RunID: runID,
				ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID,
				Payload: map[string]any{"heads": st.Heads},
			})
			return err
		}); err != nil {
			return workflow.Result{}, err
		}
		st.HeadRunID, st.PendingRunIDs = runID, nil
		return w.next(ctx, sc, st, PointImplemented, "review", out.line(runID, "implement")+nothingToAdd)
	}
	if len(out.ChangedPaths) == 0 && (writable || !out.Published) {
		return w.escalate(ctx, sc, st, "no_changes", map[string]any{"runId": runID})
	}
	if st.Source.Continues() {
		// The review is of the whole work, the person's and the implementer's.
		out.ChangedPaths = withPaths(out.ChangedPaths, st.ChangedPaths)
	}
	st.HeadRunID, st.Heads, st.ChangedPaths, st.PendingRunIDs = runID, out.advance(st.Heads), out.ChangedPaths, nil
	return w.next(ctx, sc, st, PointImplemented, "review", out.line(runID, "implement"))
}

// withPaths appends to paths each of more it lacks, once, keeping both orders.
func withPaths(paths, more []string) []string {
	seen := make(map[string]struct{}, len(paths)+len(more))
	for _, p := range paths {
		seen[p] = struct{}{}
	}
	for _, p := range more {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			paths = append(paths, p)
		}
	}
	return paths
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
	// A re-review judges what the fixer was sent: every open finding a fix
	// has attempted, by the reviewer of its category, or by one of this
	// round's when it has none (judgedBy).
	attempted, err := w.s.AttemptedFindings(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	d := directed(st, PhaseReview)
	if err := w.refreshPRWaitHeads(ctx, sc, st); err != nil {
		return workflow.Result{}, err
	}
	reviewRange := st.reviewRange()
	// Each Run's creation key; the round's own is the key of no category.
	runKey := func(c string) string {
		switch {
		case d != nil:
			return conductorKey(sc, st, d, ":review:", st.Iteration, ":", c)
		case st.Integration != nil:
			return key(sc, st, ":integration:", st.Integration.RunID, ":review:", st.Iteration, ":", c)
		}
		return key(sc, st, ":review:", st.Iteration, ":", c)
	}
	// A retry of a round some reviewers failed: only theirs run again.
	retrying := st.RetryCategories
	var categories []string
	var reasons map[string][]string
	switch {
	case len(retrying) > 0:
		categories, reasons, st.RetryCategories = retrying, eachFor(retrying, ReasonRetry), nil
	case d != nil && len(d.Categories) > 0:
		categories, reasons = d.Categories, eachFor(d.Categories, ReasonDirected)
	case reviewRange != nil && reviewRange.Of == RangeSimplify && len(st.Policy.RequiredReviewers) > 0:
		// A check that the simplifier changed no behaviour: the policy's
		// required reviewers, whatever paths it touched.
		categories, reasons = st.Policy.RequiredReviewers, eachFor(st.Policy.RequiredReviewers, ReasonRequired)
	case st.ReviewChoice != nil && st.ReviewChoice.Round == runKey("") && maps.Equal(st.ReviewChoice.Heads, st.Heads):
		// A replay: a Run of this round that completed since would make
		// the round's change start at its own checkouts. Heads that moved
		// since (refreshPRWaitHeads) are a change the choice never saw.
		categories, reasons = st.ReviewChoice.Categories, st.ReviewChoice.Reasons
	default:
		if categories, reasons, err = w.roundReviewers(ctx, sc, st, slices.Collect(maps.Keys(attempted))); err != nil {
			return workflow.Result{}, err
		}
		if len(categories) == 0 {
			if categories, reasons, err = w.gateReviewers(ctx, sc, st); err != nil {
				return workflow.Result{}, err
			}
		}
		st.ReviewChoice = &ReviewChoice{Round: runKey(""), Heads: maps.Clone(st.Heads), Categories: db.NonNil(categories), Reasons: reasons}
		if err := w.keepReviewChoice(ctx, sc, *st.ReviewChoice); err != nil {
			return workflow.Result{}, err
		}
	}
	if len(retrying) > 0 {
		// The round's other reviewers judged theirs already.
		maps.DeleteFunc(attempted, func(c string, _ []string) bool { return !slices.Contains(retrying, c) })
	}
	toJudge := judgedBy(st.Policy, categories, attempted)
	goesOn := w.goesOn(ctx, sc, st, PhaseReview)
	var runIDs []string
	for _, c := range categories {
		k := runKey(c)
		cont, err := goesOn(c)
		if err != nil {
			return workflow.Result{}, err
		}
		id, err := w.phase(ctx, sc, st, PhaseReview, k,
			func(p *PhaseRun) {
				p.Category, p.BlockingSeverities, p.FindingIDs = c, st.Policy.BlockingSeverities, toJudge[c]
				p.ReviewRange = reviewRange
				p.Continue = cont
				d.apply(p)
				if st.Integration != nil {
					p.PRFeedback = []forge.ActionableFeedback{{Source: "integration", Repo: st.Integration.Repo, Body: integrationReviewBrief(st.Integration.MergeSHA)}}
				}
			})
		if err != nil {
			return workflow.Result{}, err
		}
		runIDs = append(runIDs, id)
	}
	if err := w.s.Emit(ctx, sc.OrganizationID, st, EvRunCreated,
		map[string]any{"phase": "review", "reviewers": db.NonNil(categories), "reasons": reasons, "iteration": st.Iteration}); err != nil {
		return workflow.Result{}, err
	}
	st.ReviewRunIDs = runIDs
	if len(runIDs) == 0 {
		// No reviewer is warranted and no gate waits (gateReviewers): the
		// round is complete and clear of new findings.
		st.PendingRunIDs = nil
		return workflow.Result{Next: "awaitReview", State: st}, nil
	}
	return park("awaitReview", st, runIDs), nil
}

// roundReviewers is the policy's choice of this round's reviewers
// (RoundReviewers): a later round of the attempt is measured from the
// previous review round's checkouts to the heads. An integration's review
// is of a merge, not of what changed since a round: it is a first round.
func (w *steps) roundReviewers(ctx context.Context, sc workflow.StepContext, st *State, attempted []string) ([]string, map[string][]string, error) {
	r := ReviewRound{Whole: st.ChangedPaths, Attempted: attempted}
	if st.Integration == nil {
		from, err := w.s.lastReviewRound(ctx, sc.OrganizationID, st)
		if err != nil {
			return nil, nil, err
		}
		if from != nil {
			if r.Change, err = w.roundChange(ctx, sc, st, PhaseReview, from); err != nil {
				return nil, nil, err
			}
		}
	}
	categories, reasons := RoundReviewers(st.Policy, r)
	return categories, reasons, nil
}

// gateReviewers stands in for a round the policy gave no reviewer when a
// gate waits on a completed review at these heads: a candidate's promotion
// (unreviewedHeads) or a conductor head before the pull request
// (UntestedConductorHeadTx). An empty round completes no Run there, so the
// gate would send the delivery back to review for ever. RequiredReviewers
// run, else the first round's reviewers; nil when no gate waits.
func (w *steps) gateReviewers(ctx context.Context, sc workflow.StepContext, st *State) ([]string, map[string][]string, error) {
	gated := st.Candidate != nil
	if !gated {
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
			gated, err = UntestedConductorHeadTx(ctx, tx, st.TaskID, st.Heads)
			return err
		}); err != nil {
			return nil, nil, err
		}
	}
	switch {
	case !gated:
		return nil, nil, nil
	case len(st.Policy.RequiredReviewers) > 0:
		return st.Policy.RequiredReviewers, eachFor(st.Policy.RequiredReviewers, ReasonRequired), nil
	}
	first := ReviewersFor(st.Policy, st.ChangedPaths)
	return first, eachFor(first, ReasonFirstRound), nil
}

// reviewRange is the range the next review round covers: what the
// simplifier changed, also on a pull request's fix being checked; else
// that fix; nil for the whole branch.
func (st *State) reviewRange() *ReviewRange {
	switch {
	case len(st.SimplifyBase) > 0:
		return &ReviewRange{Of: RangeSimplify, Base: st.SimplifyBase}
	case st.Candidate != nil && len(st.Candidate.Base) > 0:
		return &ReviewRange{Of: RangePullRequestFix, Base: st.Candidate.Base}
	}
	return nil
}

// afterReview is the step a review loop that has nothing left to fix goes
// to: the simplifier, then the test — for new work and for an open pull
// request's fix on its candidate alike. A loop over the simplifier's own
// diff goes to the test: the simplifier is not run again after a fix of
// its diff; that fix was reviewed by the loop, and the tester runs next.
func (st *State) afterReview() string {
	if st.Policy.Simplify && len(st.SimplifyBase) == 0 {
		return "simplify"
	}
	return "test"
}

// judgedBy assigns each category's attempted findings to a reviewer of the
// round: its own, else the policy's first required reviewer the round has,
// else the round's first. A finding no reviewer is shown can never be
// resolved, and would count toward stuck for nothing.
func judgedBy(p Policy, round []string, attempted map[string][]string) map[string][]string {
	if len(round) == 0 {
		return attempted
	}
	fallback := round[0]
	for _, r := range p.RequiredReviewers {
		if slices.Contains(round, r) {
			fallback = r
			break
		}
	}
	out := map[string][]string{}
	for _, c := range slices.Sorted(maps.Keys(attempted)) {
		to := c
		if !slices.Contains(round, c) {
			to = fallback
		}
		out[to] = append(out[to], attempted[c]...)
	}
	return out
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
	// A reviewer that failed or was aborted read nothing: the round is not
	// complete, and its silence is not a clear review.
	round := st.ReviewRunIDs
	if len(round) == 0 {
		if round, err = w.s.ReviewRound(ctx, sc.OrganizationID, st); err != nil {
			return workflow.Result{}, err
		}
		st.ReviewRunIDs = round
	}
	failed, err := w.s.EndedUnfinished(ctx, sc.OrganizationID, round)
	if err != nil {
		return workflow.Result{}, err
	}
	if len(failed) > 0 {
		f := failed[0]
		detail := map[string]any{"runId": f.ID, "category": f.Category, "error": f.Error,
			"runIds": failedIDs(failed), "categories": failedCategories(failed)}
		return w.escalate(ctx, sc, st, "review_failed", detail)
	}
	st.ReviewRunIDs = nil
	// The round's reviewers' questions are the conductor's to answer or
	// take to a person, whatever the loop does next.
	if st.conducted() {
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			return recordReviewQuestionWakesTx(ctx, tx, sc.OrganizationID, st.TaskID, round)
		}); err != nil {
			return workflow.Result{}, err
		}
	}
	findings, conv, err := w.loopFindings(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	st.Iteration++
	st.PendingRunIDs = nil
	exit := w.loopExit(st, findings, conv)
	if (exit == nil || exit.Reason == "clear") && !st.checkingCandidate() {
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
func (w *steps) loopExit(st *State, findings []FindingState, conv Convergence) *LoopExit {
	policy := st.Policy
	policy.MaxAttemptsPerFinding += st.ExtraFixAttempts
	return Exit(policy, findings, st.Iteration, conv)
}

// loopFindings is what the review loop weighs: the reviewers' findings, with
// the loop's convergence, and the people's feedback still to be handled.
func (w *steps) loopFindings(ctx context.Context, org string, st *State) ([]FindingState, Convergence, error) {
	findings, conv, err := w.s.ConvergingFindings(ctx, org, st)
	if err != nil {
		return nil, conv, err
	}
	findings, err = w.withFeedback(ctx, org, st, findings)
	return findings, conv, err
}

// withFeedback adds the people's open feedback to the findings. Candidate
// and integration loops weigh reviewers' findings alone; open feedback
// returns to the PR fix loop.
func (w *steps) withFeedback(ctx context.Context, org string, st *State, findings []FindingState) ([]FindingState, error) {
	if st.checkingCandidate() || st.Integration != nil {
		return findings, nil
	}
	feedback, err := w.s.FeedbackStates(ctx, org, st)
	return append(findings, feedback...), err
}

// afterLoop goes where the review loop's exit says.
func (w *steps) afterLoop(ctx context.Context, sc workflow.StepContext, st *State, exit *LoopExit) (workflow.Result, error) {
	switch {
	case exit == nil:
		return workflow.Result{Next: "fix", State: st}, nil
	case exit.Reason == "clear":
		if st.Integration != nil {
			return workflow.Result{Next: "test", State: st}, nil
		}
		if st.ActingOnPR && (st.Candidate == nil || st.Candidate.Loop) {
			// A failed test's fix must be tested before returning to the PR.
			if st.RetestDue {
				return workflow.Result{Next: "test", State: st}, nil
			}
			// A loop's candidate goes on to the simplifier and the test
			// before it is promoted.
			if st.Candidate == nil {
				return workflow.Result{Next: "openPullRequest", State: st}, nil
			}
		}
		return workflow.Result{Next: st.afterReview(), State: st}, nil
	}
	return w.escalate(ctx, sc, st, exit.Reason, exit)
}

// reviewExit is "what the policy would do" after a review round the
// conductor triaged: the exit for the findings as they are now — some may
// have been dismissed since. Convergence is not weighed again: awaitReview
// weighed this round's history, and triage or a not_converging accept (which
// moves ConvergenceFrom past it) can only lower it.
func (w *steps) reviewExit(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	findings, err := w.s.Findings(ctx, sc.OrganizationID, st)
	if err == nil {
		findings, err = w.withFeedback(ctx, sc.OrganizationID, st, findings)
	}
	if err != nil {
		return workflow.Result{}, err
	}
	return w.afterLoop(ctx, sc, st, w.loopExit(st, findings, Convergence{}))
}

// findingsLine is a review round's findings in a note: how many are open,
// by severity, and the round. Counts only, never a finding's text.
func findingsLine(st *State, findings []FindingState) string {
	bySeverity := map[string]int{}
	open, people := 0, 0
	for _, f := range findings {
		if f.Human {
			people++
			continue
		}
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
	return render(reviewRoundTmpl, struct {
		Iteration, Max, Open, People int
		Severities                   []string
	}{st.Iteration, st.Policy.MaxReviewIterations, open, people, parts})
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
	if st.checkingCandidate() {
		// A pull request's fix fixes what its own reviews raised — of the
		// fix, and of its simplifier's diff — never the findings a person
		// let the pull request open with. After a failed test SimplifyBase
		// is empty: the narrow review's open findings are not sent.
		ranges := []*ReviewRange{{Of: RangePullRequestFix, Base: st.Candidate.Base}}
		if len(st.SimplifyBase) > 0 {
			ranges = append(ranges, &ReviewRange{Of: RangeSimplify, Base: st.SimplifyBase})
		}
		var ours []string
		for _, r := range ranges {
			ids, err := w.s.rangeFindings(ctx, sc.OrganizationID, st, r)
			if err != nil {
				return workflow.Result{}, err
			}
			ours = append(ours, ids...)
		}
		open = slices.DeleteFunc(open, func(id string) bool { return !slices.Contains(ours, id) })
	}
	if err := w.writerCandidate(ctx, sc, st, PhaseFix); err != nil {
		return workflow.Result{}, err
	}
	k := writerKey(sc, st, PhaseFix)
	if len(st.TestFindingIDs) > 0 {
		// A failed test's findings, those still open, in a Run of their own.
		all, err := w.s.FixFindings(ctx, sc.OrganizationID, st)
		if err != nil {
			return workflow.Result{}, err
		}
		for _, f := range all {
			if f.Status == "open" && slices.Contains(st.TestFindingIDs, f.ID) && !slices.Contains(open, f.ID) {
				open = append(open, f.ID)
			}
		}
	}
	d := directed(st, PhaseFix)
	if d != nil {
		if len(d.FindingIDs) > 0 {
			// Only those of the conductor's that are still open.
			open = slices.DeleteFunc(slices.Clone(d.FindingIDs), func(id string) bool { return !slices.Contains(open, id) })
		}
	}
	// The people's feedback still to be handled goes with every fix, the
	// conductor's included, whatever findings it named: its words are
	// dude's to give, and no decision leaves an item out. Not an
	// integration's: they go to the fix after its promotion (prFix).
	var feedback []string
	if st.Integration == nil {
		if feedback, err = w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st); err != nil {
			return workflow.Result{}, err
		}
	}
	// Counted once per fix step, however often the step is replayed: with
	// the Run's creation, which a replay finds by its key.
	cont, err := w.goesOn(ctx, sc, st, PhaseFix)("")
	if err != nil {
		return workflow.Result{}, err
	}
	runID, err := w.phase(ctx, sc, st, PhaseFix, k, func(p *PhaseRun) {
		p.FindingIDs, p.FeedbackItemIDs, p.CountAttempts = open, feedback, true
		p.Continue = cont
		d.apply(p)
	})
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
	// A repository the fix changed first joins the range under review, from
	// where it stood before the fix: the range's next review names it. On a
	// candidate, while the simplifier's range is under review, it joins the
	// fix's range too, which a failed test's fix is reviewed over.
	if r := st.reviewRange(); r != nil {
		widenRange(r.Base, out, st.Heads)
		if st.checkingCandidate() && r.Of == RangeSimplify {
			widenRange(st.Candidate.Base, out, st.Heads)
		}
	}
	st.HeadRunID, st.PendingRunIDs, st.Heads, st.ChangedPaths = runID, nil, out.advance(st.Heads), out.ChangedPaths
	// The tester's findings were sent; the next test judges them.
	st.TestFindingIDs = nil
	if st.checkingCandidate() {
		return workflow.Result{Next: "review", State: st}, nil
	}
	return w.next(ctx, sc, st, PointFixed, "review", out.line(runID, "fix"))
}

// widenRange adds to base each repository out changed that it lacks, from
// where that repository stood before the Run.
func widenRange(base map[string]string, out Outcome, heads map[string]string) {
	for repo := range out.Heads {
		if _, ok := base[repo]; !ok {
			if b := cmp.Or(heads[repo], out.Bases[repo]); b != "" {
				base[repo] = b
			}
		}
	}
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
	if err := w.writerCandidate(ctx, sc, st, PhaseSimplify); err != nil {
		return workflow.Result{}, err
	}
	k := writerKey(sc, st, PhaseSimplify)
	d := directed(st, PhaseSimplify)
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
	if !out.Succeeded || len(out.Heads) == 0 {
		return workflow.Result{Next: "test", State: st}, nil
	}
	// What it changed was never reviewed: a review round over its diff
	// alone, from where each repository it changed stood before it, then
	// the loop as usual (afterReview takes it to the test). On a pull
	// request's fix it ran on the candidate, which its heads advance.
	st.SimplifyBase = map[string]string{}
	for repo := range out.Heads {
		if base := cmp.Or(out.Bases[repo], st.Heads[repo]); base != "" {
			st.SimplifyBase[repo] = base
		}
	}
	if st.checkingCandidate() {
		widenRange(st.Candidate.Base, out, st.Heads)
	}
	st.HeadRunID, st.Heads, st.ChangedPaths = runID, out.advance(st.Heads), out.ChangedPaths
	if len(st.SimplifyBase) == 0 {
		// No starting commit known (a Run from before it was kept): the
		// range cannot be named, and a whole-branch round would end in
		// another simplification.
		return workflow.Result{Next: "test", State: st}, nil
	}
	// Mechanics, whoever decides: code nobody reviewed never reaches the
	// test or the pull request. The round's findings are the conductor's
	// to triage, as any round's are (awaitReview).
	return workflow.Result{Next: "review", State: st}, nil
}

func (w *steps) test(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	// The simplifier's diff was reviewed; a fix a failed test sends back
	// is reviewed whole (a pull request's fix: over the fix's range), then
	// simplified again, as before it.
	st.SimplifyBase = nil
	d := directed(st, PhaseTest)
	if !st.Policy.Test && !st.RetestDue && (d == nil || d.Phase != PhaseTest) {
		if st.Candidate != nil {
			// No tester: a pull request's fix, reviewed, is the pull request's.
			return workflow.Result{Next: "promoteCandidate", State: st}, nil
		}
		return w.beforePR(ctx, sc, st, "")
	}
	k := testRoundKey(sc, st, ":test")
	if d != nil {
		k = conductorKey(sc, st, d, ":test")
	}
	if err := w.refreshPRWaitHeads(ctx, sc, st); err != nil {
		return workflow.Result{}, err
	}
	// The tester's own findings a fix was sent are its to judge again,
	// never a reviewer's.
	judge, err := w.s.TesterFindingsToJudge(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	if skip, err := w.carryForward(ctx, sc, st, judge, d != nil); err != nil {
		return workflow.Result{}, err
	} else if skip {
		// The previous round's verdicts are this round's: the gate weighs them.
		return workflow.Result{Next: "testVerdict", State: st}, nil
	}
	cont, err := w.goesOn(ctx, sc, st, PhaseTest)("")
	if err != nil {
		return workflow.Result{}, err
	}
	runID, err := w.phase(ctx, sc, st, PhaseTest, k, func(p *PhaseRun) {
		p.BlockingSeverities, p.FindingIDs = st.Policy.BlockingSeverities, judge
		p.Continue, p.CarryFrom = cont, st.CarryFrom
		d.apply(p)
	})
	if err != nil {
		return workflow.Result{}, err
	}
	st.RetestDue = false
	return park("awaitTest", st, []string{runID}), nil
}

// testRoundKey is key, distinct per fix round a failed test started: the
// simplify and test after it are new Runs, not the ones before it.
func testRoundKey(sc workflow.StepContext, st *State, part string) string {
	if st.Integration != nil {
		return key(sc, st, ":integration:", st.Integration.RunID, part, ":test-round:", st.TestFixRounds)
	}
	if st.TestFixRounds > 0 {
		return key(sc, st, part, ":after-test:", st.TestFixRounds)
	}
	return key(sc, st, part)
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
		// A tester that did not finish proved nothing: a person decides.
		return w.escalate(ctx, sc, st, "test_failed", map[string]any{"runId": runID, "error": out.Error})
	}
	// Finished is not passed: the gate weighs what it found.
	st.PendingRunIDs, st.TestRunID = nil, runID
	return workflow.Result{Next: "testVerdict", State: st}, nil
}

// beforePR goes to the pull request step, through the decision before it
// (Open or Draft) — except for a pull request taken over, or open pull
// requests with nothing new to open: nothing to decide.
func (w *steps) beforePR(ctx context.Context, sc workflow.StepContext, st *State, line string) (workflow.Result, error) {
	if st.Integration != nil {
		return workflow.Result{Next: "openPullRequest", State: st}, nil
	}
	if st.adoptedPR() || st.ActingOnPR {
		return workflow.Result{Next: "openPullRequest", State: st}, nil
	}
	covered, err := w.prsCoverTheWork(ctx, sc, st)
	if err != nil {
		return workflow.Result{}, err
	}
	if covered {
		// Nothing to open: back to the wait, with reviewers of an older head asked again (openPullRequest).
		st.ActingOnPR = true
		return workflow.Result{Next: "openPullRequest", State: st}, nil
	}
	// A person's branch is never published as a pull request without their Open or Draft.
	if st.adoptedBranch() {
		st.GateRequired, st.ChoiceGate = true, false
	}
	return w.next(ctx, sc, st, PointBeforePR, "openPullRequest", line)
}

func (w *steps) openPullRequest(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if st.Integration != nil && st.Integration.Published {
		gh, err := w.forges.For(ctx, sc.OrganizationID)
		if err != nil {
			return workflow.Result{}, err
		}
		states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
		if err != nil {
			return workflow.Result{}, err
		}
		settled := true
		for _, p := range states {
			if p.Repo != st.Integration.Repo || p.Number != st.Integration.Number || p.State == forge.StateClosed || p.State == forge.StateMerged {
				continue
			}
			ref, mergeable, err := gh.Mergeability(ctx, p.Slug, p.Number)
			if err != nil {
				return workflow.Result{}, err
			}
			if ref.State == forge.StateClosed || ref.State == forge.StateMerged {
				continue
			}
			if mergeable == forge.MergeConflicting {
				return w.integrationFailure(ctx, sc, st, causeStillConflicting, reasonStillConflicting)
			}
			if mergeable == forge.MergeUnknown {
				if st.Integration.Polls < unknownMergeabilityPolls {
					wait := min(time.Second<<st.Integration.Polls, time.Minute)
					st.Integration.Polls++
					return workflow.Result{Next: "openPullRequest", State: st, SleepUntil: time.Now().Add(wait)}, nil
				}
				// GitHub still has not said. The reading on record is the
				// old head's: forgotten, so the pull request syncer's next
				// conflicting read is news and signals this workflow, and
				// the integrated head is recorded as attempted against this
				// base, so that signal escalates rather than integrating again.
				if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE pull_requests SET mergeable_state = 'unknown', updated_at = now()
						WHERE id = $1 AND state IN ('open', 'draft')`, p.ID)
					return err
				}); err != nil {
					return workflow.Result{}, err
				}
				after := *st.Integration
				after.Head = st.Heads[after.Repo]
				if st.IntegrationPairs == nil {
					st.IntegrationPairs = map[string]string{}
				}
				st.IntegrationPairs[after.pair()] = st.Integration.RunID
				if after.agentRun() {
					if st.HandedOver == nil {
						st.HandedOver = map[string]Integration{}
					}
					// Kept as it started, so its closing line names the same attempt.
					st.HandedOver[after.pair()] = *st.Integration
					st.handingOver = true
				}
				settled = false
			}
		}
		if err := w.s.RerequestReviews(ctx, sc.OrganizationID, st, gh, st.Heads); err != nil {
			return workflow.Result{}, err
		}
		// A merge GitHub made cleanly resolved nothing: its own merge-base
		// line says what happened.
		if settled && st.Integration.agentRun() {
			if err := w.integrationEvent(ctx, sc, st, "integration-completed", map[string]any{"head": st.Heads[st.Integration.Repo]}); err != nil {
				return workflow.Result{}, err
			}
		}
		st.Integration = nil
		// The pull request comments that came with the conflict, and the
		// people's open items, were not the integration fixer's: they go to
		// a fix checked on a candidate, now the integration is reviewed and
		// tested.
		if len(st.PullRequestIDs) > 0 {
			open, err := w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st)
			if err != nil {
				return workflow.Result{}, err
			}
			if len(st.PRFeedback) > 0 || len(open) > 0 {
				st.ActingOnPR = false
				line := feedbackOpenLine(feedbackAfterIntegration, len(open))
				if len(st.PRFeedback) > 0 {
					line = feedbackLine(st.PRFeedback)
				}
				return w.next(ctx, sc, st, PointPRFeedback, "prFix", line)
			}
		}
	}
	acting := st.ActingOnPR
	st.ActingOnPR = false
	// A pull request taken over is already open: none is opened, nobody is
	// asked to authorize one, and its reviewers are asked to look again at
	// what the review loop pushed on it.
	if st.adoptedPR() {
		return w.keepAdoptedPullRequest(ctx, sc, st)
	}
	// Work the conductor started while the pull requests waited may have
	// moved them: whoever reviewed an older head is asked to look again.
	if acting {
		gh, err := w.forges.For(ctx, sc.OrganizationID)
		if err != nil {
			return workflow.Result{}, err
		}
		if err := w.s.RerequestReviews(ctx, sc.OrganizationID, st, gh, st.Heads); err != nil {
			return workflow.Result{}, err
		}
	}
	// Never untested at the pull request: a head that is the conductor's
	// commit is reviewed first, whoever decides.
	if len(st.PullRequestIDs) == 0 {
		if res, diverted, err := w.reviewUntested(ctx, sc, st); diverted || err != nil {
			return res, err
		}
		// Nor past a person's feedback in dude still to be handled, read
		// now, as the step that opens it runs: it goes to a fixer first.
		open, err := w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st)
		if err != nil {
			return workflow.Result{}, err
		}
		if len(open) > 0 {
			return w.next(ctx, sc, st, PointReviewed, "fix",
				feedbackOpenLine(feedbackBeforePR, len(open)))
		}
		// Nor past a reviewer's question nobody answered, whoever decides.
		if res, held, err := w.holdForQuestions(ctx, sc, st, "openPullRequest"); held || err != nil {
			return res, err
		}
	}
	// The gate holds at the opening itself: a delivery taken over after the
	// policy chose to open waits for the person's answer like any other, and
	// one handed back after the conductor entered the gate opens only with
	// the person's Open or Draft at the heads it opens.
	if len(st.PullRequestIDs) == 0 && (!st.GateRequired || st.ChoiceGate) {
		// The task's choice before its first pull request, read now: Ask me
		// first holds as a gate the conductor entered does; Open as draft
		// opens a draft. Never once the attempt has had a pull request.
		choice, err := w.s.FirstPullRequestChoice(ctx, sc.OrganizationID, st.TaskID)
		if unresolved := (ChoiceUnresolved{}); errors.As(err, &unresolved) {
			return w.escalate(ctx, sc, st, ReasonChoiceUnresolved, map[string]any{"error": unresolved.Err.Error()})
		}
		if err != nil {
			return workflow.Result{}, err
		}
		if choice != "" {
			st.GateRequired, st.ChoiceGate = choice == PullRequestAsk, choice == PullRequestAsk
			if !st.GateRequired {
				st.Draft = choice == PullRequestDraft
			}
		}
	}
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
	if res, diverted, err := w.syncBeforePR(ctx, sc, st); diverted || err != nil {
		return res, err
	}
	prIDs, err := w.s.OpenPullRequests(ctx, sc.OrganizationID, st, w.forges)
	if errors.As(err, new(forge.OwnerAuthorizationError)) {
		return w.awaitOwnerAuthorization(ctx, sc, st, err)
	}
	if err != nil {
		return workflow.Result{}, err
	}
	if len(st.PullRequestIDs) == 0 {
		st.PRIteration = 0
	}
	st.PullRequestIDs = prIDs
	if len(prIDs) > 0 {
		// The hold is over: nothing reads it once a pull request is on record.
		st.Hold = nil
	}
	if len(prIDs) == 0 {
		// Nothing changed in any repository: the work is what the agents
		// published. A person reads it and says when it is done, or gives
		// it feedback, which takes the delivery up again (awaitRead).
		return awaitRead(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "ready to read")
	}
	// Waiting on people now: the agents are done until someone asks.
	reason := "pull request open"
	if len(prIDs) > 1 {
		reason = fmt.Sprintf("%d pull requests open", len(prIDs))
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", reason); err != nil {
		return workflow.Result{}, err
	}
	if len(st.PendingConflicts) > 0 {
		return workflow.Result{Next: "awaitPullRequest", State: st}, nil
	}
	return waitForPR(st), nil
}

// awaitOwnerAuthorization parks a publication until its frozen owner
// connects GitHub; the branch stays pushed and no retry budget is spent.
func (w *steps) awaitOwnerAuthorization(ctx context.Context, sc workflow.StepContext, st *State, cause error) (workflow.Result, error) {
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "awaiting_input", cause.Error()); err != nil {
		return workflow.Result{}, err
	}
	return workflow.Result{Next: "openPullRequest", State: st, AwaitSignals: []string{SignalGitHubAuthorized}}, nil
}

// awaitPullRequest wakes a fixer only when there is something to fix: the
// classifier has already decided a change is actionable before it becomes a
// signal, so an approval or a green check never gets here.
func (w *steps) awaitPullRequest(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if err := w.closeHandedOver(ctx, sc, st); err != nil {
		return workflow.Result{}, err
	}
	// Every signal of the batch is read before anything is decided: they are
	// consumed together, so one PR merging must not drop feedback on another
	// that arrived with it.
	var actionable []forge.ActionableFeedback
	ended, readiness := false, false
	var stop *forge.Signal // a conflict, or CI stuck: a person's to see to
	// A conflict another stop takes the place of waits its turn.
	stopAt := func(next *forge.Signal) {
		if stop != nil && stop.Kind == "conflict" {
			st.PendingConflicts = append(st.PendingConflicts, *stop)
		}
		stop = next
	}
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
			stopAt(&s)
		default:
			actionable = append(actionable, s.Feedback...)
			if s.Conflict {
				stopAt(&forge.Signal{Kind: "conflict", Repo: s.Repo, Number: s.Number})
			}
		}
	}
	if stop == nil && len(st.PendingConflicts) > 0 {
		s := st.PendingConflicts[0]
		st.PendingConflicts = st.PendingConflicts[1:]
		stop = &s
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
		if stop.Kind == "conflict" {
			start, err := w.prepareIntegration(ctx, sc, st, stop)
			if err != nil {
				return workflow.Result{}, err
			}
			if start {
				return workflow.Result{Next: "prFix", State: st}, nil
			}
			if st.Integration != nil {
				cause, problem := st.attemptCause(st.Integration)
				return w.integrationFailure(ctx, sc, st, cause, problem)
			}
			gh, err := w.forges.For(ctx, sc.OrganizationID)
			if err != nil {
				return workflow.Result{}, err
			}
			// Nothing to integrate after all: a person's request, or the
			// automatic setting, goes back to waiting rather than escalating.
			if stop.Requested || gh != nil && gh.Settings.WhenBehind == "update" {
				if len(st.PRFeedback) > 0 {
					return workflow.Result{Next: "prFix", State: st}, nil
				}
				return waitForPR(st), nil
			}
		}
		reason := "pull_request_conflict"
		if stop.Kind == "ci_stuck" {
			reason = "ci_stuck"
		}
		return w.escalate(ctx, sc, st, reason, map[string]any{"repo": stop.Repo, "number": stop.Number})
	}
	waitAgain := waitForPR(st)
	if len(actionable) == 0 {
		// A person's feedback in dude: a fix, as pull request feedback is,
		// unless what it signalled is handled already.
		if humanFeedback(sc) {
			open, err := w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st)
			if err != nil {
				return workflow.Result{}, err
			}
			if len(open) > 0 {
				st.PRFeedback = nil
				return w.next(ctx, sc, st, PointPRFeedback, "prFix", feedbackOpenLine(feedbackOpen, len(open)))
			}
		}
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

// feedbackLine counts pull request feedback by kind, without its words.
func feedbackLine(feedback []forge.ActionableFeedback) string {
	kinds := map[string]int{}
	for _, f := range feedback {
		kind := f.Source
		if len(f.Checks) > 0 {
			kind = failingCheck
		}
		kinds[kind]++
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(kinds)) {
		parts = append(parts, fmt.Sprintf("%d %s", kinds[k], k))
	}
	return render(prFeedbackFactTmpl, struct {
		Count int
		Kinds []string
	}{len(feedback), parts})
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
	if st.Integration != nil {
		k := key(sc, st, ":prfix:", max(st.PRFixKey, st.PRIteration)+1)
		existing, err := w.s.runByKey(ctx, sc.OrganizationID, st.TaskID, k)
		if err != nil {
			return workflow.Result{}, err
		}
		if existing == "" {
			if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
				if _, err := lockAndRefuseWhileMovingTx(ctx, tx, st.TaskID); err != nil {
					return err
				}
				var writer bool
				if err := tx.QueryRow(ctx, `SELECT `+liveWriter, st.TaskID).Scan(&writer); err != nil {
					return err
				}
				if writer {
					return workflow.Wait{After: time.Second, Why: "integration waits for the current writer"}
				}
				return nil
			}); err != nil {
				return workflow.Result{}, err
			}
		}
	}
	total := forge.DefaultSettings().FixRoundsPerPR
	if gh != nil {
		total = gh.Settings.FixRoundsPerPR
	}
	if st.Integration != nil {
		// Bringing the base in is dude's upkeep, not a fix the pull
		// request's people asked for: it spends none of their budget, and
		// is bounded by one Run per head/base pair instead.
		st.PRFixKey = max(st.PRFixKey, st.PRIteration) + 1
	} else if res, exhausted, err := w.chargePRFix(ctx, sc, st, total); err != nil || exhausted {
		return res, err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "addressing pull request feedback"); err != nil {
		return workflow.Result{}, err
	}
	// A new round's candidate: its Runs are keyed apart from the last's,
	// and its first review is of the fix, never of an earlier simplifier.
	st.Candidate, st.SimplifyBase = nil, nil
	k := key(sc, st, ":prfix:", st.PRFixKey)
	existing, err := w.s.runByKey(ctx, sc.OrganizationID, st.TaskID, k)
	if err != nil {
		return workflow.Result{}, err
	}
	if existing == "" {
		// The last round's candidate never reached the pull request (a
		// promoted one has nothing pending): what its fixers resolved goes
		// to this round's fixer.
		if err := w.s.abandonCandidate(ctx, sc.OrganizationID, st); err != nil {
			return workflow.Result{}, err
		}
	}
	var runBranch string
	if existing == "" && gh != nil {
		// What GitHub has, not what dude last pushed: a person may have
		// pushed since, and a fix from dude's head would fail to
		// fast-forward over theirs — or undo it.
		if err := w.fromPullRequestHeads(ctx, sc, st, gh, true); err != nil {
			return workflow.Result{}, err
		}
		// The last round's candidate branch, if a person's push left it
		// off the pull request's line, would refuse this fix's move.
		if err := w.s.dropCandidateBranches(ctx, sc.OrganizationID, st, gh); err != nil {
			return workflow.Result{}, forgeWait(err)
		}
	} else if existing != "" {
		// A replay: the fix started from the heads it was created with.
		if st.Heads, runBranch, err = w.s.runBaseRefs(ctx, sc.OrganizationID, existing); err != nil {
			return workflow.Result{}, err
		}
	}
	if st.Integration != nil && existing == "" {
		st.Integration.Head = st.Heads[st.Integration.Repo]
		st.Integration.Published = false
		st.Integration.Polls = 0
	}
	// PR feedback fixes, and an open pull request's integration, are checked
	// on a candidate before promotion. One before a pull request opened has
	// none to keep its merge from, unless another repository's is open: the
	// fixer may change that one too. A replay's Run already publishes where
	// it was created to, whatever the pull requests' states are now.
	candidate := st.Integration == nil || !st.Integration.BeforePR
	if existing != "" {
		candidate = runBranch == st.candidateBranch()
	} else if !candidate && len(st.PullRequestIDs) > 0 {
		open, err := w.openPRRepos(ctx, sc, st)
		if err != nil {
			return workflow.Result{}, err
		}
		candidate = len(open) > 0
	}
	if candidate {
		st.Candidate = &Candidate{Round: st.PRFixKey, Branch: st.candidateBranch(), From: maps.Clone(st.Heads)}
	}
	d := directed(st, PhaseFix)
	// An integration fixer is given its brief alone: the pull request
	// comments stay in st.PRFeedback and the people's items stay open, for
	// the fix openPullRequest starts once the integration is promoted.
	prFeedback := st.PRFeedback
	var feedback []string
	if st.Integration != nil {
		prFeedback = []forge.ActionableFeedback{{Repo: st.Integration.Repo, Source: "integration", Body: st.Integration.brief()}}
	} else if feedback, err = w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st); err != nil {
		return workflow.Result{}, err
	}
	cont, err := w.goesOn(ctx, sc, st, PhaseFix)("")
	if err != nil {
		return workflow.Result{}, err
	}
	runID, err := w.phase(ctx, sc, st, PhaseFix, k, func(p *PhaseRun) {
		p.PRFeedback, p.FeedbackItemIDs, p.CountAttempts = prFeedback, feedback, st.Integration == nil
		p.Continue = cont
		d.apply(p)
	})
	if err != nil {
		return workflow.Result{}, err
	}
	if st.Integration != nil {
		st.Integration.RunID = runID
		if st.IntegrationPairs == nil {
			st.IntegrationPairs = map[string]string{}
		}
		st.IntegrationPairs[st.Integration.pair()] = runID
		if err := w.integrationEvent(ctx, sc, st, "integration-started", nil); err != nil {
			return workflow.Result{}, err
		}
	}
	return park("awaitPRFix", st, []string{runID}), nil
}

// writerKey is the creation key of the Run a review-loop writer step
// (implementRun, fix, simplify) creates for phase.
func writerKey(sc workflow.StepContext, st *State, phase string) string {
	// Read, not spent: the step spends its direction once (directed).
	d := st.Directed
	if d != nil && d.Phase != "" && d.Phase != phase {
		d = nil
	}
	switch phase {
	case PhaseImplement:
		if d != nil {
			return conductorKey(sc, st, d, ":implement")
		}
		return key(sc, st, ":implement")
	case PhaseSimplify:
		if d != nil {
			return conductorKey(sc, st, d, ":simplify")
		}
		return testRoundKey(sc, st, ":simplify")
	case PhaseFix:
		switch {
		case d != nil:
			return conductorKey(sc, st, d, ":fix:", st.Iteration)
		case len(st.TestFindingIDs) > 0:
			return key(sc, st, ":fix:", st.Iteration, ":test:", st.TestFixRounds)
		case st.Integration != nil:
			return key(sc, st, ":integration:", st.Integration.RunID, ":fix:", st.Iteration)
		}
		return key(sc, st, ":fix:", st.Iteration)
	}
	if d != nil {
		return conductorKey(sc, st, d)
	}
	return key(sc, st)
}

// writerCandidate recovers this step's persisted writer before live PR routing.
// A new writer starts from the PR heads on an uncharged candidate; settle
// those heads before continuation planning measures the writer's delta.
func (w *steps) writerCandidate(ctx context.Context, sc workflow.StepContext, st *State, phase string) error {
	if st.Candidate != nil {
		return nil
	}
	proposed := *st
	proposed.PRFixKey = max(st.PRFixKey, st.PRIteration) + 1
	proposed.Candidate = &Candidate{Round: proposed.PRFixKey, Branch: st.candidateBranch(), Loop: true, opening: true}
	for _, snapshot := range []*State{st, &proposed} {
		existing, err := w.s.runByKey(ctx, sc.OrganizationID, st.TaskID, writerKey(sc, snapshot, phase))
		if err != nil {
			return err
		}
		if existing == "" {
			continue
		}
		heads, branch, err := w.s.runBaseRefs(ctx, sc.OrganizationID, existing)
		if err != nil {
			return err
		}
		st.Heads = heads
		if branch == proposed.Candidate.Branch {
			st.PRFixKey, st.Candidate = proposed.PRFixKey, proposed.Candidate
			st.Candidate.opening = false
			st.Candidate.From = maps.Clone(heads)
		}
		return nil
	}
	var open bool
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
		open, err = PullRequestOpenOnTx(ctx, tx, st.TaskID, st.taskBranch(st.TaskID))
		return err
	}); err != nil || !open {
		return err
	}
	st.PRFixKey, st.Candidate = proposed.PRFixKey, proposed.Candidate
	return w.openCandidate(ctx, sc, st, writerKey(sc, st, phase))
}

// openCandidate settles where a candidate writerCandidate opened starts:
// a replayed step's Run's heads, or else each open pull request's head as
// GitHub has it (a person may have pushed), with the last round's
// candidate branch and pending resolutions dropped, as prFix does.
func (w *steps) openCandidate(ctx context.Context, sc workflow.StepContext, st *State, k string) error {
	c := st.Candidate
	c.opening = false
	existing, err := w.s.runByKey(ctx, sc.OrganizationID, st.TaskID, k)
	if err != nil {
		return err
	}
	if existing != "" {
		if st.Heads, _, err = w.s.runBaseRefs(ctx, sc.OrganizationID, existing); err != nil {
			return err
		}
		c.From = maps.Clone(st.Heads)
		return nil
	}
	if err := w.s.abandonCandidate(ctx, sc.OrganizationID, st); err != nil {
		return err
	}
	if w.forges == nil {
		c.From = maps.Clone(st.Heads)
		return nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		return err
	}
	if gh != nil {
		if err := w.fromPullRequestHeads(ctx, sc, st, gh, true); err != nil {
			return err
		}
		if err := w.s.dropCandidateBranches(ctx, sc.OrganizationID, st, gh); err != nil {
			return forgeWait(err)
		}
	}
	c.From = maps.Clone(st.Heads)
	return nil
}

// candidateBranch is the delivery's CandidateBranch: beside its task
// branch, which a delivery from before attempts were kept names alone.
// Always under dude/: an adopted source's head branch is a person's, and
// a name beside it could be theirs too (dropCandidateBranches deletes it).
func (st *State) candidateBranch() string {
	if st.Branch != "" && st.Source == nil {
		return st.Branch + candidateSuffix
	}
	return CandidateBranch(st.TaskID, max(st.Attempt, 1))
}

// chargePRFix counts a fix against this round's budget, and refuses it
// when the round's is spent or a pull request it addresses has had its
// organization's fix rounds already; the escalation, and true, then. The
// pull requests' budget is spent only by a fix that lands with commits
// (chargeLandedPRFix).
func (w *steps) chargePRFix(ctx context.Context, sc workflow.StepContext, st *State, total int) (workflow.Result, bool, error) {
	roundSpent := st.RoundFixesSpent()
	st.PRIteration++
	// A workflow from before rounds counted its fixes in PRIteration only.
	st.PRFixKey = max(st.PRFixKey, st.PRIteration-1) + 1
	st.PRFixCharged = &PRFixCharge{Round: st.PRFixKey, Pending: true}
	repos := feedbackRepos(st.PRFeedback)
	if len(repos) == 0 {
		// A fix the conductor started while the pull requests waited, or
		// one for people's feedback in dude: refused if any still open is
		// at its budget.
		var err error
		if repos, err = w.openPRRepos(ctx, sc, st); err != nil {
			return workflow.Result{}, false, err
		}
	}
	var spent []string // pull requests at the organization's budget
	for _, repo := range repos {
		if st.PullRequestFixesSpent(repo, total) {
			spent = append(spent, repo)
		}
	}
	if roundSpent || len(spent) > 0 {
		res, err := w.escalate(ctx, sc, st, "pr_loop_exhausted", map[string]any{"iterations": st.PRIteration,
			"perRound": st.RoundFixLimit(), "total": st.PullRequestFixLimit(total), "spent": db.NonNil(spent)})
		return res, true, err
	}
	return workflow.Result{}, false, nil
}

// currentPRFixCharge recognises rounds that spent their budget at start.
func (st *State) currentPRFixCharge() *PRFixCharge {
	switch {
	case st.PRFixCharged == nil:
		// A nil marker is the old format: that round spent its budget when it started.
		st.PRFixCharged = &PRFixCharge{Round: st.PRFixKey, Repos: feedbackRepos(st.PRFeedback)}
	case st.PRFixCharged.Round != st.PRFixKey:
		// A marker from an earlier round is not evidence that this round spent
		// anything. It is the intermediate format's next round: charge it when
		// commits land, without losing the earlier charge on retry.
		st.PRFixCharged = &PRFixCharge{Round: st.PRFixKey, Pending: true}
	}
	return st.PRFixCharged
}

// chargeLandedPRFix spends the round's fix once against each open PR it changed.
func (w *steps) chargeLandedPRFix(ctx context.Context, sc workflow.StepContext, st *State, changed map[string]string) error {
	if !st.currentPRFixCharge().Pending {
		return nil
	}
	open, err := w.openPRRepos(ctx, sc, st)
	if err != nil {
		return err
	}
	charge := &PRFixCharge{Round: st.PRFixKey, Repos: []string{}}
	for _, repo := range open {
		if _, ok := changed[repo]; !ok {
			continue
		}
		if st.PRFixes == nil {
			st.PRFixes = map[string]int{}
		}
		st.PRFixes[repo]++
		charge.Repos = append(charge.Repos, repo)
	}
	st.PRFixCharged = charge
	return nil
}

// openPRRepos names the repositories of the task's pull requests still open.
func (w *steps) openPRRepos(ctx context.Context, sc workflow.StepContext, st *State) ([]string, error) {
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return nil, err
	}
	var repos []string
	for _, p := range states {
		if p.State != forge.StateMerged && p.State != forge.StateClosed && !slices.Contains(repos, p.Repo) {
			repos = append(repos, p.Repo)
		}
	}
	return repos, nil
}

// prsCoverTheWork says whether one of the task's pull requests is open and
// OpenPullRequests would open no new one (reposToOpen).
func (w *steps) prsCoverTheWork(ctx context.Context, sc workflow.StepContext, st *State) (bool, error) {
	if len(st.PullRequestIDs) == 0 {
		return false, nil
	}
	open, err := w.openPRRepos(ctx, sc, st)
	if err != nil || len(open) == 0 {
		return false, err
	}
	var todo []Repository
	err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		var err error
		if todo, _, _, err = reposToOpen(ctx, tx, st); err != nil {
			return err
		}
		todo, _, err = waitedPastRepos(ctx, tx, st, todo)
		return err
	})
	return err == nil && len(todo) == 0, err
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

// refreshPRWaitHeads starts a review or test the conductor asked for while
// the pull requests waited from GitHub's heads: a person may have pushed.
func (w *steps) refreshPRWaitHeads(ctx context.Context, sc workflow.StepContext, st *State) error {
	// A candidate's heads are ahead of the pull request's until promotion.
	if !st.ActingOnPR || st.Candidate != nil {
		return nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil || gh == nil {
		return err
	}
	return w.fromPullRequestHeads(ctx, sc, st, gh, false)
}

// fromPullRequestHeads starts the fix from each open pull request's head
// as GitHub has it, and gives failing checks the end of their logs.
//
// tails: this step creates a fix Run, so a check read in an earlier step
// has its tail read again (Full is never stored). Without it a check with
// an excerpt is left as it is.
func (w *steps) fromPullRequestHeads(ctx context.Context, sc workflow.StepContext, st *State, gh *forge.GitHub, tails bool) error {
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
	files := 0
	for i, f := range st.PRFeedback {
		for j, c := range f.Checks {
			if slugs[f.Repo] == "" || !readsCheck(c, tails, files) {
				continue
			}
			// A failed read never fails the step: the check goes to the
			// fixer with what was read and why the rest was not.
			read, err := gh.CheckOutput(ctx, slugs[f.Repo], c.RunID, CheckLogFileMax)
			if err != nil {
				// Never err's text: a failed download hop's *url.Error holds
				// GitHub's signed blob URL, a bearer credential.
				status := 0
				if fe := (*forge.Error)(nil); errors.As(err, &fe) {
					status = fe.Status
				}
				slog.Warn("reading a failing check's log failed; the fixer is told why", "check", c.RunID, "repo", slugs[f.Repo],
					"task", st.TaskID, "reason", read.Unread, "status", status, "transient", forge.Transient(err))
			}
			check := &st.PRFeedback[i].Checks[j]
			if check.Log == "" {
				check.Log, check.Unread = read.Cut(checkLogLimit), read.Unread
			} else if read.Tail == "" {
				// The excerpt survives from an earlier step; say why the
				// longer tail could not be had now.
				check.Unread = read.Unread
			}
			// The fix Run's init writes the longer tail into its workspace
			// (phases' checkLogsInit), for the first CheckLogFiles checks.
			if read.Tail != "" && files < CheckLogFiles {
				// The file is the log's end itself: no "…" saying it was cut.
				check.Full = LogEnd(forge.CleanLog(strings.TrimPrefix(read.Tail, "…\n")), CheckLogFileMax)
				files++
			}
		}
	}
	return nil
}

// readsCheck says whether a failing check is read from GitHub now. Full is
// never stored (json:"-"), so a check read in an earlier step has its
// excerpt but not its tail: a step creating a fix Run (tails) reads it
// again, for the first CheckLogFiles checks only. A step that creates no
// fix Run leaves a check with an excerpt alone. A check with an excerpt
// whose log was unreadable is not asked for again; one with neither is.
func readsCheck(c forge.FailingCheck, tails bool, files int) bool {
	if c.RunID == 0 || c.Full != "" {
		return false
	}
	if c.Log == "" {
		return true
	}
	return tails && c.Unread == "" && files < CheckLogFiles
}

// checkLogLimit: how much of a failing check's output a fixer's prompt
// quotes. CheckLogFiles checks' job log tails, CheckLogFileMax bytes each,
// go into its workspace as files.
const (
	checkLogLimit   = 4000
	CheckLogFiles   = 4
	CheckLogFileMax = 64 << 10
)

// CheckLogsOf is runs.check_logs for feedback: each check's Full by its
// check run id (decimal), as WithCheckLogs puts them back.
func CheckLogsOf(feedback []forge.ActionableFeedback) map[string]string {
	out := map[string]string{}
	for _, f := range feedback {
		for _, c := range f.Checks {
			if c.Full != "" && c.RunID != 0 {
				out[strconv.FormatInt(c.RunID, 10)] = c.Full
			}
		}
	}
	return out
}

// WithCheckLogs is a copy of feedback whose checks have their Full from
// logs (CheckLogsOf); feedback is not changed.
func WithCheckLogs(feedback []forge.ActionableFeedback, logs map[string]string) []forge.ActionableFeedback {
	out := slices.Clone(feedback)
	for i := range out {
		out[i].Checks = slices.Clone(out[i].Checks)
		for j := range out[i].Checks {
			if full, ok := logs[strconv.FormatInt(out[i].Checks[j].RunID, 10)]; ok {
				out[i].Checks[j].Full = full
			}
		}
	}
	return out
}

// LogEnd is the end of s in at most limit bytes, from a line's
// start where one is in reach.
func LogEnd(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[len(s)-limit:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < len(cut)-1 {
		cut = cut[i+1:]
	}
	return cut
}

func (w *steps) awaitPRFix(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if pending := settle(st, sc); len(pending) > 0 {
		return park("awaitPRFix", st, pending), nil
	}
	// No pull request left open for the fix to reach, signalled or not: it
	// is dropped, and nothing is opened for it (endedBeforeOpening).
	if res, ended, err := w.endedBeforeOpening(ctx, sc, st); ended || err != nil {
		return res, err
	}
	runID := st.PendingRunIDs[0]
	out, err := w.s.PhaseOutcome(ctx, sc.OrganizationID, runID)
	if err != nil {
		return workflow.Result{}, err
	}
	if !out.Succeeded {
		if st.Integration != nil {
			return w.integrationFailure(ctx, sc, st, causeRunFailed, out.Error)
		}
		return w.escalate(ctx, sc, st, "pr_fix_failed", map[string]any{"runId": runID, "error": out.Error})
	}
	if st.Integration != nil {
		st.PendingRunIDs, st.Heads, st.ChangedPaths = nil, out.advance(st.Heads), withPaths(st.ChangedPaths, out.ChangedPaths)
		// Nothing published to the conflicting repository is no merge: its
		// review and test would judge the head that conflicts. A push to
		// another repository stays on the candidate, never promoted.
		if _, merged := out.Heads[st.Integration.Repo]; !merged {
			return w.integrationFailure(ctx, sc, st, causeNoMerge, reasonNoMerge)
		}
		st.HeadRunID = runID
		st.Integration.Published = true
		st.Integration.MergeSHA = st.Heads[st.Integration.Repo]
		st.Iteration = 0
		st.ActingOnPR = true
		return workflow.Result{Next: "review", State: st}, nil
	}
	st.HeadRunID, st.PendingRunIDs, st.Heads = runID, nil, out.advance(st.Heads)
	st.ActingOnPR = false
	if len(out.Heads) > 0 {
		// The round's fix has commits: it spends the pull requests' budget,
		// even if a person's push later refuses its promotion.
		if err := w.chargeLandedPRFix(ctx, sc, st, out.Heads); err != nil {
			return workflow.Result{}, err
		}
	}
	if len(out.Heads) == 0 || st.Candidate == nil {
		// Nothing changed (or a fix from before candidates, already on the
		// pull request): nothing to check, nothing to move.
		return w.candidateMoved(ctx, sc, st, out.Heads)
	}
	// On the candidate branch: reviewed over the fix alone, then tested on
	// every criterion, before the pull request shows it. The review loop
	// and the test's fix rounds are this round's, bounded as before the
	// pull request; the round counted once against PRIteration, in prFix,
	// and against PRFixes, above.
	st.Candidate.Base = map[string]string{}
	for repo := range out.Heads {
		if base := cmp.Or(st.Candidate.From[repo], out.Bases[repo]); base != "" {
			st.Candidate.Base[repo] = base
		}
	}
	st.ChangedPaths, st.Iteration, st.TestFixRounds, st.TestFindingIDs = out.ChangedPaths, 0, 0, nil
	// Mechanics, whoever decides: the conductor chose the fix; checking it
	// before the pull request shows it is not a choice (checkingCandidate).
	return workflow.Result{Next: "review", State: st}, nil
}

// checkingCandidate says the delivery is checking a pull request's fix on
// its candidate branch: its review loop and test go on by the policy's
// rules whoever decides, and a bound reached escalates to a person.
func (st *State) checkingCandidate() bool { return st.Candidate != nil && len(st.Candidate.Base) > 0 }

// ReasonPullRequestMoved: a pull request's branch moved while a fix for
// it was checked on the candidate branch; the candidate would overwrite
// it, so it is not moved.
const ReasonPullRequestMoved = "pull_request_moved"

// promoteCandidate moves each pull request's branch to the candidate's
// head, once its review and test passed (or a person settled the test):
// a fast-forward from the head the fix started from, never a force. A
// branch GitHub only merged the base into meanwhile gets the candidate
// merged in on top, and that merge is the pull request's head. A branch
// someone moved otherwise is not overwritten: a person decides.
func (w *steps) promoteCandidate(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	c := st.Candidate
	if c == nil {
		return waitForPR(st), nil
	}
	// Nor to a pull request no longer open.
	if res, ended, err := w.endedBeforeOpening(ctx, sc, st); ended || err != nil {
		return res, err
	}
	// Never unreviewed at the pull request: a head a conductor's simplifier
	// or fix left after the last review goes back to one.
	if unreviewed, err := w.s.unreviewedHeads(ctx, sc.OrganizationID, st, c); err != nil {
		return workflow.Result{}, err
	} else if len(unreviewed) > 0 {
		return workflow.Result{Next: "review", State: st}, nil
	}
	// Nor past a question its reviewers asked that nobody answered.
	if res, held, err := w.holdForQuestions(ctx, sc, st, "promoteCandidate"); held || err != nil {
		return res, err
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		return workflow.Result{}, err
	}
	if gh == nil {
		return workflow.Result{}, fmt.Errorf("no forge credential to move the pull request's branch with")
	}
	slugs, err := w.s.writableSlugs(ctx, sc.OrganizationID, st.TaskID)
	if err != nil {
		return workflow.Result{}, err
	}
	// Every branch read before any moves: one someone pushed to refuses
	// the promotion with nothing moved. One at the candidate's head was
	// moved by an earlier run of this step.
	var repos []string
	at, moved := map[string]string{}, map[string]string{}
	for _, repo := range slices.Sorted(maps.Keys(st.Heads)) {
		if st.Heads[repo] == c.From[repo] || slugs[repo] == "" {
			continue
		}
		if at[repo], err = gh.BranchSHA(ctx, slugs[repo], st.Branch); err != nil {
			return workflow.Result{}, forgeWait(err)
		}
		if at[repo] == st.Heads[repo] {
			moved[repo] = at[repo]
		} else {
			repos = append(repos, repo)
		}
	}
	bases, err := w.s.baseBranches(ctx, sc.OrganizationID, st)
	if err != nil {
		return workflow.Result{}, err
	}
	// The candidate's head, merged in by this promotion: adopted after a
	// lost answer only when recorded before POST /merges was sent.
	attempted := func(repo string) func() (bool, error) {
		return func() (bool, error) { return w.s.fixMergeAttempted(ctx, sc.OrganizationID, st, repo, st.Heads[repo]) }
	}
	for _, repo := range repos {
		if at[repo] == c.From[repo] {
			continue
		}
		// Moved only by GitHub's merges of the base: the candidate is
		// merged in on top (advance.Existing). Anything else a person decides.
		v, _, err := advance.Admit(ctx, gh, slugs[repo], bases[repo], c.From[repo], at[repo], st.Heads[repo], attempted(repo))
		if err != nil {
			return workflow.Result{}, forgeWait(err)
		}
		if v == advance.Refuse {
			return w.candidateRefused(ctx, sc, st, repo, at[repo], moved, nil)
		}
	}
	for _, repo := range repos {
		to := st.Heads[repo]
		// A branch never on the forge here is created (a repository the
		// fix changed first); one that was is a person's deletion.
		fresh := false
		if at[repo] == "" {
			if fresh, err = w.s.neverPublished(ctx, sc.OrganizationID, st, repo); err != nil {
				return workflow.Result{}, err
			}
		}
		if fresh {
			if err = w.s.branchCreatingTx(ctx, sc.OrganizationID, st, repo); err != nil {
				return workflow.Result{}, err
			}
			// Only the create is sent: any request failing after the
			// record makes a replay read the branch as deleted.
			err = gh.CreateBranch(ctx, slugs[repo], st.Branch, to)
			if forge.Transient(err) {
				// GitHub may have created it before failing: at to, it did.
				if found, rerr := gh.BranchSHA(ctx, slugs[repo], st.Branch); rerr == nil && found == to {
					err = nil
				}
			}
		} else {
			var adv advance.Result
			adv, err = advance.Existing(ctx, gh, slugs[repo], st.Branch, bases[repo], c.From[repo], to, attempted(repo), func() error {
				return w.s.fixMergeAttemptTx(ctx, sc.OrganizationID, st, repo, to)
			})
			if err == nil {
				to = adv.Head
			}
		}
		var fe *forge.Error
		var conflict *advance.ErrBaseMergeConflict
		var unsafe *advance.ErrUnsafeMerge
		if errors.As(err, &conflict) || errors.As(err, &unsafe) || errors.As(err, &fe) && (fe.Status == 422 || fe.NotFound()) {
			// Someone pushed, created or deleted the branch between the read
			// and the move, or the candidate would not merge over the base.
			found, _ := gh.BranchSHA(ctx, slugs[repo], st.Branch)
			return w.candidateRefused(ctx, sc, st, repo, found, moved, err)
		}
		if err != nil {
			return workflow.Result{}, forgeWait(err)
		}
		moved[repo] = to
	}
	if err := w.s.CandidateMovedTx(ctx, sc.OrganizationID, st, moved); err != nil {
		return workflow.Result{}, err
	}
	// A merge of the candidate over the base is the head from here on.
	maps.Copy(st.Heads, moved)
	for repo := range moved {
		// Best effort: on the pull request's line now, it is clutter.
		_ = gh.DeleteBranch(ctx, slugs[repo], c.Branch)
	}
	return w.candidateMoved(ctx, sc, st, moved)
}

// forgeWait is err as a workflow.Wait when GitHub may answer later
// (rate-limited, down, unreachable): the step runs again after GitHub's
// Retry-After, at least forgeWaitMin, its attempt not counted.
func forgeWait(err error) error {
	if !forge.Transient(err) {
		return err
	}
	return workflow.Wait{After: max(forge.RetryAfter(err), forgeWaitMin), Why: err.Error()}
}

const forgeWaitMin = 5 * time.Second

// candidateRefused stops for a person: the pull request's branch in repo
// is at found, not where the fix started, and is not moved. moved are the
// branches a forge race let move before it (by repository, to the
// candidate's head): recorded, their reviewers asked again and their
// descriptions brought up to them. The delivery goes on from what each
// branch holds; the candidate's resolutions hold only in the moved ones
// (CandidateMovedTx), and are reopened in the rest. cause, when set, is
// GitHub's refusal of the move or of merging the candidate in.
func (w *steps) candidateRefused(ctx context.Context, sc workflow.StepContext, st *State, repo, found string, moved map[string]string, cause error) (workflow.Result, error) {
	c := st.Candidate
	detail := map[string]any{"repo": repo, "expected": c.From[repo], "found": found, "candidate": st.Heads[repo],
		"branch": c.Branch}
	if cause != nil {
		detail["error"] = cause.Error()
	}
	if n, err := w.s.pullRequestNumber(ctx, sc.OrganizationID, st, repo); err != nil {
		return workflow.Result{}, err
	} else if n > 0 {
		detail["number"] = n
	}
	// The candidate goes no further: its resolutions hold where it moved.
	if err := w.s.CandidateMovedTx(ctx, sc.OrganizationID, st, moved); err != nil {
		return workflow.Result{}, err
	}
	// A loop candidate's findings resolved only on its discarded heads are
	// obligations again, and its retry is the writer step it opened with.
	retryStep := "fix"
	if c.Loop {
		var phase string
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
			phase, err = discardLoopCandidateTx(ctx, tx, st.TaskID, sc.WorkflowRunID, st.Attempt, c.Round, slices.Sorted(maps.Keys(moved)))
			return err
		}); err != nil {
			return workflow.Result{}, err
		}
		switch phase {
		case PhaseImplement:
			retryStep = "implementRun"
		case PhaseSimplify:
			retryStep = "simplify"
		}
		st.SimplifyBase = nil
	}
	// A person's push refused an integration candidate; close its live
	// resolving-conflicts activity before escalating the moved pull request.
	if st.Integration != nil {
		if err := w.integrationEvent(ctx, sc, st, "integration-refused", map[string]any{"cause": ReasonPullRequestMoved}); err != nil {
			return workflow.Result{}, err
		}
	}
	// What the pull request shows is what the delivery stands on now; a
	// retry fixes the same feedback again from there.
	st.Heads = maps.Clone(c.From)
	maps.Copy(st.Heads, moved)
	if found != "" {
		st.Heads[repo] = found
	}
	if len(moved) > 0 {
		gh, err := w.forges.For(ctx, sc.OrganizationID)
		if err != nil {
			return workflow.Result{}, err
		}
		if err := w.s.RerequestReviews(ctx, sc.OrganizationID, st, gh, moved); err != nil {
			return workflow.Result{}, forgeWait(err)
		}
		w.s.refreshBodies(ctx, sc.OrganizationID, st, w.forges, slices.Sorted(maps.Keys(moved)), true, true)
	}
	st.Candidate = nil
	res, err := w.escalate(ctx, sc, st, ReasonPullRequestMoved, detail)
	if err == nil && c.Loop {
		// A loop's candidate fixed no pull request feedback: a retry is the
		// loop's writer again, on a candidate from what the branch holds now.
		st.Escalation.Step = retryStep
	}
	return res, err
}

// candidateMoved is what follows a fix reaching the pull requests (heads,
// by repository, those it moved): reviewers asked again, a pull request
// opened for a repository newly changed, the bodies' review and
// verification brought up to the commit they show, and back to waiting.
func (w *steps) candidateMoved(ctx context.Context, sc workflow.StepContext, st *State, heads map[string]string) (workflow.Result, error) {
	promoted := len(heads) > 0 && st.Candidate != nil
	loop := st.Candidate != nil && st.Candidate.Loop
	st.Candidate = nil
	if st.Integration == nil && !loop {
		// An integration's comments are the fix's after it (openPullRequest);
		// a loop's candidate fixed none.
		st.PRFeedback = nil
	}
	if len(heads) > 0 && !promoted {
		// The adopted pull request's verdict comment, stale past a fix from
		// before candidates, pushed unchecked. A promoted one's is refreshPromoted's.
		w.s.CommentVerdict(ctx, sc.OrganizationID, st, w.forges)
	}
	// Who reviewed an open pull request's older head is asked to look at
	// the fix. Before opening any new pull request, so that nothing that
	// opening parks on leaves the open ones unasked; a replay of this step
	// asks nobody twice (RerequestReviews).
	var gh *forge.GitHub
	if len(heads) > 0 {
		var err error
		if gh, err = w.forges.For(ctx, sc.OrganizationID); err != nil {
			return workflow.Result{}, err
		}
		if err := w.s.RerequestReviews(ctx, sc.OrganizationID, st, gh, heads); err != nil {
			return workflow.Result{}, err
		}
	}
	// A loop's candidate goes on as its loop would have (beforePR): the
	// pull request step opens one for a repository newly changed, through
	// the Open or Draft where one is due.
	next := ""
	if loop {
		next = "beforePR"
	} else {
		// The fix may have changed a repository no pull request is open for
		// yet: that one gets its own, alongside the others.
		prIDs, err := w.s.OpenPullRequests(ctx, sc.OrganizationID, st, w.forges)
		if errors.As(err, new(forge.OwnerAuthorizationError)) {
			return w.awaitOwnerAuthorization(ctx, sc, st, err)
		}
		if err != nil {
			return workflow.Result{}, err
		}
		st.PullRequestIDs = prIDs
	}
	// The fast-forward updated the PRs; they are back with the reviewers.
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "pull request updated"); err != nil {
		return workflow.Result{}, err
	}
	if promoted {
		// What they say about review and test is about the commit they
		// show now: in a step of its own, so that retrying it never moves
		// a branch or records the move again.
		repos, err := w.promotedRefreshRepos(ctx, sc, st, heads)
		if err != nil {
			return workflow.Result{}, err
		}
		st.PromotedRefresh = &PromotedRefresh{Repos: repos, Next: next}
		return workflow.Result{Next: "refreshPromoted", State: st}, nil
	}
	if loop {
		return w.beforePR(ctx, sc, st, "")
	}
	return w.prFixLanded(ctx, sc, st)
}

// prFixLanded goes on from a pull request fix that reached the pull
// requests, or changed nothing: what happened to them while the fix ran
// was signalled to a step not listening for it (one may have been merged
// or closed, or be approved with green checks already), weighed now
// rather than waited for.
func (w *steps) prFixLanded(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, error) {
	if st.Integration != nil {
		// A promoted integration: whether the pull request merges now is
		// read only once it shows the merge (openPullRequest).
		return workflow.Result{Next: "openPullRequest", State: st}, nil
	}
	res, finished, err := w.pullRequestEnded(ctx, sc, st)
	if err != nil || finished {
		return res, err
	}
	// People's feedback the fix left unanswered is open again, and no
	// signal will come for it: another fix, within MaxPRFixIterations.
	if fix, started, err := w.fixOpenFeedback(ctx, sc, st, feedbackAfterFix); err != nil || started {
		return fix, err
	}
	return res, nil
}

// fixOpenFeedback starts a pull request fix when people's feedback items
// are open (started), with when's feedbackOpenLine as its reason.
func (w *steps) fixOpenFeedback(ctx context.Context, sc workflow.StepContext, st *State, when string) (res workflow.Result, started bool, err error) {
	open, err := w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st)
	if err != nil || len(open) == 0 {
		return workflow.Result{}, false, err
	}
	res, err = w.next(ctx, sc, st, PointPRFeedback, "prFix", feedbackOpenLine(when, len(open)))
	return res, true, err
}

// nothingToAdd follows the implementer's outcome when the work it continues
// is reviewed as it is.
var nothingToAdd = render(mustTemplate("wakes/facts/nothing-to-add"), nil)

// When people's feedback items are open, for feedbackOpenLine.
const (
	feedbackOpen             = "open"
	feedbackAfterFix         = "after-fix"
	feedbackAfterIntegration = "after-integration"
	feedbackBeforePR         = "before-pr"
	feedbackReadyToRead      = "ready-to-read"
)

// feedbackOpenLine is the conductor's fact on n of people's feedback items
// open, when.
func feedbackOpenLine(when string, n int) string {
	return render(feedbackOpenTmpl, struct {
		When string
		N    int
	}{when, n})
}

// promotedRefreshRepos are the repositories a full promotion refreshes:
// every open pull request's, since Review and Verification are task-level
// and the promoted verdict covers every head; an adopted one's verdict
// comment, only the moved ones. Listed explicitly, never nil for "all".
func (w *steps) promotedRefreshRepos(ctx context.Context, sc workflow.StepContext, st *State, heads map[string]string) ([]string, error) {
	if st.adoptedPR() {
		return slices.Sorted(maps.Keys(heads)), nil
	}
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return nil, err
	}
	repos := []string{}
	for _, p := range states {
		if (p.State == forge.StateOpen || p.State == forge.StateDraft) && !slices.Contains(repos, p.Repo) {
			repos = append(repos, p.Repo)
		}
	}
	slices.Sort(repos)
	return repos, nil
}

// refreshPromoted refreshes, after a promotion, the pull requests (or the
// adopted one's verdict comment) of the repositories st.PromotedRefresh
// still names. Those that failed are tried again after GitHub's
// Retry-After, at least forgeWaitMin, up to promotedRefreshRetries times;
// the last failure is recorded and the delivery goes on as candidateMoved
// does. The settled refresh is committed before pullRequestEnded runs, so
// that step failing never replays an attempt already counted.
func (w *steps) refreshPromoted(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if pr := st.PromotedRefresh; pr != nil {
		if len(pr.Repos) > 0 {
			pr.Tries++
			last := pr.Tries > promotedRefreshRetries
			failed, after := w.s.refreshPromoted(ctx, sc.OrganizationID, st, w.forges, pr.Repos, last)
			if len(failed) > 0 && !last {
				pr.Repos = failed
				return workflow.Result{Next: "refreshPromoted", State: st, SleepUntil: time.Now().Add(max(after, forgeWaitMin))}, nil
			}
			pr.Repos = nil
			return workflow.Result{Next: "refreshPromoted", State: st}, nil
		}
		st.PromotedRefresh = nil
		if pr.Next == "beforePR" {
			return w.beforePR(ctx, sc, st, "")
		}
	}
	return w.prFixLanded(ctx, sc, st)
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
	if st.conducted() {
		// The conductor hears each one end once, however often this runs.
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			for _, p := range states {
				if err := CloseOutTx(ctx, tx, sc.OrganizationID, st.TaskID, p.Repo, p.Number, p.State); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return workflow.Result{}, true, err
		}
	}
	c := countStates(states)
	// A close the person already chose to wait past is not asked about
	// again, however often this runs: only one beyond those is.
	grown := st.closedBeyondWait(states)
	waitAgain := waitForPR(st)
	switch {
	case len(states) == 0:
		return workflow.Result{}, true, fmt.Errorf("no pull requests recorded for %s", st.TaskID)
	case !grown && c.open > 0:
		// The rest are still open: what is merged is taken, so whether the
		// task is ready depends on those.
		return waitAgain, false, w.weighReadiness(ctx, sc, st, states)
	case !grown && c.merged > 0:
		return workflow.Result{}, true, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "done", "pull requests merged")
	case c.open == 0 && c.merged == 0:
		// Closed without merging is someone deciding not to take the
		// change: an abort of the task, not a failure of it.
		return workflow.Result{}, true, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "aborted", "pull request closed without merging")
	}
	// Part taken, part refused, or part still open: a person decides.
	res, err := w.escalate(ctx, sc, st, "pull_request_closed", map[string]any{"merged": c.merged, "closed": c.closed, "open": c.open})
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
	// The move, its event and the conductor's Chat notice in one
	// transaction: one lost to a retry would never be written again (the
	// status has moved).
	return w.s.moveReadiness(ctx, sc.OrganizationID, st, states, open > 0 && ready, open)
}

// escalate stops and asks for a person, and waits for their decision
// (decide). Nothing goes on by itself: no agent works, and no timer
// resumes it.
func (w *steps) escalate(ctx context.Context, sc workflow.StepContext, st *State, reason string, detail any) (workflow.Result, error) {
	st.Escalation = &Escalation{Reason: reason, Detail: detail, Step: RetryStep[sc.Step]}
	switch reason {
	case "pull_request_conflict":
		if st.Integration != nil {
			st.Escalation.Step = "prFix"
		}
	case "review_failed":
		// Its retry is the failed reviewers again, not a fix.
		st.Escalation.Step = "review"
	case ReasonTestFailedCriteria, ReasonNotConverging:
		// One more fix round; a blocked test's retry is the tester again.
		st.Escalation.Step = "fix"
	case ReasonChoiceUnresolved:
		// The opening, which reads the choice again.
		st.Escalation.Step = "openPullRequest"
	}
	if ids := st.Escalation.RunIDs(); len(ids) > 0 && strings.HasSuffix(reason, "_failed") {
		// A Run failed, and lux keeps it: the step waiting on it can wait on
		// it again, resumed. Every one, for a round of several.
		kept := true
		for _, id := range ids {
			k, err := w.s.Kept(ctx, sc.OrganizationID, id)
			if err != nil {
				return workflow.Result{}, err
			}
			kept = kept && k
		}
		if kept {
			st.Escalation.At = sc.Step
		}
	}
	st.PendingRunIDs = nil
	if err := w.s.SetTaskStatusAndEmit(ctx, sc.OrganizationID, st, "awaiting_input", reason, EvQuestionAsked,
		map[string]any{"kind": "escalation", "reason": reason, "detail": detail, "actions": st.Escalation.Actions()}); err != nil {
		return workflow.Result{}, err
	}
	if st.conducted() {
		// The person still decides; the conductor is told, to explain and
		// propose in Chat. Once per escalation, however often replayed.
		st.Decision, st.Directed = nil, nil
		st.Escalations++
		line := escalatedLine(reason, st.Escalation.Actions(), st.Escalation.findingIDs()...)
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
	"adopt":          "adopt",
	// A review loop that gave up tries again with another round of fixes.
	"awaitReview": "fix",
	"awaitFix":    "fix",
	"awaitTest":   "test",
	"testVerdict": "test",
	"prFix":       "prFix",
	"awaitPRFix":  "prFix",
	// The fix again, from what the pull request's branch holds now.
	"promoteCandidate": "prFix",
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
	if e.Reason == ReasonChecksUnverified {
		if d.Action == "stop" {
			return workflow.Result{State: st}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "aborted", "a person stopped it")
		}
		return workflow.Result{Next: e.At, State: st, AwaitSignals: []string{SignalPhaseFinished}}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "a person decided: "+d.Action)
	}
	switch d.Action {
	case "stop":
		// Kept, so the task can be picked back up where it stopped.
		e.Decided = nil
		st.Stopped = e
		// The candidate is kept, so a recovery may still promote the checked
		// work; its resolutions are reopened now and stay open whatever it
		// does, for the next pull request fix round (prFixLanded).
		if st.Candidate != nil {
			if err := w.s.abandonCandidate(ctx, sc.OrganizationID, st); err != nil {
				return workflow.Result{}, err
			}
		}
		return workflow.Result{State: st}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "aborted", "a person stopped it")
	case "done":
		return workflow.Result{State: st}, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "done", "a person took what was merged")
	case "wait":
		if e.Reason == "pull_request_closed" {
			// What the person waited past is not asked about again: not by
			// the close's own pr.feedback still waiting for awaitPullRequest,
			// nor by a later round's pullRequestEnded. Only the closes they
			// were asked about: one since then is asked about with the rest.
			states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
			if err != nil {
				return workflow.Result{}, err
			}
			if countStates(states).closed == e.counts().closed {
				st.waitPastClosed(states)
			}
		}
		if e.Reason == ReasonPullRequestMoved && st.Integration == nil {
			// The fix stays on its candidate branch: not the pull request's.
			// An integration's comments were never its fixer's: kept.
			st.PRFeedback = nil
		}
		// Items the refused candidate reopened, or that waited out an
		// integration, have no signal coming: a fix, as prFixLanded does.
		handOpen := e.Reason == ReasonPullRequestMoved
		if st.Integration != nil {
			beforePR := st.Integration.BeforePR
			st.Integration = nil
			st.ActingOnPR = false
			if beforePR {
				return workflow.Result{Next: "openPullRequest", State: st}, nil
			}
			if len(st.PRFeedback) > 0 {
				return workflow.Result{Next: "prFix", State: st}, nil
			}
			handOpen = true
		}
		if len(st.PRFeedback) > 0 && (e.Reason == "pull_request_conflict" || e.Reason == "ci_stuck") {
			// Feedback arrived with the conflict: fixed now it is resolved.
			return workflow.Result{Next: "prFix", State: st}, nil
		}
		if handOpen && len(st.PullRequestIDs) > 0 {
			if res, started, err := w.fixOpenFeedback(ctx, sc, st, feedbackOpen); err != nil || started {
				return res, err
			}
		}
		return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "a person is waiting on the rest")
	}
	// Going on: working again, whichever step takes it up.
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "a person decided: "+d.Action); err != nil {
		return workflow.Result{}, err
	}
	if e.Reason == ReasonNotConverging {
		st.ConvergenceFrom = d.At
		switch d.Action {
		case "accept":
			if err := w.s.AcceptFindingIDs(ctx, sc.OrganizationID, st, e.findingIDs(), d.Note); err != nil {
				return workflow.Result{}, err
			}
			return workflow.Result{Next: "reviewExit", State: st}, nil
		case "amend":
			if err := w.s.AmendFindingIDs(ctx, sc.OrganizationID, st, e.findingIDs(), d.Note); err != nil {
				return workflow.Result{}, err
			}
		}
		return retry(st, "fix", e), nil
	}
	switch d.Action {
	case "accept", ActionWaive:
		if e.Reason == ReasonTestFailedCriteria || e.Reason == ReasonTestBlocked {
			// The person's waiver or acceptance is on the verdict (taken
			// with the decision): the gate weighs it again.
			return workflow.Result{Next: "testVerdict", State: st}, nil
		}
		if e.Reason == "review_failed" {
			// The round goes on without the reviewers that failed: its
			// findings are what the others reported.
			st.ReviewRunIDs = nil
			if st.Candidate != nil {
				st.Candidate.Accepted = maps.Clone(st.Heads)
			}
			return workflow.Result{Next: "awaitReview", State: st}, nil
		}
		// The findings it was stuck on ship as they are: a person's call.
		if err := w.s.AcceptFindings(ctx, sc.OrganizationID, st); err != nil {
			return workflow.Result{}, err
		}
		// The people's feedback it was stuck on is resolved by that person.
		if ids := e.feedbackIDs(); len(ids) > 0 {
			person := d.Person
			if person == "" {
				person = d.AnsweredBy
			}
			if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
				return AcceptFeedbackTx(ctx, tx, sc.OrganizationID, st.TaskID, person, ids)
			}); err != nil {
				return workflow.Result{}, err
			}
		}
		return workflow.Result{Next: st.afterReview(), State: st}, nil
	}
	if d.Action == "resume" {
		// The Run that failed was taken back up with the decision (the API
		// does it, where it can say if it no longer can): the step that
		// waited on it waits on it again.
		return park(e.At, st, e.RunIDs()), nil
	}
	return retry(st, e.Step, e), nil
}

// retry runs a step that stopped again, afresh — new Runs, a new budget
// for the loop it gave up on. e is the escalation it stopped at; nil for
// a step a person aborted.
func retry(st *State, step string, e *Escalation) workflow.Result {
	st.Retries++
	switch step {
	case "review":
		if e != nil && e.Reason == "review_failed" {
			st.RetryCategories = e.categories()
		}
	case "simplify":
		// A refused loop that opened with its simplifier: the loop's budget anew.
		st.Iteration = 0
	case "fix":
		// A new budget for the loop: its rounds, and each finding's fixes.
		st.Iteration = 0
		if e != nil && (e.Reason == "stuck" || e.Reason == ReasonNotConverging) {
			st.ExtraFixAttempts += st.Policy.MaxAttemptsPerFinding
		}
		if e != nil && e.Reason == ReasonTestFailedCriteria {
			// One more round past the bound, with the test's findings.
			st.TestFixRounds++
			st.TestFindingIDs = e.findingIDs()
			st.RetestDue = true
		}
	case "prFix":
		if st.Integration != nil {
			// One more integration Run: it spent no fix budget to give back.
			break
		}
		// The same fix again, not the next: what the stopped one counted
		// is given back — the round, and the pull requests' budget only if
		// its fix landed (a pull_request_moved; a failed fix spent none).
		// A budget spent is a new one.
		st.PRIteration--
		if c := st.currentPRFixCharge(); !c.Pending {
			for _, repo := range c.Repos {
				if st.PRFixes[repo] > 0 {
					st.PRFixes[repo]--
				}
			}
			st.PRFixCharged = nil
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
	e, halted := st.Stopped, st.Halt
	st.Recover, st.Stopped, st.Halt = nil, nil, nil
	if rc.Action == "resume" {
		if len(st.PendingRunIDs) > 0 {
			return park(rc.At, st, st.PendingRunIDs), nil
		}
		// Stopped where no phase Run was at work (a decision, the pull
		// request wait): the step it was at runs again, and waits again.
		return workflow.Result{Next: rc.At, State: st}, nil
	}
	if e == nil && halted != nil && halted.At != "" && rc.Step == halted.At {
		// Nothing of the step's own to run again: it waits again, for its
		// decision or its pull requests, with no budget given back.
		st.PendingRunIDs = nil
		return workflow.Result{Next: rc.Step, State: st}, nil
	}
	return retry(st, rc.Step, e), nil
}

// waitForPR waits on the task's pull requests: what the forge says of
// them, a person's decision, or a person's feedback in dude.
func waitForPR(st *State) workflow.Result {
	if len(st.PendingConflicts) > 0 || st.handingOver {
		return workflow.Result{Next: "awaitPullRequest", State: st}
	}
	return workflow.Result{Next: "awaitPullRequest", State: st,
		AwaitSignals: []string{SignalPRFeedback, SignalHumanDecision, SignalHumanFeedback}}
}

// awaitRead waits on a person reading work that opened no pull request:
// their feedback takes it up again; marking it done ends it.
func awaitRead(st *State) workflow.Result {
	return workflow.Result{Next: "awaitRead", State: st, AwaitSignals: []string{SignalHumanFeedback}}
}

// awaitRead (the step): feedback on work ready to read goes to a fixer,
// with a fresh loop budget as a person's retry has; a signal whose items
// are handled already waits again.
func (w *steps) awaitRead(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	open, err := w.s.OpenFeedbackIDs(ctx, sc.OrganizationID, st)
	if err != nil || len(open) == 0 {
		return awaitRead(st), err
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", "a person gave feedback on it"); err != nil {
		return workflow.Result{}, err
	}
	st.Retries++
	st.Iteration, st.PendingRunIDs = 0, nil
	return w.next(ctx, sc, st, PointReviewed, "fix", feedbackOpenLine(feedbackReadyToRead, len(open)))
}

// humanFeedback says whether the signals carry a person's feedback.
func humanFeedback(sc workflow.StepContext) bool {
	return slices.ContainsFunc(sc.Signals, func(s workflow.Signal) bool { return s.Name == SignalHumanFeedback })
}

// escalatedLine tells the conductor the delivery escalated to a person for
// reason, with the actions the person has.
func escalatedLine(reason string, actions []string, findingIDs ...string) string {
	return render(escalatedTmpl, struct {
		Reason                        string
		Actions, FindingIDs           []string
		Verdict, Stuck, NotConverging bool
	}{strings.ReplaceAll(reason, "_", " "), actions, findingIDs,
		reason == ReasonTestFailedCriteria || reason == ReasonTestBlocked,
		reason == "stuck", reason == ReasonNotConverging})
}

// integrationReviewBrief is what a reviewer of an integration merge (sha)
// is given as feedback.
func integrationReviewBrief(sha string) string { return render(integrationReviewTmpl, sha) }
