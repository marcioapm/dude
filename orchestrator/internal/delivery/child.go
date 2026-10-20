package delivery

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

type ChildParentKind string

const (
	ChildParentTaskConductor ChildParentKind = "task_conductor"
	ChildParentSession       ChildParentKind = "session"
)

type ChildParent struct {
	Kind ChildParentKind
	Run  RunRef
}

type ChildCapabilities struct {
	PublishCode bool `json:"publishCode"`
	// A verifier (verify): the repository and commit it checks. Its
	// reply's command results are read at settlement and given to the
	// parent with its result.
	Verify *VerifyTarget `json:"verify,omitempty"`
}

// VerifyTarget is what a verifier checks: a commit of one repository, and
// the commands it was asked to run (none when it was asked for paths or
// the change's tests).
type VerifyTarget struct {
	Repo     string   `json:"repo"`
	Commit   string   `json:"commit"`
	Commands []string `json:"commands,omitempty"`
}

type StartChildInput struct {
	Parent                           ChildParent
	Profile, Prompt, Note, Tier, Key string
	Capabilities                     ChildCapabilities
	// Commits to check out in place of the task's heads, by repository.
	BaseRefs map[string]string
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
	if in.Key == "" {
		return "", refuseText(rChildParentKeyRequired)
	}
	if in.Parent.Kind == ChildParentSession {
		return startSessionChildTx(ctx, tx, in, phase, betweenLocks)
	}
	if in.Parent.Kind != ChildParentTaskConductor {
		return "", refuseText(rChildParentUnauthorized)
	}
	if in.Capabilities.PublishCode != Publishes[phase] || in.Capabilities.Verify != nil && Publishes[phase] {
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
	bases := maps.Clone(d.State.Heads)
	if len(in.BaseRefs) > 0 && bases == nil {
		bases = map[string]string{}
	}
	maps.Copy(bases, in.BaseRefs)
	id, err = createPhaseRunTx(ctx, tx, ref.Org, PhaseRun{
		TaskID: ref.TaskID, Phase: phase, WorkflowID: d.WorkflowID, BaseRefs: bases, Attempt: attempt,
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

func startSessionChildTx(ctx context.Context, tx pgx.Tx, in StartChildInput, phase string, betweenProbe func()) (string, error) {
	if in.Profile != "investigator" || in.Capabilities.PublishCode {
		return "", refuseText(rChildSessionProfile)
	}
	ref := in.Parent.Run
	if err := LockSession(ctx, tx, ref.SessionID); err != nil {
		return "", err
	}
	var parent string
	err := tx.QueryRow(ctx, `SELECT r.id FROM runs r WHERE r.id=$1
		AND r.organization_id=$2 AND r.session_id=$3 AND r.role='brainstorm' AND r.kind='agent'
		AND `+LiveBrainstorm+` AND r.replaced_by IS NULL AND r.control <> 'abort'
		AND NOT COALESCE(`+Ending+`, false) FOR UPDATE`, ref.RunID, ref.Org, ref.SessionID).Scan(&parent)
	live := err == nil
	if db.IsNotFound(err) {
		err = nil
	}
	if err != nil {
		return "", err
	}
	if !live {
		return "", refuseText(rChildSessionParentEnded)
	}
	var id string
	key := "child:" + ref.RunID + ":" + in.Key
	err = tx.QueryRow(ctx, `SELECT id FROM runs WHERE session_id=$1 AND creation_key=$2`, ref.SessionID, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !db.IsNotFound(err) {
		return "", err
	}
	// betweenProbe runs after the key probe misses and before the insert, so a
	// test can commit a competing row there and reach the conflict re-read.
	if betweenProbe != nil {
		betweenProbe()
	}
	tier := ""
	if in.Tier != "" {
		tier, err = tierForRole(ctx, tx, in.Tier, PromptRoleForPhase[phase])
		if err != nil {
			return "", err
		}
	}
	id = ids.New(ids.Run)
	capabilities, _ := json.Marshal(in.Capabilities)
	err = tx.QueryRow(ctx, `INSERT INTO runs (id, organization_id, session_id, attempt, status, phase, role,
		parent_run_id, parent_session_id, source_kind, child_profile, child_prompt, child_capabilities, creation_key, base_refs)
		VALUES ($1,$2,$3,1,'pending',$4::run_phase,$5::agent_role,$6,$3,'session',$7,$8,$9::jsonb,$10,'{}'::jsonb)
		ON CONFLICT DO NOTHING RETURNING id`,
		id, ref.Org, ref.SessionID, phase, RoleForPhase[phase], ref.RunID, in.Profile, db.Nullable(in.Prompt), capabilities, key).Scan(&id)
	if err == pgx.ErrNoRows {
		if err := tx.QueryRow(ctx, `SELECT id FROM runs WHERE session_id=$1 AND creation_key=$2`, ref.SessionID, key).Scan(&id); err != nil {
			return "", err
		}
		return id, nil
	} else if err != nil {
		return "", err
	}
	if in.Note != "" {
		if _, err = tx.Exec(ctx, `UPDATE runs SET conductor_note=$2 WHERE id=$1`, id, in.Note); err != nil {
			return "", err
		}
	}
	if tier != "" {
		if _, err = tx.Exec(ctx, `UPDATE runs SET tier_override=$2 WHERE id=$1`, id, tier); err != nil {
			return "", err
		}
	}
	ev := ref.Event("run.child_started", ledger.ActorAgent, map[string]any{
		"parentRunId": ref.RunID, "profile": in.Profile, "sourceKind": string(in.Parent.Kind),
	})
	ev.RunID = id
	_, err = ledger.Append(ctx, tx, ev)
	return id, err
}
