package phases

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// advanced is where a writer's publish left the task branch. Head is the
// branch's new head: the writer's commit, or, when Commit is set, the
// merge of Commit GitHub made on top of MergedInto, a head that only
// merged the base in under the writer.
type advanced struct {
	Head, Commit, MergedInto string
}

// payload is git.commit_created's payload: headSha the branch's new head,
// and, when it is dude's merge, the writer's commit and what it merged
// into (mergedBase).
func (a advanced) payload(p map[string]any) map[string]any {
	p["headSha"] = a.Head
	if a.Commit != "" {
		p["mergedBase"] = map[string]any{"commit": a.Commit, "into": a.MergedInto}
	}
	return p
}

// maxBaseMerges bounds the base merges a refused fast-forward is judged
// over: one GitHub compare each.
const maxBaseMerges = 5

// errBaseMergeConflict: the base was merged into the task branch under
// the writer, and GitHub would not merge the writer's commit with it (409).
type errBaseMergeConflict struct {
	base, into, commit string
	cause              error
}

func (e *errBaseMergeConflict) Error() string {
	return fmt.Sprintf("%s was merged into the task branch meanwhile (now %s), and %s conflicts with that merge: %v",
		e.base, short(e.into), short(e.commit), e.cause)
}

func (e *errBaseMergeConflict) Unwrap() error { return e.cause }

type errUnsafeMerge struct {
	merge, first, second, expected string
}

func (e *errUnsafeMerge) Error() string {
	return fmt.Sprintf("ERROR: GitHub merge %s has first parent %s and second parent %s, expected first parent %s and second parent %s; automatic recovery is refused. Have a human reconcile the task branch with the documented merge recovery, then publish again", e.merge, e.first, e.second, e.expected, e.second)
}

// start, and is the one way a writer's publish moves a task branch. A
// fast-forward, never a force. When GitHub refuses it as not a
// fast-forward and the branch's head X only merged baseBranch in since
// start (baseMerges), commit is merged into the branch on GitHub (POST
// /merges) and that merge is the new head; a 204, commit already in the
// branch, keeps the branch's head. Any other move of the branch (a
// person's commit, a rebase, a force-push) returns the fast-forward's
// refusal, and a merge GitHub refuses is errBaseMergeConflict.
//
// GitHub's merges endpoint takes no expected head: a branch that moves
// between the judgement and the merge is judged again from the merge's
// first parent, and an error says so when that is not only base merges.
//
// A head that is already a pure merge of commit is adopted only when
// attempted: the caller durably recorded, in beforeMerge, sending that
// merge itself. Otherwise someone else made it, and it is refused like any
// other move.
func advanceBranch(ctx context.Context, gh *forge.GitHub, slug, branch, baseBranch, start, commit string, attempted bool, beforeMerge ...func() error) (advanced, error) {
	refusal := gh.FastForward(ctx, slug, branch, commit)
	if refusal == nil || !notFastForward(refusal) || start == "" || baseBranch == "" {
		return advanced{Head: commit}, refusal
	}
	at, err := gh.BranchSHA(ctx, slug, branch)
	if err != nil || at == commit {
		return advanced{Head: commit}, err
	}
	j, err := baseMerges(ctx, gh, slug, baseBranch, start, at, commit)
	switch {
	case forge.Refused(err):
		// GitHub will not compare them (history rewritten): not judged.
		return advanced{}, refusal
	case err != nil:
		return advanced{}, err
	case !j.only, j.own && !attempted:
		return advanced{}, refusal
	case j.own:
		// commit merged already, by an attempt whose answer was lost.
		return advanced{Head: at, Commit: commit, MergedInto: j.under}, nil
	}
	if len(beforeMerge) > 0 {
		if err := beforeMerge[0](); err != nil {
			return advanced{}, err
		}
	}
	m, err := gh.MergeCommit(ctx, slug, branch, commit)
	var fe *forge.Error
	switch {
	case errors.As(err, &fe) && fe.Status == 409:
		return advanced{}, &errBaseMergeConflict{base: baseBranch, into: at, commit: commit, cause: err}
	case err != nil:
		return advanced{}, err
	case m.SHA == "":
		return advanced{Head: at}, nil
	}
	if len(m.Parents) != 2 || m.Parents[0] != at {
		into := ""
		if len(m.Parents) > 0 {
			into = m.Parents[0]
		}
		if len(m.Parents) != 2 || into == "" {
			return advanced{}, &errUnsafeMerge{merge: m.SHA, first: into, second: "", expected: at}
		}
		j, err := baseMerges(ctx, gh, slug, baseBranch, start, into, commit)
		if err != nil {
			return advanced{}, err
		}
		if !j.only && into != start {
			return advanced{}, &errUnsafeMerge{merge: m.SHA, first: into, second: m.Parents[1], expected: at}
		}
		pure, err := pureMerge(ctx, gh, slug, into, m.Parents[1], m.SHA)
		if err != nil {
			return advanced{}, err
		}
		if !pure {
			return advanced{}, &errUnsafeMerge{merge: m.SHA, first: into, second: m.Parents[1], expected: at}
		}
		at = into
	}
	return advanced{Head: m.SHA, Commit: commit, MergedInto: at}, nil
}

// notFastForward says GitHub refused a ref update for not being a
// fast-forward (422 "Update is not a fast forward").
func notFastForward(err error) bool {
	var e *forge.Error
	return errors.As(err, &e) && e.Status == 422 && strings.Contains(strings.ToLower(e.Message), "fast forward")
}

// judgement is baseMerges' answer. only: head is start with nothing on
// top but merges of the base. own: head itself is the merge of commit,
// on under.
type judgement struct {
	only, own bool
	under     string
}

// pureMerge verifies that the merge added exactly the first parent's changes
// to the second parent. Comparing both sides by file makes a manually
// resolved extra edit visible without relying on commit messages or authors.
func pureMerge(ctx context.Context, gh *forge.GitHub, slug string, first, second, merge string) (bool, error) {
	provenance, err := gh.CommitProvenance(ctx, slug, merge)
	if err != nil {
		return false, err
	}
	if !provenance.Verified || provenance.Login != "web-flow" {
		return false, nil
	}
	left, err := gh.Compare(ctx, slug, second, first)
	if err != nil {
		return false, err
	}
	right, err := gh.Compare(ctx, slug, second, merge)
	if err != nil {
		return false, err
	}
	if left.Truncated() || right.Truncated() {
		return false, nil
	}
	return maps.Equal(comparisonFiles(left.Files), comparisonFiles(right.Files)), nil
}

func comparisonFiles(files []forge.ComparedFile) map[string]string {
	out := map[string]string{}
	for _, file := range files {
		out[file.Filename] = file.Status + "\x00" + file.SHA
	}
	return out
}

// baseMerges judges whether head moved from start only by merges of
// baseBranch into it. It does when every commit on head's first-parent
// line down to start is a two-parent merge whose second parent baseBranch
// contains (compare second...baseBranch behind by none), and that line
// reaches start (compare start...head lists its commits with their
// parents). Every other commit start..head holds is then an ancestor of
// such a second parent: the base's. The topmost may instead have commit
// as its second parent and be GitHub's pure merge of it onto its first
// parent: dude's own merge of it (own).
//
// Costs one compare, plus one per merge, at most maxBaseMerges. A commit
// list GitHub cut short, or more merges, is judged no.
func baseMerges(ctx context.Context, gh *forge.GitHub, slug, baseBranch, start, head, commit string) (judgement, error) {
	if head == "" || head == start {
		return judgement{}, nil
	}
	cmp, err := gh.CompareCommits(ctx, slug, start, head)
	if err != nil || cmp.BehindBy > 0 || len(cmp.Commits) < cmp.TotalCommits {
		return judgement{}, err
	}
	parents := map[string][]string{}
	for _, c := range cmp.Commits {
		parents[c.SHA] = []string{}
		for _, p := range c.Parents {
			parents[c.SHA] = append(parents[c.SHA], p.SHA)
		}
	}
	var j judgement
	merges := 0
	for at := head; at != start; at = parents[at][0] {
		ps, listed := parents[at]
		if !listed || len(ps) != 2 || merges == maxBaseMerges {
			return judgement{}, nil
		}
		merges++
		if at == head && ps[1] == commit {
			// dude's own merge must be exactly the first parent plus
			// commit's change, made by GitHub.
			pure, err := pureMerge(ctx, gh, slug, commit, ps[0], at)
			if err != nil || !pure {
				return judgement{}, err
			}
			j.own, j.under = true, ps[0]
			continue
		}
		pure, err := pureMerge(ctx, gh, slug, ps[0], ps[1], at)
		if err != nil {
			return judgement{}, err
		}
		if !pure {
			return judgement{}, nil
		}
		in, err := gh.Compare(ctx, slug, ps[1], baseBranch)
		if err != nil || in.BehindBy > 0 {
			return judgement{}, err
		}
	}
	j.only = true
	return j, nil
}
