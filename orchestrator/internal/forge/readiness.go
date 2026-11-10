package forge

import (
	"context"
	"fmt"
	"net/url"
	"strings"
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
	// Read only when no check reported at the head: whether
	// .github/workflows holds a workflow file at it.
	WorkflowsKnown bool
	Workflows      []string
	// What branch protection (and rulesets) on BaseBranch require, read
	// when no check reported or GitHub says the merge is blocked.
	// ProtectionKnown is false when neither could be read.
	ProtectionKnown   bool
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
	if f.Checks == ChecksUnknown && p.Head.SHA != "" {
		if f.Workflows, f.WorkflowsKnown, err = g.workflowFiles(ctx, slug, p.Head.SHA); err != nil {
			return f, err
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
// summary and the rulesets that apply to it. Neither readable leaves
// ProtectionKnown false.
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
		f.ProtectionKnown = true
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
		f.ProtectionKnown = true
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
