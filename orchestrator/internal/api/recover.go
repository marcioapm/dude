package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Picking a stopped task back up.
//
// A task stops — aborted by a person, or an agent's Run failed and a person
// stopped delivery there — and is then picked back up one of three ways:
//
//   - resume: the Runs that stopped go on, the same agents with their
//     conversations and their workspaces as they stopped. Possible while lux
//     keeps them (runs.kept_until, phases.Syncer.end).
//   - retry: the step that stopped runs again with new agents, on the same
//     branch, from what was pushed; its loop's budget is given back.
//   - restart: a new attempt — a new delivery on a new branch from the
//     default branch, the whole pipeline again. The last attempt's pull
//     requests are closed; its branch, Runs and findings stay with it.
//
// Only the task's owner picks it back up, as only they decide an
// escalation. A note is kept with the task, for every agent from then on,
// and for a resume is the agents' next message.

func (s *Server) recoverRoutes(mux *http.ServeMux) {
	mux.Handle("GET /internal/tasks/{id}/recover", s.auth(s.recoveryOptions))
	mux.Handle("POST /internal/tasks/{id}/recover", s.auth(s.recoverTask))
}

// stoppedTask is what a stopped task's recovery is worked out from.
type stoppedTask struct {
	ProjectID, Status string
	// Its delivery, the latest: nil for a task never delivered.
	WorkflowID, WorkflowStatus, Step *string
	State                            delivery.State
	// Its attempt: the highest of its Runs'.
	Attempt int
	// The Runs a resume takes up again: the last attempt's agent Runs that
	// stopped together (aborted, or the one whose failure stopped
	// delivery), kept in lux, and when the first of them stops being kept.
	Kept      []string
	KeptUntil *time.Time
}

// stoppedRun is one of the Runs a task stopped on.
type stoppedRun struct {
	ID    string
	Until *time.Time
	// Still kept to resume (delivery.KeptRun).
	Kept bool
}

func loadStopped(ctx context.Context, tx pgx.Tx, taskID string, lock bool) (stoppedTask, error) {
	var t stoppedTask
	forUpdate := ""
	if lock {
		forUpdate = "FOR UPDATE OF t"
	}
	var state []byte
	err := tx.QueryRow(ctx, `SELECT t.project_id, t.status::text, d.id, d.status::text, d.step, d.state,
			COALESCE((SELECT max(attempt) FROM runs WHERE task_id = t.id AND kind = 'agent'), 1)
		FROM tasks t LEFT JOIN LATERAL (SELECT * FROM workflow_runs d WHERE d.task_id = t.id
		  ORDER BY d.created_at DESC LIMIT 1) d ON true
		WHERE t.id = $1 `+forUpdate, taskID).
		Scan(&t.ProjectID, &t.Status, &t.WorkflowID, &t.WorkflowStatus, &t.Step, &state, &t.Attempt)
	if db.IsNotFound(err) {
		return t, fail(http.StatusNotFound, "not_found", "task %s not found", taskID)
	}
	if err != nil {
		return t, err
	}
	if state != nil {
		_ = json.Unmarshal(state, &t.State)
	}
	// The Runs it stopped on: those the stopped step was waiting on, or the
	// failed Run the escalation named.
	stoppedRuns := t.State.PendingRunIDs
	if e := t.State.Stopped; e != nil && e.RunID() != "" {
		stoppedRuns = []string{e.RunID()}
	}
	if len(stoppedRuns) == 0 {
		return t, nil
	}
	// Each one that ended stopped (aborted, failed) must be kept to resume:
	// a step resumed with one gone would wait on it for ever. One that
	// finished meanwhile (a reviewer done just before the abort) is not
	// resumed: its phase.finished is waiting for the step that resumes.
	rows, err := tx.Query(ctx, `SELECT id, kept_until, COALESCE(`+delivery.KeptRun+`, false) FROM runs
		WHERE id = ANY($1) AND status IN ('aborted', 'failed') ORDER BY created_at`, stoppedRuns)
	if err != nil {
		return t, err
	}
	kept, err := pgx.CollectRows(rows, pgx.RowToStructByPos[stoppedRun])
	if err != nil {
		return t, err
	}
	if len(kept) == 0 || slices.ContainsFunc(kept, func(r stoppedRun) bool { return !r.Kept }) {
		return t, nil
	}
	for _, k := range kept {
		t.Kept = append(t.Kept, k.ID)
		if k.Until != nil && (t.KeptUntil == nil || k.Until.Before(*t.KeptUntil)) {
			t.KeptUntil = k.Until
		}
	}
	return t, nil
}

// stopped says whether the task stopped where it can be picked back up:
// aborted (or failed) with a delivery to pick up.
func (t stoppedTask) stopped() bool {
	return (t.Status == "aborted" || t.Status == "failed") && t.WorkflowID != nil
}

// retryStep is the step a retry runs again, "" when there is none: the one
// an escalation stopped at, or the one that made the Runs an abort stopped.
func (t stoppedTask) retryStep() string {
	if e := t.State.Stopped; e != nil {
		return e.Step
	}
	if t.Step != nil && len(t.State.PendingRunIDs) > 0 {
		return delivery.AbortedRetryStep[*t.Step]
	}
	return ""
}

// resumeAt is the step a resume waits at again, "" when there is none.
func (t stoppedTask) resumeAt() string {
	if len(t.Kept) == 0 {
		return ""
	}
	if e := t.State.Stopped; e != nil {
		return e.At
	}
	if t.Step != nil && delivery.AbortedRetryStep[*t.Step] != "" {
		return *t.Step
	}
	return ""
}

// actions are the ways the task can be picked back up now, the one that
// fits best first: a resume while there is one; then trying again, unless
// what stopped it was a pull request closed (a fresh start is what is
// left); starting over always.
func (t stoppedTask) actions() []string {
	if !t.stopped() {
		return nil
	}
	var out []string
	closedPR := t.State.Stopped == nil && len(t.State.PendingRunIDs) == 0
	if t.resumeAt() != "" {
		out = append(out, "resume")
	}
	if !closedPR && t.retryStep() != "" {
		out = append(out, "retry")
	}
	return append(out, "restart")
}

// recoveryOptions says how a stopped task can be picked back up, and until
// when a resume can: what the task page offers.
func (s *Server) recoveryOptions(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var t stoppedTask
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) (err error) {
		t, err = loadStopped(r.Context(), tx, taskID, false)
		return err
	}); err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"taskId": taskID, "actions": db.NonNil(t.actions()),
		"attempt": t.Attempt, "keptUntil": t.KeptUntil, "runIds": db.NonNil(t.Kept)})
	return nil
}

// recoverTask picks a stopped task back up.
func (s *Server) recoverTask(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	note := strings.TrimSpace(body.Note)
	var wfID string
	var closePRs []string
	attempt := 0
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		t, err := loadStopped(r.Context(), tx, taskID, true)
		if err != nil {
			return err
		}
		if !t.stopped() {
			return fail(http.StatusConflict, "conflict", "task %s is %s, not stopped", taskID, t.Status)
		}
		if !slices.Contains(t.actions(), body.Action) {
			if body.Action == "resume" {
				return fail(http.StatusConflict, "not_kept",
					"what stopped is no longer kept to resume; try again or start over")
			}
			return fail(http.StatusBadRequest, "bad_request", "%q is not a way to pick task %s back up; one of %s",
				body.Action, taskID, strings.Join(t.actions(), ", "))
		}
		if err := ownerOnly(r.Context(), tx, taskID, principalOf(r).Person, "pick up"); err != nil {
			return err
		}
		// Out of aborted, the one way: the status change says who and why.
		if err := reopenTask(r.Context(), tx, org, t.ProjectID, taskID, "a person picked it back up: "+body.Action); err != nil {
			return err
		}
		if note != "" {
			if err := delivery.RecordDecisionTx(r.Context(), tx, org, taskID,
				"The work stopped. How should it go on?", note); err != nil {
				return err
			}
		}
		if body.Action != "resume" {
			if err := release(r.Context(), tx, taskID); err != nil {
				return err
			}
		}
		switch body.Action {
		case "restart":
			attempt = t.Attempt + 1
			st := t.State
			if wfID, err = s.startOver(r.Context(), tx, org, taskID, t.ProjectID, st.Policy, attempt); err != nil {
				return err
			}
			closePRs = st.PullRequestIDs
		default:
			wfID = *t.WorkflowID
			if err := pickUp(r.Context(), tx, org, t, body.Action, note); err != nil {
				return err
			}
		}
		return humanEvent(r.Context(), tx, org, "", runInfo{ProjectID: t.ProjectID, TaskID: taskID}, "task.recovered",
			principalOf(r), map[string]any{"action": body.Action, "note": note, "attempt": max(attempt, t.Attempt),
				"runIds": db.NonNil(t.Kept)})
	})
	if err != nil {
		return err
	}
	// The last attempt's pull requests are closed after: best effort, as a
	// person can close one on GitHub, and the new attempt does not wait on it.
	s.closePullRequests(r.Context(), org, closePRs)
	s.kick()
	write(w, http.StatusOK, map[string]any{"taskId": taskID, "action": body.Action, "workflowRunId": wfID})
	return nil
}

// reopenTask takes a stopped task out of aborted (or failed): the one move
// out of those SetTaskStatusTx refuses.
func reopenTask(ctx context.Context, tx pgx.Tx, org, projectID, taskID, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status = 'running', updated_at = now() WHERE id = $1`, taskID); err != nil {
		return err
	}
	return delivery.RecordStatusTx(ctx, tx, org, projectID, taskID, "running", reason)
}

// pickUp reopens the stopped delivery for its recover step to carry out:
// a resume takes the kept Runs back up (paused, to be resumed by the
// syncer, with the note as their next message); a retry runs the step
// again.
func pickUp(ctx context.Context, tx pgx.Tx, org string, t stoppedTask, action, note string) error {
	st := t.State
	rc := &delivery.Recover{Action: action}
	if action == "resume" {
		rc.At = t.resumeAt()
		st.PendingRunIDs = t.Kept
		if err := resumeKept(ctx, tx, org, t.ProjectID, st.TaskID, t.Kept, note); err != nil {
			return err
		}
	} else {
		rc.Step = t.retryStep()
		if e := st.Stopped; e != nil {
			rc.Reason, rc.Detail = e.Reason, e.Detail
		}
	}
	st.Recover = rc
	next, _ := json.Marshal(st)
	_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, status = 'running', step = 'recover',
			awaiting_signals = '[]'::jsonb, wake_at = NULL, last_error = NULL, attempt = 0,
			locked_by = NULL, locked_until = NULL
		WHERE id = $1`, *t.WorkflowID, next)
	return err
}

// resumeKept takes kept Runs back up: paused, for the syncer to resume as
// it resumes any (phases.Syncer.whilePaused), the agent's conversation and
// workspace as they stopped. Their next message is the note, if any, and —
// what it asks for having changed since they stopped — the task as it is
// now: an agent that goes on works to the task it has in mind.
func resumeKept(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, runIDs []string, note string) error {
	var title, goal string
	var criteria []byte
	var edited bool
	if err := tx.QueryRow(ctx, `SELECT title, goal, acceptance_criteria, EXISTS (SELECT 1 FROM events e
			WHERE e.task_id = t.id AND e.event_type = 'task.updated'
			  AND (e.payload ? 'title' OR e.payload ? 'goal' OR e.payload ? 'acceptanceCriteria')
			  AND e.occurred_at > (SELECT min(ended_at) FROM runs WHERE id = ANY($2)))
		FROM tasks t WHERE t.id = $1`, taskID, runIDs).Scan(&title, &goal, &criteria, &edited); err != nil {
		return err
	}
	if edited {
		note = strings.TrimSpace(changedTask(title, goal, criteria) + "\n\n" + note)
	}
	// Its push, if it had asked for one, is asked for again when its turn
	// ends: what it pushed then is not what it will have done by then.
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'paused', control = 'resume', control_requested_at = now(),
			control_reason = 'picked back up', lux_stop_reason = 'pause', keep = false, kept_until = NULL,
			ended_at = NULL, error = NULL, phase_notified_at = NULL, dude_pause = NULL, finishes = finishes + 1,
			push_request_id = NULL, push_result = NULL, turn_done_at = NULL, next_attempt_at = NULL
		WHERE id = ANY($1) AND `+delivery.KeptRun, runIDs)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(runIDs) {
		return fail(http.StatusConflict, "not_kept", "what stopped is no longer kept to resume; try again or start over")
	}
	if note == "" {
		return nil
	}
	for _, id := range runIDs {
		if _, _, err := insertDirective(ctx, tx, org, id, runInfo{ProjectID: projectID, TaskID: taskID}, note, "run", "", false); err != nil {
			return err
		}
	}
	return nil
}

// changedTask tells a resumed agent what its task asks for now.
func changedTask(title, goal string, rawCriteria []byte) string {
	var criteria []string
	_ = json.Unmarshal(rawCriteria, &criteria)
	var b strings.Builder
	fmt.Fprintf(&b, "While you were stopped, the task was changed. Work to it as it is now.\n\n**%s**\n\n%s", title, strings.TrimSpace(goal))
	if len(criteria) > 0 {
		b.WriteString("\n\nAcceptance criteria:")
		for _, c := range criteria {
			b.WriteString("\n- " + c)
		}
	}
	return b.String()
}

// release lets a task's stopped Runs go — a retry or a start over took
// their work up afresh, and nothing will resume them — so the syncer
// cancels them now, kept already or not yet, rather than when their time
// is up.
func release(ctx context.Context, tx pgx.Tx, taskID string) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET keep = false, kept_until = now()
		WHERE task_id = $1 AND status IN ('aborted', 'failed') AND keep`, taskID)
	return err
}

// startOver starts the task's next attempt: a delivery of its own, on a
// branch of its own, from the default branch.
func (s *Server) startOver(ctx context.Context, tx pgx.Tx, org, taskID, projectID string, policy delivery.Policy, attempt int) (string, error) {
	// The last attempt's delivery is over, whatever state it stopped in.
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET status = 'completed', wake_at = NULL,
			awaiting_signals = '[]'::jsonb, locked_by = NULL, locked_until = NULL
		WHERE task_id = $1 AND status <> 'completed'`, taskID); err != nil {
		return "", err
	}
	return s.Workflow.StartTx(ctx, tx, workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: org, TaskID: taskID,
		IdempotencyKey: fmt.Sprintf("delivery:%s:attempt-%d", taskID, attempt),
		Input: delivery.State{TaskID: taskID, ProjectID: projectID, Policy: policy, Attempt: attempt,
			Branch: delivery.BranchFor(taskID, attempt)},
	})
}

// closePullRequests closes an attempt's pull requests still open on GitHub.
// Best effort: the pull request sync records the close either way.
func (s *Server) closePullRequests(ctx context.Context, org string, ids []string) {
	if len(ids) == 0 || s.Forges == nil {
		return
	}
	var prs []struct {
		Number int
		URL    string
	}
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT p.number, r.url FROM pull_requests p JOIN repositories r ON r.id = p.repository_id
			WHERE p.id = ANY($1) AND p.state IN ('open', 'draft')`, ids)
		if err != nil {
			return err
		}
		prs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			Number int
			URL    string
		}])
		return err
	}); err != nil || len(prs) == 0 {
		return
	}
	gh, err := s.Forges.For(ctx, org)
	if err != nil || gh == nil {
		return
	}
	for _, p := range prs {
		if slug := forge.SlugFromURL(p.URL); slug != "" {
			if err := gh.ClosePullRequest(ctx, slug, p.Number); err != nil && s.Log != nil {
				s.Log.Warn("closing an earlier attempt's pull request failed", "pr", p.Number, "repo", slug, "error", err)
			}
		}
	}
}
