package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// adopt starts a delivery that took over existing work: nothing to
// implement, so it goes to review. Where the work is the task branch's
// head as GitHub has it now; an adopted pull request is recorded as the
// task's, so the pull request step opens none and its feedback loop
// follows it.
func (w *steps) adopt(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	src := st.Source
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		return workflow.Result{}, err
	}
	if gh == nil {
		return workflow.Result{}, fmt.Errorf("no forge credential to read %s with", src.HeadBranch)
	}
	var repo Repository
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		repos, err := TaskRepositories(ctx, tx, st.TaskID)
		for _, r := range repos {
			if r.ID == src.RepositoryID {
				repo = r
			}
		}
		return err
	}); err != nil {
		return workflow.Result{}, err
	}
	if repo.ID == "" {
		return w.escalate(ctx, sc, st, "source_gone", map[string]any{"repository": src.Repository})
	}
	checked, err := CheckSource(ctx, w.s.DB, gh, sc.OrganizationID, st.ProjectID, SourceRequest{
		Kind: src.Kind, RepositoryID: src.RepositoryID, PullRequest: fmt.Sprint(src.Number),
		Branch: src.HeadBranch, Base: src.BaseBranch, TaskID: st.TaskID,
	})
	if err != nil {
		var refusal SourceRefusal
		if errors.As(err, &refusal) {
			return w.escalate(ctx, sc, st, "source_changed", map[string]any{"reason": sourceChanged(refusal.Error()), "code": refusal.Code})
		}
		return workflow.Result{}, err
	}
	if checked.HeadBranch != src.HeadBranch || checked.BaseBranch != src.BaseBranch {
		return w.escalate(ctx, sc, st, "source_changed", map[string]any{"reason": sourceChanged(fmt.Sprintf(
			"it was %s → %s, and is now %s → %s", src.HeadBranch, src.BaseBranch, checked.HeadBranch, checked.BaseBranch))})
	}
	slug := forge.SlugFromURL(repo.URL)
	head, err := gh.BranchSHA(ctx, slug, src.HeadBranch)
	if err != nil {
		return workflow.Result{}, err
	}
	if head == "" {
		return w.escalate(ctx, sc, st, "source_gone", map[string]any{"repository": src.Repository, "branch": src.HeadBranch})
	}
	if src.Kind == SourcePR {
		pr, err := gh.PullRequest(ctx, slug, src.Number)
		if err != nil {
			return workflow.Result{}, err
		}
		if pr.State == forge.StateMerged || pr.State == forge.StateClosed {
			return w.escalate(ctx, sc, st, "source_gone", map[string]any{"repository": src.Repository, "number": src.Number,
				"state": pr.State})
		}
		id, err := w.s.recordAdopted(ctx, sc.OrganizationID, st, repo, pr)
		if err != nil {
			return workflow.Result{}, err
		}
		if w.s.SyncPullRequest != nil {
			if err := w.s.SyncPullRequest(ctx, sc.OrganizationID, id); err != nil {
				return workflow.Result{}, err
			}
		}
		st.PullRequestIDs = []string{id}
	}
	// What it changed is what the reviewers are picked by.
	changed, err := gh.ChangedFiles(ctx, slug, src.BaseBranch, head)
	if err != nil {
		return workflow.Result{}, err
	}
	st.Source.HeadSHA = head
	st.Heads = map[string]string{repo.Name: head}
	st.ChangedPaths = nil
	for _, p := range changed {
		st.ChangedPaths = append(st.ChangedPaths, repo.Name+"/"+p)
	}
	return w.next(ctx, sc, st, PointImplemented, "review",
		fmt.Sprintf("Took over %s at %s, %d files changed against %s.", src.HeadBranch, short(head), len(changed), src.BaseBranch))
}

// adoptedPR says the delivery took over a pull request.
func (st *State) adoptedPR() bool { return st.Source != nil && st.Source.Kind == SourcePR }

// sourceChanged is a source_changed escalation's reason: the saved source
// is immutable once delivery started, so only these two ways out work.
func sourceChanged(what string) string {
	return "GitHub's pull request (or branch) no longer matches what this task adopted: " + what +
		". Put it back on GitHub and Try again, or stop this task and adopt it in a new one."
}

// keepAdoptedPullRequest is the pull request step of a delivery that took
// one over: what the loop pushed on it is put before its reviewers again,
// and the delivery waits on it as on one it opened.
func (w *steps) keepAdoptedPullRequest(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, error) {
	if len(st.PullRequestIDs) == 0 {
		return workflow.Result{}, fmt.Errorf("the pull request %s took over is not recorded", st.TaskID)
	}
	if st.Source.HeadSHA != "" && st.Heads[st.Source.Repository] != st.Source.HeadSHA {
		gh, err := w.forges.For(ctx, sc.OrganizationID)
		if err != nil {
			return workflow.Result{}, err
		}
		if err := w.s.RerequestReviews(ctx, sc.OrganizationID, st, gh, st.Heads); err != nil {
			return workflow.Result{}, err
		}
	}
	if err := w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "pull request open"); err != nil {
		return workflow.Result{}, err
	}
	return waitForPR(st), nil
}

// recordAdopted records an adopted pull request as the task's, as GitHub
// has it now; one already recorded (a replay) is the one.
func (s *Store) recordAdopted(ctx context.Context, org string, st *State, repo Repository, pr forge.Status) (string, error) {
	src := st.Source
	id := ids.New(ids.PullRequest)
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		// The task's now: an attempt started over takes it from the last.
		tag, err := tx.Exec(ctx, `
			INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, node_id, url,
			                           head_branch, base_branch, head_sha, title, state, checks, adopted)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::pull_request_state, 'unknown', true)
			ON CONFLICT (repository_id, number) DO UPDATE SET task_id = EXCLUDED.task_id, adopted = true, updated_at = now()`,
			id, org, st.ProjectID, st.TaskID, repo.ID, src.Number, db.Nullable(pr.NodeID), pr.URL, src.HeadBranch,
			src.BaseBranch, pr.HeadSHA, src.Title, pr.State)
		if err != nil {
			return err
		}
		var announced bool
		if err := tx.QueryRow(ctx, `SELECT id, EXISTS (SELECT 1 FROM events WHERE task_id = $3 AND event_type = $4
				AND (payload->>'number')::int = $2 AND payload->>'repo' = $5)
			FROM pull_requests WHERE repository_id = $1 AND number = $2`,
			repo.ID, src.Number, st.TaskID, EvPullRequestOpened, repo.Name).Scan(&id, &announced); err != nil || announced || tag.RowsAffected() == 0 {
			return err
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{
			Type: EvPullRequestOpened, OrganizationID: org, ProjectID: st.ProjectID, TaskID: st.TaskID,
			ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID,
			Payload: map[string]any{"number": src.Number, "url": pr.URL, "repo": repo.Name, "headBranch": src.HeadBranch,
				"baseBranch": src.BaseBranch, "draft": pr.State == forge.StateDraft, "adopted": true,
				"reviewersRequested": []string{}},
		})
		return err
	})
	return id, err
}
