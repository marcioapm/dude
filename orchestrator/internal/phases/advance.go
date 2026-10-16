package phases

import (
	"context"
	"errors"
	"fmt"
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

// advanceBranch moves branch to commit, a writer's work started from
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
func advanceBranch(ctx context.Context, gh *forge.GitHub, slug, branch, baseBranch, start, commit string) (advanced, error) {
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
	case !j.only:
		return advanced{}, refusal
	case j.own:
		// commit merged already, by an attempt whose answer was lost.
		return advanced{Head: at, Commit: commit, MergedInto: j.under}, nil
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
		j, err := baseMerges(ctx, gh, slug, baseBranch, start, into, commit)
		if err != nil {
			return advanced{}, err
		}
		if !j.only && into != start {
			return advanced{}, fmt.Errorf("the task branch moved while dude merged %s into it: the merge %s is on it over %s, which is not only merges of %s",
				short(commit), short(m.SHA), short(into), baseBranch)
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

// baseMerges judges whether head moved from start only by merges of
// baseBranch into it. It does when every commit on head's first-parent
// line down to start is a two-parent merge whose second parent baseBranch
// contains (compare second...baseBranch behind by none), and that line
// reaches start (compare start...head lists its commits with their
// parents). Every other commit start..head holds is then an ancestor of
// such a second parent: the base's. The topmost may instead have commit
// as its second parent: dude's own merge of it (own).
//
// Costs one compare, plus one per merge, at most maxBaseMerges. A commit
// list GitHub cut short, or more merges, is judged no.
func baseMerges(ctx context.Context, gh *forge.GitHub, slug, baseBranch, start, head, commit string) (judgement, error) {
	if head == "" || head == start {
		return judgement{}, nil
	}
	cmp, err := gh.Compare(ctx, slug, start, head)
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
			j.own, j.under = true, ps[0]
			continue
		}
		in, err := gh.Compare(ctx, slug, ps[1], baseBranch)
		if err != nil || in.BehindBy > 0 {
			return judgement{}, err
		}
	}
	j.only = true
	return j, nil
}
