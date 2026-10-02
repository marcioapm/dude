package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A person's message in a task's Chat (EvChatMessage, on the conductor's Run).
const evChatMessage = "chat.message"

// liveConductor (SQL, over runs r): the task's conductor that can still
// hear a message — at most one (runs_live_conductor_idx).
const liveConductor = `r.role = 'conductor' AND r.kind = 'agent' AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`

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
	var out map[string]any
	created := false
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		// One message per task at a time: two people writing at once are
		// taken one after the other, and the second finds the first's
		// conductor. A lock of its own, not the task's row: the conductor's
		// own transactions (a question it asks) hold its Run and then
		// update the task, and this one takes its Run before the task too.
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('chat:' || $1))`, taskID); err != nil {
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
		err := tx.QueryRow(r.Context(), `SELECT r.id, r.status::text, r.dude_pause FROM runs r
			WHERE r.task_id = $1 AND `+liveConductor+` FOR NO KEY UPDATE`, taskID).Scan(&runID, &status, &dudePause)
		if db.IsNotFound(err) {
			created = true
			runID, err = s.startConductor(r.Context(), tx, org, projectID, taskID, p, body.Text)
			if err != nil {
				return err
			}
			out = map[string]any{"runId": runID, "taskId": taskID, "created": true}
			return chatEvent(r.Context(), tx, org, projectID, taskID, runID, p, map[string]any{"text": body.Text})
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
		return chatEvent(r.Context(), tx, org, projectID, taskID, runID, p, map[string]any{"text": body.Text, "directiveId": directiveID})
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

// startConductor creates the task's conductor: a Run with no phase, role
// conductor, from the task's head in each repository (the default branch
// where nothing was published), briefed by dude with the person's message.
func (s *Server) startConductor(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, p principal, message string) (string, error) {
	id := ids.New(ids.Run)
	var person string
	if p.Person != "" {
		_ = tx.QueryRow(ctx, `SELECT name FROM people WHERE id = $1`, p.Person).Scan(&person)
	}
	briefing, err := delivery.Briefing(ctx, tx, taskID, id, person, message)
	if err != nil {
		return "", err
	}
	heads, err := delivery.TaskHeads(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	baseRefs := map[string]string{}
	for _, h := range heads {
		if h.SHA != "" {
			baseRefs[h.Repo] = h.SHA
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role,
			base_refs, prompt, started_by)
		VALUES ($1, $2, $3, $4, COALESCE((SELECT max(attempt) FROM runs WHERE task_id = $4), 1), 'pending', 'agent',
			'conductor', $5, $6, NULLIF($7, ''))`,
		id, org, projectID, taskID, baseRefs, briefing, p.Person); err != nil {
		return "", err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: "run.created", OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, RunID: id, ActorType: actorTypeOf(p), ActorID: p.Actor, Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: map[string]any{"role": delivery.RoleConductor, "publishes": false, "baseRefs": baseRefs}})
	return id, err
}

func chatEvent(ctx context.Context, tx pgx.Tx, org, projectID, taskID, runID string, p principal, payload map[string]any) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: evChatMessage, OrganizationID: org, ProjectID: projectID, TaskID: taskID, RunID: runID,
		ActorType: actorTypeOf(p), ActorID: p.Actor, Source: ledger.SourceOrchestrator, CorrelationID: taskID, Payload: payload,
	})
	return err
}

func actorTypeOf(p principal) string {
	if p.ActorType == "" {
		return ledger.ActorHuman
	}
	return p.ActorType
}
