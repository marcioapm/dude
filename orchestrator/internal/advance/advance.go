// Package advance moves a task branch to a writer's commit: a
// fast-forward, or, when GitHub only merged the base into the branch
// under the writer, a merge of the commit on top.
package advance

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Result is where a writer's publish left the task branch. Head is the
// branch's new head: the writer's commit, or, when Commit is set, the
// merge of Commit GitHub made on top of MergedInto, a head that only
// merged the base in under the writer.
type Result struct {
	Head, Commit, MergedInto string
}

// Payload is git.commit_created's payload: headSha the branch's new head,
// and, when it is dude's merge, the writer's commit and what it merged
// into (mergedBase).
func (a Result) Payload(p map[string]any) map[string]any {
	p["headSha"] = a.Head
	if a.Commit != "" {
		p["mergedBase"] = map[string]any{"commit": a.Commit, "into": a.MergedInto}
	}
	return p
}

// maxBaseMerges bounds the base merges a refused fast-forward is judged
// over: one GitHub compare each.
const maxBaseMerges = 5

// ErrBaseMergeConflict: the base was merged into the task branch under
// the writer, and GitHub would not merge the writer's commit with it (409).
type ErrBaseMergeConflict struct {
	base, into, commit string
	cause              error
}

func (e *ErrBaseMergeConflict) Error() string {
	return fmt.Sprintf("%s was merged into the task branch meanwhile (now %s), and %s conflicts with that merge: %v",
		e.base, short(e.into), short(e.commit), e.cause)
}

func (e *ErrBaseMergeConflict) Unwrap() error { return e.cause }

// ErrUnsafeMerge: a merge of the writer's commit on the task branch that
// dude will not adopt. Found and Expected are clauses naming what the
// merge is and what dude's own merge would have been.
type ErrUnsafeMerge struct {
	Merge, Found, Expected string
}

func (e *ErrUnsafeMerge) Error() string {
	return fmt.Sprintf("ERROR: GitHub merge %s %s, expected %s; automatic recovery is refused. Have a human reconcile the task branch with the documented merge recovery, then publish again", e.Merge, e.Found, e.Expected)
}

// mergedOnto is ErrUnsafeMerge's expectation for dude's merge of commit:
// first parent at, or what only merged baseBranch in on top of it.
func mergedOnto(at, baseBranch, commit string) string {
	return fmt.Sprintf("first parent %s or a merge of %s on it, and second parent the writer's commit %s", at, baseBranch, commit)
}

func parentsOf(parents []string) string {
	return fmt.Sprintf("has parents [%s]", strings.Join(parents, " "))
}

// Branch moves branch to commit, a writer's work started from start, and
// is the one way a writer's publish moves a task branch: a fast-forward,
// never a force, then Settle with its answer.
func Branch(ctx context.Context, gh *forge.GitHub, slug, branch, baseBranch, start, commit string, attempted func() (bool, error), beforeMerge ...func() error) (Result, error) {
	return Settle(ctx, gh, slug, branch, baseBranch, start, commit,
		gh.FastForward(ctx, slug, branch, commit), attempted, beforeMerge...)
}

// Existing is Branch for a branch that must exist already: a deleted one
// is GitHub's refusal, never created again.
func Existing(ctx context.Context, gh *forge.GitHub, slug, branch, baseBranch, start, commit string, attempted func() (bool, error), beforeMerge ...func() error) (Result, error) {
	return Settle(ctx, gh, slug, branch, baseBranch, start, commit,
		gh.FastForwardExisting(ctx, slug, branch, commit), attempted, beforeMerge...)
}

// Settle goes on from a fast-forward of branch to commit that answered
// refusal. When GitHub refused it as not a fast-forward and the branch's
// head X only merged baseBranch in since start (BaseMerges), commit is
// merged into the branch on GitHub (POST /merges) and that merge is the
// new head; a 204, commit already in the branch, keeps the branch's head.
// Any other move of the branch (a person's commit, a rebase, a force-push)
// returns refusal, and a merge GitHub refuses is ErrBaseMergeConflict.
//
// GitHub's merges endpoint takes no expected head: a branch that moves
// between the judgement and the merge is judged again from the merge's
// first parent, and an error says so when that is not only base merges.
//
// Bound: the judgement and the re-judgement inspect at most
// maxBaseMerges merges each, 2 × maxBaseMerges in all. Each inspected
// merge costs up to four GitHub reads (provenance, two compares, base
// containment), so a fallback makes about 48 reads at most, and one write
// (POST /merges) besides the fast-forward.
//
// A head that is already a pure merge of commit is adopted only when
// attempted says so: the caller durably recorded, in beforeMerge, sending
// that merge itself. Otherwise someone else made it, and it is refused like
// any other move. attempted is called only for such a head, never on a
// fast-forward or an ordinary fallback.
func Settle(ctx context.Context, gh *forge.GitHub, slug, branch, baseBranch, start, commit string, refusal error, attempted func() (bool, error), beforeMerge ...func() error) (Result, error) {
	if refusal == nil || !NotFastForward(refusal) || start == "" || baseBranch == "" {
		return Result{Head: commit}, refusal
	}
	at, err := gh.BranchSHA(ctx, slug, branch)
	if err != nil || at == commit {
		return Result{Head: commit}, err
	}
	v, under, err := Admit(ctx, gh, slug, baseBranch, start, at, commit, attempted)
	switch {
	case err != nil:
		return Result{}, err
	case v == Refuse:
		return Result{}, refusal
	case v == Adopt:
		// commit merged already, by an attempt whose answer was lost.
		return Result{Head: at, Commit: commit, MergedInto: under}, nil
	}
	if len(beforeMerge) > 0 {
		if err := beforeMerge[0](); err != nil {
			return Result{}, err
		}
	}
	m, err := gh.MergeCommit(ctx, slug, branch, commit)
	var fe *forge.Error
	switch {
	case errors.As(err, &fe) && fe.Status == 409:
		return Result{}, &ErrBaseMergeConflict{base: baseBranch, into: at, commit: commit, cause: err}
	case err != nil:
		return Result{}, err
	case m.SHA == "":
		return Result{Head: at}, nil
	}
	if len(m.Parents) == 2 && m.Parents[0] == at {
		return Result{Head: m.SHA, Commit: commit, MergedInto: at}, nil
	}
	into := ""
	if len(m.Parents) > 0 {
		into = m.Parents[0]
	}
	if len(m.Parents) != 2 || into == "" {
		return Result{}, &ErrUnsafeMerge{Merge: m.SHA, Found: parentsOf(m.Parents), Expected: mergedOnto(at, baseBranch, commit)}
	}
	j, err := BaseMerges(ctx, gh, slug, baseBranch, start, into, commit)
	if err != nil {
		return Result{}, err
	}
	if !j.Only && into != start {
		return Result{}, &ErrUnsafeMerge{Merge: m.SHA, Found: parentsOf(m.Parents), Expected: mergedOnto(at, baseBranch, commit)}
	}
	pure, err := pureMerge(ctx, gh, slug, into, m.Parents[1], m.SHA)
	if err != nil {
		return Result{}, err
	}
	if !pure {
		return Result{}, &ErrUnsafeMerge{Merge: m.SHA, Found: parentsOf(m.Parents) + " and content beyond its parents' automatic merge",
			Expected: "GitHub's automatic merge of the writer's commit " + commit + " onto " + into}
	}
	return Result{Head: m.SHA, Commit: commit, MergedInto: into}, nil
}

// Verdict is Admit's decision on a head found where start was expected.
type Verdict int

const (
	// Refuse: someone else moved the branch; it is not merged over.
	Refuse Verdict = iota
	// MergeIn: only GitHub's pure merges of the base since start.
	MergeIn
	// Adopt: as MergeIn, topped by GitHub's pure merge of commit that
	// the caller recorded attempting.
	Adopt
)

// Admit judges head, found on a task branch where a writer's start was
// expected, for the writer's commit (BaseMerges). A head that is already
// a merge of commit is adopted only when attempted says the caller sent
// it, and under is that merge's first parent. A compare GitHub refuses
// (history rewritten) is Refuse with no error, as is a writer with no
// start or base branch to judge against.
func Admit(ctx context.Context, gh *forge.GitHub, slug, baseBranch, start, head, commit string, attempted func() (bool, error)) (v Verdict, under string, err error) {
	if start == "" || baseBranch == "" {
		return Refuse, "", nil
	}
	j, err := BaseMerges(ctx, gh, slug, baseBranch, start, head, commit)
	switch {
	case forge.Refused(err):
		return Refuse, "", nil
	case err != nil:
		return Refuse, "", err
	case !j.Only:
		return Refuse, "", nil
	case !j.Own:
		return MergeIn, "", nil
	}
	ours, err := attempted()
	if err != nil || !ours {
		return Refuse, "", err
	}
	return Adopt, j.Under, nil
}

// NotFastForward says GitHub refused a ref update for not being a
// fast-forward (422 "Update is not a fast forward").
func NotFastForward(err error) bool {
	var e *forge.Error
	return errors.As(err, &e) && e.Status == 422 && strings.Contains(strings.ToLower(e.Message), "fast forward")
}

// Judgement is BaseMerges' answer. Only: head is start with nothing on
// top but merges of the base. Own: head itself is the merge of commit,
// on Under.
type Judgement struct {
	Only, Own bool
	Under     string
}

// pureMerge verifies that merge is GitHub's (web-flow, verified) and added
// exactly first's changes to second: compare second...first and compare
// second...merge list the same files, each with the same status and the
// same resulting blob SHA.
//
// Sound against false positives: any content in merge beyond first's
// changes either adds a path to second...merge or changes a listed path's
// blob SHA, and a blob SHA is the hash of the file's whole content.
//
// Conservative: a file both parents changed, which git merged cleanly,
// has a merge blob different from first's, so the merge is judged impure
// and the writer's fast-forward refusal stands.
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

// BaseMerges judges whether head moved from start only by merges of
// baseBranch into it. It does when every commit on head's first-parent
// line down to start is a two-parent merge whose second parent baseBranch
// contains (compare second...baseBranch behind by none), and that line
// reaches start (compare start...head lists its commits with their
// parents). Every other commit start..head holds is then an ancestor of
// such a second parent: the base's. The topmost may instead have commit
// as its second parent and be GitHub's pure merge of it onto its first
// parent: dude's own merge of it (Own).
//
// Costs one compare, plus one per merge, at most maxBaseMerges. A commit
// list GitHub cut short, or more merges, is judged no.
func BaseMerges(ctx context.Context, gh *forge.GitHub, slug, baseBranch, start, head, commit string) (Judgement, error) {
	if head == "" || head == start {
		return Judgement{}, nil
	}
	cmp, err := gh.CompareCommits(ctx, slug, start, head)
	if err != nil || cmp.BehindBy > 0 || len(cmp.Commits) < cmp.TotalCommits {
		return Judgement{}, err
	}
	parents := map[string][]string{}
	for _, c := range cmp.Commits {
		parents[c.SHA] = []string{}
		for _, p := range c.Parents {
			parents[c.SHA] = append(parents[c.SHA], p.SHA)
		}
	}
	var j Judgement
	merges := 0
	for at := head; at != start; at = parents[at][0] {
		ps, listed := parents[at]
		if !listed || len(ps) != 2 || merges == maxBaseMerges {
			return Judgement{}, nil
		}
		merges++
		if at == head && ps[1] == commit {
			// dude's own merge must be exactly the first parent plus
			// commit's change, made by GitHub.
			pure, err := pureMerge(ctx, gh, slug, commit, ps[0], at)
			if err != nil || !pure {
				return Judgement{}, err
			}
			j.Own, j.Under = true, ps[0]
			continue
		}
		pure, err := pureMerge(ctx, gh, slug, ps[0], ps[1], at)
		if err != nil {
			return Judgement{}, err
		}
		if !pure {
			return Judgement{}, nil
		}
		in, err := gh.Compare(ctx, slug, ps[1], baseBranch)
		if err != nil || in.BehindBy > 0 {
			return Judgement{}, err
		}
	}
	j.Only = true
	return j, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
