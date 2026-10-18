package delivery

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

type ChildParentKind string

const ChildParentTaskConductor ChildParentKind = "task_conductor"

type ChildParent struct {
	Kind ChildParentKind
	Run  RunRef
}

type ChildCapabilities struct {
	PublishCode bool `json:"publishCode"`
}

type StartChildInput struct {
	Parent                           ChildParent
	Profile, Prompt, Note, Tier, Key string
	Capabilities                     ChildCapabilities
}

func childPhase(profile string) (string, error) {
	phase := PhaseForPromptRole(profile)
	if phase == "" {
		return "", refusef("an existing phase profile is required")
	}
	return phase, nil
}

// StartChild only admits a durable Run. It has no tool adapter until child
// lifecycle and publication isolation are wired into the phase syncer.
func (s *Store) StartChild(ctx context.Context, in StartChildInput) (string, error) {
	phase, err := childPhase(in.Profile)
	if err != nil {
		return "", err
	}
	if in.Parent.Kind != ChildParentTaskConductor || in.Key == "" {
		return "", refusef("a task conductor parent and idempotency key are required")
	}
	if in.Capabilities.PublishCode != Publishes[phase] {
		return "", refusef("capabilities must match the named profile")
	}
	if len([]rune(in.Note)) > 4000 {
		return "", refusef("note exceeds 4000 characters")
	}
	ref := in.Parent.Run
	var id string
	err = s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		var err error
		id, err = StartChildTx(ctx, tx, ref, phase, in)
		return err
	})
	return id, err
}

func StartChildTx(ctx context.Context, tx pgx.Tx, ref RunRef, phase string, in StartChildInput) (id string, err error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return "", err
	}
	var live bool
	var parentAttempt int
	// Parent Run precedes task, matching Chat and conductor ending.
	err = tx.QueryRow(ctx, `SELECT `+LiveConductor+` AND r.phase IS NULL
			AND r.source_kind IS NULL AND r.replaced_by IS NULL AND r.control <> 'abort'
			AND NOT COALESCE(`+Ending+`, false), r.attempt
			FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND r.project_id = $3
			AND r.organization_id = $4 FOR UPDATE`, ref.RunID, ref.TaskID, ref.ProjectID, ref.Org).Scan(&live, &parentAttempt)
	if db.IsNotFound(err) {
		return "", refusef("the parent is not this task's authorized conductor")
	}
	if err != nil {
		return "", err
	}
	if !live {
		return "", refusef("the parent conductor is ended, replaced, or itself a child")
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1 FOR UPDATE`, ref.TaskID).Scan(&status); err != nil {
		return "", err
	}
	if Ended(status) {
		return "", refusef("this task has ended")
	}
	keyBytes, _ := json.Marshal([]string{string(in.Parent.Kind), ref.RunID, in.Key})
	key := "child:" + string(keyBytes)
	id, err = runByKey(ctx, tx, ref.TaskID, key)
	if err != nil || id != "" {
		return id, err
	}
	d, err := LoadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	if d == nil || !d.Live() {
		return "", refusef("this task has no live delivery")
	}
	if d.State.Attempt != parentAttempt {
		return "", refusef("the parent conductor is from an old delivery attempt")
	}
	if Publishes[phase] {
		if err := refuseWriterAdmissionTx(ctx, tx, ref.TaskID, "", d); err != nil {
			return "", err
		}
	}
	tier := ""
	if in.Tier != "" {
		tier, err = tierForRole(ctx, tx, in.Tier, PromptRoleForPhase[phase])
		if err != nil {
			return "", err
		}
	}
	id, err = createPhaseRunTx(ctx, tx, ref.Org, PhaseRun{
		TaskID: ref.TaskID, Phase: phase, BaseRefs: d.State.Heads, Attempt: d.State.Attempt,
		ParentRunID: ref.RunID, ConductorRunID: ref.RunID, ConductorNote: in.Note,
		Key: key, Tier: tier, Branch: d.State.Branch, Child: true,
	})
	if err != nil {
		return "", err
	}
	capabilities, _ := json.Marshal(in.Capabilities)
	if _, err := tx.Exec(ctx, `UPDATE runs SET source_kind = $2, child_profile = $3,
			child_prompt = $4, child_capabilities = $5::jsonb WHERE id = $1`,
		id, string(in.Parent.Kind), in.Profile, db.Nullable(in.Prompt), capabilities); err != nil {
		return "", err
	}
	ev := ref.Event("run.child_started", ledger.ActorAgent, map[string]any{
		"parentRunId": ref.RunID, "profile": in.Profile, "sourceKind": string(in.Parent.Kind),
	})
	ev.RunID = id
	_, err = ledger.Append(ctx, tx, ev)
	return id, err
}
