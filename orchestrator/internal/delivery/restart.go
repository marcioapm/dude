package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Restarting a phase Run: the same phase and category, in the same slot of
// the same round of the delivery, started fresh from the task's heads. The
// conductor's restart_run and the owner's Restart on a stalled Run's banner
// both come here. Steering keeps the agent's context; a restart starts over.

// EvRunRestarted is a phase Run replaced by a fresh one in its slot.
// Payload: {from, to, by?, note, tier?}: by is "conductor" for the
// conductor's restart_run (the actor is its Run), absent for a person's.
const EvRunRestarted = "run.restarted"

// UnreadRunAborted is why a Run's unread directives fail when it is aborted.
const UnreadRunAborted = "the run was aborted before the agent read it"

// Restart is a restart asked for: the Run, why, the tier it runs on in
// place of its role's ("" for the role's), and who asks.
type Restart struct {
	RunID, Note, Tier string
	Actor             Writer
	// The conductor's Run, for its restart_run; "" for a person.
	Conductor string
}

// RestartRunTx aborts a live phase Run the task's delivery waits on and
// creates its replacement in the same slot, in one transaction, under the
// task's lock order (LockEscalationTx: delivery → task, then the Run). The
// caller holds the Chat lock first where it takes it (the conductor's
// tool). Refused (Refusal) for a Run not of the task, not one the delivery
// waits on, or already ended. Returns the new Run.
func RestartRunTx(ctx context.Context, tx pgx.Tx, org, taskID string, in Restart) (string, error) {
	projectID, d, err := LockEscalationTx(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	var runTask, phase, category, status, role string
	var attempt int
	var findingIDs, blocking []string
	var feedback json.RawMessage
	var conductorRun, conductorNote string
	err = tx.QueryRow(ctx, `SELECT COALESCE(task_id, ''), COALESCE(phase::text, ''), COALESCE(category, ''), status::text,
			COALESCE(role::text, ''), attempt, finding_ids, blocking_severities, pr_feedback,
			COALESCE(conductor_run_id, ''), COALESCE(conductor_note, '')
		FROM runs WHERE id = $1 FOR UPDATE`, in.RunID).
		Scan(&runTask, &phase, &category, &status, &role, &attempt, &findingIDs, &blocking, &feedback, &conductorRun, &conductorNote)
	switch {
	case db.IsNotFound(err) || err == nil && runTask != taskID:
		return "", refusef("%s is not a Run of this task: only this task's Runs are restarted here", in.RunID)
	case err != nil:
		return "", err
	case phase == "" || role == RoleConductor:
		return "", refusef("%s is not a phase Run: only a phase Run is restarted", in.RunID)
	case !slices.Contains([]string{"pending", "scheduled", "starting", "running", "paused"}, status):
		return "", refusef("%s has ended (%s): there is nothing to restart", in.RunID, status)
	case d == nil || !d.Live() || !slices.Contains(d.State.PendingRunIDs, in.RunID):
		return "", refusef("%s is not a Run the delivery waits on: only a Run it waits on is restarted", in.RunID)
	case d.Status != "waiting":
		// A step running now writes the Runs it waits on as it read them.
		return "", refusef("the delivery is taking a step now; restart %s again in a moment", in.RunID)
	}
	note := clip(strings.TrimSpace(in.Note), noteChars)
	var tierID string
	if in.Tier != "" {
		if tierID, err = tierForRole(ctx, tx, in.Tier, PromptRoleFor(phase, role)); err != nil {
			return "", err
		}
	}
	// The old Run ends as an abort does; nothing will resume it, so lux
	// lets it go (not kept).
	if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
		control_reason = $2, ended_at = now(), keep = false
		WHERE id = $1`, in.RunID, "restarted: "+note); err != nil {
		return "", err
	}
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: in.RunID}
	if err := FailUnreadTx(ctx, tx, ref, time.Time{}, UnreadRunAborted); err != nil {
		return "", err
	}
	var prFeedback []forge.ActionableFeedback
	_ = json.Unmarshal(feedback, &prFeedback)
	newID, err := createPhaseRunTx(ctx, tx, org, PhaseRun{
		TaskID: taskID, Phase: phase, BaseRefs: d.State.Heads, ParentRunID: d.State.HeadRunID, Attempt: attempt,
		Category: category, FindingIDs: findingIDs, BlockingSeverities: blocking, PRFeedback: prFeedback,
		ConductorRunID: conductorRun, ConductorNote: conductorNote,
		RestartNote: restartLine(in.Conductor != "", note), Tier: tierID,
	})
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET replaced_by = $2 WHERE id = $1`, in.RunID, newID); err != nil {
		return "", err
	}
	// The new Run takes the old one's slot: the round waits for it, and the
	// old one's finish is not this step's (settle never sees its id again).
	swap := func(ids []string) []string {
		out := slices.Clone(ids)
		if i := slices.Index(out, in.RunID); i >= 0 {
			out[i] = newID
		}
		return db.NonNil(out)
	}
	pending, _ := json.Marshal(swap(d.State.PendingRunIDs))
	review, _ := json.Marshal(swap(d.State.ReviewRunIDs))
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(jsonb_set(state, '{pendingRunIds}', $2::jsonb),
			'{reviewRunIds}', $3::jsonb) WHERE id = $1`, d.WorkflowID, pending, review); err != nil {
		return "", err
	}
	payload := map[string]any{"from": in.RunID, "to": newID, "note": note, "phase": phase}
	if category != "" {
		payload["category"] = category
	}
	if tierID != "" {
		payload["tier"] = tierID
	}
	if in.Conductor != "" {
		payload["by"] = "conductor"
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvRunRestarted, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
		RunID: in.RunID, ActorType: in.Actor.ActorType, ActorID: in.Actor.ActorID, Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: payload})
	return newID, err
}

// restartLine is what a restarted Run's agent is told of why.
func restartLine(byConductor bool, note string) string {
	who := "a person"
	if byConductor {
		who = "the conductor"
	}
	if note == "" {
		return "Restarted by " + who + "."
	}
	return fmt.Sprintf("Restarted by %s: %s", who, note)
}

// tierForRole is the id of the organization's tier named (by id or name),
// refused when there is none or it names no model: a restart runs on a
// tier the role could be given.
func tierForRole(ctx context.Context, tx pgx.Tx, tier, role string) (string, error) {
	var id string
	var model *string
	err := tx.QueryRow(ctx, `SELECT id, model FROM model_tiers WHERE id = $1 OR name = $1 ORDER BY id = $1 DESC LIMIT 1`, tier).
		Scan(&id, &model)
	if db.IsNotFound(err) {
		return "", refusef("no model tier %q: the tiers are the organization's (in Models)", tier)
	}
	if err != nil {
		return "", err
	}
	if model == nil {
		return "", refusef("the tier %q names no model yet: a %s cannot run on it", tier, RoleName(role))
	}
	return id, nil
}

// FailUnreadTx fails what the agent of Run r, ending in this transaction,
// never read and now never will: each directive neither delivered nor
// failed gets run.directive.failed with why, its wake reasons go back to
// pending, and a conductor's steer wakes its conductor (SteerSettledTx).
// A receipt that lands after still delivers it. at is the events' time;
// zero is now.
func FailUnreadTx(ctx context.Context, tx pgx.Tx, r RunRef, at time.Time, why string) error {
	rows, err := tx.Query(ctx, `UPDATE directives SET failed_at = now(), error = $2
		WHERE run_id = $1 AND delivered_at IS NULL AND failed_at IS NULL RETURNING id`, r.RunID, why)
	if err != nil {
		return err
	}
	unread, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range unread {
		ev := r.Event(evDirectiveFailed, ledger.ActorSystem, map[string]any{"directiveId": id, "error": why})
		ev.Source, ev.OccurredAt = ledger.SourceRunner, at
		if _, err := ledger.Append(ctx, tx, ev); err != nil {
			return err
		}
		if err := RequeueWakesTx(ctx, tx, id); err != nil {
			return err
		}
		if err := SteerSettledTx(ctx, tx, r.Org, id, false, why); err != nil {
			return err
		}
	}
	return nil
}
