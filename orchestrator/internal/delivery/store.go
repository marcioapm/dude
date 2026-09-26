package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

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
	EvRepositoryRequested   = "repository.requested"
	// Every pull request approved with its checks passing: a person's merge.
	EvReadyToMerge         = "work_item.ready_to_merge"
	EvReviewCompleted      = "review.completed"
	EvPullRequestOpened    = "pull_request.opened"
	EvPullRequestUpdated   = "pull_request.updated"
	EvPullRequestChecks    = "pull_request.checks_changed"
	EvPullRequestReviewed  = "pull_request.reviewed"
	EvPullRequestCommented = "pull_request.commented"
	EvPullRequestMerged    = "pull_request.merged"
	EvPullRequestClosed    = "pull_request.closed"
	EvGitCommitCreated     = "git.commit_created"
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
	WorkItemID string
	Phase      string
	// The commit each repository starts from, by name; a repository not
	// named starts from its default branch.
	BaseRefs    map[string]string
	ParentRunID string
	Category    string
	FindingIDs  []string
	PRFeedback  []forge.ActionableFeedback
	// Review: the severities the delivery's policy blocks on, which the
	// reviewer is told.
	BlockingSeverities []string
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
		bases, _ := json.Marshal(nonNilMap(in.BaseRefs))
		runID = ids.New(ids.Run)
		if _, err := tx.Exec(ctx, `
			INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status, phase, role,
			                  parent_run_id, base_refs, category, pr_feedback, creation_key,
			                  finding_ids, blocking_severities)
			VALUES ($1, $2, $3, $4, $5, 'pending', $6::run_phase, $7::agent_role, $8, $9::jsonb, $10, $11::jsonb, $12, $13, $14)`,
			runID, org, projectID, in.WorkItemID, attempt, in.Phase, RoleForPhase[in.Phase],
			db.Nullable(in.ParentRunID), bases, db.Nullable(in.Category), feedback,
			db.Nullable(in.Key), db.NonNil(in.FindingIDs), db.NonNil(in.BlockingSeverities)); err != nil {
			return err
		}
		payload := map[string]any{
			"attempt": attempt, "phase": in.Phase, "role": RoleForPhase[in.Phase],
			"publishes": Publishes[in.Phase], "baseRefs": nonNilMap(in.BaseRefs),
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
	// Where the phase left each repository it changed, by name.
	Heads map[string]string
	// What it changed, as <repo>/<path> across repositories: selects the
	// conditional reviewers.
	ChangedPaths []string
	// It published something for people (an artifact), which is work even
	// when no repository changed.
	Published bool
}

// advance is where the work stands after this phase: the heads it moved,
// and the rest where they were.
func (o Outcome) advance(heads map[string]string) map[string]string {
	out := maps.Clone(heads)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, o.Heads)
	return out
}

func (s *Store) PhaseOutcome(ctx context.Context, org, runID string) (Outcome, error) {
	var o Outcome
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var status string
		var errText *string
		var heads []byte
		err := tx.QueryRow(ctx, `SELECT status::text, error, heads,
				EXISTS (SELECT 1 FROM artifacts WHERE run_id = runs.id)
			FROM runs WHERE id = $1`, runID).Scan(&status, &errText, &heads, &o.Published)
		if db.IsNotFound(err) {
			o.Error = "run not found"
			return nil
		}
		if err != nil {
			return err
		}
		o.Succeeded, o.Error = status == "completed", deref(errText)
		o.Heads, o.ChangedPaths, err = ReadHeads(heads)
		return err
	})
	return o, err
}

// RunHead is what a phase Run left in one repository it changed.
type RunHead struct {
	SHA          string   `json:"sha"`
	ChangedPaths []string `json:"changedPaths"`
}

// ReadHeads reads runs.heads: the commit per repository, and every changed
// path prefixed with its repository's name, in a stable order.
func ReadHeads(raw []byte) (map[string]string, []string, error) {
	var heads map[string]RunHead
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &heads); err != nil {
			return nil, nil, fmt.Errorf("run heads: %w", err)
		}
	}
	shas := map[string]string{}
	var paths []string
	for _, name := range slices.Sorted(maps.Keys(heads)) {
		shas[name] = heads[name].SHA
		for _, p := range heads[name].ChangedPaths {
			paths = append(paths, name+"/"+p)
		}
	}
	return shas, paths, nil
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
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

// AttemptedFindings is, per category, the open findings a fix has been
// sent — what the next reviewer of that category is asked to judge.
func (s *Store) AttemptedFindings(ctx context.Context, org, workItemID string) (map[string][]string, error) {
	out := map[string][]string{}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT category, id FROM review_findings
			WHERE work_item_id = $1 AND status = 'open' AND fix_attempts > 0 ORDER BY created_at`, workItemID)
		if err != nil {
			return err
		}
		pairs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Category, ID string }])
		for _, p := range pairs {
			out[p.Category] = append(out[p.Category], p.ID)
		}
		return err
	})
	return out, err
}

// SetWorkItemStatus moves a work item to a new status and says why. A no-op
// when unchanged, so a step that re-runs does not add a duplicate event.
func (s *Store) SetWorkItemStatus(ctx context.Context, org string, st *State, status, reason string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := SetWorkItemStatusTx(ctx, tx, org, st.ProjectID, st.WorkItemID, "", status, reason)
		return err
	})
}

// SetWorkItemStatusFrom moves the work item only from the given status, and
// says whether it moved.
func (s *Store) SetWorkItemStatusFrom(ctx context.Context, org string, st *State, from, status, reason string) (bool, error) {
	var moved bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		moved, err = SetWorkItemStatusTx(ctx, tx, org, st.ProjectID, st.WorkItemID, from, status, reason)
		return err
	})
	return moved, err
}

// SetWorkItemStatusTx is SetWorkItemStatus in the caller's transaction, for
// a change that must commit with something else: an agent's question and
// the work item waiting on it. With from set, only a work item in that
// status moves — a person answering must not revive an aborted one.
func SetWorkItemStatusTx(ctx context.Context, tx pgx.Tx, org, projectID, workItemID, from, status, reason string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE work_items SET status = $2::work_item_status, updated_at = now()
		WHERE id = $1 AND status <> $2::work_item_status
		  AND ($3 = '' OR status = $3::work_item_status)
		  AND status NOT IN ('done', 'failed', 'aborted')`, workItemID, status, from)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{
		Type: EvWorkItemStatusChanged, OrganizationID: org, ProjectID: projectID, WorkItemID: workItemID,
		ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
		CorrelationID: workItemID, Payload: map[string]any{"status": status, "reason": reason},
	})
	return true, err
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

// Repository is one the work item names, as delivery uses it.
type Repository struct {
	ID, Name, URL, DefaultBranch string
	// "write": may change, and gets a pull request when it does. "read":
	// cloned for context, never pushed.
	Access string
}

// WorkItemRepositories are the repositories a work item works on; none is
// work that changes no code.
func WorkItemRepositories(ctx context.Context, tx pgx.Tx, workItemID string) ([]Repository, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id, r.name, r.url, r.default_branch, wr.access::text
		FROM work_item_repositories wr JOIN repositories r ON r.id = wr.repository_id
		WHERE wr.work_item_id = $1 ORDER BY r.name`, workItemID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Repository])
}

// RunRef names the Run something happened on, for its ledger events.
type RunRef struct{ Org, ProjectID, WorkItemID, RunID string }

// Event is a ledger event on the Run, by the given actor, in the work item's
// correlation.
func (r RunRef) Event(typ, actorType string, payload map[string]any) ledger.Event {
	return ledger.Event{Type: typ, OrganizationID: r.Org, ProjectID: r.ProjectID, WorkItemID: r.WorkItemID, RunID: r.RunID,
		ActorType: actorType, ActorID: r.RunID, Source: ledger.SourceOrchestrator, CorrelationID: r.WorkItemID, Payload: payload}
}

// openQuestion (SQL, over a Run aliased r): it has a question waiting for
// a person's answer.
const openQuestion = `EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')`

// OpenAsk is true (SQL, over a Run aliased r) while the Run is blocked on a
// person: a question, or a repository it said it cannot go on without. An
// agent that ends its turn with one open is waiting, not done.
const OpenAsk = `(` + openQuestion + `
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking))`

// HoldsTurn is true (SQL, over a Run aliased r) while a turn that ends
// is not done: something is open for a person (OpenAsk), or a repository
// the agent waits on was approved and has not reached it yet — it is
// resumed with it.
const HoldsTurn = `(` + openQuestion + `
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.blocking AND q.status IN ('pending', 'approved')))`

// Directive is a message queued for a Run's agent: delivered by the phase
// syncer, acknowledged by lux when the agent takes it. Scope "run" holds for
// the rest of the Run, "turn" for the current turn only; Supersedes names
// the one it replaces; Interrupt stops the turn so it is heard now.
type Directive struct {
	Text, Scope, Supersedes string
	Interrupt               bool
}

// QueueDirective records a directive for the Run, and returns its id and
// when it was queued.
func QueueDirective(ctx context.Context, tx pgx.Tx, r RunRef, d Directive) (string, time.Time, error) {
	id := ids.New(ids.Directive)
	var createdAt time.Time
	err := tx.QueryRow(ctx, `INSERT INTO directives (id, organization_id, work_item_id, run_id, text, scope, supersedes, interrupt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at`,
		id, r.Org, r.WorkItemID, r.RunID, d.Text, d.Scope, db.Nullable(d.Supersedes), d.Interrupt).Scan(&createdAt)
	return id, createdAt, err
}

// HasOpenQuestion says whether the Run has a question waiting on a person.
func HasOpenQuestion(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `SELECT `+openQuestion+` FROM runs r WHERE r.id = $1`, runID).Scan(&open)
	return open, err
}

// AskTx records an agent's question for a person, and the work item waiting
// on it.
func AskTx(ctx context.Context, tx pgx.Tx, r RunRef, prompt string, options []string) (string, error) {
	id := ids.New(ids.Question)
	opts, _ := json.Marshal(db.NonNil(options))
	if _, err := tx.Exec(ctx, `INSERT INTO questions (id, organization_id, work_item_id, run_id, prompt, options)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, id, r.Org, r.WorkItemID, r.RunID, prompt, opts); err != nil {
		return "", err
	}
	if _, err := SetWorkItemStatusTx(ctx, tx, r.Org, r.ProjectID, r.WorkItemID, "", "awaiting_input",
		"the agent asked a question"); err != nil {
		return "", err
	}
	_, err := ledger.Append(ctx, tx, r.Event(EvQuestionAsked, ledger.ActorAgent,
		map[string]any{"kind": "agent", "questionId": id, "prompt": prompt, "options": db.NonNil(options)}))
	return id, err
}

// HasWritableRepository says whether the work item may change code.
func (s *Store) HasWritableRepository(ctx context.Context, org, workItemID string) (bool, error) {
	var ok bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM work_item_repositories
			WHERE work_item_id = $1 AND access = 'write')`, workItemID).Scan(&ok)
	})
	return ok, err
}

// NameOnlyRepository records a project's only repository on a work item that
// names none, when it is delivered: a work item in a one-repository project
// works on that repository unless it says otherwise. Recorded rather than
// worked out each time, so adding a second repository later changes nothing
// for work already under way.
func NameOnlyRepository(ctx context.Context, tx pgx.Tx, workItemID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO work_item_repositories (organization_id, work_item_id, repository_id, access)
		SELECT w.organization_id, w.id, (SELECT r.id FROM repositories r WHERE r.project_id = w.project_id), 'write'
		FROM work_items w
		WHERE w.id = $1
		  AND NOT EXISTS (SELECT 1 FROM work_item_repositories WHERE work_item_id = w.id)
		  AND (SELECT count(*) FROM repositories r WHERE r.project_id = w.project_id) = 1`, workItemID)
	return err
}

// OpenPullRequests opens a pull request in each repository the work changed
// that has none yet, and returns every one the work item has, oldest first.
// Idempotent: one already recorded is kept, and one GitHub already has for
// the branch is recorded rather than treated as a failure.
//
// Bodies are rendered from what the ledger recorded — the goal, the
// acceptance criteria, what review found — rather than asking a model to
// describe work it already finished (plan §13.2); each names its siblings.
func (s *Store) OpenPullRequests(ctx context.Context, org string, st *State, forges Forges) ([]string, error) {
	// The repositories the work moved that have no pull request yet.
	var todo, changed []Repository
	var ids []string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		repos, err := WorkItemRepositories(ctx, tx, st.WorkItemID)
		if err != nil {
			return err
		}
		// An open one carries whatever the branch gets next. A merged or
		// closed one covers only what it held: a later change in its
		// repository needs a pull request of its own.
		rows, err := tx.Query(ctx, `SELECT id, repository_id, state IN ('open', 'draft'), COALESCE(head_sha, '')
			FROM pull_requests WHERE work_item_id = $1 AND head_branch = $2 ORDER BY created_at, id`, st.WorkItemID, st.Branch)
		if err != nil {
			return err
		}
		prs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			ID, RepositoryID string
			Open             bool
			HeadSHA          string
		}])
		if err != nil {
			return err
		}
		covered := map[string]string{} // repository → the head its pull requests carry; "*" while one is open
		for _, pr := range prs {
			ids = append(ids, pr.ID)
			if pr.Open {
				covered[pr.RepositoryID] = "*"
			} else if covered[pr.RepositoryID] != "*" {
				covered[pr.RepositoryID] = pr.HeadSHA
			}
		}
		for _, r := range repos {
			if _, moved := st.Heads[r.Name]; moved && r.Access == "write" {
				changed = append(changed, r)
				if c, ok := covered[r.ID]; !ok || c != "*" && c != st.Heads[r.Name] {
					todo = append(todo, r)
				}
			}
		}
		return nil
	})
	if err != nil || len(todo) == 0 {
		return ids, err
	}

	gh, err := forges.For(ctx, org)
	if err != nil {
		return nil, err
	}
	if gh == nil {
		return nil, fmt.Errorf("no forge credential to open pull requests with")
	}
	title, body, err := s.pullRequestText(ctx, org, st.WorkItemID)
	if err != nil {
		return nil, err
	}
	for _, r := range todo {
		id, err := s.openPullRequest(ctx, org, st, gh, r, changed, title, body)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// pullRequestText is a work item's pull request title and body, rendered
// from what the ledger recorded.
func (s *Store) pullRequestText(ctx context.Context, org, workItemID string) (string, string, error) {
	var title, goal string
	var criteria []string
	var findings []struct{ Category, Severity, Status, Title string }
	var reviewers int
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT title, goal, acceptance_criteria FROM work_items WHERE id = $1`, workItemID).
			Scan(&title, &goal, &raw); err != nil {
			return err
		}
		_ = json.Unmarshal(raw, &criteria)
		rows, err := tx.Query(ctx, `SELECT category, severity::text, status::text, title FROM review_findings
			WHERE work_item_id = $1 ORDER BY created_at`, workItemID)
		if err != nil {
			return err
		}
		if findings, err = pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Category, Severity, Status, Title string }]); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(DISTINCT category) FROM runs
			WHERE work_item_id = $1 AND phase = 'review' AND status = 'completed'`, workItemID).Scan(&reviewers)
	})
	return title, prBody(goal, criteria, findings, reviewers), err
}

func (s *Store) openPullRequest(ctx context.Context, org string, st *State, gh *forge.GitHub, repo Repository,
	all []Repository, title, body string) (string, error) {
	slug := forge.SlugFromURL(repo.URL)
	if slug == "" {
		return "", fmt.Errorf("cannot derive owner/repo from %s", repo.URL)
	}
	if len(all) > 1 {
		var others []string
		for _, r := range all {
			if r.ID != repo.ID {
				others = append(others, "`"+forge.SlugFromURL(r.URL)+"`")
			}
		}
		body += fmt.Sprintf("\n\nOne of %d pull requests for this work, all from `%s`; the others are in %s.",
			len(all), st.Branch, strings.Join(others, ", "))
	}
	ref, err := gh.OpenPullRequest(ctx, forge.OpenPullRequest{Slug: slug, Title: title, Body: body, Head: st.Branch, Base: repo.DefaultBranch})
	if e, ok := err.(*forge.Error); ok && e.AlreadyExists() {
		// A replay after a crash between opening the PR and recording it:
		// adopt the one GitHub already has.
		existing, ferr := gh.FindPullRequest(ctx, slug, st.Branch, repo.DefaultBranch)
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
	id := ids.New(ids.PullRequest)
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		// One already recorded (a replay that got this far) is the one: its
		// id is returned, and it is not announced twice.
		tag, err := tx.Exec(ctx, `
			INSERT INTO pull_requests (id, organization_id, project_id, work_item_id, run_id, repository_id, number,
			                           node_id, url, head_branch, base_branch, head_sha, title, body, state)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::pull_request_state)
			ON CONFLICT (repository_id, number) DO NOTHING`,
			id, org, st.ProjectID, st.WorkItemID, db.Nullable(st.HeadRunID), repo.ID, ref.Number,
			db.Nullable(ref.NodeID), ref.URL, st.Branch, repo.DefaultBranch, ref.HeadSHA, title, body, ref.State)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return tx.QueryRow(ctx, `SELECT id FROM pull_requests WHERE repository_id = $1 AND number = $2`,
				repo.ID, ref.Number).Scan(&id)
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{
			Type: EvPullRequestOpened, OrganizationID: org, ProjectID: st.ProjectID, WorkItemID: st.WorkItemID,
			RunID: st.HeadRunID, ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
			CorrelationID: st.WorkItemID,
			Payload: map[string]any{"number": ref.Number, "url": ref.URL, "repo": repo.Name,
				"headBranch": st.Branch, "baseBranch": repo.DefaultBranch, "draft": false},
		})
		return err
	})
	return id, err
}

func prBody(goal string, criteria []string, findings []struct{ Category, Severity, Status, Title string }, reviewers int) string {
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
	return joinNonEmpty(sections, "\n\n")
}

// PullRequestStates is each pull request's state as last synced: open,
// draft, merged or closed.
// PullRequestState is a work item's pull request as the workflow weighs it.
type PullRequestState struct{ State, Checks, Review string }

func (s *Store) PullRequestStates(ctx context.Context, org string, prIDs []string) ([]PullRequestState, error) {
	var out []PullRequestState
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT state::text, checks::text, review::text FROM pull_requests WHERE id = ANY($1)`, prIDs)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[PullRequestState])
		return err
	})
	return out, err
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
