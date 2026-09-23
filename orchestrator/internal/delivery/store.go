package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Event types this package writes. The string values are the contract with
// the backend and the browser (packages/domain/src/events/types.ts).
const (
	EvRunCreated            = "run.created"
	EvWorkItemStatusChanged = "work_item.status_changed"
	EvQuestionAsked         = "question.asked"
	EvReviewCompleted       = "review.completed"
	EvPullRequestOpened     = "pull_request.opened"
	EvPullRequestUpdated    = "pull_request.updated"
	EvPullRequestChecks     = "pull_request.checks_changed"
	EvPullRequestReviewed   = "pull_request.reviewed"
	EvPullRequestCommented  = "pull_request.commented"
	EvPullRequestMerged     = "pull_request.merged"
	EvPullRequestClosed     = "pull_request.closed"
	EvGitCommitCreated      = "git.commit_created"
)

// Store is the delivery workflow's side effects. Each is one durable action;
// the runtime persists every transition before the next step, so a crash
// between two of these replays rather than skips — which is why each one is
// safe to repeat.
type Store struct {
	DB *db.DB
}

// runByKey finds the Run a workflow step already created, or "".
func (s *Store) runByKey(ctx context.Context, org, workItemID, key string) (string, error) {
	var id string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		id, err = runByKey(ctx, tx, workItemID, key)
		return err
	})
	return id, err
}

func runByKey(ctx context.Context, tx pgx.Tx, workItemID, key string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM runs WHERE work_item_id = $1 AND creation_key = $2`, workItemID, key).Scan(&id)
	if db.IsNotFound(err) {
		return "", nil
	}
	return id, err
}

// PhaseRun describes a phase Run to create.
type PhaseRun struct {
	WorkItemID   string
	RepositoryID string
	Phase        string
	// The commit this phase starts from; "" means the default branch.
	BaseRef     string
	ParentRunID string
	Category    string
	FindingIDs  []string
	PRFeedback  []forge.ActionableFeedback
	// Makes creation idempotent: a step that runs twice after a crash finds
	// the Run the first attempt made instead of creating a second.
	Key string
}

// CreatePhaseRun creates a Run for one phase, pending, for the lux
// dispatcher to pick up.
//
// Phase Runs of one attempt share its number: a review beside an implement
// is the same attempt, not a new one.
func (s *Store) CreatePhaseRun(ctx context.Context, org string, in PhaseRun) (string, error) {
	var runID string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if in.Key != "" {
			var err error
			if runID, err = runByKey(ctx, tx, in.WorkItemID, in.Key); err != nil || runID != "" {
				return err
			}
		}
		var projectID string
		var attempt int
		if err := tx.QueryRow(ctx, `
			SELECT w.project_id, COALESCE((SELECT max(attempt) FROM runs WHERE work_item_id = w.id), 1)
			FROM work_items w WHERE w.id = $1`, in.WorkItemID).Scan(&projectID, &attempt); err != nil {
			return fmt.Errorf("work item %s: %w", in.WorkItemID, err)
		}
		feedback, _ := json.Marshal(db.NonNil(in.PRFeedback))
		runID = ids.New(ids.Run)
		if _, err := tx.Exec(ctx, `
			INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status, phase, role,
			                  parent_run_id, base_ref, category, pr_feedback, repository_id, creation_key)
			VALUES ($1, $2, $3, $4, $5, 'pending', $6::run_phase, $7::agent_role, $8, $9, $10, $11::jsonb, $12, $13)`,
			runID, org, projectID, in.WorkItemID, attempt, in.Phase, RoleForPhase[in.Phase],
			db.Nullable(in.ParentRunID), db.Nullable(in.BaseRef), db.Nullable(in.Category), feedback,
			db.Nullable(in.RepositoryID), db.Nullable(in.Key)); err != nil {
			return err
		}
		payload := map[string]any{
			"attempt": attempt, "phase": in.Phase, "role": RoleForPhase[in.Phase],
			"publishes": Publishes[in.Phase], "baseRef": db.Nullable(in.BaseRef),
		}
		if in.Category != "" {
			payload["category"] = in.Category
		}
		if len(in.FindingIDs) > 0 {
			payload["findingIds"] = in.FindingIDs
		}
		if len(in.PRFeedback) > 0 {
			payload["prFeedbackCount"] = len(in.PRFeedback)
		}
		_, err := ledger.Append(ctx, tx, ledger.Event{
			Type: EvRunCreated, OrganizationID: org, ProjectID: projectID, WorkItemID: in.WorkItemID, RunID: runID,
			ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
			CorrelationID: in.WorkItemID, Payload: payload,
		})
		return err
	})
	return runID, err
}

// Outcome is what a finished phase Run produced.
type Outcome struct {
	Succeeded bool
	Error     string
	HeadSHA   string
	// What the phase changed: selects the conditional reviewers and retires
	// findings about files a fix rewrote.
	ChangedPaths []string
}

func (s *Store) PhaseOutcome(ctx context.Context, org, runID string) (Outcome, error) {
	var o Outcome
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var status string
		var errText, head *string
		err := tx.QueryRow(ctx, `SELECT status::text, error, head_sha, changed_paths FROM runs WHERE id = $1`, runID).
			Scan(&status, &errText, &head, &o.ChangedPaths)
		if db.IsNotFound(err) {
			o.Error = "run not found"
			return nil
		}
		o.Succeeded = status == "completed"
		o.Error, o.HeadSHA = deref(errText), deref(head)
		return err
	})
	return o, err
}

func (s *Store) Findings(ctx context.Context, org, workItemID string) ([]FindingState, error) {
	var out []FindingState
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, severity::text, status::text, fix_attempts FROM review_findings
			WHERE work_item_id = $1 ORDER BY created_at`, workItemID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[FindingState])
		return err
	})
	return out, err
}

// MarkAttempted counts one fix attempt against each finding the fixer was
// given — before the attempt, so a fixer that crashes still spends one, and
// a crash loop reaches the bound meant to stop it.
func (s *Store) MarkAttempted(ctx context.Context, org string, findingIDs []string) error {
	if len(findingIDs) == 0 {
		return nil
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE review_findings SET fix_attempts = fix_attempts + 1, updated_at = now()
			WHERE id = ANY($1)`, findingIDs)
		return err
	})
}

// SupersedeStale retires open findings about files the given fix Run
// rewrote.
//
// Deliberately narrow: superseded is not resolved. The re-review that
// follows raises the problem again if it is still there; this only stops a
// fixer being sent back for code that no longer exists in that form.
func (s *Store) SupersedeStale(ctx context.Context, org, workItemID string, changed []string, headSHA string) error {
	if len(changed) == 0 {
		return nil
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE review_findings SET status = 'superseded', resolution_note = $3, updated_at = now()
			WHERE work_item_id = $1 AND status = 'open' AND file IS NOT NULL AND file = ANY($2)`,
			workItemID, changed, "file rewritten at "+headSHA)
		return err
	})
}

// SetWorkItemStatus moves a work item to a new status and says why. A no-op
// when unchanged, so a step that re-runs does not add a duplicate event.
func (s *Store) SetWorkItemStatus(ctx context.Context, org string, st *State, status, reason string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE work_items SET status = $2::work_item_status, updated_at = now()
			WHERE id = $1 AND status <> $2::work_item_status`, st.WorkItemID, status)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{
			Type: EvWorkItemStatusChanged, OrganizationID: org, ProjectID: st.ProjectID, WorkItemID: st.WorkItemID,
			ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
			CorrelationID: st.WorkItemID, Payload: map[string]any{"status": status, "reason": reason},
		})
		return err
	})
}

// Emit records a workflow event about the work item.
func (s *Store) Emit(ctx context.Context, org string, st *State, typ string, payload map[string]any) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := ledger.Append(ctx, tx, ledger.Event{
			Type: typ, OrganizationID: org, ProjectID: st.ProjectID, WorkItemID: st.WorkItemID,
			ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
			CorrelationID: st.WorkItemID, Payload: payload,
		})
		return err
	})
}

// Forges resolves an organization's forge client.
type Forges interface {
	For(ctx context.Context, org string) (*forge.GitHub, error)
}

// OpenPullRequest opens the work item's pull request, idempotently: a PR
// already recorded for the work item is returned, and one GitHub already has
// for the branch is recorded rather than treated as a failure.
//
// The body is rendered from what the ledger recorded — the goal, the
// acceptance criteria, what review found — rather than asking a model to
// describe work it already finished (plan §13.2).
func (s *Store) OpenPullRequest(ctx context.Context, org string, st *State, forges Forges) (string, error) {
	var existing, repoURL, repoName, baseBranch, title, goal string
	var criteria []string
	var findings []struct{ Category, Severity, Status, Title string }
	var reviewers int
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id FROM pull_requests WHERE work_item_id = $1 AND repository_id = $2 LIMIT 1`,
			st.WorkItemID, st.RepositoryID).Scan(&existing)
		if err == nil || !db.IsNotFound(err) {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT url, name, default_branch FROM repositories WHERE id = $1`, st.RepositoryID).
			Scan(&repoURL, &repoName, &baseBranch); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT title, goal, acceptance_criteria FROM work_items WHERE id = $1`, st.WorkItemID).
			Scan(&title, &goal, &raw); err != nil {
			return err
		}
		_ = json.Unmarshal(raw, &criteria)
		rows, err := tx.Query(ctx, `SELECT category, severity::text, status::text, title FROM review_findings
			WHERE work_item_id = $1 ORDER BY created_at`, st.WorkItemID)
		if err != nil {
			return err
		}
		findings, err = pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Category, Severity, Status, Title string }])
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(DISTINCT category) FROM runs
			WHERE work_item_id = $1 AND phase = 'review' AND status = 'completed'`, st.WorkItemID).Scan(&reviewers)
	})
	if err != nil || existing != "" {
		return existing, err
	}

	slug := forge.SlugFromURL(repoURL)
	if slug == "" {
		return "", fmt.Errorf("cannot derive owner/repo from %s", repoURL)
	}
	gh, err := forges.For(ctx, org)
	if err != nil {
		return "", err
	}

	sections := []string{strings.TrimSpace(goal)}
	if len(criteria) > 0 {
		sections = append(sections, "## Acceptance criteria\n"+bullets(criteria))
	}
	if len(findings) > 0 {
		addressed := 0
		var lines []string
		for _, f := range findings {
			if f.Status != "open" {
				addressed++
			}
			lines = append(lines, fmt.Sprintf("- `%s` **%s** — %s _(%s)_", f.Severity, f.Category, f.Title, f.Status))
		}
		sections = append(sections, fmt.Sprintf("## Review\n%d finding(s) across %d reviewer(s); %d addressed.\n\n%s",
			len(findings), reviewers, addressed, strings.Join(lines, "\n")))
	}
	sections = append(sections, "---\n\nOpened by the dude factory.")
	body := joinNonEmpty(sections, "\n\n")

	ref, err := gh.OpenPullRequest(ctx, forge.OpenPullRequest{Slug: slug, Title: title, Body: body, Head: st.Branch, Base: baseBranch})
	if e, ok := err.(*forge.Error); ok && e.AlreadyExists() {
		// A replay after a crash between opening the PR and recording it:
		// adopt the one GitHub already has.
		existing, ferr := gh.FindPullRequest(ctx, slug, st.Branch, baseBranch)
		if ferr != nil {
			return "", ferr
		}
		if existing == nil {
			return "", fmt.Errorf("GitHub says a pull request exists for %s but lists none: %w", st.Branch, err)
		}
		ref, err = *existing, nil
	}
	if err != nil {
		return "", err
	}

	prID := ids.New(ids.PullRequest)
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO pull_requests (id, organization_id, project_id, work_item_id, run_id, repository_id, number,
			                           node_id, url, head_branch, base_branch, head_sha, title, body, state)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::pull_request_state)`,
			prID, org, st.ProjectID, st.WorkItemID, db.Nullable(st.HeadRunID), st.RepositoryID, ref.Number,
			db.Nullable(ref.NodeID), ref.URL, st.Branch, baseBranch, ref.HeadSHA, title, body, ref.State); err != nil {
			return err
		}
		_, err := ledger.Append(ctx, tx, ledger.Event{
			Type: EvPullRequestOpened, OrganizationID: org, ProjectID: st.ProjectID, WorkItemID: st.WorkItemID,
			RunID: st.HeadRunID, ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
			CorrelationID: st.WorkItemID,
			Payload: map[string]any{"number": ref.Number, "url": ref.URL, "repo": repoName,
				"headBranch": st.Branch, "baseBranch": baseBranch, "draft": false},
		})
		return err
	})
	return prID, err
}

func bullets(items []string) string {
	var b strings.Builder
	for _, i := range items {
		b.WriteString("- " + i + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func joinNonEmpty(parts []string, sep string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
