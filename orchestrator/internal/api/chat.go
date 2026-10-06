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
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// chat is a person writing in a task's Chat. With no conductor, the
// message creates the task's conductor, briefed by dude; with one, it is
// the conductor's next input, delivered as a steer is (a directive), which
// resumes it when it is parked. A question it is waiting on is answered by
// it, as through the question's own route.
//
// The first message on a task whose delivery is in progress hands its
// decisions to the conductor (taking over): the step running finishes, and
// the next decision is the conductor's. On a task not started it is Talk
// it through: a delivery starts, decided by the conductor, waiting on its
// first decision. A merged or closed task's conductor stays read-only.
func (s *Server) chat(w http.ResponseWriter, r *http.Request, org string) error {
	var body struct {
		Text string `json:"text"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Text) == "" {
		return fail(http.StatusBadRequest, "bad_request", "text is required")
	}
	if len(body.Text) > delivery.ChatMessageMax {
		return fail(http.StatusBadRequest, "bad_request", "a message is at most %d bytes", delivery.ChatMessageMax)
	}
	return s.converse(w, r, org, body.Text, false)
}

// TalkItThrough is what a person who pressed Talk it through says to the
// conductor first: the task is the subject, and nothing is built yet.
const TalkItThrough = "Let's talk this task through before anything is built. Read it and the code, then ask me " +
	"what you need to know, or propose a plan."

// talk is Talk it through: a task not started gets its delivery, decided
// by the conductor and waiting on its first decision, and its conductor,
// asked to plan it with the person.
func (s *Server) talk(w http.ResponseWriter, r *http.Request, org string) error {
	return s.converse(w, r, org, TalkItThrough, true)
}

// converse takes a person's message in a task's Chat (see chat); with
// talk, only on a task not started.
func (s *Server) converse(w http.ResponseWriter, r *http.Request, org, text string, talk bool) error {
	taskID := r.PathValue("id")
	p := principalOf(r)
	writer := delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person}
	if writer.ActorType == "" {
		writer.ActorType = ledger.ActorHuman
	}
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
		// The message may answer the conductor's question about the
		// escalation: the task's row before the delivery and the question,
		// as every escalation path takes them (delivery.LockEscalationTx).
		if err := delivery.LockTaskTx(r.Context(), tx, taskID, nil); err != nil {
			return err
		}
		decider, err := s.decideFor(r.Context(), tx, org, taskID, writer, talk)
		if err != nil {
			return err
		}
		var runID string
		var ending bool
		find := func() error {
			return tx.QueryRow(r.Context(), `SELECT r.id, `+delivery.Ending+` FROM runs r
				WHERE r.task_id = $1 AND `+delivery.LiveConductor+` FOR NO KEY UPDATE`, taskID).Scan(&runID, &ending)
		}
		err = find()
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
			runID, err = delivery.StartConductor(r.Context(), tx, org, projectID, taskID, writer, text)
			if err != nil {
				return err
			}
			out = map[string]any{"runId": runID, "taskId": taskID, "created": true, "decider": decider}
			return nil
		}
		if err != nil {
			return err
		}
		ref := delivery.RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: runID}

		// Waiting on its question: this is the answer.
		var questionID, prompt string
		qerr := tx.QueryRow(r.Context(), `SELECT q.id, q.prompt
			FROM questions q WHERE q.run_id = $1 AND q.status = 'open' ORDER BY q.asked_at DESC LIMIT 1 FOR UPDATE`, runID).
			Scan(&questionID, &prompt)
		if qerr != nil && !db.IsNotFound(qerr) {
			return qerr
		}
		if qerr == nil {
			ri := runInfo{ProjectID: projectID, TaskID: taskID, Role: delivery.RoleConductor}
			if err := ownerOnly(r.Context(), tx, taskID, p.Person, "answer"); err != nil {
				return err
			}
			directiveID, err := answerQuestion(r.Context(), tx, ref, delivery.RoleConductor, questionID, prompt, text, p)
			if err != nil {
				return err
			}
			out = map[string]any{"runId": runID, "taskId": taskID, "created": false, "questionId": questionID,
				"directiveId": directiveID, "decider": decider}
			return humanEvent(r.Context(), tx, org, runID, ri, "question.answered", p,
				map[string]any{"questionId": questionID, "answer": text, "directiveId": directiveID})
		}

		directiveID, _, err := delivery.QueueDirective(r.Context(), tx, ref, delivery.Directive{Text: text, Scope: "run"})
		if err != nil {
			return err
		}
		// Parked by dude, it resumes for the directive on its own
		// (resumable); paused by a person, or idle, a message is asking for
		// it back.
		if err := delivery.RequestResumeForMessage(r.Context(), tx, runID, "a message in Chat"); err != nil {
			return err
		}
		out = map[string]any{"runId": runID, "taskId": taskID, "created": false, "directiveId": directiveID, "decider": decider}
		return delivery.ChatEvent(r.Context(), tx, ref, writer, map[string]any{"text": text, "directiveId": directiveID})
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

// decideFor settles who decides the task's delivery as a person writes in
// its Chat, and says who does: a delivery in progress becomes the
// conductor's (unless its decisions were handed back to Deliver); a task
// not started gets a delivery the conductor decides, parked on its first
// decision; anything else is unchanged. talk is Talk it through, refused
// on a task already started.
func (s *Server) decideFor(ctx context.Context, tx pgx.Tx, org, taskID string, wr delivery.Writer, talk bool) (string, error) {
	d, err := delivery.LoadDelivery(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	if d == nil {
		var status string
		var started bool
		if err := tx.QueryRow(ctx, `SELECT t.status::text, EXISTS (SELECT 1 FROM runs r WHERE r.task_id = t.id AND r.phase IS NOT NULL)
			FROM tasks t WHERE t.id = $1`, taskID).Scan(&status, &started); err != nil {
			return "", err
		}
		if !notStarted(status) || started {
			if talk {
				return "", fail(http.StatusConflict, "conflict", "task %s is %s: it can no longer be talked through", taskID, status)
			}
			return delivery.DeciderPolicy, nil
		}
		return delivery.DeciderConductor, s.talkThrough(ctx, tx, org, taskID, wr)
	}
	if talk {
		return "", fail(http.StatusConflict, "conflict", "task %s has started: write in its Chat instead", taskID)
	}
	decider := d.State.Decider
	if decider == "" {
		decider = delivery.DeciderPolicy
	}
	if !d.Live() || delivery.Ended(d.TaskStatus) || d.State.HandedBack {
		return decider, nil
	}
	if _, err := delivery.SetDeciderTx(ctx, tx, org, d.WorkflowID, &d.State, delivery.DeciderConductor,
		"a person wrote in Chat", wr.ActorType, wr.ActorID); err != nil {
		return "", err
	}
	return delivery.DeciderConductor, nil
}

// notStarted is a task status from before any delivery: where Deliver and
// Talk it through are offered. A task with no delivery in any other status
// (marked done, or moved by hand) is not talked through.
func notStarted(status string) bool {
	switch status {
	case "received", "intake", "awaiting_confirmation", "queued":
		return true
	}
	return false
}

// talkThrough starts a task's delivery for its conductor to decide: as
// Deliver starts one, with decider conductor, so its first step parks on
// the conductor's decision to start the implementer.
func (s *Server) talkThrough(ctx context.Context, tx pgx.Tx, org, taskID string, wr delivery.Writer) error {
	projectID, policy, err := policyFor(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if err := delivery.NameOnlyRepository(ctx, tx, taskID); err != nil {
		return err
	}
	var attempt int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt), 1) FROM runs WHERE task_id = $1`, taskID).Scan(&attempt); err != nil {
		return err
	}
	st := delivery.State{TaskID: taskID, ProjectID: projectID, Policy: policy, Branch: delivery.BranchFor(taskID, attempt),
		Decider: delivery.DeciderConductor}
	if _, err := s.Workflow.StartTx(ctx, tx, workflow.StartOptions{Type: delivery.WorkflowType, OrganizationID: org,
		IdempotencyKey: deliveryKey(taskID), TaskID: taskID, Input: st}); err != nil {
		return err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: delivery.EvDeciderChanged, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, ActorType: wr.ActorType, ActorID: wr.ActorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID,
		Payload: map[string]any{"from": nil, "to": delivery.DeciderConductor, "why": "talk it through"}})
	return err
}

// handBack is "Let Deliver finish it" (decider policy), or taking the
// decisions back for the conductor (decider conductor): from the next
// decision on. An untaken pending decision goes to the policy at once;
// a taken decision is still carried out, and a required PR gate still holds.
func (s *Server) handBack(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body struct {
		Decider string `json:"decider"`
		// The person confirmed that Deliver opens the pull request now,
		// though the conductor's gate question is unanswered or not Open.
		OpenPullRequest bool `json:"openPullRequest"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if body.Decider == "" {
		body.Decider = delivery.DeciderPolicy
	}
	if body.Decider != delivery.DeciderPolicy && body.Decider != delivery.DeciderConductor {
		return fail(http.StatusBadRequest, "bad_request", "decider is policy or conductor")
	}
	p := principalOf(r)
	var wfID string
	var wake bool
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		d, err := delivery.LoadDelivery(r.Context(), tx, taskID)
		if err != nil {
			return err
		}
		if d == nil || !d.Live() || delivery.Ended(d.TaskStatus) {
			return fail(http.StatusConflict, "conflict", "task %s has no delivery in progress", taskID)
		}
		if err := ownerOnly(r.Context(), tx, taskID, p.Person, "decide"); err != nil {
			return err
		}
		handedBack := body.Decider == delivery.DeciderPolicy
		if handedBack && d.AtGate() {
			// Handed back at the gate, Deliver opens the pull request at once:
			// only on the person's Open or Draft, or their confirmation,
			// written as the opening's authorization at the heads now (the
			// step keeps it).
			draft, err := delivery.GateAnswer(r.Context(), tx, &d.State)
			var refused delivery.Refusal
			isRefusal := errors.As(err, &refused)
			switch {
			case isRefusal && !body.OpenPullRequest:
				return fail(http.StatusConflict, "pull_request_gate",
					"Deliver will open the pull request now, and the person has not answered Open or Draft; confirm with openPullRequest")
			case err != nil && !isRefusal:
				return err
			}
			draft = err == nil && draft
			if _, err := tx.Exec(r.Context(), `UPDATE workflow_runs SET state = state || jsonb_build_object('gateOpened', true)
					|| CASE WHEN $2 THEN '{"draft": true}'::jsonb ELSE '{}'::jsonb END WHERE id = $1`,
				d.WorkflowID, draft); err != nil {
				return err
			}
			if err := delivery.AuthorizeGateTx(r.Context(), tx, d.WorkflowID, &d.State, draft); err != nil {
				return err
			}
		}
		why := "a person let Deliver finish it"
		if body.Decider == delivery.DeciderConductor {
			why = "a person gave the decisions to the conductor"
		}
		if _, err := delivery.SetDeciderTx(r.Context(), tx, org, d.WorkflowID, &d.State, body.Decider, why,
			p.ActorType, p.Actor); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE workflow_runs SET state = jsonb_set(state, '{handedBack}', to_jsonb($2::boolean))
			WHERE id = $1`, d.WorkflowID, handedBack); err != nil {
			return err
		}
		wfID = d.WorkflowID
		// Handed back, a decision the delivery waits on, or is about to,
		// is the policy's: the signal waits for it if it is not parked yet.
		wake = handedBack
		return nil
	})
	if err != nil {
		return err
	}
	if wake {
		if err := s.Workflow.Signal(r.Context(), org, wfID, delivery.SignalConductorDecision, map[string]any{"action": "policy"}, ""); err != nil {
			return err
		}
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"taskId": taskID, "decider": body.Decider})
	return nil
}
