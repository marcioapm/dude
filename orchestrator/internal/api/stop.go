package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Stop: a person stops the whole task where it stands — every live Run of
// the task whatever its attempt, the conductor and its children included —
// so it can be picked back up (recover.go), or stops it and starts it over
// at once. The task's owner or an admin, as Close.

func (s *Server) stopRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/tasks/{id}/stop", s.auth(s.stopTask))
}

type stopBody struct {
	AndRestart bool   `json:"andRestart"`
	Decider    string `json:"decider"`
	Note       string `json:"note"`
}

// inProgress says a task is between its start and its end, with a delivery
// going on: what Stop stops. With queued, a queued task with a live
// delivery (parked on a blocker added after its start) is too: a
// supersede stops it, a person's Stop does not.
func inProgress(status string, d *delivery.Delivery, queued bool) bool {
	if d == nil || !d.Live() || delivery.Ended(status) {
		return false
	}
	return !delivery.NotStarted(status) || queued && status == "queued"
}

func (s *Server) stopTask(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body stopBody
	if err := read(r, &body); err != nil {
		return err
	}
	switch body.Decider {
	case "", delivery.DeciderConductor, delivery.DeciderPolicy:
	default:
		return fail(http.StatusBadRequest, "bad_request", "decider is conductor or policy")
	}
	if body.Decider != "" && !body.AndRestart {
		return fail(http.StatusBadRequest, "bad_request", "decider is who decides the next attempt: only with andRestart")
	}
	note := strings.TrimSpace(body.Note)
	p := principalOf(r)
	out := map[string]any{"taskId": taskID, "status": "aborted"}
	var closePRs []string
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM tasks WHERE id = $1)`, taskID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fail(http.StatusNotFound, "not_found", "task %s not found", taskID)
		}
		if p.Person == "" {
			return fail(http.StatusForbidden, "not_owner", "only a person stops a task")
		}
		if err := mayDecide(r.Context(), tx, taskID, p); err != nil {
			return err
		}
		stopped, decider, err := stopTaskTx(r.Context(), tx, org, taskID, note, "a person stopped the task", "stop", p, false)
		if err != nil {
			return err
		}
		ri := runInfo{ProjectID: stopped.ProjectID, TaskID: taskID}
		next := ""
		if body.AndRestart {
			next = body.Decider
			if next == "" {
				next = decider
			}
		}
		payload := map[string]any{"by": p.Person, "note": note, "runIds": db.NonNil(stopped.All()), "decider": decider}
		if next != "" {
			payload["andRestart"], payload["nextDecider"] = true, next
		}
		if err := humanEvent(r.Context(), tx, org, "", ri, "task.stopped", p, payload); err != nil {
			return err
		}
		if !body.AndRestart {
			return nil
		}
		t, err := loadStopped(r.Context(), tx, taskID, true)
		if err != nil {
			return err
		}
		res, err := s.restartTx(r.Context(), tx, org, taskID, t, "", delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person},
			note, next, nil, "a person stopped it and started it over")
		if err != nil {
			return err
		}
		closePRs = res.ClosePRs
		out["status"], out["attempt"], out["workflowRunId"], out["decider"] = "running", res.Attempt, res.WorkflowID, res.Decider
		return humanEvent(r.Context(), tx, org, "", ri, "task.recovered", p, map[string]any{"action": "restart", "note": note,
			"attempt": res.Attempt, "runIds": []string{}, "decider": next, "via": "stop"})
	})
	var taken delivery.SourceRefusal
	if errors.As(err, &taken) {
		return sourceFail(taken)
	}
	if err != nil {
		if body.AndRestart {
			return s.refusedStart(r.Context(), org, taskID, "start_over", p, err)
		}
		return err
	}
	s.closePullRequests(r.Context(), org, closePRs)
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}

// stopTaskTx is a person's Stop of a task in progress: every live Run
// stopped and kept to resume, the delivery halted where it stands,
// the task aborted for why, each Run's end recorded as by. Its Stop, and a
// supersede of a task by another on the same branch (queued: a queued
// task with a live delivery is stopped too). Returns what it stopped and
// the delivery's decider.
func stopTaskTx(ctx context.Context, tx pgx.Tx, org, taskID, note, why, by string, p principal, queued bool) (delivery.Stopped, string, error) {
	stopped, err := delivery.StopAllTx(ctx, tx, org, taskID, note, true, func(locked delivery.Stopped) error {
		if !inProgress(locked.TaskStatus, locked.Delivery, queued) {
			return fail(http.StatusConflict, "not_in_progress", "task %s is %s: only a task in progress can be stopped", taskID, locked.TaskStatus)
		}
		return delivery.StopEscalationTx(ctx, tx, org, locked.Delivery, p.ActorType, p.Actor)
	})
	if err != nil {
		return stopped, "", err
	}
	d := stopped.Delivery
	decider := wireDecider(&d.State)
	if _, err := delivery.SetTaskStatusTx(ctx, tx, org, stopped.ProjectID, taskID, "", "aborted", why); err != nil {
		return stopped, "", err
	}
	if err := delivery.HaltDeliveryTx(ctx, tx, org, d, stopped.Conductor, why, p.ActorType, p.Actor); err != nil {
		return stopped, "", err
	}
	ri := runInfo{ProjectID: stopped.ProjectID, TaskID: taskID}
	for _, id := range stopped.All() {
		if err := humanEvent(ctx, tx, org, id, ri, "run.aborted", p, map[string]any{"reason": db.Nullable(note), "by": by}); err != nil {
			return stopped, "", err
		}
	}
	return stopped, decider, nil
}
