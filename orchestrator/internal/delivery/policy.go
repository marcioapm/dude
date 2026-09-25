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
	"regexp"
	"slices"
	"strings"
)

type ReviewerRule struct {
	Category string `json:"category"`
	// Run this reviewer only when the diff touches a matching path: a security
	// reviewer on a CSS change is a tax on every work item.
	WhenPathsMatch []string `json:"whenPathsMatch,omitempty"`
}

type Policy struct {
	// Severities that stop a work item from progressing.
	BlockingSeverities   []string       `json:"blockingSeverities"`
	RequiredReviewers    []string       `json:"requiredReviewers"`
	ConditionalReviewers []ReviewerRule `json:"conditionalReviewers"`
	// Review → fix cycles before escalating to a person. The bound exists
	// because a loop that cannot converge will spend a budget finding that out.
	MaxReviewIterations int `json:"maxReviewIterations"`
	// Fix attempts one finding may survive before it is escalated on its own.
	// Tighter than the loop bound on purpose: a finding the fixer failed twice
	// is one it misunderstood, and a third try is a third variation of the
	// same wrong change.
	MaxAttemptsPerFinding int  `json:"maxAttemptsPerFinding"`
	Simplify              bool `json:"simplify"`
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
}

func DefaultPolicy() Policy {
	return Policy{
		BlockingSeverities: []string{"blocking", "high"},
		RequiredReviewers:  []string{"correctness"},
		ConditionalReviewers: []ReviewerRule{
			{Category: "security", WhenPathsMatch: []string{"**/auth/**", "**/payments/**", "**/*credential*"}},
			{Category: "database", WhenPathsMatch: []string{"**/migrations/**", "**/*.sql"}},
			{Category: "frontend", WhenPathsMatch: []string{"**/*.tsx", "**/*.css"}},
			{Category: "api", WhenPathsMatch: []string{"**/routes/**", "**/api/**"}},
		},
		MaxReviewIterations:   5,
		MaxAttemptsPerFinding: 2,
		Simplify:              true,
		Test:                  false,
		MaxPRFixIterations:    3,
		ParkAfterMinutes:      10,
		IdleNudgeMinutes:      0, // off
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

// RoleLabel is how a role is named to a person, as the app names it
// (ROLE_LABEL, packages/design-system AgentAvatar).
var RoleLabel = map[string]string{
	"orchestrator": "Orchestrator",
	"investigator": "Investigator",
	"implementer":  "Implementer",
	"reviewer":     "Reviewer",
	"simplifier":   "Simplifier",
	"qa_browser":   "QA browser",
}

// Publishes says whether a phase's commits reach the work item's branch.
//
// A property of the phase rather than a prompt instruction: a reviewer gets a
// full sandbox and may run and change anything, but nothing it does is ever
// pushed, so "the reviewer does not commit" is structural.
var Publishes = map[string]bool{
	PhaseImplement: true,
	PhaseFix:       true,
	PhaseSimplify:  true,
}

var reviewerOrder = []string{"correctness", "security", "database", "api", "frontend", "performance"}

// ReviewersFor picks the reviewers a diff warrants, in a stable order so a
// work item's fan-out is reproducible.
func ReviewersFor(p Policy, changedPaths []string) []string {
	selected := map[string]bool{}
	for _, c := range p.RequiredReviewers {
		selected[c] = true
	}
	for _, rule := range p.ConditionalReviewers {
		if len(rule.WhenPathsMatch) == 0 {
			selected[rule.Category] = true
			continue
		}
		for _, path := range changedPaths {
			// A path is <repo>/<path in repo>: a rule may name either, so
			// "migrations/**" matches api/migrations/x.sql, and "web/**"
			// matches everything in web.
			_, inRepo, _ := strings.Cut(path, "/")
			if slices.ContainsFunc(rule.WhenPathsMatch, func(g string) bool { return MatchesGlob(g, path) || MatchesGlob(g, inRepo) }) {
				selected[rule.Category] = true
				break
			}
		}
	}
	var out []string
	for _, c := range reviewerOrder {
		if selected[c] {
			out = append(out, c)
		}
	}
	return out
}

// FindingState is what the loop reads about a finding.
type FindingState struct {
	ID          string
	Severity    string
	Status      string
	FixAttempts int
}

// IsBlocking: only open findings block. A resolved one is fixed, a
// superseded one describes code that no longer exists, and an accepted one
// is a person's decision to ship anyway.
func IsBlocking(p Policy, f FindingState) bool {
	return f.Status == "open" && slices.Contains(p.BlockingSeverities, f.Severity)
}

// LoopExit says why the review loop should stop, or nil to keep going.
//
// Three exits, named separately because they mean different things to a
// person: "clear" needs no attention, "stuck" means the fixer is stuck on
// something specific, and "exhausted" means the whole loop ran out of budget.
type LoopExit struct {
	Reason     string   `json:"reason"`
	FindingIDs []string `json:"findingIds,omitempty"`
	Iterations int      `json:"iterations,omitempty"`
}

func Exit(p Policy, findings []FindingState, iteration int) *LoopExit {
	var blocking []FindingState
	for _, f := range findings {
		if IsBlocking(p, f) {
			blocking = append(blocking, f)
		}
	}
	if len(blocking) == 0 {
		return &LoopExit{Reason: "clear"}
	}
	// A finding the fixer keeps failing is escalated on its own, before the
	// loop's budget is spent on it.
	var stuck []string
	for _, f := range blocking {
		if f.FixAttempts >= p.MaxAttemptsPerFinding {
			stuck = append(stuck, f.ID)
		}
	}
	if len(stuck) > 0 {
		return &LoopExit{Reason: "stuck", FindingIDs: stuck}
	}
	if iteration >= p.MaxReviewIterations {
		return &LoopExit{Reason: "exhausted", Iterations: iteration}
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
