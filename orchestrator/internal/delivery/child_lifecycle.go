package delivery

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

type ChildOutcome struct {
	Status string
}

type lockedChild struct {
	TaskID, ParentID, Profile, Phase, Status, StopReason, ReplacedBy string
	Prompt, Note, Tier, Branch                                       string
	Attempt, Generation                                              int
	Capabilities                                                     ChildCapabilities
	BaseRefs                                                         map[string]string
	Actionable                                                       bool
	ParentLive                                                       bool
}

// Read attribution without a lock, then acquire parent-before-child locks.
// Attribution is rechecked after locking; no request can select a different parent.
func lockChild(ctx context.Context, tx pgx.Tx, childID string) (*lockedChild, error) {
	c := &lockedChild{}
	err := tx.QueryRow(ctx, `SELECT task_id, parent_run_id FROM runs
		WHERE id = $1 AND source_kind = 'task_conductor'`, childID).Scan(&c.TaskID, &c.ParentID)
	if db.IsNotFound(err) {
		return nil, refusef("not an independent task child")
	}
	if err != nil {
		return nil, err
	}
	if err := LockChat(ctx, tx, c.TaskID); err != nil {
		return nil, err
	}
	var live bool
	err = tx.QueryRow(ctx, `SELECT `+LiveConductor+` AND r.source_kind IS NULL AND r.phase IS NULL
		AND r.replaced_by IS NULL AND r.control <> 'abort' AND NOT COALESCE(`+Ending+`, false)
		FROM runs r WHERE r.id = $1 AND r.task_id = $2 FOR UPDATE`, c.ParentID, c.TaskID).Scan(&live)
	if err != nil {
		return nil, err
	}
	var taskStatus string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1 FOR UPDATE`, c.TaskID).Scan(&taskStatus); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT child_profile, phase::text, status::text, attempt, child_generation,
		COALESCE(child_stop_reason, ''), COALESCE(replaced_by, ''), COALESCE(child_prompt, ''),
		COALESCE(conductor_note, ''), COALESCE(tier_override, ''), COALESCE(branch, ''), child_capabilities, base_refs
		FROM runs WHERE id = $1 AND task_id = $2 AND parent_run_id = $3 AND source_kind = 'task_conductor' FOR UPDATE`, childID, c.TaskID, c.ParentID).
		Scan(&c.Profile, &c.Phase, &c.Status, &c.Attempt, &c.Generation, &c.StopReason, &c.ReplacedBy,
			&c.Prompt, &c.Note, &c.Tier, &c.Branch, &c.Capabilities, &c.BaseRefs)
	if db.IsNotFound(err) {
		return nil, refusef("child ownership changed")
	}
	if err != nil {
		return nil, err
	}
	d, err := ReadDelivery(ctx, tx, c.TaskID)
	if err != nil {
		return nil, err
	}
	c.ParentLive = live && !Ended(taskStatus) && d != nil && d.Live() && d.State.Attempt == c.Attempt
	c.Actionable = c.ParentLive && c.ReplacedBy == "" && c.StopReason != "parent-ended"
	return c, nil
}

func (s *Store) SettleChild(ctx context.Context, org, childID string, outcome ChildOutcome) (bool, error) {
	return s.SettleChildWith(ctx, org, childID, outcome, nil)
}

// SettleChildWith collects role results under the same parent/child locks as
// settlement, so another notifier cannot replace findings after the snapshot.
func (s *Store) SettleChildWith(ctx context.Context, org, childID string, outcome ChildOutcome, collect func(pgx.Tx) error) (bool, error) {
	if outcome.Status != "completed" && outcome.Status != "failed" && outcome.Status != "aborted" {
		return false, refusef("a terminal child outcome is required")
	}
	created := false
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		c, err := lockChild(ctx, tx, childID)
		if err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM child_results WHERE parent_run_id = $1 AND child_run_id = $2 AND generation = $3)`, c.ParentID, childID, c.Generation).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		if collect != nil {
			if err := collect(tx); err != nil {
				return err
			}
		}
		status := outcome.Status
		if c.StopReason != "" {
			status = "aborted"
		}
		// Summaries contain counts and validated identifiers only, never finding,
		// artifact, or agent prose. Bound lists independently of stored Run size.
		_, err = tx.Exec(ctx, `INSERT INTO child_results (organization_id, task_id, parent_run_id, child_run_id,
			generation, source_kind, profile, status, findings, verdicts, commits, artifact_ids, cost_usd, stale)
			SELECT r.organization_id, r.task_id, r.parent_run_id, r.id, r.child_generation, r.source_kind, r.child_profile, $2,
			(SELECT count(*) FROM review_findings WHERE run_id = r.id),
			(SELECT count(*) FROM acceptance_verdicts WHERE run_id = r.id),
			COALESCE((SELECT jsonb_object_agg(k, v) FROM (SELECT k, v FROM (SELECT key AS k,
			  CASE jsonb_typeof(value) WHEN 'object' THEN value->>'sha' ELSE value #>> '{}' END AS v FROM jsonb_each(r.heads)) normalized
			  WHERE k ~ '^[A-Za-z0-9_.-]{1,100}$' AND v ~ '^[0-9a-f]{40,64}$' ORDER BY k LIMIT 100) refs), '{}'),
			ARRAY(SELECT id FROM artifacts WHERE run_id = r.id ORDER BY id LIMIT 100),
			GREATEST(0, run_model_usd(r))::double precision, $3
			FROM runs r WHERE r.id = $1`, childID, status, !c.Actionable)
		if err != nil {
			return err
		}
		if c.Actionable {
			if _, err := tx.Exec(ctx, `INSERT INTO child_result_outbox (organization_id, parent_run_id, child_run_id, generation)
				VALUES ($1, $2, $3, $4)`, org, c.ParentID, childID, c.Generation); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = $2::run_status, ended_at = COALESCE(ended_at, now()), keep = false WHERE id = $1`, childID, status); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created && err == nil, err
}

// ConsumeChildResult is the durable parent acknowledgement, not an HTTP sender.
// A restarted worker cannot consume the same generation twice or retarget it.
func (s *Store) ConsumeChildResult(ctx context.Context, org, parentID, childID string, generation int) (bool, error) {
	consumed := false
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		c, err := lockChild(ctx, tx, childID)
		if err != nil {
			return err
		}
		if parentID != c.ParentID || generation != c.Generation {
			return nil
		}
		if !c.Actionable {
			if _, err := tx.Exec(ctx, `UPDATE child_results SET stale = true WHERE parent_run_id = $1 AND child_run_id = $2 AND generation = $3`, parentID, childID, generation); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE child_result_outbox SET stale_at = COALESCE(stale_at, now())
				WHERE parent_run_id = $1 AND child_run_id = $2 AND generation = $3 AND consumed_at IS NULL`, parentID, childID, generation)
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE child_result_outbox SET consumed_at = now()
			WHERE parent_run_id = $1 AND child_run_id = $2 AND generation = $3 AND consumed_at IS NULL AND stale_at IS NULL
			AND EXISTS (SELECT 1 FROM child_results r WHERE r.parent_run_id = $1 AND r.child_run_id = $2 AND r.generation = $3 AND NOT r.stale)`, parentID, childID, generation)
		consumed = err == nil && tag.RowsAffected() == 1
		return err
	})
	return consumed && err == nil, err
}

func (s *Store) AbortChild(ctx context.Context, org, taskID, childID string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return AbortChildTx(ctx, tx, taskID, childID)
	})
}

func AbortChildTx(ctx context.Context, tx pgx.Tx, taskID, childID string) error {
	c, err := lockChild(ctx, tx, childID)
	if err != nil {
		return err
	}
	if c.TaskID != taskID {
		return refusef("child does not belong to this task")
	}
	_, err = tx.Exec(ctx, `UPDATE runs SET control = 'abort', control_requested_at = now(),
			child_stop_reason = 'aborted', control_reason = 'child aborted', keep = false
			WHERE id = $1 AND child_stop_reason IS NULL AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, childID)
	return err
}

func (s *Store) RestartChild(ctx context.Context, org, taskID, childID, key string) (string, error) {
	var id string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) { id, err = RestartChildTx(ctx, tx, org, taskID, childID, key); return err })
	return id, err
}

func RestartChildTx(ctx context.Context, tx pgx.Tx, org, taskID, childID, key string) (string, error) {
	if key == "" {
		return "", refusef("a restart idempotency key is required")
	}
	var id string
	err := func() error {
		c, err := lockChild(ctx, tx, childID)
		if err != nil {
			return err
		}
		if c.TaskID != taskID {
			return refusef("child does not belong to this task")
		}
		// Replaced children can replay their own restart request, but not after
		// the parent has ended or the task has moved to another attempt.
		if c.ReplacedBy != "" {
			var previousKey string
			if err := tx.QueryRow(ctx, `SELECT child_restart_key FROM runs WHERE id = $1`, c.ReplacedBy).Scan(&previousKey); err != nil {
				return err
			}
			if previousKey != key {
				return refusef("this child already has a replacement")
			}
			if !c.ParentLive {
				return refusef("the parent is no longer live for this attempt")
			}
			id = c.ReplacedBy
			return nil
		}
		if !c.Actionable {
			return refusef("the parent is no longer live for this attempt")
		}
		if Publishes[c.Phase] {
			d, err := LoadDelivery(ctx, tx, taskID)
			if err != nil {
				return err
			}
			if d == nil || !d.Live() || d.State.Attempt != c.Attempt {
				return refusef("the parent is no longer live for this attempt")
			}
			if err := refuseWriterAdmissionTx(ctx, tx, taskID, childID, d); err != nil {
				return err
			}
		}
		encoded, _ := json.Marshal([]string{childID, key})
		id, err = createPhaseRunTx(ctx, tx, org, PhaseRun{TaskID: taskID, Phase: c.Phase,
			BaseRefs: c.BaseRefs, ParentRunID: c.ParentID, Attempt: c.Attempt, ConductorRunID: c.ParentID,
			ConductorNote: c.Note, Tier: c.Tier, Branch: c.Branch, Key: "child-restart:" + string(encoded), Child: true})
		if err != nil {
			return err
		}
		policy, _ := json.Marshal(c.Capabilities)
		if _, err := tx.Exec(ctx, `UPDATE runs SET source_kind = 'task_conductor', child_profile = $2, child_prompt = $3,
			child_capabilities = $4::jsonb, child_generation = $5, child_replaces = $6, child_restart_key = $7 WHERE id = $1`,
			id, c.Profile, db.Nullable(c.Prompt), policy, c.Generation+1, childID, key); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE runs SET replaced_by = $2,
			child_stop_reason = CASE WHEN status IN ('pending', 'scheduled', 'starting', 'running', 'paused') THEN 'restarted' ELSE child_stop_reason END,
			control = CASE WHEN status IN ('pending', 'scheduled', 'starting', 'running', 'paused') THEN 'abort'::run_control ELSE control END,
			control_requested_at = now(), keep = false WHERE id = $1`, childID, id)
		return err
	}()
	return id, err
}

func (s *Store) EndChildParent(ctx context.Context, org, taskID, parentID string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := LockChat(ctx, tx, taskID); err != nil {
			return err
		}
		var live bool
		if err := tx.QueryRow(ctx, `SELECT `+LiveConductor+` AND r.replaced_by IS NULL AND r.control <> 'abort'
			AND NOT COALESCE(`+Ending+`, false) FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND r.source_kind IS NULL FOR UPDATE`, parentID, taskID).Scan(&live); err != nil {
			if db.IsNotFound(err) {
				return refusef("not this task's parent")
			}
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1 FOR UPDATE`, taskID).Scan(&status); err != nil {
			return err
		}
		if live && !Ended(status) {
			return refusef("the parent has not ended")
		}
		_, err := tx.Exec(ctx, `UPDATE runs SET child_stop_reason = 'parent-ended', control = 'abort',
			control_requested_at = now(), control_reason = 'child parent ended', keep = false
			WHERE task_id = $1 AND parent_run_id = $2 AND source_kind = 'task_conductor'
			AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused') AND child_stop_reason IS DISTINCT FROM 'parent-ended'`, taskID, parentID)
		return err
	})
}

// EnqueueChildResults claims bounded work and commits the parent's durable input
// and receipt together. A crash rolls both back; lux transport happens later.
func (s *Store) EnqueueChildResults(ctx context.Context) (int, error) {
	type pending struct {
		Org, Parent, Child string
		Generation         int
	}
	var todo []pending
	if err := s.DB.InSystem(ctx, "phase-sync", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT organization_id, parent_run_id, child_run_id, generation FROM child_result_outbox
			WHERE consumed_at IS NULL AND stale_at IS NULL AND (claimed_until IS NULL OR claimed_until < now())
			ORDER BY created_at LIMIT 50`)
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[pending])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, p := range todo {
		err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error {
			c, err := lockChild(ctx, tx, p.Child)
			if err != nil {
				return err
			}
			token := ids.New(ids.Directive)
			tag, err := tx.Exec(ctx, `UPDATE child_result_outbox SET claimed_by = $4, claimed_until = now() + interval '1 minute'
				WHERE parent_run_id = $1 AND child_run_id = $2 AND generation = $3 AND consumed_at IS NULL AND stale_at IS NULL
				AND (claimed_until IS NULL OR claimed_until < now())`, p.Parent, p.Child, p.Generation, token)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			if !c.Actionable || c.ParentID != p.Parent {
				if _, err := tx.Exec(ctx, `UPDATE child_results SET stale = true WHERE parent_run_id=$1 AND child_run_id=$2 AND generation=$3`, p.Parent, p.Child, p.Generation); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `UPDATE child_result_outbox SET stale_at=now(), claimed_by=NULL, claimed_until=NULL WHERE parent_run_id=$1 AND child_run_id=$2 AND generation=$3`, p.Parent, p.Child, p.Generation)
				return err
			}
			var summary []byte
			if err := tx.QueryRow(ctx, `SELECT jsonb_build_object('childId',child_run_id,'profile',profile,'status',status,
				'findings',findings,'verdicts',verdicts,'commits',commits,'artifactIds',artifact_ids,'modelCostUsd',cost_usd,'stale',stale)
				FROM child_results WHERE parent_run_id=$1 AND child_run_id=$2 AND generation=$3 AND NOT stale`, p.Parent, p.Child, p.Generation).Scan(&summary); err != nil {
				return err
			}
			text, err := childResultText(summary)
			if err != nil {
				return err
			}
			directive, _, err := QueueDirective(ctx, tx, RunRef{Org: p.Org, TaskID: c.TaskID, RunID: c.ParentID}, Directive{Text: text, Scope: "run"})
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE child_result_outbox SET consumed_at=now(), directive_id=$4, claimed_by=NULL, claimed_until=NULL
				WHERE parent_run_id=$1 AND child_run_id=$2 AND generation=$3 AND claimed_by=$5`, p.Parent, p.Child, p.Generation, directive, token)
			if err == nil {
				n++
			}
			return err
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func childResultText(summary []byte) (string, error) {
	var out strings.Builder
	err := mustTemplate("contracts/child-result").Execute(&out, string(summary))
	return out.String(), err
}

// The caller holds Chat before the parent Run and task rows.
func MarkChildParentEndedTx(ctx context.Context, tx pgx.Tx, taskID, parentID string) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id=$1 AND task_id=$2 FOR UPDATE`, parentID, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM tasks WHERE id=$1 FOR UPDATE`, taskID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE runs SET child_stop_reason='parent-ended',control='abort',control_requested_at=now(),keep=false
		WHERE parent_run_id=$1 AND task_id=$2 AND source_kind='task_conductor' AND status IN ('pending','scheduled','starting','running','paused')`, parentID, taskID)
	return err
}
