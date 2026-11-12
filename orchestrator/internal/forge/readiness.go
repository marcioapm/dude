package forge

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ReadinessFacts is what GitHub says of a pull request that the ready
// gate (delivery.ReadyGate) judges: its state and head, GitHub's own
// mergeable_state, the checks at that exact head, the review threads
// nobody resolved, and, when it matters, whether the head has workflow
// files and what branch protection requires. Read only; never written.
type ReadinessFacts struct {
	PullRequestRef
	Draft      bool
	BaseBranch string
	// GitHub's mergeable_state (clean, unstable, has_hooks, behind, dirty,
	// blocked, draft, unknown) and its mergeable flag; nil while GitHub
	// has not computed it.
	MergeableState string
	Mergeable      *bool
	// How many commits the base has that the head lacks (compare
	// base...head): GitHub reports a branch behind as clean unless
	// protection requires it be up to date.
	BehindBy int
	// The checks at HeadSHA, both commit statuses and check runs: their
	// rollup (ChecksPassing, ChecksFailing, ChecksPending, ChecksUnknown
	// for none at all), and each.
	Checks    string
	CheckList []Check
	// Review threads left unresolved; ThreadsUnknown when GitHub would not
	// count them.
	UnresolvedThreads int
	ThreadsUnknown    bool
	// Read only when Actions reported no check at the head: whether
	// .github/workflows holds a workflow file at it, and whether any of
	// them runs for this pull request's head by its on: (ActionsExpected,
	// ActionsNotExpected; "" when a file could not be read or parsed).
	WorkflowsKnown  bool
	Workflows       []string
	ActionsExpected string
	// What branch protection (and rulesets) on BaseBranch require, read
	// when no check reported or GitHub says the merge is blocked; none
	// when neither could be read.
	RequiredChecks    []string
	RequiredApprovals int
	// GitHub's count of approving reviews standing, for naming a missing
	// required approval.
	Approvals int
}

// Readiness reads a pull request for the ready gate. Each read is GitHub's
// answer now; nothing here is taken from what dude last synced.
func (g *GitHub) Readiness(ctx context.Context, slug string, number int) (ReadinessFacts, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return ReadinessFacts{}, err
	}
	f := ReadinessFacts{PullRequestRef: p.ref(), Draft: p.Draft, BaseBranch: p.Base.Ref,
		MergeableState: p.MergeableState, Mergeable: p.Mergeable}
	if f.State != StateOpen && f.State != StateDraft {
		return f, nil
	}
	var err error
	if p.Head.SHA != "" {
		if f.Checks, f.CheckList, _, err = g.checks(ctx, slug, p.Head.SHA); err != nil {
			return f, err
		}
	}
	if p.Base.Ref != "" && p.Head.SHA != "" {
		var cmp struct {
			BehindBy int `json:"behind_by"`
		}
		err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s", slug, url.PathEscape(p.Base.Ref), p.Head.SHA), nil, &cmp)
		var e *Error
		if err != nil && !(asError(err, &e) && e.NotFound()) {
			return f, err
		}
		f.BehindBy = cmp.BehindBy
	}
	if f.UnresolvedThreads, f.ThreadsUnknown, err = g.unresolvedThreads(ctx, slug, number); err != nil {
		return f, err
	}
	// The workflows are read when Actions has reported nothing at the head:
	// whether it should (on:) is what tells "no CI for this head" from "CI
	// yet to register", and an external status alone from a green head.
	if !actionsReported(f.CheckList) && p.Head.SHA != "" {
		if f.Workflows, f.WorkflowsKnown, err = g.workflowFiles(ctx, slug, p.Head.SHA); err != nil {
			return f, err
		}
		if f.WorkflowsKnown && len(f.Workflows) > 0 {
			if f.ActionsExpected, err = g.actionsExpected(ctx, slug, p.Head.SHA, f.Workflows); err != nil {
				return f, err
			}
		} else if f.WorkflowsKnown {
			f.ActionsExpected = ActionsNotExpected
		}
	}
	if (f.Checks == ChecksUnknown || f.MergeableState == "blocked") && f.BaseBranch != "" {
		if err := g.protection(ctx, slug, f.BaseBranch, &f); err != nil {
			return f, err
		}
		if f.MergeableState == "blocked" && f.RequiredApprovals > 0 {
			reviews, err := pages[ghReview](ctx, g, fmt.Sprintf("/repos/%s/pulls/%d/reviews", slug, number))
			if err != nil {
				return f, err
			}
			for _, r := range latestReviews(reviews, nil) {
				if r.State == "APPROVED" {
					f.Approvals++
				}
			}
		}
	}
	return f, nil
}

// workflowFiles lists the workflow files under .github/workflows at ref.
// known is false when GitHub would not say (a token that may not read
// contents): neither "some" nor "none".
func (g *GitHub) workflowFiles(ctx context.Context, slug, ref string) (files []string, known bool, err error) {
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	err = g.do(ctx, "GET", fmt.Sprintf("/repos/%s/contents/.github/workflows?ref=%s", slug, url.QueryEscape(ref)), nil, &entries)
	var e *Error
	switch {
	case asError(err, &e) && e.NotFound():
		return nil, true, nil
	case Refused(err):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	for _, x := range entries {
		if x.Type == "file" && (strings.HasSuffix(x.Name, ".yml") || strings.HasSuffix(x.Name, ".yaml")) {
			files = append(files, x.Name)
		}
	}
	return files, true, nil
}

// protection reads what the base branch requires, from the two places
// GitHub keeps it that a reader may see: the branch's own protection
// summary and the rulesets that apply to it. Neither readable requires
// nothing that can be named.
func (g *GitHub) protection(ctx context.Context, slug, branch string, f *ReadinessFacts) error {
	var b struct {
		Protected  bool `json:"protected"`
		Protection *struct {
			RequiredStatusChecks *struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	err := g.do(ctx, "GET", "/repos/"+slug+"/branches/"+branch, nil, &b)
	switch {
	case err == nil:
		if b.Protection != nil && b.Protection.RequiredStatusChecks != nil {
			f.RequiredChecks = append(f.RequiredChecks, b.Protection.RequiredStatusChecks.Contexts...)
			for _, c := range b.Protection.RequiredStatusChecks.Checks {
				f.RequiredChecks = appendNew(f.RequiredChecks, c.Context)
			}
		}
	case !Refused(err):
		return err
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredApprovingReviewCount int `json:"required_approving_review_count"`
			RequiredStatusChecks         []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	err = g.do(ctx, "GET", "/repos/"+slug+"/rules/branches/"+branch, nil, &rules)
	switch {
	case err == nil:
		for _, r := range rules {
			switch r.Type {
			case "pull_request":
				f.RequiredApprovals = max(f.RequiredApprovals, r.Parameters.RequiredApprovingReviewCount)
			case "required_status_checks":
				for _, c := range r.Parameters.RequiredStatusChecks {
					f.RequiredChecks = appendNew(f.RequiredChecks, c.Context)
				}
			}
		}
	case !Refused(err):
		return err
	}
	return nil
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// CommitParents is a commit's parents, first parent first.
func (g *GitHub) CommitParents(ctx context.Context, slug, sha string) ([]string, error) {
	var c struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s", slug, sha), nil, &c); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(c.Parents))
	for _, p := range c.Parents {
		out = append(out, p.SHA)
	}
	return out, nil
}

// CheckOutcome is one check's reading for the gate: passing (completed as
// success, neutral or skipped), failing, or pending.
func CheckOutcome(c Check) string { return checkRunState(c.Status, c.Conclusion) }

// GateMoved says what the ready gate reads of a pull request's status
// changed between two syncs, though Classify had nothing to say: an
// approval branch protection may ask for, checks settling, mergeability,
// review threads, a draft marked ready, or a head someone pushed. The
// workflow weighs readiness again for it.
func GateMoved(prior, current Status) bool {
	return gateReading(prior) != gateReading(current)
}

type reading struct {
	state, head, checks, review, mergeable string
	threads                                int
}

func gateReading(s Status) reading {
	review := s.Review
	if review == "" {
		review = ReviewPending
	}
	return reading{state: s.State, head: s.HeadSHA, checks: s.Checks, review: review, mergeable: s.Mergeable,
		threads: s.UnresolvedThreads}
}

// Whether GitHub Actions should report on a pull request's head, by its
// workflows' on: (ReadinessFacts.ActionsExpected).
const (
	ActionsExpected    = "expected"
	ActionsNotExpected = "not_expected"
)

// DefaultCIGrace is how long CI may take to show up on a new head
// (prs.Syncer's grace, and the ready gate's where the syncer has not
// judged it).
const DefaultCIGrace = 10 * time.Minute

// maxWorkflowBytes bounds one workflow file read for its on:.
const maxWorkflowBytes = 256 << 10

// actionsReported says a check at the head is GitHub Actions'.
func actionsReported(checks []Check) bool {
	for _, c := range checks {
		if c.App == "github-actions" {
			return true
		}
	}
	return false
}

// actionsExpected reads each workflow file's on: at ref: ActionsExpected
// when one runs on pull_request or pull_request_target with no filter
// (branches, paths, types) that could leave this head out;
// ActionsNotExpected when none names either (push to main, tags,
// schedule, workflow_dispatch only); "" when a file could not be read or
// parsed, or a filter decides: the gate falls back to the CI grace.
func (g *GitHub) actionsExpected(ctx context.Context, slug, ref string, files []string) (string, error) {
	out := ActionsNotExpected
	for _, name := range files {
		raw, err := g.FileAt(ctx, slug, ".github/workflows/"+name, ref, maxWorkflowBytes)
		switch {
		case Refused(err):
			out = ""
			continue
		case err != nil:
			var e *Error
			if asError(err, &e) && e.NotFound() {
				out = ""
				continue
			}
			return "", err
		}
		switch WorkflowRunsOnPullRequests(raw) {
		case ActionsExpected:
			return ActionsExpected, nil
		case "":
			out = ""
		}
	}
	return out, nil
}

// WorkflowRunsOnPullRequests reads one workflow file's on:
// ActionsExpected when it runs on every pull request (pull_request or
// pull_request_target, no filter), ActionsNotExpected when it names
// neither, "" when it cannot tell (unparseable, or a filter).
func WorkflowRunsOnPullRequests(raw []byte) string {
	on, ok := workflowOn(raw)
	if !ok {
		return ""
	}
	isPR := func(s string) bool { return s == "pull_request" || s == "pull_request_target" }
	switch on.Kind {
	case yaml.ScalarNode:
		if isPR(on.Value) {
			return ActionsExpected
		}
		return ActionsNotExpected
	case yaml.SequenceNode:
		for _, n := range on.Content {
			if isPR(n.Value) {
				return ActionsExpected
			}
		}
		return ActionsNotExpected
	case yaml.MappingNode:
		out := ActionsNotExpected
		for i := 0; i+1 < len(on.Content); i += 2 {
			if !isPR(on.Content[i].Value) {
				continue
			}
			v := on.Content[i+1]
			if v.Kind == yaml.MappingNode && len(v.Content) > 0 {
				// branches, paths, types: whether this head is in is not
				// read here.
				out = ""
				continue
			}
			return ActionsExpected
		}
		return out
	}
	return ""
}

// workflowOn is one workflow file's on: node; ok false when the file does
// not parse or names none. WorkflowRunsOnPullRequests and WorkflowTriggers
// read it.
func workflowOn(raw []byte) (yaml.Node, bool) {
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return yaml.Node{}, false
	}
	// YAML 1.1 reads a bare on as true: a key "true".
	on, ok := doc["on"]
	if !ok {
		on, ok = doc["true"]
	}
	return on, ok
}
