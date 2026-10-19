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
		return "", refuseText(rChildProfileRequired)
	}
	return phase, nil
}

// EffectiveChildAttemptTx resolves legacy deliveries from persisted task Runs,
// never from the requesting parent or child. Callers hold the task's locks.
func EffectiveChildAttemptTx(ctx context.Context, tx pgx.Tx, taskID string, d *Delivery) (int, error) {
	if d != nil && d.State.Attempt > 0 {
		return d.State.Attempt, nil
	}
	var attempt int
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt), 1) FROM runs WHERE task_id=$1`, taskID).Scan(&attempt)
	return attempt, err
}

func (s *Store) StartChild(ctx context.Context, in StartChildInput) (string, error) {
	var id string
	err := s.DB.InOrg(ctx, in.Parent.Run.Org, func(tx pgx.Tx) error {
		var err error
		id, err = StartChildTx(ctx, tx, in)
		return err
	})
	return id, err
}

func StartChildTx(ctx context.Context, tx pgx.Tx, in StartChildInput) (string, error) {
	return startChildTx(ctx, tx, in, nil)
}

func startChildTx(ctx context.Context, tx pgx.Tx, in StartChildInput, betweenLocks func()) (string, error) {
	phase, err := childPhase(in.Profile)
	if err != nil {
		return "", err
	}
	if in.Parent.Kind != ChildParentTaskConductor || in.Key == "" {
		return "", refuseText(rChildParentKeyRequired)
	}
	if in.Capabilities.PublishCode != Publishes[phase] {
		return "", refuseText(rChildCapabilitiesMismatch)
	}
	if len([]rune(in.Note)) > 4000 {
		return "", refuseText(rChildNoteTooLong)
	}
	ref := in.Parent.Run
	var id string
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return "", err
	}
	var live bool
	var parentAttempt int
	// Chat, the parent Run, the delivery, then the task: the Run before
	// the delivery as a publish takes them, the delivery before the task
	// as every other holder of both does (LockEscalationTx).
	err = tx.QueryRow(ctx, `SELECT `+LiveConductor+` AND r.phase IS NULL
			AND r.source_kind IS NULL AND r.replaced_by IS NULL AND r.control <> 'abort'
			AND NOT COALESCE(`+Ending+`, false), r.attempt
			FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND r.project_id = $3
			AND r.organization_id = $4 FOR UPDATE`, ref.RunID, ref.TaskID, ref.ProjectID, ref.Org).Scan(&live, &parentAttempt)
	if db.IsNotFound(err) {
		return "", refuseText(rChildParentUnauthorized)
	}
	if err != nil {
		return "", err
	}
	if !live {
		return "", refuseText(rChildParentEnded)
	}
	d, err := LoadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	if betweenLocks != nil {
		betweenLocks()
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1 FOR UPDATE`, ref.TaskID).Scan(&status); err != nil {
		return "", err
	}
	if Ended(status) {
		return "", refuseText(rChildTaskEnded)
	}
	keyBytes, _ := json.Marshal([]string{string(in.Parent.Kind), ref.RunID, in.Key})
	key := "child:" + string(keyBytes)
	id, err = runByKey(ctx, tx, ref.TaskID, key)
	if err != nil || id != "" {
		return id, err
	}
	if d == nil || !d.Live() {
		return "", refuseText(rChildNoLiveDelivery)
	}
	attempt, err := EffectiveChildAttemptTx(ctx, tx, ref.TaskID, d)
	if err != nil {
		return "", err
	}
	if attempt != parentAttempt {
		return "", refuseText(rChildParentOldAttempt)
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
		TaskID: ref.TaskID, Phase: phase, WorkflowID: d.WorkflowID, BaseRefs: d.State.Heads, Attempt: attempt,
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
