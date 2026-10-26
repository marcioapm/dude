// Package delivery is the delivery workflow: implement → review ⟲ fix →
// simplify → [test] → pull request ⟲ fix → done.
//
// This file is its policy: what blocks, what runs, and when to stop trying.
//
// The reviewer reports findings; it does not decide whether the work may
// progress (plan §11.2). A reviewer that could also decide could talk itself
// into shipping, and a policy that lived in a prompt could not be audited or
// changed without another model call.
//
// Every loop terminates on a bound declared here. None terminates on a model
// deciding it is finished.
package delivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
)

type ReviewerRule struct {
	Category string `json:"category"`
	// Run this reviewer only when the diff touches a matching path: a security
	// reviewer on a CSS change is a tax on every task.
	WhenPathsMatch []string `json:"whenPathsMatch,omitempty"`
}

type Policy struct {
	// Severities that stop a task from progressing.
	BlockingSeverities   []string       `json:"blockingSeverities"`
	RequiredReviewers    []string       `json:"requiredReviewers"`
	ConditionalReviewers []ReviewerRule `json:"conditionalReviewers"`
	// Run on every review round, a later round whose own change is
	// non-product included.
	AlwaysReviewers []string `json:"alwaysReviewers"`
	// Globs of paths that are not the product (CI, docs). A later review
	// round whose own change touches only these runs no required reviewer
	// beyond AlwaysReviewers and the reviewers of its findings; a change
	// of these alone since a passing test round starts no tester
	// (carryForward). Matched against in-repo paths only; to make a whole
	// repository non-product, list its paths.
	NonProductPaths []string `json:"nonProductPaths"`
	// Review → fix cycles before escalating to a person. The bound exists
	// because a loop that cannot converge will spend a budget finding that out.
	MaxReviewIterations int `json:"maxReviewIterations"`
	// Fix attempts one finding may survive before it is escalated on its own.
	// Tighter than the loop bound on purpose: a finding the fixer failed twice
	// is one it misunderstood, and a third try is a third variation of the
	// same wrong change.
	MaxAttemptsPerFinding int `json:"maxAttemptsPerFinding"`
	// Consecutive explicit still verdicts on a blocking criterion; 0 disables.
	MaxStillRounds int `json:"maxStillRounds"`
	// Consecutive fix rounds replacing fixed blockers with caused-by-fix blockers; 0 disables.
	MaxRegressionRounds int  `json:"maxRegressionRounds"`
	Simplify            bool `json:"simplify"`
	// Drive a browser to demonstrate the change (plan §12).
	Test bool `json:"test"`
	// PR fix rounds before escalating. Separate from the review bound: a person
	// asking for changes is not the same failure as an agent finding them.
	MaxPRFixIterations int `json:"maxPrFixIterations"`
	// Minutes an agent waiting on a person stays live before it is parked
	// (stopped, its conversation kept, resumed by the answer). Long enough
	// for someone at their desk to answer while it is still running.
	ParkAfterMinutes int `json:"parkAfterMinutes"`
	// Minutes an agent may be quiet mid-turn — saying nothing, running no
	// tool, waiting on nobody — before it is nudged; as long again after the
	// nudge and it is parked for a person. 0: never.
	IdleNudgeMinutes int `json:"idleNudgeMinutes"`
	// Minutes a task's conductor stays running after its turn ends, so a
	// person still at their desk gets a live reply; past it, it is parked
	// and their next message resumes it.
	ConductorWarmMinutes int `json:"conductorWarmMinutes"`
	// The most a conductor may publish at once, against the task branch's
	// head: changed lines (additions and deletions) and files. Past
	// either, the publish is refused and the work delegated.
	ConductorEditLines int `json:"conductorEditLines"`
	ConductorEditFiles int `json:"conductorEditFiles"`
	// A reviewer's or tester's next round takes over its previous round's
	// agent, conversation and workspace (continueOrCreateTx), rather than
	// starting a fresh one.
	ResumeRounds bool `json:"resumeRounds"`
	// What Deliver does once review, simplify and the test pass and no pull
	// request is open yet: PullRequestOpen, PullRequestDraft or
	// PullRequestAsk. A project's setting only (ProjectOnlyPolicyKeys), and
	// a task's own over it.
	PullRequest string `json:"pullRequest"`
}

// The values of Policy.PullRequest.
const (
	PullRequestOpen  = "open"
	PullRequestDraft = "draft"
	PullRequestAsk   = "ask"
)

// IsPullRequestChoice says whether v is one of Policy.PullRequest's values.
func IsPullRequestChoice(v string) bool {
	return v == PullRequestOpen || v == PullRequestDraft || v == PullRequestAsk
}

// ProjectOnlyPolicyKeys are the keys an organisation's policy never sets:
// ResolvePolicy passes over them in its organisation layer.
var ProjectOnlyPolicyKeys = []string{"pullRequest"}

// LivePolicyKeys are the Policy keys dude reads from the project's and
// organisation's policy as it is now, never from a delivery's frozen
// State.Policy: the syncer's park, idle and warm limits (phases runFrom),
// one conductor publish's bounds (PublishLimitsTx), and what happens before
// the pull request (PullRequestChoiceTx, read at the gate).
var LivePolicyKeys = []string{"parkAfterMinutes", "idleNudgeMinutes", "conductorWarmMinutes", "conductorEditLines", "conductorEditFiles", "pullRequest"}

func DefaultPolicy() Policy {
	return Policy{
		BlockingSeverities: []string{"blocking", "high"},
		RequiredReviewers:  []string{"correctness", "tests", "performance"},
		ConditionalReviewers: []ReviewerRule{
			{Category: "security", WhenPathsMatch: []string{"**/auth/**", "**/payments/**", "**/*credential*"}},
			{Category: "database", WhenPathsMatch: []string{"**/migrations/**", "**/*.sql"}},
			{Category: "frontend", WhenPathsMatch: []string{"**/*.tsx", "**/*.css"}},
			{Category: "api", WhenPathsMatch: []string{"**/routes/**", "**/api/**"}},
		},
		AlwaysReviewers:       []string{"correctness"},
		NonProductPaths:       []string{".github/**", "docs/**", "**/*.md"},
		MaxReviewIterations:   5,
		MaxAttemptsPerFinding: 2,
		MaxStillRounds:        2,
		MaxRegressionRounds:   2,
		Simplify:              true,
		// On for every project: a delivery reaches a pull request with
		// every criterion shown working, or a person's waiver.
		Test:                 true,
		MaxPRFixIterations:   3,
		ParkAfterMinutes:     10,
		IdleNudgeMinutes:     0, // off
		ConductorWarmMinutes: 5,
		ConductorEditLines:   60,
		ConductorEditFiles:   3,
		ResumeRounds:         true,
		PullRequest:          PullRequestOpen,
	}
}

// Phases.
const (
	PhaseInvestigate = "investigate"
	PhaseImplement   = "implement"
	PhaseReview      = "review"
	PhaseFix         = "fix"
	PhaseSimplify    = "simplify"
	PhaseTest        = "test"
)

// RoleForPhase is the agent role that performs each phase.
var RoleForPhase = map[string]string{
	PhaseInvestigate: "investigator",
	PhaseImplement:   "implementer",
	PhaseReview:      "reviewer",
	PhaseFix:         "implementer",
	PhaseSimplify:    "simplifier",
	PhaseTest:        "qa_browser",
}

// RoleConductor is the role of a task's conductor: a Run of the task with
// no phase, which people talk to in its Chat.
const RoleConductor = "conductor"

// RoleBrainstorm is the role of a session's agent: a Run of the session,
// which its members talk to and which reads, never changes, code.
const RoleBrainstorm = "brainstorm"

// RoleLabel is how a role is named to a person, as the app names it
// (ROLE_LABEL, packages/design-system AgentAvatar).
var RoleLabel = map[string]string{
	RoleConductor:  "Conductor",
	RoleBrainstorm: "Brainstorm",
	"investigator": "Investigator",
	"implementer":  "Implementer",
	"reviewer":     "Reviewer",
	"simplifier":   "Simplifier",
	"qa_browser":   "QA browser",
}

// Publishes says whether a phase's commits reach the task's branch.
//
// A property of the phase rather than a prompt instruction: a reviewer gets a
// full sandbox and may run and change anything, but nothing it does is ever
// pushed, so "the reviewer does not commit" is structural.
var Publishes = map[string]bool{
	PhaseImplement: true,
	PhaseFix:       true,
	PhaseSimplify:  true,
}

var reviewerOrder = []string{"correctness", "tests", "security", "database", "api", "frontend", "documentation", "performance"}

// ReviewersFor picks the reviewers a diff warrants on a first review round,
// in a stable order so a task's fan-out is reproducible.
func ReviewersFor(p Policy, changedPaths []string) []string {
	return inReviewerOrder(slices.Concat(p.RequiredReviewers, pathReviewers(p, changedPaths)))
}

// pathReviewers are the conditional reviewers whose rules match a path,
// or that have no paths.
func pathReviewers(p Policy, paths []string) []string {
	var out []string
	for _, rule := range p.ConditionalReviewers {
		if len(rule.WhenPathsMatch) == 0 || slices.ContainsFunc(paths, func(path string) bool { return matchesRepoOrPath(rule.WhenPathsMatch, path) }) {
			out = append(out, rule.Category)
		}
	}
	return out
}

// Both matchers take a <repo>/<path in repo>. A conditional rule's glob may
// name either form, so "migrations/**" matches api/migrations/x.sql and
// "web/**" everything in web; a NonProductPaths glob names the in-repo path
// only, so "docs/**" does not cover a repository named docs.
func matchesRepoOrPath(globs []string, path string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return MatchesGlob(g, path) }) || matchesPathInRepo(globs, path)
}

func matchesPathInRepo(globs []string, path string) bool {
	_, inRepo, _ := strings.Cut(path, "/")
	return slices.ContainsFunc(globs, func(g string) bool { return MatchesGlob(g, inRepo) })
}

func inReviewerOrder(categories []string) []string {
	var out []string
	for _, c := range reviewerOrder {
		if slices.Contains(categories, c) {
			out = append(out, c)
		}
	}
	return out
}

// Why a reviewer runs in a round, as the round's run.created records it.
const (
	ReasonAlways     = "always"
	ReasonFinding    = "finding"
	ReasonPaths      = "paths"
	ReasonRequired   = "required"
	ReasonFirstRound = "first_round"
	ReasonLargeRound = "large_round"
	ReasonDirected   = "directed"
	ReasonRetry      = "retry"
)

// ReviewRound is what a review round's reviewers are picked from.
type ReviewRound struct {
	// The task's changed paths, <repo>/<path>.
	Whole []string
	// What changed since the attempt's previous completed review round;
	// not Measured on a first round.
	Change RoundChange
	// The categories of the open findings a fix attempted.
	Attempted []string
}

// RoundReviewers picks a review round's reviewers, in reviewerOrder, and
// why each runs. A round whose own change is not measured (a first round,
// or one GitHub could not compare) or is over MaxResumeDeltaLines runs the
// whole change's reviewers (ReviewersFor). A later round runs the
// reviewers of the findings it judges, the conditional reviewers its own
// change's paths match, and RequiredReviewers when that change touches a
// path outside NonProductPaths. Every round runs AlwaysReviewers.
func RoundReviewers(p Policy, r ReviewRound) ([]string, map[string][]string) {
	why := map[string][]string{}
	add := func(categories []string, reason string) {
		for _, c := range inReviewerOrder(categories) {
			if !slices.Contains(why[c], reason) {
				why[c] = append(why[c], reason)
			}
		}
	}
	add(p.AlwaysReviewers, ReasonAlways)
	switch {
	case !r.Change.Measured:
		add(ReviewersFor(p, r.Whole), ReasonFirstRound)
	case r.Change.Lines > MaxResumeDeltaLines:
		add(ReviewersFor(p, r.Whole), ReasonLargeRound)
	default:
		add(r.Attempted, ReasonFinding)
		add(pathReviewers(p, r.Change.Paths), ReasonPaths)
		if slices.ContainsFunc(r.Change.Paths, func(path string) bool { return !matchesPathInRepo(p.NonProductPaths, path) }) {
			add(p.RequiredReviewers, ReasonRequired)
		}
	}
	return inReviewerOrder(slices.Collect(maps.Keys(why))), why
}

// eachFor is categories, each run for one reason.
func eachFor(categories []string, reason string) map[string][]string {
	why := map[string][]string{}
	for _, c := range categories {
		why[c] = []string{reason}
	}
	return why
}

// The bounds packages/domain's deliveryPolicySchema puts on nonProductPaths.
const (
	maxNonProductPaths = 50
	maxNonProductGlob  = 200
)

// checkReviewerKeys refuses what deliveryPolicySchema refuses: an
// always-run reviewer not in the catalogue, more than maxNonProductPaths
// globs, or one blank or longer than maxNonProductGlob UTF-16 units once
// trimmed. It trims each glob, as the schema stores it.
func (p *Policy) checkReviewerKeys() error {
	for _, c := range p.AlwaysReviewers {
		if !slices.Contains(reviewerOrder, c) {
			return fmt.Errorf("alwaysReviewers: "+rNoReviewerCategoriesAre, c, listOr(reviewCategories()))
		}
	}
	if len(p.NonProductPaths) > maxNonProductPaths {
		return fmt.Errorf("nonProductPaths: %d globs, at most %d", len(p.NonProductPaths), maxNonProductPaths)
	}
	for i, g := range p.NonProductPaths {
		g = strings.TrimSpace(g)
		switch {
		case g == "":
			return errors.New("nonProductPaths: a glob is empty")
		case len(utf16.Encode([]rune(g))) > maxNonProductGlob:
			return fmt.Errorf("nonProductPaths: a glob is over %d characters", maxNonProductGlob)
		}
		p.NonProductPaths[i] = g
	}
	return nil
}

// refuseNullReviewerKeys refuses a policy layer naming alwaysReviewers or
// nonProductPaths as null, which json.Unmarshal would read as an empty
// list: the stored schema has no null (a settings reset removes the key).
func refuseNullReviewerKeys(raw json.RawMessage) error {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return nil
	}
	for _, k := range []string{"alwaysReviewers", "nonProductPaths"} {
		if v, ok := keys[k]; ok && string(bytes.TrimSpace(v)) == "null" {
			return fmt.Errorf("%s: null is not a list; leave it out for the inherited value", k)
		}
	}
	return nil
}

// FindingState is what the loop reads about a finding, or about a
// person's feedback item (Human): open or given to an agent, it blocks
// whatever the policy's severities say.
type FindingState struct {
	ID          string
	Severity    string
	Status      string
	FixAttempts int
	// Not a column of review_findings: set only for a person's item.
	Human        bool `db:"-"`
	HasCriterion bool `db:"-"`
	StillRounds  int  `db:"-"`
}

// Convergence is derived from completed review rounds in the current attempt.
// Callers copy StillRounds onto each FindingState before evaluating Exit.
type Convergence struct {
	StillRounds      map[string]int
	RegressionRounds int
	FindingIDs       []string
}

// IsBlocking: only open findings block. A resolved one is fixed, a
// superseded one describes code that no longer exists, and an accepted one
// is a person's decision to ship anyway.
func IsBlocking(p Policy, f FindingState) bool {
	if f.Human {
		return f.Status == "open" || f.Status == "delivered"
	}
	return f.Status == "open" && slices.Contains(p.BlockingSeverities, f.Severity)
}

// LoopExit says why the review loop should stop, or nil to keep going.
//
// "clear" needs no attention; "not_converging" identifies repeated unmet
// criteria or regressions; "stuck" identifies exhausted finding attempts;
// "exhausted" identifies the whole loop's budget.
// A person's item a stuck or exhausted loop is held by is named in
// FeedbackIDs, never among the findings.
type LoopExit struct {
	Reason           string   `json:"reason"`
	FindingIDs       []string `json:"findingIds,omitempty"`
	FeedbackIDs      []string `json:"feedbackIds,omitempty"`
	Iterations       int      `json:"iterations,omitempty"`
	RegressionRounds int      `json:"regressionRounds,omitempty"`
}

// Exit weighs the findings against the policy's bounds. conv is the review
// loop's convergence (Store.ConvergingFindings); a zero one weighs neither
// convergence bound.
func Exit(p Policy, findings []FindingState, iteration int, conv Convergence) *LoopExit {
	var blocking []FindingState
	for _, f := range findings {
		if IsBlocking(p, f) {
			blocking = append(blocking, f)
		}
	}
	if len(blocking) == 0 {
		return &LoopExit{Reason: "clear"}
	}
	// A regression escalates only while one of the newest round's fix-caused
	// blockers is open: with those resolved or accepted, what is left blocks
	// as any finding does, and nothing names what to direct.
	regressing := p.MaxRegressionRounds > 0 && conv.RegressionRounds >= p.MaxRegressionRounds
	var nonConverging []string
	var regressed bool
	for _, f := range blocking {
		if f.Human {
			continue
		}
		caused := regressing && slices.Contains(conv.FindingIDs, f.ID)
		if caused || p.MaxStillRounds > 0 && f.HasCriterion && f.StillRounds >= p.MaxStillRounds {
			nonConverging = append(nonConverging, f.ID)
		}
		regressed = regressed || caused
	}
	if len(nonConverging) > 0 {
		exit := &LoopExit{Reason: "not_converging", FindingIDs: nonConverging}
		if regressed {
			exit.RegressionRounds = conv.RegressionRounds
		}
		return exit
	}
	// A finding the fixer keeps failing is escalated on its own, before the
	// loop's budget is spent on it.
	var stuck, stuckFeedback []string
	for _, f := range blocking {
		if f.FixAttempts >= p.MaxAttemptsPerFinding {
			if f.Human {
				stuckFeedback = append(stuckFeedback, f.ID)
			} else {
				stuck = append(stuck, f.ID)
			}
		}
	}
	if len(stuck)+len(stuckFeedback) > 0 {
		return &LoopExit{Reason: "stuck", FindingIDs: stuck, FeedbackIDs: stuckFeedback}
	}
	if iteration >= p.MaxReviewIterations {
		// The person's items still blocking are named, so accepting the
		// escalation resolves them rather than meeting them again at the
		// pull request.
		var feedback []string
		for _, f := range blocking {
			if f.Human {
				feedback = append(feedback, f.ID)
			}
		}
		return &LoopExit{Reason: "exhausted", Iterations: iteration, FeedbackIDs: feedback}
	}
	return nil
}

// MatchesGlob: `**` crosses directories, `*` does not.
//
// Not a glob library: patterns come from policy an operator writes, the
// vocabulary is small, and a dependency with subtly different semantics would
// be harder to reason about than these lines.
func MatchesGlob(pattern, path string) bool {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case pattern[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	re, err := regexp.Compile("^" + b.String() + "$")
	return err == nil && re.MatchString(path)
}
