package api

import (
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
// going on: what Stop stops.
func inProgress(status string, d *delivery.Delivery) bool {
	return d != nil && d.Live() && !delivery.NotStarted(status) && !delivery.Ended(status)
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
		stopped, err := delivery.StopAllTx(r.Context(), tx, org, taskID, note, true)
		if err != nil {
			return err
		}
		d := stopped.Delivery
		if !inProgress(stopped.TaskStatus, d) {
			return fail(http.StatusConflict, "not_in_progress", "task %s is %s: only a task in progress can be stopped", taskID, stopped.TaskStatus)
		}
		decider := d.State.Decider
		if decider == "" {
			decider = delivery.DeciderPolicy
		}
		if _, err := delivery.SetTaskStatusTx(r.Context(), tx, org, stopped.ProjectID, taskID, "", "aborted", "a person stopped the task"); err != nil {
			return err
		}
		if err := delivery.HaltDeliveryTx(r.Context(), tx, org, d, stopped.Conductor, "a person stopped the task", p.ActorType, p.Actor); err != nil {
			return err
		}
		ri := runInfo{ProjectID: stopped.ProjectID, TaskID: taskID}
		for _, id := range stopped.All() {
			if err := humanEvent(r.Context(), tx, org, id, ri, "run.aborted", p, map[string]any{"reason": db.Nullable(note), "by": "stop"}); err != nil {
				return err
			}
		}
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
		return fail(http.StatusConflict, taken.Code, "%s", taken.Msg)
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
