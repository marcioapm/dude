package delivery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Conductor-first for the two asks that are not a Run's question
// (design A10, A11): a reviewer's question (review_questions) and an
// agent's repository request (repository_requests). On a task whose
// delivery a live conductor decides, each goes to the conductor first
// (addressee 'conductor'): no person is asked, notified or waited on. The
// conductor settles it, or forwards it to the person through ToPersonTx;
// dude hands it over when the conductor does not in time, failed, or has
// no model (HandOverReviewQuestionTx, HandOverRepositoryRequestTx). The
// task's owner may always settle either directly.

// EvRepositoryRouted is an agent's repository request given to its
// conductor first: the ledger's record of the ask, which notifies nobody
// (repository.requested is the person's, through ToPersonTx).
const EvRepositoryRouted = "repository.routed"

// conductorHeldTx says whether a new ask on the task goes to its conductor
// first: the delivery is live, a conductor decides it, and a conductor is
// available to hear it (AskConductorTx's fallback predicates).
func conductorHeldTx(ctx context.Context, tx pgx.Tx, taskID string, d *Delivery) (bool, error) {
	if d == nil || !d.Live() || !d.State.conducted() {
		return false, nil
	}
	unavailable, err := questionConductorUnavailableTx(ctx, tx, taskID)
	return !unavailable, err
}

// ReviewQuestionAddresseeTx is who a reviewer's question raised now on the
// task is for: HolderConductor while conductorHeldTx holds, else
// HolderPerson (as every question was before conductor-first).
func ReviewQuestionAddresseeTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	held, err := conductorHeldTx(ctx, tx, taskID, d)
	if err != nil || !held {
		return HolderPerson, err
	}
	return HolderConductor, nil
}

// reviewQuestionAsk is a reviewer's question as ToPersonTx asks the person.
type reviewQuestionAsk struct {
	ID, RunID, Question string
	// How it reached the person: "conductor" forwarded it, "handover" dude
	// handed it over.
	Via string
}

// ForwardReviewQuestion is forward_review_question: the task's live
// conductor gives a reviewer's question it holds to the person. Locks
// Chat → delivery → task → question (LockAnswerTx's order).
func ForwardReviewQuestion(ctx context.Context, tx pgx.Tx, ref RunRef, id string) (SettledReviewQuestion, error) {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return SettledReviewQuestion{}, err
	}
	d, err := LockAnswerTx(ctx, tx, ref.TaskID)
	if err != nil {
		return SettledReviewQuestion{}, err
	}
	switch {
	case d != nil && Ended(d.TaskStatus):
		return SettledReviewQuestion{}, refusef(rThisTaskYouAreReadOnly, d.TaskStatus)
	case d == nil || !d.Live():
		return SettledReviewQuestion{}, refuseText(rThisTaskHasNoDeliveryIn)
	case !d.State.conducted():
		return SettledReviewQuestion{}, refuseText(rDeliverTakesThisTaskDecisionsNot)
	}
	out, moved, err := reviewQuestionToPersonTx(ctx, tx, ref.Org, ref.TaskID, d, id, WhyConductor)
	if err != nil || !moved {
		if err == nil {
			err = refusef(rReviewQuestionNotHeld, id)
		}
		return out, err
	}
	return out, nil
}

// reviewQuestionToPersonTx flips an open conductor-held question of d's
// task to the person and asks them (ToPersonTx). moved is false, and
// nothing written, when the question is not open and the conductor's: a
// second forward, a handover after an answer. The caller holds
// LockAnswerTx.
func reviewQuestionToPersonTx(ctx context.Context, tx pgx.Tx, org, taskID string, d *Delivery, id, why string) (SettledReviewQuestion, bool, error) {
	out := SettledReviewQuestion{ID: id, TaskID: taskID, Status: "open"}
	var ask reviewQuestionAsk
	err := tx.QueryRow(ctx, `UPDATE review_questions q SET addressee = 'person'
		FROM tasks t WHERE q.id = $1 AND q.task_id = $2 AND t.id = q.task_id
		  AND q.status = 'open' AND q.addressee = 'conductor'
		RETURNING t.project_id, COALESCE(q.run_id, ''), q.question`, id, taskID).
		Scan(&out.ProjectID, &ask.RunID, &ask.Question)
	if db.IsNotFound(err) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	ask.ID, ask.Via = id, why
	st := d.State
	st.TaskID, st.ProjectID = taskID, out.ProjectID
	if _, err := ToPersonTx(ctx, tx, ToPerson{Ref: RunRef{Org: org, ProjectID: out.ProjectID, TaskID: taskID}, State: &st,
		reviewQuestion: &ask, Reason: reasonQuestionsOpen, Why: why}); err != nil {
		return out, false, err
	}
	return out, true, nil
}

// heldDue (SQL, over an item aliased q with task_id and created_at, $1
// the wait in seconds): a conductor-held item dude hands to the person
// now. The task's newest delivery is live, and either the conductor did
// not settle it within the wait, its latest Run failed since the item was
// raised, it has no model (quietNoModel), or no conductor decides any
// more.
const heldDue = `EXISTS (SELECT 1 FROM workflow_runs hw WHERE hw.id = (SELECT w.id FROM workflow_runs w WHERE w.task_id = q.task_id` + latestDelivery + `)
		AND hw.status IN ('running', 'waiting'))
	AND (q.created_at <= now() - $1 * interval '1 second'
		OR COALESCE((SELECT w.state->>'decider' FROM workflow_runs w WHERE w.task_id = q.task_id` + latestDelivery + `), '') <> 'conductor'
		OR COALESCE((SELECT c.status = 'failed' AND c.ended_at >= q.created_at FROM runs c
			WHERE c.task_id = q.task_id AND c.role = 'conductor' ORDER BY c.created_at DESC, c.id DESC LIMIT 1), false)
		OR (SELECT ` + quietNoModel + ` FROM (SELECT q.task_id AS task_id) qn))`

// ReviewQuestionsDueSQL (SQL, $1 the wait in seconds) selects the open
// conductor-held reviewer questions dude hands over now: id, organization,
// task. Through review_questions_conductor_open_idx.
const ReviewQuestionsDueSQL = `SELECT q.id, q.organization_id, q.task_id FROM review_questions q
	WHERE q.status = 'open' AND q.addressee = 'conductor' AND ` + heldDue + ` ORDER BY q.created_at, q.id`

// RepositoryRequestsDueSQL is ReviewQuestionsDueSQL's for pending
// conductor-held repository requests, through
// repository_requests_conductor_pending_idx.
const RepositoryRequestsDueSQL = `SELECT q.id, q.organization_id, q.task_id FROM repository_requests q
	WHERE q.status = 'pending' AND q.addressee = 'conductor' AND ` + heldDue + ` ORDER BY q.created_at, q.id`

// HandOverReviewQuestionTx hands one conductor-held reviewer question to
// the person when it is still due (heldDue, read again under the locks):
// question.asked, a push, the held task waiting on them. Idempotent: an
// answered, withdrawn or forwarded question is left as it is.
func HandOverReviewQuestionTx(ctx context.Context, tx pgx.Tx, org, taskID, id string, wait time.Duration) (bool, error) {
	d, err := LockAnswerTx(ctx, tx, taskID)
	if err != nil || d == nil {
		return false, err
	}
	var due bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM review_questions q WHERE q.id = $2 AND q.task_id = $3
		AND q.status = 'open' AND q.addressee = 'conductor' AND `+heldDue+`)`, wait.Seconds(), id, taskID).Scan(&due); err != nil || !due {
		return false, err
	}
	_, moved, err := reviewQuestionToPersonTx(ctx, tx, org, taskID, d, id, WhyHandover)
	return moved, err
}

// heldReviewQuestionsTx says whether the task's open reviewer questions of
// the attempt are all its conductor's: a person holds none.
func heldReviewQuestionsTx(ctx context.Context, tx pgx.Tx, taskID string, open []OpenReviewQuestion) (bool, error) {
	ids := make([]string, len(open))
	for i, q := range open {
		ids[i] = q.ID
	}
	var person bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM review_questions WHERE task_id = $1 AND id = ANY($2) AND addressee = 'person')`,
		taskID, ids).Scan(&person)
	return !person, err
}

// RepositoryRequest is an agent's request, as its deciders read it.
type RepositoryRequest struct {
	ID, TaskID, RunID, RepoID, RepoName, Access, Status, Addressee string
	// The repository is of the task's project.
	SameProject bool
}

// RepositoryDecider is who decides a repository request: a person, by
// their principal, or the task's conductor (Run).
type RepositoryDecider struct {
	ActorType, ActorID string
	// The conductor's Run.
	Run string
}

func (b RepositoryDecider) conductor() bool { return b.Run != "" }

// RepositoryDecision is what a decision left.
type RepositoryDecision struct {
	ID, Status, RunID, ProjectID string
	Request                      RepositoryRequest
}

// RouteRepositoryRequestTx is who a Run's new repository request is for:
// HolderConductor when a writer (not the conductor itself) asks on a task
// conductorHeldTx gives its conductor first. Takes Chat, the delivery and
// the task (AskConductorTx's order) before the request is written.
func RouteRepositoryRequestTx(ctx context.Context, tx pgx.Tx, ref RunRef, role string) (string, error) {
	if ref.TaskID == "" || role == RoleConductor {
		return HolderPerson, nil
	}
	d, err := LockAnswerTx(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	held, err := conductorHeldTx(ctx, tx, ref.TaskID, d)
	if err != nil || !held {
		return HolderPerson, err
	}
	return HolderConductor, nil
}

// RepositoryRoutedTx records a request given to the conductor first: its
// ledger event (EvRepositoryRouted, the asking Run's) and the conductor's
// wake, keyed on the request.
func RepositoryRoutedTx(ctx context.Context, tx pgx.Tx, ev ledger.Event, id, repo, access, reason string) error {
	if _, err := ledger.Append(ctx, tx, ev); err != nil {
		return err
	}
	var phase string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(phase::text, role::text, 'agent') FROM runs WHERE id = $1`, ev.RunID).Scan(&phase); err != nil {
		return err
	}
	line := render(repositoryRequestWakeTmpl, struct{ Phase, RunID, ID, Repo, Access, Reason string }{
		phase, ev.RunID, id, repo, access, ExternalText(ev.RunID, clip(oneLine(reason), 80))})
	_, err := RecordWakeTx(ctx, tx, ev.OrganizationID, ev.TaskID, "question", "repository-request:"+id, line)
	return err
}

// repositoryAsk is a repository request as ToPersonTx asks the person:
// the asking Run's repository.requested event.
type repositoryAsk struct{ Event ledger.Event }

// RepositoryRequestToPersonTx asks the person to decide a request (the
// asking Run's repository.requested, which pushes): a request made on a
// task with no conductor first (ev as the tool builds it), through
// ToPersonTx.
func RepositoryRequestToPersonTx(ctx context.Context, tx pgx.Tx, ev ledger.Event, why string) error {
	_, err := ToPersonTx(ctx, tx, ToPerson{Ref: RunRef{Org: ev.OrganizationID, ProjectID: ev.ProjectID, TaskID: ev.TaskID, RunID: ev.RunID},
		repository: &repositoryAsk{ev}, Why: why})
	return err
}

// lockRepositoryRequestTx locks a request for its decision in the
// canonical order: Chat → delivery → task (LockAnswerTx), then the asking
// Run, then the request (a Run ending takes its row before its requests:
// cancel_repository_requests). nil for a request not found (of the task,
// when taskID is given).
func lockRepositoryRequestTx(ctx context.Context, tx pgx.Tx, id, taskID string) (*Delivery, *RepositoryRequest, string, error) {
	var req RepositoryRequest
	// A request's task and Run never change: read unlocked, to lock.
	err := tx.QueryRow(ctx, `SELECT task_id, run_id FROM repository_requests WHERE id = $1 AND ($2 = '' OR task_id = $2)`, id, taskID).
		Scan(&req.TaskID, &req.RunID)
	if db.IsNotFound(err) {
		return nil, nil, "", nil
	}
	if err != nil {
		return nil, nil, "", err
	}
	d, err := LockAnswerTx(ctx, tx, req.TaskID)
	if err != nil {
		return nil, nil, "", err
	}
	var runStatus string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM runs WHERE id = $1 FOR UPDATE`, req.RunID).Scan(&runStatus); err != nil {
		return nil, nil, "", err
	}
	err = tx.QueryRow(ctx, `SELECT q.id, q.repository_id, repo.name, q.access::text, q.status::text, q.addressee,
			repo.project_id = t.project_id
		FROM repository_requests q JOIN repositories repo ON repo.id = q.repository_id JOIN tasks t ON t.id = q.task_id
		WHERE q.id = $1 FOR UPDATE OF q`, id).
		Scan(&req.ID, &req.RepoID, &req.RepoName, &req.Access, &req.Status, &req.Addressee, &req.SameProject)
	if err != nil {
		return nil, nil, "", err
	}
	return d, &req, runStatus, nil
}

// LockRepositoryRequestTx is lockRepositoryRequestTx for a person's
// decision (the API): nil when there is no such request.
func LockRepositoryRequestTx(ctx context.Context, tx pgx.Tx, id string) (*RepositoryRequest, error) {
	_, req, _, err := lockRepositoryRequestTx(ctx, tx, id, "")
	return req, err
}

// DecideRepositoryRequestTx carries out a decision on a pending request
// the caller locked (LockRepositoryRequestTx) and authorized: approved,
// the repository joins the task (the syncer brings it to the live Run,
// pausing and resuming it once); one the Run has checked out already is
// settled now and the agent told; denied, the agent is told. Only a
// pending row changes, so of two decisions at once one wins and the other
// is a conflict (ok false, nothing written).
func DecideRepositoryRequestTx(ctx context.Context, tx pgx.Tx, org string, req *RepositoryRequest, by RepositoryDecider, approve bool, note string) (RepositoryDecision, bool, error) {
	out := RepositoryDecision{ID: req.ID, RunID: req.RunID, Request: *req, Status: "denied"}
	if approve {
		out.Status = "approved"
	}
	decidedBy := by.ActorID
	if by.conductor() {
		decidedBy = by.Run
	}
	tag, err := tx.Exec(ctx, `UPDATE repository_requests SET status = $2::repository_request_status,
		decided_by = $3, decided_at = now() WHERE id = $1 AND status = 'pending'`, req.ID, out.Status, decidedBy)
	if err != nil || tag.RowsAffected() == 0 {
		return out, false, err
	}
	if err := tx.QueryRow(ctx, `SELECT project_id FROM runs WHERE id = $1`, req.RunID).Scan(&out.ProjectID); err != nil {
		return out, false, err
	}
	who := "A person"
	if by.conductor() {
		who = "Your conductor"
	}
	ref := RunRef{Org: org, ProjectID: out.ProjectID, TaskID: req.TaskID, RunID: req.RunID}
	if approve {
		// The task works on it from now on: every later phase gets it too.
		if _, err := tx.Exec(ctx, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
			VALUES ($1, $2, $3, $4::repository_access) ON CONFLICT (task_id, repository_id) DO NOTHING`,
			org, req.TaskID, req.RepoID, req.Access); err != nil {
			return out, false, err
		}
		// The lux Run has it already (a person took it off the task and
		// back): nothing to bring, so it is settled now; nothing would ever
		// clone it.
		tag, err := tx.Exec(ctx, `UPDATE repository_requests q SET status = 'cloned'
			FROM runs run WHERE q.id = $1 AND run.id = q.run_id AND lux_name($2) = ANY (run.lux_repositories)`, req.ID, req.RepoName)
		if err != nil {
			return out, false, err
		}
		if tag.RowsAffected() > 0 {
			// Told, as a steer is: that starts the turn it may be waiting
			// for, and is the input a parked one is resumed with.
			text := fmt.Sprintf("%s approved your request: %s is already checked out at %s. Carry on.", who, req.RepoName, RepoPath(req.RepoName))
			if _, _, err := QueueDirective(ctx, tx, ref, Directive{Text: text, Scope: "run"}); err != nil {
				return out, false, err
			}
		}
	} else {
		text := fmt.Sprintf("Your request for %s was declined.", req.RepoName)
		if by.conductor() {
			text = fmt.Sprintf("Your conductor declined your request for %s.", req.RepoName)
		}
		if n := strings.TrimSpace(note); n != "" {
			text += " " + n
		}
		if _, _, err := QueueDirective(ctx, tx, ref, Directive{Text: text, Scope: "run"}); err != nil {
			return out, false, err
		}
	}
	// It may have been the last thing the task waited on a person for.
	if err := EndConductorWait(ctx, tx, org, out.ProjectID, req.TaskID); err != nil {
		return out, false, err
	}
	payload := map[string]any{"requestId": req.ID, "repository": req.RepoName, "access": req.Access, "note": note, "by": "person"}
	ev := ref.Event("repository."+out.Status, by.ActorType, payload)
	ev.ActorID = by.ActorID
	if by.conductor() {
		payload["by"] = "conductor"
		ev.ActorType, ev.ActorID = ledger.ActorAgent, by.Run
	}
	_, err = ledger.Append(ctx, tx, ev)
	return out, true, err
}

// Conductor's actions on a repository request it holds.
const (
	RepositoryDeny    = "deny"
	RepositoryApprove = "approve"
	RepositoryForward = "forward"
)

// ConductDecideRepositoryRequest is decide_repository_request: the task's
// live conductor denies a request dude gave it first, approves it when it
// asks to read a repository of the task's project, or forwards it to the
// person. Approving write access, or a repository of another project, is
// refused: only the owner approves those (RepositoryAccessOwnerOnly), so
// the conductor forwards them.
func ConductDecideRepositoryRequest(ctx context.Context, tx pgx.Tx, ref RunRef, id, action, note string) (RepositoryDecision, error) {
	switch action {
	case RepositoryDeny, RepositoryApprove, RepositoryForward:
	default:
		return RepositoryDecision{}, refuseText(rRepositoryRequestAction)
	}
	if err := liveConductor(ctx, tx, ref); err != nil {
		return RepositoryDecision{}, err
	}
	d, req, _, err := lockRepositoryRequestTx(ctx, tx, id, ref.TaskID)
	if err != nil {
		return RepositoryDecision{}, err
	}
	if req == nil || req.Status != "pending" || req.Addressee != HolderConductor {
		return RepositoryDecision{}, refusef(rRepositoryRequestNotHeld, id)
	}
	switch {
	case d != nil && Ended(d.TaskStatus):
		return RepositoryDecision{}, refusef(rThisTaskYouAreReadOnly, d.TaskStatus)
	case d == nil || !d.Live():
		return RepositoryDecision{}, refuseText(rThisTaskHasNoDeliveryIn)
	case !d.State.conducted():
		return RepositoryDecision{}, refuseText(rDeliverTakesThisTaskDecisionsNot)
	}
	if action == RepositoryForward {
		moved, err := repositoryRequestToPersonTx(ctx, tx, ref.Org, req, WhyConductor)
		if err == nil && !moved {
			err = refusef(rRepositoryRequestNotHeld, id)
		}
		return RepositoryDecision{ID: id, Status: "pending", RunID: req.RunID, Request: *req}, err
	}
	if action == RepositoryApprove && (RepositoryAccessOwnerOnly(req.Access) || !req.SameProject) {
		what := "write access"
		if !req.SameProject {
			what = "a repository of another project"
		}
		return RepositoryDecision{}, refusef(rRepositoryRequestAskThePerson, what)
	}
	out, ok, err := DecideRepositoryRequestTx(ctx, tx, ref.Org, req, RepositoryDecider{Run: ref.RunID}, action == RepositoryApprove, note)
	if err == nil && !ok {
		err = refusef(rRepositoryRequestNotHeld, id)
	}
	return out, err
}

// repositoryRequestToPersonTx flips a pending conductor-held request to
// the person and asks them (repository.requested, once). moved is false,
// and nothing written, when it is no longer pending and the conductor's.
func repositoryRequestToPersonTx(ctx context.Context, tx pgx.Tx, org string, req *RepositoryRequest, why string) (bool, error) {
	var projectID, reason string
	var blocking bool
	err := tx.QueryRow(ctx, `UPDATE repository_requests q SET addressee = 'person' FROM runs r
		WHERE q.id = $1 AND r.id = q.run_id AND q.status = 'pending' AND q.addressee = 'conductor'
		RETURNING r.project_id, q.reason, q.blocking`, req.ID).Scan(&projectID, &reason, &blocking)
	if db.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	req.Addressee = HolderPerson
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: req.TaskID, RunID: req.RunID}
	payload := map[string]any{"requestId": req.ID, "repository": req.RepoName, "repositoryId": req.RepoID,
		"access": req.Access, "reason": reason, "handedOverFrom": "conductor", "via": why}
	return true, RepositoryRequestToPersonTx(ctx, tx, ref.Event(EvRepositoryRequested, ledger.ActorAgent, payload), why)
}

// HandOverRepositoryRequestTx is HandOverReviewQuestionTx's for a pending
// conductor-held repository request: the person is asked to decide it.
func HandOverRepositoryRequestTx(ctx context.Context, tx pgx.Tx, org, taskID, id string, wait time.Duration) (bool, error) {
	_, req, _, err := lockRepositoryRequestTx(ctx, tx, id, taskID)
	if err != nil || req == nil {
		return false, err
	}
	var due bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM repository_requests q WHERE q.id = $2
		AND q.status = 'pending' AND q.addressee = 'conductor' AND `+heldDue+`)`, wait.Seconds(), id).Scan(&due); err != nil || !due {
		return false, err
	}
	return repositoryRequestToPersonTx(ctx, tx, org, req, WhyHandover)
}

// RepoPath is where a Run finds a repository it has checked out: the
// workspace's repos directory, by the name lux gives it.
func RepoPath(name string) string { return "/workspace/repos/" + lux.SpecName(name) }

// signalReviewQuestionTx wakes a delivery held by reviewer questions.
func signalReviewQuestionTx(ctx context.Context, tx pgx.Tx, org, wfID, id string) error {
	return workflow.SignalTx(ctx, tx, org, wfID, SignalReviewQuestion, map[string]any{"questionId": id}, "review-question:"+id)
}
