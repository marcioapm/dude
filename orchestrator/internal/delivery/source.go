package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Adopted work: a task that starts from an existing pull request or branch
// instead of the default branch. The source is checked against GitHub when
// it is attached; its head branch becomes the task branch, its base what
// every diff and the pull request measure against.

// Source kinds.
const (
	SourcePR     = "pr"
	SourceBranch = "branch"
)

// SourceRequest is a source as a person names it.
type SourceRequest struct {
	Kind         string `json:"kind"`
	RepositoryID string `json:"repositoryId"`
	// A pull request: its number, or its URL.
	PullRequest string `json:"pullRequest,omitempty"`
	// A branch, and the base it is to merge into ("" is the repository's
	// default branch).
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	// The task it is for, when it exists: its own source is not "another
	// task's branch".
	TaskID string `json:"taskId,omitempty"`
	// The mode asked for, "" for the default. ModeReviewOnly is checked by
	// its own rules: read access, a pull request only, a protected head
	// allowed.
	Mode string `json:"mode,omitempty"`
	// A person's confirmation that the live task holding the branch is to
	// be stopped for this one: true, or that task's id, refused when the
	// holder is another by now.
	Supersede     bool   `json:"supersede,omitempty"`
	SupersedeTask string `json:"supersedeTask,omitempty"`
}

// What a delivery does first with the work it took over (task_sources.mode).
const (
	// An implementer carries the work on, then the review loop.
	ModeContinue = "continue"
	// The review loop at once, and its fixes.
	ModeReview = "review"
	// Review a person's pull request and change no code: no writer Run is
	// ever made for it (refuseReviewOnlyWriterTx).
	ModeReviewOnly = "review_only"
)

// Source is a checked source, as task_sources keeps it.
type Source struct {
	Kind         string `json:"kind"`
	RepositoryID string `json:"repositoryId"`
	Repository   string `json:"repository"`
	Number       int    `json:"number,omitempty"`
	Title        string `json:"title,omitempty"`
	URL          string `json:"url,omitempty"`
	HeadBranch   string `json:"headBranch"`
	BaseBranch   string `json:"baseBranch"`
	// ModeContinue or ModeReview. A check answers the default for what the
	// source is: a draft pull request or a bare branch is unfinished work.
	// A delivery state from before modes has "": review.
	Mode string `json:"mode,omitempty"`
	// The head when the delivery took it over: a later head is someone's
	// work on it, dude's or a person's.
	HeadSHA string `json:"headSha,omitempty"`
	// The live task the check found holding the branch, which a person
	// confirmed this one supersedes; nil for none. Not kept.
	Supersedes *SourceHolder `json:"supersedes,omitempty"`
}

// SourceHolder is a live task holding a branch: its id, key and source
// mode ("" for one on its own new work).
type SourceHolder struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Mode string `json:"mode"`
}

// SourceRefusal is a source dude will not adopt, with why in a person's
// words. Code names the rule, for the UI and the tests. Holder is the
// live task in the way, for superseded_required.
type SourceRefusal struct {
	Code, Msg string
	Holder    *SourceHolder
}

func (r SourceRefusal) Error() string { return r.Msg }

func sourceRefusal(code, format string, a ...any) error {
	return SourceRefusal{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// SourceUnreachable: GitHub could not be asked (no credential, down, rate
// limited); nothing about the source was decided.
type SourceUnreachable struct{ Err error }

func (e SourceUnreachable) Error() string {
	return "GitHub could not be asked about it: " + e.Err.Error()
}
func (e SourceUnreachable) Unwrap() error { return e.Err }

// CheckSource checks in against the project and GitHub, and returns it
// resolved: a refusal (SourceRefusal) says what rule it breaks.
func CheckSource(ctx context.Context, d *db.DB, gh *forge.GitHub, org, projectID string, in SourceRequest) (Source, error) {
	out := Source{Kind: in.Kind, RepositoryID: in.RepositoryID}
	if in.Kind != SourcePR && in.Kind != SourceBranch {
		return out, sourceRefusal("bad_kind", "a source is a pull request (pr) or a branch (branch)")
	}
	var repoURL, defaultBranch string
	err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT name, url, default_branch FROM repositories WHERE id = $1 AND project_id = $2`,
			in.RepositoryID, projectID).Scan(&out.Repository, &repoURL, &defaultBranch)
	})
	if db.IsNotFound(err) {
		return out, sourceRefusal("not_in_project", "that repository is not one of this project's")
	}
	if err != nil {
		return out, err
	}
	slug := forge.SlugFromURL(repoURL)
	if slug == "" {
		return out, sourceRefusal("not_github", "%s is not on GitHub: only a GitHub repository's work can be adopted", out.Repository)
	}
	if gh == nil {
		return out, SourceUnreachable{errors.New("GitHub is not connected for this organisation")}
	}
	reviewOnly := in.Mode == ModeReviewOnly
	if reviewOnly && in.Kind != SourcePR {
		return out, sourceRefusal("bad_kind", "review only reviews a pull request: name one, not a branch")
	}
	// Review only never pushes: reading the pull request below proves read access.
	if !reviewOnly {
		if err := gh.CheckPushAccess(ctx, repoURL); err != nil {
			if forge.Refused(err) {
				return out, sourceRefusal("no_write", "dude may not push to %s with this organisation's GitHub connection: %v", slug, err)
			}
			return out, SourceUnreachable{err}
		}
	}
	defaultBranch, err = gh.SourceDefaultBranch(ctx, slug)
	if err != nil {
		return out, unreachableOr(err)
	}
	switch in.Kind {
	case SourcePR:
		n, err := gh.SourcePullNumber(in.PullRequest, slug)
		if err != nil {
			return out, sourceRefusal("bad_pull_request", "%v", err)
		}
		pr, err := gh.PullRequestSource(ctx, slug, n)
		var e *forge.Error
		if errors.As(err, &e) && e.NotFound() {
			return out, sourceRefusal("pr_not_found", "%s has no pull request #%d", slug, n)
		}
		if errors.As(err, &e) && e.Status == 403 {
			return out, sourceRefusal("no_read", "dude may not read %s with this organisation's GitHub connection: %v", slug, err)
		}
		if err != nil {
			return out, unreachableOr(err)
		}
		if pr.State == forge.StateMerged || pr.State == forge.StateClosed {
			return out, sourceRefusal("pr_"+pr.State, "pull request #%d is %s: only an open one can be adopted", n, pr.State)
		}
		if (pr.Fork() || !strings.EqualFold(pr.BaseRepo, slug)) && reviewOnly {
			return out, sourceRefusal("pr_fork", "pull request #%d is from a fork: dude reviews only this repository's own pull requests for now", n)
		}
		if pr.Fork() || !strings.EqualFold(pr.BaseRepo, slug) {
			return out, sourceRefusal("pr_fork", "pull request #%d is from a fork: its branch is not in %s, and dude cannot push to it",
				n, slug)
		}
		out.Number, out.Title, out.URL, out.HeadBranch, out.BaseBranch = n, pr.Title, pr.URL, pr.HeadRef, pr.BaseRef
		out.Mode = ModeReview
		if reviewOnly {
			out.Mode = ModeReviewOnly
		} else if pr.State == forge.StateDraft {
			out.Mode = ModeContinue
		}
	case SourceBranch:
		out.Mode = ModeContinue
		out.HeadBranch, out.BaseBranch = strings.TrimSpace(in.Branch), strings.TrimSpace(in.Base)
		if out.HeadBranch == "" {
			return out, sourceRefusal("bad_branch", "name the branch to start from")
		}
		if out.BaseBranch == "" {
			out.BaseBranch = defaultBranch
		}
	}
	// What the branch is comes first: the default branch is its own default
	// base, and is refused for being the default branch.
	if out.HeadBranch == defaultBranch {
		return out, sourceRefusal("default_branch", "%s is %s's default branch: dude never works on it directly", out.HeadBranch, slug)
	}
	// A release line by its name alone; GitHub's protection is asked below.
	if strings.HasPrefix(out.HeadBranch, "release/") {
		return out, sourceRefusal("release_branch", "%s is a release branch: dude never works on one directly", out.HeadBranch)
	}
	info, err := gh.Branch(ctx, slug, out.HeadBranch)
	if err != nil {
		return out, unreachableOr(err)
	}
	if info == nil {
		return out, sourceRefusal("branch_not_found", "%s has no branch %s", slug, out.HeadBranch)
	}
	if info.Protected && !reviewOnly {
		return out, sourceRefusal("protected", "GitHub protects %s: dude only fast-forwards branches nobody protects", out.HeadBranch)
	}
	if in.Kind == SourceBranch {
		if out.BaseBranch == out.HeadBranch {
			return out, sourceRefusal("same_branch", "the branch and its base are the same branch")
		}
		base, err := gh.Branch(ctx, slug, out.BaseBranch)
		if err != nil {
			return out, unreachableOr(err)
		}
		if base == nil {
			return out, sourceRefusal("branch_not_found", "%s has no branch %s", slug, out.BaseBranch)
		}
	}
	err = d.InOrg(ctx, org, func(tx pgx.Tx) error {
		out.Supersedes, err = supersedeOrRefuseTx(ctx, tx, in.RepositoryID, out.HeadBranch, in.TaskID, in.Supersede, in.SupersedeTask)
		return err
	})
	return out, err
}

// supersedeOrRefuseTx is the live task holding branch in repo, which the
// asking task may take over only as a person confirmed (supersede, or
// expect naming that very task): nil for none. Without it, or when the
// holder is another than expect, it is superseded_required naming the
// holder.
func supersedeOrRefuseTx(ctx context.Context, tx pgx.Tx, repo, branch, taskID string, supersede bool, expect string) (*SourceHolder, error) {
	var h SourceHolder
	err := tx.QueryRow(ctx, branchHolder, repo, branch, taskID).Scan(&h.ID, &h.Key, &h.Mode)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expect != "" && expect != h.ID {
		return nil, SourceRefusal{Code: "superseded_required", Holder: &h,
			Msg: fmt.Sprintf("%s is now the branch of %s, not of the task you confirmed: confirm again to stop %s for this task", branch, h.Key, h.Key)}
	}
	if !supersede && expect == "" {
		return nil, SourceRefusal{Code: "superseded_required", Holder: &h,
			Msg: fmt.Sprintf("%s is already the branch of %s, which is still in progress: confirm to stop %s and take the work over in this task", branch, h.Key, h.Key)}
	}
	return &h, nil
}

// branchHolder (SQL; $1 repository, $2 branch, $3 the task asking): the id,
// key and source mode of another task that is not over whose task branch is
// $2 in $1 — adopted as its source (review only too: a writer on the same
// pull request supersedes it, and it them), or its live delivery's own
// branch. The control plane's setTaskSource asks the same.
const branchHolder = `SELECT t.id, p.key_prefix || '-' || t.number,
	COALESCE((SELECT s.mode FROM task_sources s WHERE s.task_id = t.id), '') FROM tasks t JOIN projects p ON p.id = t.project_id
	WHERE t.id IS DISTINCT FROM NULLIF($3, '') AND t.status NOT IN ('done', 'aborted', 'failed')
	  AND (EXISTS (SELECT 1 FROM task_sources s WHERE s.task_id = t.id AND s.repository_id = $1 AND s.head_branch = $2)
	    OR EXISTS (SELECT 1 FROM workflow_runs w JOIN task_repositories tr ON tr.task_id = w.task_id AND tr.repository_id = $1
	      WHERE w.task_id = t.id AND w.status IN ('running', 'waiting') AND w.state->>'branch' = $2))
	ORDER BY t.created_at LIMIT 1`

func unreachableOr(err error) error {
	if forge.Transient(err) {
		return SourceUnreachable{err}
	}
	return err
}

// NewState is a new delivery's state: on the branch BranchFor names for
// the attempt, or, for a task that took over existing work, on its
// source's head branch, measured against its base. Attempt must be positive.
func NewState(ctx context.Context, tx pgx.Tx, taskID, projectID string, policy Policy, attempt int) (State, error) {
	if attempt < 1 {
		return State{}, refuseText(rChildPositiveAttemptRequired)
	}
	st := State{TaskID: taskID, ProjectID: projectID, Policy: policy, Attempt: attempt, Branch: BranchFor(taskID, attempt)}
	src, err := TaskSource(ctx, tx, taskID)
	if err != nil || src == nil {
		return st, err
	}
	if src.ReviewOnly() {
		// It writes nothing, so it holds no branch; and its tester runs only
		// when its conductor starts one.
		st.Policy.Test = false
	} else if err := HoldBranchTx(ctx, tx, taskID, src); err != nil {
		return st, err
	}
	st.Branch, st.Source = src.HeadBranch, src
	st.Bases = map[string]string{src.Repository: src.BaseBranch}
	return st, nil
}

// HoldBranchTx refuses (superseded_required) when another live task holds
// src's head branch, under src's repository row lock held to tx's end. A
// start never supersedes: that is a person's confirmation on the source.
func HoldBranchTx(ctx context.Context, tx pgx.Tx, taskID string, src *Source) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM repositories WHERE id = $1 FOR UPDATE`, src.RepositoryID); err != nil {
		return err
	}
	_, err := supersedeOrRefuseTx(ctx, tx, src.RepositoryID, src.HeadBranch, taskID, false, "")
	return err
}

// BaseFor is what repo's task branch merges into: its stored base, else
// its default branch.
func (st *State) BaseFor(repo Repository) string {
	if b := st.Bases[repo.Name]; b != "" {
		return b
	}
	return repo.DefaultBranch
}

// TaskBranch is where an attempt's work goes, as its delivery keeps it.
type TaskBranch struct {
	Branch string
	Bases  map[string]string
	Source *Source
	// How its conductor speaks, when one decides (State.Voice).
	Voice string
}

// TaskBranchTx is the task branch of the task's attempt: its delivery's
// stored branch, else BranchFor's (an attempt with no delivery yet, or one
// from before the branch was kept).
func TaskBranchTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) (TaskBranch, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT w.state FROM workflow_runs w WHERE w.task_id = $1 AND w.workflow_type = $3
		AND COALESCE(NULLIF(w.state->>'attempt', '')::int, $2) = $2 ORDER BY w.created_at DESC LIMIT 1`,
		taskID, attempt, WorkflowType).Scan(&raw)
	if err != nil && !db.IsNotFound(err) {
		return TaskBranch{}, err
	}
	var st State
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			return TaskBranch{}, fmt.Errorf("decode delivery state: %w", err)
		}
	}
	if st.Branch == "" {
		st.Branch = BranchFor(taskID, attempt)
	}
	return TaskBranch{Branch: st.Branch, Bases: st.Bases, Source: st.Source, Voice: st.voice()}, nil
}

// TaskSource is the task's source, nil when it starts from new work.
func TaskSource(ctx context.Context, tx pgx.Tx, taskID string) (*Source, error) {
	var s Source
	var number *int
	var title *string
	err := tx.QueryRow(ctx, `SELECT s.kind, s.repository_id, r.name, s.number, s.title, s.head_branch, s.base_branch, s.mode
		FROM task_sources s JOIN repositories r ON r.id = s.repository_id WHERE s.task_id = $1`, taskID).
		Scan(&s.Kind, &s.RepositoryID, &s.Repository, &number, &title, &s.HeadBranch, &s.BaseBranch, &s.Mode)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if number != nil {
		s.Number = *number
	}
	if title != nil {
		s.Title = *title
	}
	return &s, nil
}
