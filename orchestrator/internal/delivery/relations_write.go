package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Writing relations from the orchestrator: an agent's tools and a filed
// proposal. The backend's routes write the same rows (relations.ts).

// Relation sides as a tool names them, from the task it is on.
const (
	SideBlockedBy = "blocked_by"
	SideBlocks    = "blocks"
	SideRelated   = "related"
)

// RelatedTask is a task an edge names: its id, key and project.
type RelatedTask struct{ ID, Key, ProjectID string }

// TaskByOrgKey finds a task of the organisation by its key (CP-41).
func TaskByOrgKey(ctx context.Context, tx pgx.Tx, key string) (RelatedTask, bool, error) {
	k, ok := TaskKey(key)
	if !ok {
		return RelatedTask{}, false, nil
	}
	dash := strings.LastIndexByte(k, '-')
	var t RelatedTask
	err := tx.QueryRow(ctx, `SELECT t.id, p.key_prefix || '-' || t.number, t.project_id FROM tasks t
		JOIN projects p ON p.id = t.project_id WHERE upper(p.key_prefix) = $1 AND t.number = $2::int`, k[:dash], k[dash+1:]).
		Scan(&t.ID, &t.Key, &t.ProjectID)
	if db.IsNotFound(err) {
		return t, false, nil
	}
	return t, err == nil, err
}

// relatedTask is a task by id, as an edge names it.
func relatedTask(ctx context.Context, tx pgx.Tx, id string) (RelatedTask, error) {
	t := RelatedTask{ID: id}
	err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number, t.project_id FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, id).Scan(&t.Key, &t.ProjectID)
	return t, err
}

// Edge is one relation to write: from the task Here, a side and the other.
type Edge struct {
	Here, Other RelatedTask
	// SideBlockedBy, SideBlocks or SideRelated, read from Here.
	Side string
	// Who: a person, or an agent's Run; ProposedByRun for a filed proposal.
	Person, RunID, ProposedByRun string
	Why                          string
}

// RelationRefusal is an edge refused: a self-relation, one already there,
// or one that would close a loop (Loop, its keys).
type RelationRefusal struct {
	Code string
	Msg  string
	Loop []string
}

func (r RelationRefusal) Error() string { return r.Msg }

// ends is the edge as stored: from, to, kind.
func (e Edge) ends() (from, to RelatedTask, kind string) {
	switch e.Side {
	case SideBlockedBy:
		return e.Other, e.Here, "blocks"
	case SideBlocks:
		return e.Here, e.Other, "blocks"
	}
	from, to = e.Here, e.Other
	if to.ID < from.ID {
		from, to = to, from
	}
	return from, to, "related"
}

// lockRelations is the organisation's relation lock: a loop's check and
// the insert that would close it are one writer's at a time.
func lockRelations(ctx context.Context, tx pgx.Tx, org string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('task_relations:' || $1))`, org)
	return err
}

// AddRelationTx writes an edge, on both tasks' history: refused when it
// names its own task, exists already, or for blocks would close a loop.
func AddRelationTx(ctx context.Context, tx pgx.Tx, org string, e Edge) error {
	if e.Here.ID == e.Other.ID {
		return RelationRefusal{Code: "self", Msg: "a task can't be related to itself"}
	}
	from, to, kind := e.ends()
	if err := lockRelations(ctx, tx, org); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_relations WHERE (from_task_id = $1 AND to_task_id = $2)
		OR (from_task_id = $2 AND to_task_id = $1))`, from.ID, to.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return RelationRefusal{Code: "duplicate", Msg: fmt.Sprintf("%s and %s are already related", e.Here.Key, e.Other.Key)}
	}
	if kind == "blocks" {
		var loop []string
		if err := tx.QueryRow(ctx, `SELECT task_relation_would_cycle($1, $2)`, from.ID, to.ID).Scan(&loop); err != nil {
			return err
		}
		if loop != nil {
			return RelationRefusal{Code: "cycle", Loop: loop,
				Msg: fmt.Sprintf("that would make a loop: %s. A task can't wait on itself", strings.Join(loop, " → "))}
		}
	}
	why := strings.TrimSpace(e.Why)
	if _, err := tx.Exec(ctx, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person,
			created_by_run_id, proposed_by_run_id, why)
		VALUES ($1, $2, $3, $4::task_relation_kind, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''))`,
		org, from.ID, to.ID, kind, e.Person, e.RunID, e.ProposedByRun, clip(why, 2000)); err != nil {
		return err
	}
	payload := map[string]any{"kind": kind, "from": from.Key, "to": to.Key}
	if why != "" {
		payload["why"] = clip(why, 2000)
	}
	if e.ProposedByRun != "" {
		payload["proposedByRun"] = e.ProposedByRun
	}
	return relationEventsTx(ctx, tx, org, EvRelationAdded, e, from, to, payload)
}

// relationEventsTx records a relation's event on both its tasks, in one
// correlation, as the person or the agent's Run that acted.
func relationEventsTx(ctx context.Context, tx pgx.Tx, org, typ string, e Edge, from, to RelatedTask, payload map[string]any) error {
	actorType, actorID := ledger.ActorAgent, e.RunID
	if e.Person != "" {
		actorType, actorID = "person", e.Person
	}
	for _, t := range []RelatedTask{from, to} {
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: org, ProjectID: t.ProjectID, TaskID: t.ID,
			RunID: e.RunID, ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: from.ID + ":" + to.ID,
			Payload: payload}); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `SELECT pg_notify('dude_events', $1)`,
		fmt.Sprintf(`{"organizationId":%q,"relationsChanged":{"taskIds":[%q,%q]}}`, org, from.ID, to.ID))
	return err
}

// RemoveAgentRelationTx removes an edge for an agent of the task Here:
// only one an agent of that same task added (created_by_run_id is a Run
// of it). With nothing open in front of the blocked task after, its start
// when unblocked ends and a delivery parked on it is told.
func RemoveAgentRelationTx(ctx context.Context, tx pgx.Tx, org string, e Edge, ownerName string) error {
	from, to, kind := e.ends()
	if err := lockRelations(ctx, tx, org); err != nil {
		return err
	}
	var ours *bool
	if err := tx.QueryRow(ctx, `SELECT r.created_by_run_id IS NOT NULL
			AND EXISTS (SELECT 1 FROM runs x WHERE x.id = r.created_by_run_id AND x.task_id = $4)
		FROM task_relations r WHERE r.from_task_id = $1 AND r.to_task_id = $2 AND r.kind = $3::task_relation_kind`,
		from.ID, to.ID, kind, e.Here.ID).Scan(&ours); err != nil && !db.IsNotFound(err) {
		return err
	}
	switch {
	case ours == nil:
		return RelationRefusal{Code: "not_found", Msg: fmt.Sprintf("%s has no %s relation with %s", e.Here.Key, strings.ReplaceAll(e.Side, "_", " "), e.Other.Key)}
	case !*ours:
		return RelationRefusal{Code: "not_ours", Msg: fmt.Sprintf("you can remove only a relation this task's agents added; ask %s, its owner", ownerName)}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_relations WHERE from_task_id = $1 AND to_task_id = $2 AND kind = $3::task_relation_kind`,
		from.ID, to.ID, kind); err != nil {
		return err
	}
	if err := relationEventsTx(ctx, tx, org, EvRelationRemoved, e, from, to, map[string]any{"kind": kind, "from": from.Key, "to": to.Key}); err != nil {
		return err
	}
	if kind != "blocks" {
		return nil
	}
	return releasedByRemovalTx(ctx, tx, org, to.ID, ledger.ActorAgent, e.RunID)
}

// releasedByRemovalTx is what removing a blocks edge into taskID releases,
// once nothing is open in front of it: its start when unblocked ends, and
// a delivery parked on it reads its blockers again.
func releasedByRemovalTx(ctx context.Context, tx pgx.Tx, org, taskID, actorType, actorID string) error {
	blockers, err := OpenBlockers(ctx, tx, taskID)
	if err != nil || len(blockers) > 0 {
		return err
	}
	if err := EndAutoStartTx(ctx, tx, org, taskID, "unblocked_by_removal", actorType, actorID); err != nil {
		return err
	}
	var wf string
	err = tx.QueryRow(ctx, `SELECT id FROM workflow_runs WHERE task_id = $1 AND status = 'waiting' AND awaiting_signals ? $2
		ORDER BY created_at DESC LIMIT 1`, taskID, SignalRelationsUnblock).Scan(&wf)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return workflow.SignalTx(ctx, tx, org, wf, SignalRelationsUnblock, map[string]any{"removed": true}, "")
}

// ConductRelationsTx is update_task's relations: the task's live conductor
// adds and removes edges of its own task, at any status. Each is checked
// as it is written; the first refused refuses the call.
func ConductRelationsTx(ctx context.Context, tx pgx.Tx, ref RunRef, add, remove []Edge) error {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return err
	}
	for _, e := range add {
		e.RunID = ref.RunID
		if err := AddRelationTx(ctx, tx, ref.Org, e); err != nil {
			return asRefusal(err)
		}
	}
	if len(remove) == 0 {
		return nil
	}
	var owner string
	_ = tx.QueryRow(ctx, `SELECT p.name FROM task_people tp JOIN people p ON p.id = tp.person_id
		WHERE tp.task_id = $1 AND p.removed_at IS NULL ORDER BY tp.position, tp.person_id LIMIT 1`, ref.TaskID).Scan(&owner)
	if owner == "" {
		owner = "the person"
	} else {
		owner = firstName(owner)
	}
	for _, e := range remove {
		e.RunID = ref.RunID
		if err := RemoveAgentRelationTx(ctx, tx, ref.Org, e, owner); err != nil {
			return asRefusal(err)
		}
	}
	return nil
}

// asRefusal is a relation refused, as the tools say a refusal.
func asRefusal(err error) error {
	if r, ok := err.(RelationRefusal); ok {
		return Refusal{Msg: r.Msg}
	}
	return err
}

// TaskRelationsOut is a task's relations as list_tasks gives them.
type TaskRelationsOut struct {
	BlockedBy []BlockerOut `json:"blockedBy"`
	Blocks    []BlocksOut  `json:"blocks"`
	Related   []string     `json:"related"`
	// Not started, and nothing unmerged in front of it.
	Startable           bool    `json:"startable"`
	StartsWhenUnblocked *string `json:"startsWhenUnblocked"`
}

// BlockerOut is a task's blocker: merged ones too, said so.
type BlockerOut struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Merged bool   `json:"merged"`
}

// BlocksOut is a task this one blocks.
type BlocksOut struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

// RelationsOf reads the relations of the given tasks, for list_tasks.
func RelationsOf(ctx context.Context, tx pgx.Tx, taskIDs []string) (map[string]*TaskRelationsOut, error) {
	out := map[string]*TaskRelationsOut{}
	rows, err := tx.Query(ctx, `SELECT t.id, t.status = ANY($2::task_status[]) AND NOT EXISTS (SELECT 1 FROM task_blocked_by(t.id)),
			(SELECT a.how FROM task_auto_starts a WHERE a.task_id = t.id)
		FROM tasks t WHERE t.id = ANY($1)`, taskIDs, notStartedStatuses)
	if err != nil {
		return nil, err
	}
	var id string
	var startable bool
	var how *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &startable, &how}, func() error {
		out[id] = &TaskRelationsOut{BlockedBy: []BlockerOut{}, Blocks: []BlocksOut{}, Related: []string{}, Startable: startable, StartsWhenUnblocked: how}
		return nil
	}); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT r.from_task_id, r.to_task_id, r.kind::text,
			fp.key_prefix || '-' || f.number, f.status::text,
			f.status = 'done' AND EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = f.id AND pr.state = 'merged'),
			tp.key_prefix || '-' || t.number, t.status::text
		FROM task_relations r JOIN tasks f ON f.id = r.from_task_id JOIN projects fp ON fp.id = f.project_id
		JOIN tasks t ON t.id = r.to_task_id JOIN projects tp ON tp.id = t.project_id
		WHERE r.from_task_id = ANY($1) OR r.to_task_id = ANY($1) ORDER BY r.created_at`, taskIDs)
	if err != nil {
		return nil, err
	}
	var fromID, toID, kind, fromKey, fromStatus, toKey, toStatus string
	var merged bool
	_, err = pgx.ForEachRow(rows, []any{&fromID, &toID, &kind, &fromKey, &fromStatus, &merged, &toKey, &toStatus}, func() error {
		if o := out[toID]; o != nil {
			if kind == "blocks" {
				o.BlockedBy = append(o.BlockedBy, BlockerOut{Key: fromKey, Status: fromStatus, Merged: merged})
			} else {
				o.Related = append(o.Related, fromKey)
			}
		}
		if o := out[fromID]; o != nil {
			if kind == "blocks" {
				o.Blocks = append(o.Blocks, BlocksOut{Key: toKey, Status: toStatus})
			} else {
				o.Related = append(o.Related, toKey)
			}
		}
		return nil
	})
	return out, err
}

// RelationLine is a task's relations on one line, as the CLI shows them:
// "blocked by CP-41 (review), CP-43 (running)".
func RelationLine(r *TaskRelationsOut) string {
	if r == nil {
		return ""
	}
	var parts []string
	var open []string
	for _, b := range r.BlockedBy {
		if !b.Merged {
			open = append(open, fmt.Sprintf("%s (%s)", b.Key, b.Status))
		}
	}
	if len(open) > 0 {
		parts = append(parts, "blocked by "+strings.Join(open, ", "))
	}
	if len(r.Blocks) > 0 {
		keys := make([]string, len(r.Blocks))
		for i, b := range r.Blocks {
			keys[i] = b.Key
		}
		parts = append(parts, "blocks "+strings.Join(keys, ", "))
	}
	if len(r.Related) > 0 {
		parts = append(parts, "related "+strings.Join(r.Related, ", "))
	}
	return strings.Join(parts, " · ")
}
