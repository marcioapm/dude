package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// chatMessageMax bounds one message, as a steer's text is bounded by the
// directive it becomes.
const chatMessageMax = 16_384

// chat is a person writing in a task's Chat. With no conductor, the
// message creates the task's conductor, briefed by dude; with one, it is
// the conductor's next input, delivered as a steer is (a directive), which
// resumes it when it is parked. A question it is waiting on is answered by
// it, as through the question's own route. Any task, whatever its status:
// the conductor reads and answers, and changes nothing.
func (s *Server) chat(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Text) == "" {
		return fail(http.StatusBadRequest, "bad_request", "text is required")
	}
	if len(body.Text) > chatMessageMax {
		return fail(http.StatusBadRequest, "bad_request", "a message is at most %d bytes", chatMessageMax)
	}
	p := principalOf(r)
	writer := delivery.Writer{ActorType: actorTypeOf(p), ActorID: p.Actor, Person: p.Person}
	var out map[string]any
	created := false
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		// One message per task at a time: two people writing at once are
		// taken one after the other, and the second finds the first's
		// conductor. The syncer ends a conductor under the same lock.
		if err := delivery.LockChat(r.Context(), tx, taskID); err != nil {
			return err
		}
		var projectID string
		if err := tx.QueryRow(r.Context(), `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "task %s not found", taskID)
			}
			return err
		}
		var runID, status string
		var dudePause *string
		var ending bool
		find := func() error {
			return tx.QueryRow(r.Context(), `SELECT r.id, r.status::text, r.dude_pause, `+delivery.Ending+` FROM runs r
				WHERE r.task_id = $1 AND `+delivery.LiveConductor+` FOR NO KEY UPDATE`, taskID).Scan(&runID, &status, &dudePause, &ending)
		}
		err := find()
		if err == nil && ending {
			// Its container stopped and nothing will resume it: it is ended
			// here, as the syncer would, and what it never read goes to the
			// next conductor — which this message then reaches too.
			ref := delivery.RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: runID}
			if err := delivery.EndConductor(r.Context(), tx, ref, "its container stopped"); err != nil {
				return err
			}
			err = find()
		}
		if db.IsNotFound(err) {
			created = true
			runID, err = delivery.StartConductor(r.Context(), tx, org, projectID, taskID, writer, body.Text)
			if err != nil {
				return err
			}
			out = map[string]any{"runId": runID, "taskId": taskID, "created": true}
			return nil
		}
		if err != nil {
			return err
		}
		ri := runInfo{ProjectID: projectID, TaskID: taskID, Status: status, DudePaused: dudePause != nil, Role: delivery.RoleConductor}
		ref := delivery.RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: runID}

		// Waiting on its question: this is the answer.
		var questionID, prompt, priorStatus string
		qerr := tx.QueryRow(r.Context(), `SELECT q.id, q.prompt,
				COALESCE((SELECT e.payload->>'taskStatus' FROM events e WHERE e.run_id = q.run_id AND e.event_type = 'question.asked'
					AND e.payload->>'questionId' = q.id LIMIT 1), '')
			FROM questions q WHERE q.run_id = $1 AND q.status = 'open' ORDER BY q.asked_at DESC LIMIT 1 FOR UPDATE`, runID).
			Scan(&questionID, &prompt, &priorStatus)
		if qerr != nil && !db.IsNotFound(qerr) {
			return qerr
		}
		if qerr == nil {
			if err := ownerOnly(r.Context(), tx, taskID, p.Person, "answer"); err != nil {
				return err
			}
			directiveID, err := answerQuestion(r.Context(), tx, ref, ri, questionID, prompt, priorStatus, body.Text, actor(r))
			if err != nil {
				return err
			}
			out = map[string]any{"runId": runID, "taskId": taskID, "created": false, "questionId": questionID, "directiveId": directiveID}
			return humanEvent(r.Context(), tx, org, runID, ri, "question.answered", p,
				map[string]any{"questionId": questionID, "answer": body.Text, "directiveId": directiveID})
		}

		directiveID, _, err := delivery.QueueDirective(r.Context(), tx, ref, delivery.Directive{Text: body.Text, Scope: "run"})
		if err != nil {
			return err
		}
		// Parked by dude, it resumes for the directive on its own
		// (resumable); paused by a person, or idle, a message is asking for
		// it back.
		if status == "paused" && (dudePause == nil || *dudePause == "idle") {
			if _, err := tx.Exec(r.Context(), `UPDATE runs SET control = 'resume', control_requested_at = now(),
				control_reason = 'a message in Chat' WHERE id = $1`, runID); err != nil {
				return err
			}
		}
		out = map[string]any{"runId": runID, "taskId": taskID, "created": false, "directiveId": directiveID}
		return delivery.ChatEvent(r.Context(), tx, ref, writer, map[string]any{"text": body.Text, "directiveId": directiveID})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "runs_live_conductor_idx" {
		// Another process made it between our read and insert: theirs.
		return fail(http.StatusConflict, "conflict", "task %s's conductor was just started; send again", taskID)
	}
	if err != nil {
		return err
	}
	s.kick()
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	write(w, status, out)
	return nil
}

func actorTypeOf(p principal) string {
	if p.ActorType == "" {
		return ledger.ActorHuman
	}
	return p.ActorType
}
