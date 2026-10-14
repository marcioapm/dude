package delivery

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Starting a task's delivery: the one start Deliver, Talk it through (and
// a first Chat message) and a fired start when unblocked all go through,
// with the same guard at its top.

// Ways to start a task.
const (
	HowDeliver = "deliver"
	HowTalk    = "talk"
)

// TalkItThrough is what a person who pressed Talk it through says to the
// conductor first: the task is the subject, and nothing is built yet.
const TalkItThrough = "Let's talk this task through before anything is built. Read it and the code, then ask me " +
	"what you need to know, or propose a plan."

// Starter starts a workflow in the caller's transaction (workflow.Runtime).
type Starter interface {
	StartInTx(ctx context.Context, tx pgx.Tx, o workflow.StartOptions) (id string, deduplicated bool, err error)
}

// StartInput is one start of a task.
type StartInput struct {
	Org, TaskID string
	// HowDeliver or HowTalk.
	How string
	// Who starts it: a person, or dude for a fired request.
	Writer Writer
	// Deliver: the task's own policy, over the layers PolicyFor resolves.
	Policy json.RawMessage
	// A person starting it past its open blockers; nil, the guard holds.
	Override *Override
	// Talk: the conductor's first message, when the start makes the
	// conductor too (a fired request). "" leaves it to the caller (Chat).
	Message string
}

// Started is what a start made.
type Started struct {
	WorkflowID string
	// The delivery already existed: this start joined it.
	Joined    bool
	ProjectID string
	// The conductor a talk made, when it made one.
	ConductorRunID string
}

// TaskNotFound is a start of a task that is not there.
type TaskNotFound struct{ TaskID string }

func (e TaskNotFound) Error() string { return fmt.Sprintf("task %s not found", e.TaskID) }

// PolicyFor is the task's project and the delivery policy it gets: the
// factory's defaults, then the organization's, then the project's, each
// layer setting only what it names.
func PolicyFor(ctx context.Context, tx pgx.Tx, taskID string) (string, Policy, error) {
	var projectID string
	var orgPolicy, projectPolicy []byte
	if err := tx.QueryRow(ctx, `SELECT w.project_id, o.delivery_policy, p.delivery_policy FROM tasks w
		JOIN projects p ON p.id = w.project_id JOIN organizations o ON o.id = p.organization_id
		WHERE w.id = $1`, taskID).Scan(&projectID, &orgPolicy, &projectPolicy); err != nil {
		if db.IsNotFound(err) {
			return "", Policy{}, TaskNotFound{taskID}
		}
		return "", Policy{}, err
	}
	policy, err := ResolvePolicy(orgPolicy, projectPolicy)
	if err != nil {
		return "", policy, fmt.Errorf("project %s: %w", projectID, err)
	}
	return projectID, policy, nil
}

// PolicyError is a task's own policy that does not decode.
type PolicyError struct{ Err error }

func (e PolicyError) Error() string { return "policy: " + e.Err.Error() }

// StartTx starts the task's delivery, in tx: refused while it is blocked
// (RefuseBlockedTx) unless a person overrides; then its policy, its only
// repository named, and the delivery — decided by Deliver's policy, or for
// a talk by the conductor, parked on its first decision.
func StartTx(ctx context.Context, tx pgx.Tx, rt Starter, in StartInput) (Started, error) {
	var out Started
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tasks WHERE id = $1 FOR UPDATE`, in.TaskID); err != nil {
		return out, err
	}
	if in.How == HowTalk && in.Message != "" {
		// As Chat takes it, before the delivery: a conductor is made under it.
		if err := LockChat(ctx, tx, in.TaskID); err != nil {
			return out, err
		}
	}
	d, err := LoadDelivery(ctx, tx, in.TaskID)
	if err != nil {
		return out, err
	}
	if d == nil || !d.Live() {
		if err := GuardStartTx(ctx, tx, in.Org, in.TaskID, in.Override); err != nil {
			return out, err
		}
	}
	projectID, policy, err := PolicyFor(ctx, tx, in.TaskID)
	if err != nil {
		return out, err
	}
	out.ProjectID = projectID
	if len(in.Policy) > 0 {
		if err := json.Unmarshal(in.Policy, &policy); err != nil {
			return out, PolicyError{err}
		}
	}
	// The prompt's images are the ones the task's text references, kept so
	// by the backend on every save (PromptAttachments). Which repositories
	// it works on is the task's to say: none is work that changes no code —
	// unless its project has just one.
	if err := NameOnlyRepository(ctx, tx, in.TaskID); err != nil {
		return out, err
	}
	var attempt int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt), 1) FROM runs WHERE task_id = $1`, in.TaskID).Scan(&attempt); err != nil {
		return out, err
	}
	st, err := NewState(ctx, tx, in.TaskID, projectID, policy, attempt)
	if err != nil {
		return out, err
	}
	st.Overridden = in.Override != nil
	if in.How == HowTalk {
		st.Decider = DeciderConductor
	}
	if out.WorkflowID, out.Joined, err = rt.StartInTx(ctx, tx, workflow.StartOptions{Type: WorkflowType, OrganizationID: in.Org,
		IdempotencyKey: "delivery:" + in.TaskID, TaskID: in.TaskID, Input: st}); err != nil {
		return out, err
	}
	// Any start consumes a start when unblocked.
	if err := EndAutoStartTx(ctx, tx, in.Org, in.TaskID, "started", in.Writer.ActorType, in.Writer.ActorID); err != nil {
		return out, err
	}
	if in.How != HowTalk || out.Joined {
		return out, nil
	}
	if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvDeciderChanged, OrganizationID: in.Org, ProjectID: projectID,
		TaskID: in.TaskID, ActorType: in.Writer.ActorType, ActorID: in.Writer.ActorID, Source: ledger.SourceOrchestrator,
		CorrelationID: in.TaskID, Payload: map[string]any{"from": nil, "to": DeciderConductor, "why": "talk it through"}}); err != nil {
		return out, err
	}
	if in.Message != "" {
		out.ConductorRunID, err = StartConductor(ctx, tx, in.Org, projectID, in.TaskID, in.Writer, in.Message)
	}
	return out, err
}
