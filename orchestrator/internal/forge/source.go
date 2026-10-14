package forge

import (
	"context"
	"fmt"
)

// What dude reads of a pull request or branch it is asked to take over
// (adopted work): enough to refuse what it must not touch.

type ghRepo struct {
	FullName string `json:"full_name"`
}

// PullRequestSource is an existing pull request as an adopted task starts
// from it.
type PullRequestSource struct {
	PullRequestRef
	Title string
	// The branch it is from and the one it merges into.
	HeadRef, BaseRef string
	// The repositories those branches are in, owner/name: a fork's head is
	// in another; "" when GitHub no longer has the head's (a deleted fork).
	HeadRepo, BaseRepo string
}

// Fork says the pull request's head is not in its base's repository.
func (p PullRequestSource) Fork() bool { return p.HeadRepo == "" || p.HeadRepo != p.BaseRepo }

// PullRequestSource reads pull request number of slug.
func (g *GitHub) PullRequestSource(ctx context.Context, slug string, number int) (PullRequestSource, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return PullRequestSource{}, err
	}
	out := PullRequestSource{PullRequestRef: p.ref(), Title: p.Title, HeadRef: p.Head.Ref, BaseRef: p.Base.Ref}
	if p.Head.Repo != nil {
		out.HeadRepo = p.Head.Repo.FullName
	}
	if p.Base.Repo != nil {
		out.BaseRepo = p.Base.Repo.FullName
	}
	return out, nil
}

// BranchInfo is a branch as GitHub's branches endpoint has it.
type BranchInfo struct {
	SHA string
	// Branch protection or a ruleset applies to it: GitHub's own answer,
	// not a guess from its name.
	Protected bool
}

// Branch reads a branch; nil when slug has no such branch.
func (g *GitHub) Branch(ctx context.Context, slug, branch string) (*BranchInfo, error) {
	var b struct {
		Protected bool `json:"protected"`
		Commit    struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	err := g.do(ctx, "GET", "/repos/"+slug+"/branches/"+branch, nil, &b)
	var e *Error
	if asError(err, &e) && e.NotFound() {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &BranchInfo{SHA: b.Commit.SHA, Protected: b.Protected}, nil
}
