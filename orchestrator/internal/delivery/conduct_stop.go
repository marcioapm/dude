package delivery

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// StopReasonMax bounds the reason the conductor gives stop_run, in
// characters.
const StopReasonMax = 500

// What ConductStopRunTx answers: the child asked to abort by this call, or
// asked already.
const (
	StopStopping        = "stopping"
	StopAlreadyStopping = "already_stopping"
)

// ConductStopRunTx is the stop_run tool: the task's live conductor stops a
// live child agent it started (start_agent, start_agents, verify), as a
// person's abort of a child does (AbortChildTx), with its reason. The
// child's result comes back to the conductor as aborted, or in its
// cohort's digest. A phase Run the delivery waits on is refused: the
// conductor restarts one (restart_run); stopping a step is a person's.
// Locks: Chat, then lockChild's order; nothing before Chat.
func ConductStopRunTx(ctx context.Context, tx pgx.Tx, ref RunRef, runID, reason string) (string, error) {
	// As ConductSteerFeedback: the Chat lock serialises the check with a
	// conductor's end or replacement, so a superseded conductor's late call
	// does nothing.
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return "", err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
		AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
		return "", err
	}
	if !live {
		return "", refuseText(rYouAreNoLongerThisTaskStop)
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, ref.TaskID).Scan(&status); err != nil {
		return "", err
	}
	if Ended(status) {
		return "", refusef(rThisTaskYouAreReadOnlyStop, status)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", refuseText(rSayWhyReasonRequiredStop)
	}
	if utf8.RuneCountInString(reason) > StopReasonMax {
		return "", refusef(rStopReasonAtMostChars, StopReasonMax)
	}
	// Unlocked: what the Run is never changes; whether it lives is read
	// again under the child's lock.
	var taskID, source, parent, role, phase, rstatus string
	err := tx.QueryRow(ctx, `SELECT COALESCE(task_id, ''), COALESCE(source_kind, ''), COALESCE(parent_run_id, ''),
			COALESCE(role::text, ''), COALESCE(phase::text, ''), status::text
		FROM runs WHERE id = $1`, runID).Scan(&taskID, &source, &parent, &role, &phase, &rstatus)
	switch {
	case db.IsNotFound(err) || err == nil && taskID != ref.TaskID:
		return "", refusef(rNotRunYourTaskStop, runID)
	case err != nil:
		return "", err
	case runID == ref.RunID || role == RoleConductor:
		return "", refusef(rStopRunConductor, runID)
	case source == "" && phase != "":
		return "", refusef(rStopRunPhaseRun, runID)
	case source != string(ChildParentTaskConductor):
		// A branch preview, a session's child: nothing a conductor started.
		return "", refusef(rNotRunYourTaskStop, runID)
	case parent != ref.RunID:
		return "", refusef(rNotYourChildStop, runID)
	}
	if !runLive(rstatus) {
		return "", refusef(rHasEndedNothingStop, runID, rstatus)
	}
	asked, locked, err := abortChildTx(ctx, tx, ref.TaskID, runID, reason)
	if err != nil {
		return "", err
	}
	if !asked {
		if !runLive(locked) {
			return "", refusef(rHasEndedNothingStop, runID, locked)
		}
		return StopAlreadyStopping, nil
	}
	// The event a person's abort writes, by the conductor: Chat and
	// Activity show who stopped it and why.
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: "run.aborted", OrganizationID: ref.Org, ProjectID: ref.ProjectID,
		TaskID: ref.TaskID, RunID: runID, ActorType: ledger.ActorAgent, ActorID: ref.RunID, Source: ledger.SourceOrchestrator,
		CorrelationID: ref.TaskID, Payload: map[string]any{"by": "conductor", "conductorRunId": ref.RunID, "reason": reason}})
	if err != nil {
		return "", err
	}
	return StopStopping, nil
}

// runLive says a Run's status is not an end.
func runLive(status string) bool {
	switch status {
	case "pending", "scheduled", "starting", "running", "paused":
		return true
	}
	return false
}
