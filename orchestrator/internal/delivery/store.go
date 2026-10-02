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
	EvRunCreated          = "run.created"
	EvTaskStatusChanged   = "task.status_changed"
	EvQuestionAsked       = "question.asked"
	EvRepositoryRequested = "repository.requested"
	// Every pull request approved with its checks passing: a person's merge.
	EvReadyToMerge         = "task.ready_to_merge"
	EvReviewCompleted      = "review.completed"
	EvPullRequestOpened    = "pull_request.opened"
	EvPullRequestUpdated   = "pull_request.updated"
	EvPullRequestChecks    = "pull_request.checks_changed"
	EvPullRequestReviewed  = "pull_request.reviewed"
	EvPullRequestCommented = "pull_request.commented"
	EvPullRequestMerged    = "pull_request.merged"
	EvPullRequestClosed    = "pull_request.closed"
	// Someone other than dude pushed to the pull request's branch: the
	// next fix starts from their commit.
	EvPullRequestPushed = "pull_request.pushed"
	// A person acted on a pull request through dude: merge, update the
	// branch, re-run failed checks, request a review.
	EvPullRequestAction = "pull_request.action"
	// Mergeability or distance from the base changed.
	EvPullRequestMergeable = "pull_request.mergeable_changed"
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
func (s *Store) runByKey(ctx context.Context, org, taskID, key string) (string, error) {
	var id string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		id, err = runByKey(ctx, tx, taskID, key)
		return err
	})
	return id, err
}

func runByKey(ctx context.Context, tx pgx.Tx, taskID, key string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM runs WHERE task_id = $1 AND creation_key = $2`, taskID, key).Scan(&id)
	if db.IsNotFound(err) {
		return "", nil
	}
	return id, err
}

// PhaseRun describes a phase Run to create.
type PhaseRun struct {
	TaskID string
	Phase  string
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
	// The attempt at the task it is part of; zero for the task's highest.
	Attempt int
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
			if runID, err = runByKey(ctx, tx, in.TaskID, in.Key); err != nil || runID != "" {
				return err
			}
		}
		var projectID string
		var attempt int
		if err := tx.QueryRow(ctx, `
			SELECT w.project_id, COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = w.id), 1)
			FROM tasks w WHERE w.id = $1`, in.TaskID, in.Attempt).Scan(&projectID, &attempt); err != nil {
			return fmt.Errorf("task %s: %w", in.TaskID, err)
		}
		feedback, _ := json.Marshal(db.NonNil(in.PRFeedback))
		bases, _ := json.Marshal(nonNilMap(in.BaseRefs))
		runID = ids.New(ids.Run)
		if _, err := tx.Exec(ctx, `
			INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role,
			                  parent_run_id, base_refs, category, pr_feedback, creation_key,
			                  finding_ids, blocking_severities)
			VALUES ($1, $2, $3, $4, $5, 'pending', $6::run_phase, $7::agent_role, $8, $9::jsonb, $10, $11::jsonb, $12, $13, $14)`,
			runID, org, projectID, in.TaskID, attempt, in.Phase, RoleForPhase[in.Phase],
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
			Type: EvRunCreated, OrganizationID: org, ProjectID: projectID, TaskID: in.TaskID, RunID: runID,
			ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
			CorrelationID: in.TaskID, Payload: payload,
		})
		return err
	})
	return runID, err
}

// Kept says whether a failed Run is kept for a person to resume
// (phases.Syncer.end): its agent died, rather than dude failing it for a
// reason a resume would meet again.
func (s *Store) Kept(ctx context.Context, org, runID string) (bool, error) {
	var kept bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT keep FROM runs WHERE id = $1`, runID).Scan(&kept)
	})
	if db.IsNotFound(err) {
		return false, nil
	}
	return kept, err
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

// thisAttempt (SQL, with $1 the task and $2 its attempt, 0 for any): a
// finding of the attempt delivering now. Each start over is reviewed
// afresh; what an earlier attempt's reviewers found stays with it.
const thisAttempt = `($2 = 0 OR EXISTS (SELECT 1 FROM runs fr WHERE fr.id = review_findings.run_id AND fr.attempt = $2))`

func (s *Store) Findings(ctx context.Context, org string, st *State) ([]FindingState, error) {
	var out []FindingState
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, severity::text, status::text, fix_attempts FROM review_findings
			WHERE task_id = $1 AND `+thisAttempt+` ORDER BY created_at`, st.TaskID, st.Attempt)
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

// AcceptFindings is a person deciding the task ships with the findings
// still open: accepted, they block nothing.
func (s *Store) AcceptFindings(ctx context.Context, org string, st *State) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE review_findings SET status = 'accepted',
			resolution_note = 'accepted by a person', updated_at = now()
			WHERE task_id = $1 AND status = 'open' AND `+thisAttempt, st.TaskID, st.Attempt)
		return err
	})
}

// AttemptedFindings is, per category, the open findings a fix has been
// sent — what the next reviewer of that category is asked to judge.
func (s *Store) AttemptedFindings(ctx context.Context, org string, st *State) (map[string][]string, error) {
	out := map[string][]string{}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT category, id FROM review_findings
			WHERE task_id = $1 AND status = 'open' AND fix_attempts > 0 AND `+thisAttempt+` ORDER BY created_at`,
			st.TaskID, st.Attempt)
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

// SetTaskStatus moves a task to a new status and says why. A no-op
// when unchanged, so a step that re-runs does not add a duplicate event.
func (s *Store) SetTaskStatus(ctx context.Context, org string, st *State, status, reason string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, "", status, reason)
		return err
	})
}

// SetTaskStatusFrom moves the task only from the given status.
func (s *Store) SetTaskStatusFrom(ctx context.Context, org string, st *State, from, status, reason string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, from, status, reason)
		return err
	})
}

// ReadyToMerge moves the task from review to ready to merge and records
// that it is, for whoever is told when something waits on them — together.
func (s *Store) ReadyToMerge(ctx context.Context, org string, st *State, pullRequests int) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		moved, err := SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, "review", "ready_to_merge", "approved, checks passing")
		if err != nil || !moved {
			return err
		}
		return emitTx(ctx, tx, org, st, EvReadyToMerge, map[string]any{"pullRequests": pullRequests})
	})
}

// SetTaskStatusTx is SetTaskStatus in the caller's transaction, for
// a change that must commit with something else: an agent's question and
// the task waiting on it. With from set, only a task in that
// status moves — a person answering must not revive an aborted one.
func SetTaskStatusTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID, from, status, reason string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE tasks SET status = $2::task_status, updated_at = now()
		WHERE id = $1 AND status <> $2::task_status
		  AND ($3 = '' OR status = $3::task_status)
		  AND status NOT IN ('done', 'failed', 'aborted')`, taskID, status, from)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	return true, RecordStatusTx(ctx, tx, org, projectID, taskID, status, reason)
}

// RecordStatusTx records that a task's status changed, and why: what time
// and cost count from.
func RecordStatusTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID, status, reason string) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: EvTaskStatusChanged, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
		ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: map[string]any{"status": status, "reason": reason},
	})
	return err
}

// Emit records a workflow event about the task.
func (s *Store) Emit(ctx context.Context, org string, st *State, typ string, payload map[string]any) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return emitTx(ctx, tx, org, st, typ, payload)
	})
}

// emitTx is Emit in the caller's transaction.
func emitTx(ctx context.Context, tx pgx.Tx, org string, st *State, typ string, payload map[string]any) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: typ, OrganizationID: org, ProjectID: st.ProjectID, TaskID: st.TaskID,
		ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
		CorrelationID: st.TaskID, Payload: payload,
	})
	return err
}

// Forges resolves an organization's forge client.
type Forges interface {
	For(ctx context.Context, org string) (*forge.GitHub, error)
}

// Repository is one the task names, as delivery uses it.
type Repository struct {
	ID, Name, URL, DefaultBranch string
	// "write": may change, and gets a pull request when it does. "read":
	// cloned for context, never pushed.
	Access string
}

// TaskRepositories are the repositories a task works on; none is
// work that changes no code.
func TaskRepositories(ctx context.Context, tx pgx.Tx, taskID string) ([]Repository, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id, r.name, r.url, r.default_branch, wr.access::text
		FROM task_repositories wr JOIN repositories r ON r.id = wr.repository_id
		WHERE wr.task_id = $1 ORDER BY r.name`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Repository])
}

// RunRef names the Run something happened on, for its ledger events.
type RunRef struct{ Org, ProjectID, TaskID, RunID string }

// Event is a ledger event on the Run, by the given actor, in the task's
// correlation.
func (r RunRef) Event(typ, actorType string, payload map[string]any) ledger.Event {
	return ledger.Event{Type: typ, OrganizationID: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.RunID,
		ActorType: actorType, ActorID: r.RunID, Source: ledger.SourceOrchestrator, CorrelationID: r.TaskID, Payload: payload}
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
//
// An interrupt superseding a directive of this Run with the same words is
// "Interrupt now": it resends that directive's root (the directive itself,
// or the one it resends in turn), so a chain of clicks is one instruction.
// Whether it carries the words is decided at its first send attempt
// (phases deliverDirectives), so interrupt_only is left unset.
func QueueDirective(ctx context.Context, tx pgx.Tx, r RunRef, d Directive) (string, time.Time, error) {
	id := ids.New(ids.Directive)
	var createdAt time.Time
	err := tx.QueryRow(ctx, `WITH root AS (
			SELECT COALESCE(s.resends, s.id) AS id FROM directives s
			WHERE $8 AND s.id = $7 AND s.run_id = $4 AND s.text = $5)
		INSERT INTO directives (id, organization_id, task_id, run_id, text, scope, supersedes, interrupt, resends, interrupt_only)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, root.id, CASE WHEN root.id IS NULL THEN false END
		FROM (SELECT 1) one LEFT JOIN root ON true
		RETURNING created_at`,
		id, r.Org, r.TaskID, r.RunID, d.Text, d.Scope, db.Nullable(d.Supersedes), d.Interrupt).Scan(&createdAt)
	return id, createdAt, err
}

// HasOpenQuestion says whether the Run has a question waiting on a person.
func HasOpenQuestion(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `SELECT `+openQuestion+` FROM runs r WHERE r.id = $1`, runID).Scan(&open)
	return open, err
}

// AskTx records an agent's question for a person, and the task waiting
// on it.
func AskTx(ctx context.Context, tx pgx.Tx, r RunRef, prompt string, options []string) (string, error) {
	id := ids.New(ids.Question)
	opts, _ := json.Marshal(db.NonNil(options))
	if _, err := tx.Exec(ctx, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, id, r.Org, r.TaskID, r.RunID, prompt, opts); err != nil {
		return "", err
	}
	if _, err := SetTaskStatusTx(ctx, tx, r.Org, r.ProjectID, r.TaskID, "", "awaiting_input",
		"the agent asked a question"); err != nil {
		return "", err
	}
	_, err := ledger.Append(ctx, tx, r.Event(EvQuestionAsked, ledger.ActorAgent,
		map[string]any{"kind": "agent", "questionId": id, "prompt": prompt, "options": db.NonNil(options)}))
	return id, err
}

// RecordDecisionTx records something a person decided about the task, as
// an answered question: every phase from then on is told it (Decisions).
func RecordDecisionTx(ctx context.Context, tx pgx.Tx, org, taskID, question, answer string) error {
	_, err := tx.Exec(ctx, `INSERT INTO questions (id, organization_id, task_id, prompt, status, answer, answered_at)
		VALUES ($1, $2, $3, $4, 'answered', $5, now())`, ids.New(ids.Question), org, taskID, question, answer)
	return err
}

// HasWritableRepository says whether the task may change code.
func (s *Store) HasWritableRepository(ctx context.Context, org, taskID string) (bool, error) {
	var ok bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_repositories
			WHERE task_id = $1 AND access = 'write')`, taskID).Scan(&ok)
	})
	return ok, err
}

// NameOnlyRepository records a project's only repository on a task that
// names none, when it is delivered: a task in a one-repository project
// works on that repository unless it says otherwise. Recorded rather than
// worked out each time, so adding a second repository later changes nothing
// for work already under way.
func NameOnlyRepository(ctx context.Context, tx pgx.Tx, taskID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		SELECT w.organization_id, w.id, (SELECT r.id FROM repositories r WHERE r.project_id = w.project_id), 'write'
		FROM tasks w
		WHERE w.id = $1
		  AND NOT EXISTS (SELECT 1 FROM task_repositories WHERE task_id = w.id)
		  AND (SELECT count(*) FROM repositories r WHERE r.project_id = w.project_id) = 1`, taskID)
	return err
}

// OpenPullRequests opens a pull request in each repository the work changed
// that has none yet, and returns every one the task has, oldest first.
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
		repos, err := TaskRepositories(ctx, tx, st.TaskID)
		if err != nil {
			return err
		}
		// An open one carries whatever the branch gets next. A merged or
		// closed one covers only what it held: a later change in its
		// repository needs a pull request of its own.
		rows, err := tx.Query(ctx, `SELECT id, repository_id, state IN ('open', 'draft'), COALESCE(head_sha, '')
			FROM pull_requests WHERE task_id = $1 AND head_branch = $2 ORDER BY created_at, id`, st.TaskID, st.Branch)
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
	title, body, err := s.pullRequestText(ctx, org, st)
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

// pullRequestText is a task's pull request title and body, rendered
// from what the ledger recorded.
func (s *Store) pullRequestText(ctx context.Context, org string, st *State) (string, string, error) {
	taskID := st.TaskID
	var title, goal string
	var criteria []string
	var findings []struct{ Category, Severity, Status, Title string }
	var reviewers int
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT title, goal, acceptance_criteria FROM tasks WHERE id = $1`, taskID).
			Scan(&title, &goal, &raw); err != nil {
			return err
		}
		_ = json.Unmarshal(raw, &criteria)
		rows, err := tx.Query(ctx, `SELECT category, severity::text, status::text, title FROM review_findings
			WHERE task_id = $1 AND `+thisAttempt+` ORDER BY created_at`, taskID, st.Attempt)
		if err != nil {
			return err
		}
		if findings, err = pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Category, Severity, Status, Title string }]); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(DISTINCT category) FROM runs
			WHERE task_id = $1 AND phase = 'review' AND status = 'completed' AND ($2 = 0 OR attempt = $2)`,
			taskID, st.Attempt).Scan(&reviewers)
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
	draft := gh.Settings.OpenAs == "draft"
	ref, err := gh.OpenPullRequest(ctx, forge.OpenPullRequest{Slug: slug, Title: title, Body: body, Head: st.Branch,
		Base: repo.DefaultBranch, Draft: draft})
	fresh := err == nil
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
	// Whom the organization asks for a review. CODEOWNERS GitHub asks by
	// itself. Best effort, and asked once: a login GitHub will not ask (not
	// a collaborator, the author), or GitHub failing now, must not keep the
	// pull request from being recorded — a person can ask from the task.
	var asked []string
	if fresh && gh.Settings.RequestReviewFrom == "logins" && len(gh.Settings.ReviewLogins) > 0 &&
		gh.RequestReviewers(ctx, slug, ref.Number, gh.Settings.ReviewLogins) == nil {
		asked = gh.Settings.ReviewLogins
	}
	id := ids.New(ids.PullRequest)
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		// One already recorded (a replay that got this far) is the one: its
		// id is returned, and it is not announced twice.
		tag, err := tx.Exec(ctx, `
			INSERT INTO pull_requests (id, organization_id, project_id, task_id, run_id, repository_id, number,
			                           node_id, url, head_branch, base_branch, head_sha, title, body, state, checks)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::pull_request_state, 'unknown')
			ON CONFLICT (repository_id, number) DO NOTHING`,
			id, org, st.ProjectID, st.TaskID, db.Nullable(st.HeadRunID), repo.ID, ref.Number,
			db.Nullable(ref.NodeID), ref.URL, st.Branch, repo.DefaultBranch, ref.HeadSHA, title, body, ref.State)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return tx.QueryRow(ctx, `SELECT id FROM pull_requests WHERE repository_id = $1 AND number = $2`,
				repo.ID, ref.Number).Scan(&id)
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{
			Type: EvPullRequestOpened, OrganizationID: org, ProjectID: st.ProjectID, TaskID: st.TaskID,
			RunID: st.HeadRunID, ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator,
			CorrelationID: st.TaskID,
			Payload: map[string]any{"number": ref.Number, "url": ref.URL, "repo": repo.Name,
				"headBranch": st.Branch, "baseBranch": repo.DefaultBranch, "draft": ref.State == forge.StateDraft,
				"reviewersRequested": db.NonNil(asked)},
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

// PullRequestState is a task's pull request as last synced from the forge,
// with what finds it on GitHub.
type PullRequestState struct {
	forge.Status
	Repo, Slug string
}

// PullRequestStates reads the pull requests as last synced.
func (s *Store) PullRequestStates(ctx context.Context, org string, prIDs []string) ([]PullRequestState, error) {
	var out []PullRequestState
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT pr.state::text, pr.checks::text, pr.review::text, pr.mergeable_state,
				pr.unresolved_threads, pr.number, COALESCE(pr.head_sha, ''), r.name, r.url
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = ANY($1) ORDER BY pr.created_at, pr.id`, prIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PullRequestState
			var url string
			if err := rows.Scan(&p.State, &p.Checks, &p.Review, &p.Mergeable, &p.UnresolvedThreads, &p.Number,
				&p.HeadSHA, &p.Repo, &url); err != nil {
				return err
			}
			p.Slug = forge.SlugFromURL(url)
			out = append(out, p)
		}
		return rows.Err()
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
