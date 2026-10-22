package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
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
	WorkflowID, Step *string
	State            delivery.State
	// Its attempt: the highest of its Runs'.
	Attempt int
	// The Runs a resume takes up again: the last attempt's agent Runs that
	// stopped together (aborted, or the one whose failure stopped
	// delivery), kept in lux, and when the first of them stops being kept.
	Kept      []string
	KeptUntil *time.Time
	// The Runs a resume queues again: they stopped before lux had them.
	Unstarted []string
}

// stoppedRun is one of the Runs a task stopped on.
type stoppedRun struct {
	ID    string
	Until *time.Time
	// Still kept to resume (delivery.KeptRun).
	Kept bool
	// Stopped before lux had it: nothing to resume, and nothing lost.
	Unstarted bool
}

func loadStopped(ctx context.Context, tx pgx.Tx, taskID string, lock bool) (stoppedTask, error) {
	var t stoppedTask
	forUpdate := ""
	if lock {
		// The delivery's row before the task's (delivery.LockEscalationTx).
		if _, err := delivery.LoadDelivery(ctx, tx, taskID); err != nil {
			return t, err
		}
		forUpdate = "FOR UPDATE OF t"
	}
	var state []byte
	err := tx.QueryRow(ctx, `SELECT t.project_id, t.status::text, d.id, d.step, d.state,
			COALESCE((SELECT max(attempt) FROM runs WHERE task_id = t.id AND kind = 'agent'), 1)
		FROM tasks t LEFT JOIN LATERAL (SELECT * FROM workflow_runs d WHERE d.task_id = t.id
		  ORDER BY d.created_at DESC LIMIT 1) d ON true
		WHERE t.id = $1 `+forUpdate, taskID).
		Scan(&t.ProjectID, &t.Status, &t.WorkflowID, &t.Step, &state, &t.Attempt)
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
	// failed Run the escalation named; and the conductor a person's Stop
	// stopped with them, which a resume goes on with too.
	stoppedRuns := t.State.PendingRunIDs
	if e := t.State.Stopped; e != nil && e.RunID() != "" {
		stoppedRuns = e.RunIDs()
	}
	if h := t.State.Halt; h != nil && h.Conductor != "" {
		// No conductor starts while the task is halted (delivery.HaltedTx:
		// Chat, Talk, Try again, a mention, a wake and the hand-over all
		// check it), so the one the Stop kept is still the task's.
		stoppedRuns = append(slices.Clone(stoppedRuns), h.Conductor)
	}
	if len(stoppedRuns) == 0 {
		return t, nil
	}
	// Each one that ended stopped (aborted, failed) must be kept to resume —
	// a step resumed with one gone would wait on it for ever — or never have
	// reached lux (a reviewer still waiting for room), which loses nothing
	// and is simply queued again. One that finished meanwhile (a reviewer
	// done just before the abort) is not resumed: its phase.finished is
	// waiting for the step that resumes.
	rows, err := tx.Query(ctx, `SELECT id, kept_until, `+delivery.KeptRun+`, lux_run_id IS NULL FROM runs
		WHERE id = ANY($1) AND status IN ('aborted', 'failed') ORDER BY created_at`, stoppedRuns)
	if err != nil {
		return t, err
	}
	ended, err := pgx.CollectRows(rows, pgx.RowToStructByPos[stoppedRun])
	if err != nil {
		return t, err
	}
	if !slices.ContainsFunc(ended, func(r stoppedRun) bool { return r.Kept }) ||
		slices.ContainsFunc(ended, func(r stoppedRun) bool { return !r.Kept && !r.Unstarted }) {
		return t, nil
	}
	for _, r := range ended {
		if r.Unstarted {
			t.Unstarted = append(t.Unstarted, r.ID)
			continue
		}
		t.Kept = append(t.Kept, r.ID)
		if r.Until != nil && (t.KeptUntil == nil || r.Until.Before(*t.KeptUntil)) {
			t.KeptUntil = r.Until
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
// an escalation stopped at, the one that made the Runs an abort stopped,
// or the one a person's Stop found the delivery waiting at.
func (t stoppedTask) retryStep() string {
	if e := t.State.Stopped; e != nil {
		return e.Step
	}
	if t.Step != nil && len(t.State.PendingRunIDs) > 0 {
		return delivery.AbortedRetryStep[*t.Step]
	}
	if h := t.State.Halt; h != nil {
		return h.At
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
	if h := t.State.Halt; h != nil {
		return h.At
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
	closedPR := t.State.Stopped == nil && t.State.Halt == nil && len(t.State.PendingRunIDs) == 0
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
	var refused *string
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) (err error) {
		if t, err = loadStopped(r.Context(), tx, taskID, false); err != nil {
			return err
		}
		return tx.QueryRow(r.Context(), `SELECT task_start_refusal($1)`, taskID).Scan(&refused)
	}); err != nil {
		return err
	}
	actions := t.actions()
	// A closed or archived task is picked back up by none of them: the
	// page offers Reopen or Unarchive instead (refused says which).
	if refused != nil && *refused != delivery.RefusedBlocked {
		actions = nil
	} else {
		refused = nil
	}
	write(w, http.StatusOK, map[string]any{"taskId": taskID, "actions": db.NonNil(actions),
		"attempt": t.Attempt, "keptUntil": t.KeptUntil, "refused": refused})
	return nil
}

// recoverTask picks a stopped task back up. Start over may name who decides
// the new attempt (decider); by default whoever decided the stopped one.
func (s *Server) recoverTask(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body struct {
		Action  string `json:"action"`
		Note    string `json:"note"`
		Decider string `json:"decider"`
		// Start over past open blockers (Start anyway): a person only.
		Override bool `json:"override"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	switch body.Decider {
	case "", delivery.DeciderConductor, delivery.DeciderPolicy:
	default:
		return fail(http.StatusBadRequest, "bad_request", "decider is conductor or policy")
	}
	if body.Decider != "" && body.Action != "restart" {
		return fail(http.StatusBadRequest, "bad_request", "decider is who decides the next attempt: only with restart")
	}
	note := strings.TrimSpace(body.Note)
	p := principalOf(r)
	var override *delivery.Override
	if body.Action == "restart" {
		o, err := overrideFor(r, startOverride{Override: body.Override, Note: note}, "deliver")
		if err != nil {
			return err
		}
		override = o
	}
	var wfID, decider string
	var closePRs []string
	attempt := 0
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		live, err := delivery.LockLiveWorkTx(r.Context(), tx, taskID)
		if err != nil {
			return err
		}
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
		if err := ownerOnly(r.Context(), tx, taskID, p.Person, "pick up"); err != nil {
			return err
		}
		writer := delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person}
		decider = t.decider()
		// Starting over is a fresh attempt from the default branch: a start,
		// guarded as one (path 6). Resume and Try again carry on the same
		// attempt: blockers don't hold them, a person's Close or Archive
		// does (path 7).
		if body.Action == "restart" {
			res, err := s.restartTx(r.Context(), tx, org, taskID, t, live, writer, note, body.Decider, override,
				"a person picked it back up: restart")
			if err != nil {
				return err
			}
			wfID, attempt, closePRs, decider = res.WorkflowID, res.Attempt, res.ClosePRs, res.Decider
		} else {
			if err := delivery.RefuseLifecycleTx(r.Context(), tx, taskID); err != nil {
				return pickUpRefusal(err)
			}
			if src := t.State.Source; src != nil {
				// The adopted branch was released when the task stopped.
				if err := delivery.HoldBranchTx(r.Context(), tx, taskID, src); err != nil {
					return err
				}
			}
			// Out of aborted, the one way: the status change says who and why.
			if err := reopenTask(r.Context(), tx, org, t.ProjectID, taskID, "a person picked it back up: "+body.Action); err != nil {
				return err
			}
			if err := recordPickUpNote(r.Context(), tx, org, taskID, note); err != nil {
				return err
			}
			if body.Action != "resume" {
				if decider == delivery.DeciderConductor {
					if err := endLiveConductorTx(r.Context(), tx, org, t.ProjectID, taskID, live, "a person tried again"); err != nil {
						return err
					}
				}
				if err := delivery.ReleaseKeptTx(r.Context(), tx, taskID); err != nil {
					return err
				}
			}
			wfID = *t.WorkflowID
			if err := pickUp(r.Context(), tx, org, t, body.Action, note); err != nil {
				return err
			}
			// Try again under the conductor: a new conductor on this
			// attempt, told the note, with the stopped one's conversation.
			if body.Action == "retry" && decider == delivery.DeciderConductor {
				if _, err := delivery.StartPickedUpConductor(r.Context(), tx, delivery.PickUp{Writer: writer, Note: note,
					Org: org, Project: t.ProjectID, TaskID: taskID}); err != nil {
					return err
				}
			}
		}
		return humanEvent(r.Context(), tx, org, "", runInfo{ProjectID: t.ProjectID, TaskID: taskID}, "task.recovered",
			p, map[string]any{"action": body.Action, "note": note, "attempt": max(attempt, t.Attempt),
				"runIds": db.NonNil(t.Kept), "decider": decider})
	})
	var taken delivery.SourceRefusal
	if errors.As(err, &taken) {
		return fail(http.StatusConflict, taken.Code, "%s", taken.Msg)
	}
	if err != nil {
		path := "start_over"
		if body.Action != "restart" {
			path = body.Action
		}
		return s.refusedStart(r.Context(), org, taskID, path, p, err)
	}
	// The last attempt's pull requests are closed after: best effort, as a
	// person can close one on GitHub, and the new attempt does not wait on it.
	s.closePullRequests(r.Context(), org, closePRs)
	s.kick()
	write(w, http.StatusOK, map[string]any{"taskId": taskID, "action": body.Action, "workflowRunId": wfID, "decider": decider})
	return nil
}

// pickUpRefusal words a closed task's refusal for Resume and Try again.
func pickUpRefusal(err error) error {
	var closed delivery.ClosedError
	if errors.As(err, &closed) {
		closed.PickUp = true
		return closed
	}
	return err
}

// recordPickUpNote keeps a person's note on picking the task back up, for
// every agent from then on (delivery.Decisions).
func recordPickUpNote(ctx context.Context, tx pgx.Tx, org, taskID, note string) error {
	if note == "" {
		return nil
	}
	return delivery.RecordDecisionTx(ctx, tx, org, taskID, "The work stopped. How should it go on?", note)
}

// restarted is what a Start over started.
type restarted struct {
	WorkflowID string
	Attempt    int
	// Who decides it, and its conductor ("" under Deliver).
	Decider, Conductor string
	// The stopped attempt's pull requests, closed after the commit.
	ClosePRs []string
}

// restartTx starts a stopped task over as its next attempt: a start,
// guarded as one (path 6), out of aborted with reason, what was kept for a
// resume let go, a conductor still live ended, and a new delivery decided
// by decider ("" for the stopped attempt's). Under the conductor, the
// attempt gets a new one, briefed with the note and what the last did.
// The caller holds LockLiveWorkTx's locks, then the delivery's
// (loadStopped); live is the conductor it found.
func (s *Server) restartTx(ctx context.Context, tx pgx.Tx, org, taskID string, t stoppedTask, live string, w delivery.Writer,
	note, decider string, override *delivery.Override, reason string) (restarted, error) {
	var out restarted
	if err := delivery.GuardStartTx(ctx, tx, org, taskID, override); err != nil {
		return out, err
	}
	if decider == "" {
		decider = t.decider()
	}
	out.Decider = decider
	if decider == delivery.DeciderConductor {
		if err := endLiveConductorTx(ctx, tx, org, t.ProjectID, taskID, live, reason); err != nil {
			return out, err
		}
	}
	if err := reopenTask(ctx, tx, org, t.ProjectID, taskID, reason); err != nil {
		return out, err
	}
	if err := recordPickUpNote(ctx, tx, org, taskID, note); err != nil {
		return out, err
	}
	if err := delivery.ReleaseKeptTx(ctx, tx, taskID); err != nil {
		return out, err
	}
	var err error
	if out.Attempt, err = delivery.NextAttemptTx(ctx, tx, taskID, t.State.Attempt); err != nil {
		return out, err
	}
	if out.WorkflowID, err = s.startOver(ctx, tx, org, taskID, t.ProjectID, t.State.Policy, out.Attempt, decider, w, override != nil); err != nil {
		return out, err
	}
	out.ClosePRs = t.State.PullRequestIDs
	if decider != delivery.DeciderConductor {
		return out, nil
	}
	out.Conductor, err = delivery.StartPickedUpConductor(ctx, tx, delivery.PickUp{Writer: w, Note: note, Attempt: out.Attempt,
		Earlier: t.Attempt, ClosedPRs: out.ClosePRs, Org: org, Project: t.ProjectID, TaskID: taskID})
	return out, err
}

// endLiveConductorTx ends the task's conductor still live as its attempt
// is picked back up afresh (Try again, Start over): aborted and not kept,
// its children let go. Each attempt has its own.
func endLiveConductorTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID, live, reason string) error {
	if live == "" {
		return nil
	}
	if _, err := delivery.StopChildrenTx(ctx, tx, taskID); err != nil {
		return err
	}
	return delivery.AbortConductorTx(ctx, tx, delivery.RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: live}, reason, false)
}

// decider is who decided the stopped attempt.
func (t stoppedTask) decider() string {
	if t.State.Decider == delivery.DeciderConductor {
		return delivery.DeciderConductor
	}
	return delivery.DeciderPolicy
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
		// The step waits on its phase Runs again; the conductor a Stop
		// kept is no step's to wait on.
		st.PendingRunIDs = slices.DeleteFunc(append(slices.Clone(t.Kept), t.Unstarted...), func(id string) bool {
			return st.Halt != nil && id == st.Halt.Conductor
		})
		if err := escalationFailure(delivery.ResumeKeptTx(ctx, tx, org, t.ProjectID, st.TaskID, t.Kept, note)); err != nil {
			return err
		}
		// Those lux never had start now, as they would have.
		// As if new: any image it waited on before is asked for afresh.
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'pending', control = 'none', keep = false, ended_at = NULL,
				control_reason = NULL, phase_notified_at = NULL, finishes = finishes + 1,
				image_build_id = NULL, image_waiting_since = NULL, next_attempt_at = NULL
			WHERE id = ANY($1) AND lux_run_id IS NULL AND status IN ('aborted', 'failed')`, t.Unstarted); err != nil {
			return err
		}
	} else {
		rc.Step = t.retryStep()
	}
	st.Recover = rc
	next, _ := json.Marshal(st)
	_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, status = 'running', step = 'recover',
			awaiting_signals = '[]'::jsonb, wake_at = NULL, last_error = NULL, attempt = 0,
			locked_by = NULL, locked_until = NULL
		WHERE id = $1`, *t.WorkflowID, next)
	return err
}

// startOver starts the task's next attempt: a delivery of its own, decided
// by decider, on a branch of its own from the default branch — or, for a
// task started from existing work, on that work's head branch again
// (delivery.NewState), whose adopted pull request it carries on.
func (s *Server) startOver(ctx context.Context, tx pgx.Tx, org, taskID, projectID string, policy delivery.Policy, attempt int,
	decider string, w delivery.Writer, overridden bool) (string, error) {
	// The last attempt's delivery is over, whatever state it stopped in.
	var was string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT state->>'decider' FROM workflow_runs WHERE task_id = $1
		ORDER BY created_at DESC LIMIT 1), '')`, taskID).Scan(&was); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET status = 'completed', wake_at = NULL,
			awaiting_signals = '[]'::jsonb, locked_by = NULL, locked_until = NULL
		WHERE task_id = $1 AND status <> 'completed'`, taskID); err != nil {
		return "", err
	}
	// The last attempt's reasons are its own: the next is briefed afresh.
	if err := delivery.EndWakesTx(ctx, tx, taskID); err != nil {
		return "", err
	}
	st, err := delivery.NewState(ctx, tx, taskID, projectID, policy, attempt)
	if err != nil {
		return "", err
	}
	st.Attempt, st.Overridden = attempt, overridden
	if decider == delivery.DeciderConductor {
		st.Decider = decider
	}
	id, err := s.Workflow.StartTx(ctx, tx, workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: org, TaskID: taskID,
		IdempotencyKey: fmt.Sprintf("delivery:%s:attempt-%d", taskID, attempt),
		Input:          st,
	})
	if err != nil || cmp.Or(was, delivery.DeciderPolicy) == decider {
		return id, err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: delivery.EvDeciderChanged, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, ActorType: w.ActorType, ActorID: w.ActorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID,
		Payload: map[string]any{"from": cmp.Or(was, delivery.DeciderPolicy), "to": decider, "why": "start over"}})
	return id, err
}

// closePullRequests closes an attempt's pull requests still open on GitHub.
// Best effort: the pull request sync records the close either way.
func (s *Server) closePullRequests(ctx context.Context, org string, ids []string) {
	s.closeOnForge(ctx, org, ids, "", false, nil)
}

// closedOnForge is what GitHub did with one pull request asked closed:
// err nil when it closed it.
type closedOnForge struct {
	ProjectID, TaskID, Repo string
	Number                  int
	Err                     error
}

// closeOnForge closes the pull requests still open on GitHub, each with
// comment first when one is given; adopted ones too only with adopted (a
// person asked). Best effort: done, when given, is told what came of each
// one it tried.
func (s *Server) closeOnForge(ctx context.Context, org string, ids []string, comment string, adopted bool, done func(closedOnForge)) {
	if len(ids) == 0 || s.Forges == nil {
		return
	}
	type openPR struct {
		Number            int
		URL               string
		Repo              string
		ProjectID, TaskID string
	}
	var prs []openPR
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT p.number, r.url, r.name, t.project_id, p.task_id FROM pull_requests p
			JOIN repositories r ON r.id = p.repository_id JOIN tasks t ON t.id = p.task_id
			WHERE p.id = ANY($1) AND p.state IN ('open', 'draft') AND (NOT p.adopted OR $2)`, ids, adopted)
		if err != nil {
			return err
		}
		prs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[openPR])
		return err
	}); err != nil || len(prs) == 0 {
		return
	}
	report := func(p openPR, err error) {
		if done != nil {
			done(closedOnForge{ProjectID: p.ProjectID, TaskID: p.TaskID, Repo: p.Repo, Number: p.Number, Err: err})
		}
	}
	gh, err := s.Forges.For(ctx, org)
	if err != nil || gh == nil {
		if err == nil {
			err = errors.New("no GitHub connection for the organization")
		}
		for _, p := range prs {
			report(p, err)
		}
		return
	}
	for _, p := range prs {
		slug := forge.SlugFromURL(p.URL)
		if slug == "" {
			report(p, fmt.Errorf("no GitHub repository in %q", p.URL))
			continue
		}
		if comment != "" {
			if _, err := gh.Comment(ctx, slug, p.Number, comment); err != nil && s.Log != nil {
				s.Log.Warn("commenting on a pull request before closing it failed", "pr", p.Number, "repo", slug, "error", err)
			}
		}
		err := gh.ClosePullRequest(ctx, slug, p.Number)
		if err != nil && s.Log != nil {
			s.Log.Warn("closing a pull request failed", "pr", p.Number, "repo", slug, "error", err)
		}
		report(p, err)
	}
}
