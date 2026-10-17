package delivery

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Close, Reopen, Archive and Unarchive (docs/design/tracking-work.md,
// Epic 1): a person's decisions about a task, each in the caller's
// transaction, refused with a LifecycleRefusal the API answers as a 409
// (or 400) with its code. Who may is the API's to check (Authorize).

// Lifecycle events.
const (
	EvTaskClosed     = "task.closed"
	EvTaskReopened   = "task.reopened"
	EvTaskArchived   = "task.archived"
	EvTaskUnarchived = "task.unarchived"
	EvEpicArchived   = "epic.archived"
	EvEpicUnarchived = "epic.unarchived"
)

// The menu's words for a task Close and Archive refuse.
const (
	StopAgentsFirst = "Stop its agents first"
	PullRequestOpen = "It has a pull request open"
)

// LifecycleRefusal is a lifecycle write refused: Code is the 409's
// (running, review, not_ended, kind, loop, unfinished, archived, conflict)
// or bad_request; Details go with it.
type LifecycleRefusal struct {
	Code, Msg string
	Details   map[string]any
}

func (r LifecycleRefusal) Error() string { return r.Msg }

func lifecycleRefuse(code, format string, a ...any) error {
	return LifecycleRefusal{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Actor is the person who closes, reopens, archives or unarchives.
type Actor struct {
	ActorType, ActorID, Person string
	// Their name, for the status change's reason ("closed as won't do by Ana").
	Name string
}

func (a Actor) event(org, projectID, taskID, typ string, payload map[string]any) ledger.Event {
	return ledger.Event{Type: typ, OrganizationID: org, ProjectID: projectID, TaskID: taskID, ActorType: a.ActorType,
		ActorID: a.ActorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID, Payload: payload}
}

// lifecycleTask is a task as the lifecycle writes read it, its row locked.
type lifecycleTask struct {
	ID, ProjectID, Key, Status, Resolution string
	EpicID                                 *string
	Archived                               bool
	// Its latest delivery, locked before the task's row; nil for none.
	Delivery *Delivery
}

// ended is lifecycle's: done, or aborted with a resolution.
func (t lifecycleTask) ended() bool {
	return t.Status == "done" || t.Status == "aborted" && t.Resolution != ""
}

// lockLifecycleTask locks the task's delivery, then its row
// (LockEscalationTx's order), and reads it.
func lockLifecycleTask(ctx context.Context, tx pgx.Tx, taskID string) (lifecycleTask, error) {
	t := lifecycleTask{ID: taskID}
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil {
		return t, err
	}
	t.Delivery = d
	err = tx.QueryRow(ctx, `SELECT t.project_id, p.key_prefix || '-' || t.number, t.status::text, COALESCE(t.resolution::text, ''),
			t.epic_id, t.archived_at IS NOT NULL
		FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1 FOR NO KEY UPDATE OF t`, taskID).
		Scan(&t.ProjectID, &t.Key, &t.Status, &t.Resolution, &t.EpicID, &t.Archived)
	if db.IsNotFound(err) {
		return t, lifecycleRefuse("not_found", "task %s not found", taskID)
	}
	return t, err
}

// refuseBusy refuses a task with agents working or a pull request open,
// in the menu's words: Close and Archive wait for those to end.
func refuseBusy(ctx context.Context, tx pgx.Tx, t lifecycleTask) error {
	switch t.Status {
	case "review", "ready_to_merge":
		return lifecycleRefuse("review", "%s: %s is in review. Closing it is GitHub's or Mark done's.", PullRequestOpen, t.Key)
	case "running", "awaiting_input":
		return lifecycleRefuse("running", "%s: %s is in progress.", StopAgentsFirst, t.Key)
	}
	// Its phase Runs: a conductor talks about the work and stays, read-only
	// once the task has ended. Its pull requests: one still open refuses
	// whatever the status (a stopped task can keep one).
	var live, open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL
		AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')),
		EXISTS (SELECT 1 FROM pull_requests WHERE task_id = $1 AND state = 'open')`, t.ID).Scan(&live, &open); err != nil {
		return err
	}
	if live {
		return lifecycleRefuse("running", "%s: an agent of %s is still working.", StopAgentsFirst, t.Key)
	}
	if open {
		return lifecycleRefuse("review", "%s: close it on GitHub first.", PullRequestOpen)
	}
	return nil
}

// CloseInput is a person's Close.
type CloseInput struct {
	TaskID, Resolution, Note string
	// The task it duplicates, by key (CP-62) or id: for a duplicate.
	DuplicateOf string
	// Delete the blocks edges from it in the same transaction.
	RemoveRelations bool
	Actor           Actor
}

// Closed is what a Close wrote.
type Closed struct {
	Key         string   `json:"key"`
	Resolution  string   `json:"resolution"`
	DuplicateOf string   `json:"duplicateOf,omitempty"`
	HandedOver  []string `json:"handedOver,omitempty"`
	Removed     []string `json:"removedRelations,omitempty"`
	eventID     string
}

// CloseTx closes a task with a resolution: refused while agents work on it
// or a pull request is open, on an ended or archived task, as Nothing to
// merge (Mark done's), and as a duplicate of itself, of a duplicate, or one
// that would close a loop.
func CloseTx(ctx context.Context, tx pgx.Tx, org string, in CloseInput) (Closed, error) {
	t, err := lockLifecycleTask(ctx, tx, in.TaskID)
	if err != nil {
		return Closed{}, err
	}
	if t.Archived {
		return Closed{}, lifecycleRefuse("archived", "%s is archived: unarchive it first.", t.Key)
	}
	if err := refuseBusy(ctx, tx, t); err != nil {
		return Closed{}, err
	}
	if t.ended() {
		return Closed{}, lifecycleRefuse("conflict", "%s has already ended.", t.Key)
	}
	return closeTx(ctx, tx, org, t, in, false)
}

// closeTx is Close past the task's own state checks, which CloseTx and
// ArchiveTaskTx make.
func closeTx(ctx context.Context, tx pgx.Tx, org string, t lifecycleTask, in CloseInput, andArchived bool) (Closed, error) {
	out := Closed{Key: t.Key, Resolution: in.Resolution}
	switch in.Resolution {
	case ResolutionWontDo, ResolutionDuplicate:
	case ResolutionCantReproduce:
		// TODO(tracking-work.md, "Close, and resolutions"): Can't reproduce
		// is a bug's (409 kind) once tasks have a kind (Epic 2).
	case ResolutionNothingToMerge:
		return out, lifecycleRefuse("bad_request", "Nothing to merge is Mark done's: only for work that ran, once there is a write-up or an answer.")
	default:
		return out, lifecycleRefuse("bad_request", "a resolution is one of wont_do, duplicate, cant_reproduce")
	}
	if err := lockRelations(ctx, tx, org); err != nil {
		return out, err
	}
	var canonical RelatedTask
	if in.Resolution == ResolutionDuplicate {
		var err error
		if canonical, err = duplicateTarget(ctx, tx, t, in.DuplicateOf); err != nil {
			return out, err
		}
		out.DuplicateOf = canonical.Key
		// A canonical task stays one: task_blocked_by follows one hop.
		var dups []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(p.key_prefix || '-' || d.number ORDER BY d.project_id, d.number), '{}')
			FROM task_relations r JOIN tasks d ON d.id = r.from_task_id JOIN projects p ON p.id = d.project_id
			WHERE r.to_task_id = $1 AND r.kind = 'duplicates'`, t.ID).Scan(&dups); err != nil {
			return out, err
		}
		if len(dups) > 0 {
			names := strings.Join(dups, ", ")
			return out, LifecycleRefusal{Code: "conflict", Details: map[string]any{"duplicates": dups},
				Msg: fmt.Sprintf("%s is what %s duplicates: reopen %s first.", t.Key, names, names)}
		}
		// What it blocks waits on the canonical task from now on: refused
		// when that closes a loop, named as Add relation names one.
		rows, err := tx.Query(ctx, `SELECT r.to_task_id, p.key_prefix || '-' || d.number FROM task_relations r
			JOIN tasks d ON d.id = r.to_task_id JOIN projects p ON p.id = d.project_id
			WHERE r.from_task_id = $1 AND r.kind = 'blocks' ORDER BY d.project_id, d.number`, t.ID)
		if err != nil {
			return out, err
		}
		type dependant struct{ ID, Key string }
		deps, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dependant])
		if err != nil {
			return out, err
		}
		for _, d := range deps {
			var loop []string
			if err := tx.QueryRow(ctx, `SELECT task_relation_would_cycle($1, $2)`, canonical.ID, d.ID).Scan(&loop); err != nil {
				return out, err
			}
			if loop != nil {
				return out, LifecycleRefusal{Code: "loop", Details: map[string]any{"loop": loop},
					Msg: fmt.Sprintf("closing %s as a duplicate of %s would make a loop: %s. A task can't wait on itself.",
						t.Key, canonical.Key, strings.Join(loop, " → "))}
			}
			out.HandedOver = append(out.HandedOver, d.Key)
		}
	}
	// A delivery parked with no agent working (at its start, or on its
	// blockers) ends with the task; its kept Runs are let go.
	if d := t.Delivery; d != nil && d.Live() {
		if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET status = 'completed', wake_at = NULL, awaiting_signals = '[]'::jsonb,
			locked_by = NULL, locked_until = NULL WHERE id = $1`, d.WorkflowID); err != nil {
			return out, err
		}
	}
	if err := ReleaseKeptTx(ctx, tx, t.ID); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status = 'aborted', resolution = $2::task_resolution, closed_by = NULLIF($3, ''),
		updated_at = now() WHERE id = $1`, t.ID, in.Resolution, in.Actor.Person); err != nil {
		return out, err
	}
	reason := "closed as " + strings.ToLower(ResolutionLabel[in.Resolution])
	if in.Actor.Name != "" {
		reason += " by " + firstName(in.Actor.Name)
	}
	if t.Status != "aborted" {
		if _, err := PeerEndedTx(ctx, tx, org, t.ID, "aborted"); err != nil {
			return out, err
		}
	}
	if err := RecordStatusTx(ctx, tx, org, t.ProjectID, t.ID, "aborted", reason); err != nil {
		return out, err
	}
	if err := EndAutoStartTx(ctx, tx, org, t.ID, "closed", in.Actor.ActorType, in.Actor.ActorID); err != nil {
		return out, err
	}
	here := RelatedTask{ID: t.ID, Key: t.Key, ProjectID: t.ProjectID}
	if in.Resolution == ResolutionDuplicate {
		if _, err := tx.Exec(ctx, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
			VALUES ($1, $2, $3, 'duplicates', $4)`, org, t.ID, canonical.ID, in.Actor.Person); err != nil {
			return out, err
		}
		if err := notifyRelations(ctx, tx, org, t.ID, canonical.ID); err != nil {
			return out, err
		}
	} else if in.RemoveRelations {
		removed, err := removeBlocksFromTx(ctx, tx, org, here, in.Actor)
		if err != nil {
			return out, err
		}
		out.Removed = removed
	}
	payload := map[string]any{"resolution": in.Resolution}
	if out.DuplicateOf != "" {
		payload["duplicateOf"] = out.DuplicateOf
	}
	if len(out.HandedOver) > 0 {
		payload["handedOver"] = out.HandedOver
	}
	if len(out.Removed) > 0 {
		payload["removedRelations"] = out.Removed
	}
	if n := strings.TrimSpace(in.Note); n != "" {
		payload["note"] = clip(n, 2000)
	}
	if andArchived {
		payload["andArchived"] = true
	}
	id, err := ledger.Append(ctx, tx, in.Actor.event(org, t.ProjectID, t.ID, EvTaskClosed, payload))
	out.eventID = id
	return out, err
}

// duplicateTarget is the task a duplicate is closed as one of, by key or
// id: refused for none, itself, or one closed as a duplicate (one hop).
func duplicateTarget(ctx context.Context, tx pgx.Tx, t lifecycleTask, ref string) (RelatedTask, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return RelatedTask{}, lifecycleRefuse("bad_request", "a duplicate names the task it duplicates (duplicateOf)")
	}
	target, found, err := TaskByOrgKey(ctx, tx, ref)
	if err != nil {
		return target, err
	}
	if !found {
		if target, err = relatedTask(ctx, tx, ref); db.IsNotFound(err) {
			return target, lifecycleRefuse("not_found", "no task %s", ref)
		} else if err != nil {
			return target, err
		}
	}
	if target.ID == t.ID {
		return target, lifecycleRefuse("bad_request", "a task can't be a duplicate of itself")
	}
	var of string
	err = tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || c.number FROM task_relations r JOIN tasks c ON c.id = r.to_task_id
		JOIN projects p ON p.id = c.project_id WHERE r.from_task_id = $1 AND r.kind = 'duplicates'`, target.ID).Scan(&of)
	if err == nil {
		return target, lifecycleRefuse("conflict", "%s was closed as a duplicate of %s: pick that one.", target.Key, of)
	}
	if !db.IsNotFound(err) {
		return target, err
	}
	return target, nil
}

// removeBlocksFromTx deletes the blocks edges from a task being closed,
// each on both tasks' history as Remove relation writes it, releasing what
// waited on nothing else. Returns the dependants' keys.
func removeBlocksFromTx(ctx context.Context, tx pgx.Tx, org string, here RelatedTask, a Actor) ([]string, error) {
	rows, err := tx.Query(ctx, `DELETE FROM task_relations r USING tasks d, projects p
		WHERE r.from_task_id = $1 AND r.kind = 'blocks' AND d.id = r.to_task_id AND p.id = d.project_id
		RETURNING d.id, p.key_prefix || '-' || d.number, d.project_id`, here.ID)
	if err != nil {
		return nil, err
	}
	deps, err := pgx.CollectRows(rows, pgx.RowToStructByPos[RelatedTask])
	if err != nil {
		return nil, err
	}
	slices.SortFunc(deps, func(x, y RelatedTask) int { return strings.Compare(x.Key, y.Key) })
	keys := make([]string, len(deps))
	for i, d := range deps {
		keys[i] = d.Key
		e := Edge{Here: here, Other: d, Side: SideBlocks, Person: a.Person}
		if err := relationEventsTx(ctx, tx, org, EvRelationRemoved, e, here, d,
			map[string]any{"kind": "blocks", "from": here.Key, "to": d.Key, "why": "closed"}); err != nil {
			return nil, err
		}
		if err := releasedByRemovalTx(ctx, tx, org, d.ID, a.ActorType, a.ActorID); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// notifyRelations tells the backend's live stream two tasks' relations changed.
func notifyRelations(ctx context.Context, tx pgx.Tx, org, a, b string) error {
	_, err := tx.Exec(ctx, `SELECT pg_notify('dude_events', $1)`,
		fmt.Sprintf(`{"organizationId":%q,"relationsChanged":{"taskIds":[%q,%q]}}`, org, a, b))
	return err
}

// ReopenTx undoes a Close: the resolution cleared, the task back in
// Backlog (received) with its runs kept, and a duplicate's edge removed,
// so what it handed over comes back. Refused on an archived task.
func ReopenTx(ctx context.Context, tx pgx.Tx, org, taskID string, a Actor) error {
	t, err := lockLifecycleTask(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if t.Archived {
		return lifecycleRefuse("archived", "%s is archived. Unarchive it first, then reopen it.", t.Key)
	}
	if t.Resolution == "" || t.Resolution == ResolutionNothingToMerge {
		return lifecycleRefuse("conflict", "%s isn't closed: there is nothing to reopen.", t.Key)
	}
	if err := lockRelations(ctx, tx, org); err != nil {
		return err
	}
	var canonical string
	err = tx.QueryRow(ctx, `DELETE FROM task_relations WHERE from_task_id = $1 AND kind = 'duplicates' RETURNING to_task_id`, t.ID).Scan(&canonical)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	if canonical != "" {
		if err := notifyRelations(ctx, tx, org, t.ID, canonical); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status = 'received', resolution = NULL, closed_by = NULL, updated_at = now()
		WHERE id = $1`, t.ID); err != nil {
		return err
	}
	reason := "reopened"
	if a.Name != "" {
		reason += " by " + firstName(a.Name)
	}
	if err := RecordStatusTx(ctx, tx, org, t.ProjectID, t.ID, "received", reason); err != nil {
		return err
	}
	_, err = ledger.Append(ctx, tx, a.event(org, t.ProjectID, t.ID, EvTaskReopened, map[string]any{"was": t.Resolution}))
	return err
}

// ArchiveInput is a person's Archive of a task: on one that hasn't ended,
// with the resolution (and the rest of Close's input) it is closed with.
type ArchiveInput struct {
	CloseInput
}

// Archived is what an Archive wrote.
type Archived struct {
	Key    string  `json:"key"`
	Closed *Closed `json:"closed,omitempty"`
}

// ArchiveTaskTx archives a task: one that has ended at once; one that
// hasn't, closed with in.Resolution first, in the same transaction (one
// batch: task.closed, then task.archived caused by it); without one,
// refused not_ended. Refused while agents work on it or a pull request is
// open, as Close is.
func ArchiveTaskTx(ctx context.Context, tx pgx.Tx, org string, in ArchiveInput) (Archived, error) {
	t, err := lockLifecycleTask(ctx, tx, in.TaskID)
	if err != nil {
		return Archived{}, err
	}
	out := Archived{Key: t.Key}
	if t.Archived {
		return out, lifecycleRefuse("conflict", "%s is already archived.", t.Key)
	}
	if err := refuseBusy(ctx, tx, t); err != nil {
		return out, err
	}
	payload := map[string]any{}
	causation := ""
	if !t.ended() {
		if in.Resolution == "" {
			return out, lifecycleRefuse("not_ended", "%s hasn't ended: archiving it needs a resolution.", t.Key)
		}
		closed, err := closeTx(ctx, tx, org, t, in.CloseInput, true)
		if err != nil {
			return out, err
		}
		out.Closed = &closed
		payload["viaClose"] = true
		causation = closed.eventID
	}
	if err := archiveRowTx(ctx, tx, t.ID, in.Actor.Person); err != nil {
		return out, err
	}
	e := in.Actor.event(org, t.ProjectID, t.ID, EvTaskArchived, payload)
	e.CausationID = causation
	_, err = ledger.Append(ctx, tx, e)
	return out, err
}

func archiveRowTx(ctx context.Context, tx pgx.Tx, taskID, person string) error {
	_, err := tx.Exec(ctx, `UPDATE tasks SET archived_at = now(), archived_by = NULLIF($2, ''), updated_at = now() WHERE id = $1`,
		taskID, person)
	return err
}

// UnarchiveTaskTx puts an archived task back as it was: still closed, if
// it was; Reopen is its own step. Refused while its epic is archived.
func UnarchiveTaskTx(ctx context.Context, tx pgx.Tx, org, taskID string, a Actor) error {
	// The epic before the task, the order epic archive and unarchive take
	// them in; FOR SHARE waits out either of those on this epic.
	var epicID, epic string
	var epicArchived bool
	err := tx.QueryRow(ctx, `SELECT e.id, e.title, e.archived_at IS NOT NULL FROM tasks t JOIN epics e ON e.id = t.epic_id
		WHERE t.id = $1 FOR SHARE OF e`, taskID).Scan(&epicID, &epic, &epicArchived)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	t, err := lockLifecycleTask(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if t.EpicID == nil && epicID != "" || t.EpicID != nil && *t.EpicID != epicID {
		return lifecycleRefuse("conflict", "%s moved epics: try again.", t.Key)
	}
	if !t.Archived {
		return lifecycleRefuse("conflict", "%s isn't archived.", t.Key)
	}
	if epicArchived {
		return lifecycleRefuse("archived", "%s's epic %s is archived: unarchive the epic first.", t.Key, epic)
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET archived_at = NULL, archived_by = NULL, updated_at = now() WHERE id = $1`, t.ID); err != nil {
		return err
	}
	_, err = ledger.Append(ctx, tx, a.event(org, t.ProjectID, t.ID, EvTaskUnarchived, map[string]any{}))
	return err
}

// TaskOwnerOf is the owner of the task aliased t, as a scalar subquery:
// its first active person by position. Every owner check reads this one.
const TaskOwnerOf = `(SELECT tp.person_id FROM task_people tp JOIN people pe ON pe.id = tp.person_id
	WHERE tp.task_id = t.id AND pe.removed_at IS NULL ORDER BY tp.position, tp.person_id LIMIT 1)`

// EpicTask is one of an epic's tasks, as the epic's archive names it.
type EpicTask struct {
	ID     string `json:"-"`
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Owner  string `json:"-"`
	ended  bool
}

// ArchiveEpicTx archives an epic with every task of it: refused, naming
// them, while any of its tasks hasn't ended (unfinished). With
// withEndedTasks, the ended ones are archived (each its task.archived) and
// the epic stays while any hasn't. authorize is asked of each task archived.
func ArchiveEpicTx(ctx context.Context, tx pgx.Tx, org, epicID string, withEndedTasks bool, a Actor, authorize func(EpicTask) error) (archivedEpic bool, tasks []string, err error) {
	var projectID, title string
	var already bool
	err = tx.QueryRow(ctx, `SELECT project_id, title, archived_at IS NOT NULL FROM epics WHERE id = $1 FOR UPDATE`, epicID).
		Scan(&projectID, &title, &already)
	if db.IsNotFound(err) {
		return false, nil, lifecycleRefuse("not_found", "epic %s not found", epicID)
	}
	if err != nil {
		return false, nil, err
	}
	if already {
		return false, nil, lifecycleRefuse("conflict", "%s is already archived.", title)
	}
	// Each task's row, in id order, as a status move would take it.
	rows, err := tx.Query(ctx, `SELECT t.id, p.key_prefix || '-' || t.number, t.title, t.status::text,
			COALESCE(`+TaskOwnerOf+`, ''),
			t.status = 'done' OR (t.status = 'aborted' AND t.resolution IS NOT NULL)
		FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.epic_id = $1 AND t.archived_at IS NULL ORDER BY t.id FOR NO KEY UPDATE OF t`, epicID)
	if err != nil {
		return false, nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (EpicTask, error) {
		var t EpicTask
		err := r.Scan(&t.ID, &t.Key, &t.Title, &t.Status, &t.Owner, &t.ended)
		return t, err
	})
	if err != nil {
		return false, nil, err
	}
	var unfinished, done []EpicTask
	for _, t := range all {
		if t.ended {
			done = append(done, t)
		} else {
			unfinished = append(unfinished, t)
		}
	}
	slices.SortFunc(unfinished, func(x, y EpicTask) int { return strings.Compare(x.Key, y.Key) })
	if len(unfinished) > 0 && !withEndedTasks {
		return false, nil, LifecycleRefusal{Code: "unfinished", Details: map[string]any{"tasks": unfinished, "ended": len(done)},
			Msg: fmt.Sprintf("%s can't be archived yet: %d of its tasks haven't ended.", title, len(unfinished))}
	}
	for _, t := range done {
		if err := authorize(t); err != nil {
			return false, nil, err
		}
	}
	archivedEpic = len(unfinished) == 0
	if archivedEpic {
		if _, err := tx.Exec(ctx, `UPDATE epics SET archived_at = now(), archived_by = NULLIF($2, ''), updated_at = now() WHERE id = $1`,
			epicID, a.Person); err != nil {
			return false, nil, err
		}
	}
	for _, t := range done {
		if err := archiveRowTx(ctx, tx, t.ID, a.Person); err != nil {
			return false, nil, err
		}
		if _, err := ledger.Append(ctx, tx, a.event(org, projectID, t.ID, EvTaskArchived, map[string]any{"viaEpic": epicID})); err != nil {
			return false, nil, err
		}
		tasks = append(tasks, t.Key)
	}
	if archivedEpic {
		e := a.event(org, projectID, "", EvEpicArchived, map[string]any{"epicId": epicID, "title": title, "tasks": len(done)})
		e.CorrelationID = epicID
		if _, err := ledger.Append(ctx, tx, e); err != nil {
			return false, nil, err
		}
	}
	return archivedEpic, tasks, nil
}

// UnarchiveEpicTx puts an epic back, with the tasks archived with it (the
// same archive's time); a task archived on its own before stays archived.
func UnarchiveEpicTx(ctx context.Context, tx pgx.Tx, org, epicID string, a Actor) ([]string, error) {
	var projectID, title string
	var at *string
	err := tx.QueryRow(ctx, `SELECT project_id, title, archived_at::text FROM epics WHERE id = $1 FOR UPDATE`, epicID).Scan(&projectID, &title, &at)
	if db.IsNotFound(err) {
		return nil, lifecycleRefuse("not_found", "epic %s not found", epicID)
	}
	if err != nil {
		return nil, err
	}
	if at == nil {
		return nil, lifecycleRefuse("conflict", "%s isn't archived.", title)
	}
	rows, err := tx.Query(ctx, `UPDATE tasks t SET archived_at = NULL, archived_by = NULL, updated_at = now()
		FROM epics e, projects p WHERE e.id = $1 AND t.epic_id = e.id AND t.archived_at = e.archived_at AND p.id = t.project_id
		RETURNING t.id, p.key_prefix || '-' || t.number`, epicID)
	if err != nil {
		return nil, err
	}
	type back struct{ ID, Key string }
	restored, err := pgx.CollectRows(rows, pgx.RowToStructByPos[back])
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE epics SET archived_at = NULL, archived_by = NULL, updated_at = now() WHERE id = $1`, epicID); err != nil {
		return nil, err
	}
	keys := make([]string, len(restored))
	for i, r := range restored {
		keys[i] = r.Key
		if _, err := ledger.Append(ctx, tx, a.event(org, projectID, r.ID, EvTaskUnarchived, map[string]any{"viaEpic": epicID})); err != nil {
			return nil, err
		}
	}
	slices.Sort(keys)
	e := a.event(org, projectID, "", EvEpicUnarchived, map[string]any{"epicId": epicID, "title": title, "tasks": len(restored)})
	e.CorrelationID = epicID
	_, err = ledger.Append(ctx, tx, e)
	return keys, err
}
