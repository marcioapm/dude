package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// What Deliver does before a task's first pull request: the task's own
// choice (tasks.pull_request_choice), else its project's (Policy.PullRequest
// as the project's settings say now), else the factory's Open it. Read when
// the delivery reaches the point before the pull request, never frozen.

// PullRequestOpenText is the refusal of a change once a pull request is
// open: the choice no longer applies to anything.
const PullRequestOpenText = "The pull request is already open."

// EvPullRequestChoiceChanged is the task's owner changing its choice.
// Payload: {from, to} (null: the project's).
const EvPullRequestChoiceChanged = "task.pull_request_choice_changed"

// pullRequestOpenedSQL ($1 the task): a pull request row of the task's
// current attempt, dude's own or adopted, open or ended. One whose Run is
// unknown belongs to the current attempt.
const pullRequestOpenedSQL = `SELECT EXISTS (SELECT 1 FROM pull_requests pr LEFT JOIN runs r ON r.id = pr.run_id
	WHERE pr.task_id = $1 AND COALESCE(r.attempt, task_attempt($1)) = task_attempt($1))`

// PullRequestOpenedTx says whether the task's current attempt has had a
// pull request.
func PullRequestOpenedTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	var opened bool
	err := tx.QueryRow(ctx, pullRequestOpenedSQL, taskID).Scan(&opened)
	return opened, err
}

// PullRequestChoiceTx is what Deliver does before the task's pull request
// now: the task's own choice whatever its project's policy says, else its
// project's (PolicyFor). A project policy that no longer resolves leaves
// nothing to follow: ChoiceUnresolved.
func PullRequestChoiceTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	var own *string
	err := tx.QueryRow(ctx, `SELECT pull_request_choice FROM tasks WHERE id = $1`, taskID).Scan(&own)
	if db.IsNotFound(err) {
		return "", TaskNotFound{taskID}
	}
	if err != nil {
		return "", err
	}
	if own != nil && IsPullRequestChoice(*own) {
		return *own, nil
	}
	_, p, err := PolicyFor(ctx, tx, taskID)
	var notFound TaskNotFound
	if errors.As(err, &notFound) {
		return "", err
	}
	if err != nil {
		return "", ChoiceUnresolved{err}
	}
	if !IsPullRequestChoice(p.PullRequest) {
		return "", ChoiceUnresolved{fmt.Errorf("pullRequest %q", p.PullRequest)}
	}
	return p.PullRequest, nil
}

// ChoiceUnresolved is a task with no choice of its own whose project's
// policy does not resolve: Deliver cannot tell whether to ask, so the
// opening escalates (ReasonChoiceUnresolved) rather than open.
type ChoiceUnresolved struct{ Err error }

func (e ChoiceUnresolved) Error() string { return "before the pull request: " + e.Err.Error() }
func (e ChoiceUnresolved) Unwrap() error { return e.Err }

// ReasonChoiceUnresolved is the escalation of an opening whose choice
// before the pull request could not be read (ChoiceUnresolved).
const ReasonChoiceUnresolved = "pull_request_choice_unresolved"

// FirstPullRequestChoice is the task's choice before its pull request, as
// the step that opens it reads it: "" once the current attempt has had a
// pull request, whatever the choice says, so only the first opening is
// held or made a draft by it.
func (s *Store) FirstPullRequestChoice(ctx context.Context, org, taskID string) (string, error) {
	choice := ""
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		choice, err = firstPullRequestChoiceTx(ctx, tx, taskID)
		return err
	})
	return choice, err
}

func firstPullRequestChoiceTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	opened, err := PullRequestOpenedTx(ctx, tx, taskID)
	if err != nil || opened {
		return "", err
	}
	return PullRequestChoiceTx(ctx, tx, taskID)
}

// ReasonChoiceChanged is the task's status reason once a change of its
// choice released the hold its Ask me first made.
const ReasonChoiceChanged = "the owner changed what happens before the pull request"

// choiceReleased says a hold the task's Ask me first made is no longer
// asked for: its choice changed since. It drops the hold's decision; the
// opening step reads the choice again.
func (w *steps) choiceReleased(ctx context.Context, sc workflow.StepContext, st *State) (bool, error) {
	if !st.ChoiceGate {
		return false, nil
	}
	choice, err := w.s.FirstPullRequestChoice(ctx, sc.OrganizationID, st.TaskID)
	if errors.As(err, new(ChoiceUnresolved)) {
		// Nothing says to stop asking: the hold stays.
		return false, nil
	}
	if err != nil || choice == PullRequestAsk || choice == "" {
		return false, err
	}
	releaseChoiceGate(st)
	return true, w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "running", ReasonChoiceChanged)
}

func releaseChoiceGate(st *State) {
	st.Decision = nil
	st.Decisions++
	st.GateRequired, st.ChoiceGate = false, false
	if st.Hold != nil {
		st.Hold.SentBack = false
	}
}

// choiceReleasedAtCommitTx reads the choice again as a step parking at the
// hold its Ask me first makes commits (recheck): a change saved while the
// step entered the hold found no hold to signal (HeldByChoice), so the park
// becomes the opening here, in the transition's transaction.
func choiceReleasedAtCommitTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, res workflow.Result) (workflow.Result, bool, error) {
	d := st.Decision
	if res.Next != "conductorDecision" || !st.ChoiceGate || st.conducted() || d == nil || d.Point != PointBeforePR || d.Taken != nil {
		return res, false, nil
	}
	choice, err := firstPullRequestChoiceTx(ctx, tx, st.TaskID)
	if errors.As(err, new(ChoiceUnresolved)) {
		return res, false, nil
	}
	if err != nil || choice == PullRequestAsk || choice == "" {
		return res, false, err
	}
	releaseChoiceGate(st)
	if _, err := SetTaskStatusTx(ctx, tx, sc.OrganizationID, st.ProjectID, st.TaskID, "", "running", ReasonChoiceChanged); err != nil {
		return res, false, err
	}
	return workflow.Result{Next: "openPullRequest", State: st}, true, nil
}

// FeedbackStillOpenText is why the opening is refused at the hold while
// the people's feedback is open.
func FeedbackStillOpenText(n int) string {
	if n == 1 {
		return "1 feedback item still open"
	}
	return fmt.Sprintf("%d feedback items still open", n)
}

// HeldByChoice says the task's delivery is held before its pull request by
// its Ask me first, under Deliver: a change of the choice wakes it.
func (d *Delivery) HeldByChoice() bool {
	return d != nil && d.AtGate() && d.State.ChoiceGate && !d.State.conducted()
}

// ChoiceRefusal is a change of the task's choice that is not made: its
// Code is the API's ("not_owner", "pull_request_open", "bad_request",
// "not_found").
type ChoiceRefusal struct{ Code, Msg string }

func (e ChoiceRefusal) Error() string { return e.Msg }

// SetPullRequestChoiceTx sets the task's own choice ("" for the project's)
// as person, its owner: refused for anyone else and once the current
// attempt has a pull request. Returns whether it changed. The change is
// read by the delivery at its gate; a delivery Deliver holds at the gate is
// woken to read it (the caller signals, AtAskHold).
func SetPullRequestChoiceTx(ctx context.Context, tx pgx.Tx, org, taskID, choice string, actor Actor) (bool, error) {
	if choice != "" && !IsPullRequestChoice(choice) {
		return false, ChoiceRefusal{"bad_request", fmt.Sprintf("pullRequest is open, draft, ask or null, not %q", choice)}
	}
	var projectID string
	var current *string
	err := tx.QueryRow(ctx, `SELECT project_id, pull_request_choice FROM tasks WHERE id = $1 FOR NO KEY UPDATE`, taskID).
		Scan(&projectID, &current)
	if db.IsNotFound(err) {
		return false, ChoiceRefusal{"not_found", fmt.Sprintf("task %s not found", taskID)}
	}
	if err != nil {
		return false, err
	}
	var ownerID, ownerName *string
	if err := tx.QueryRow(ctx, `SELECT o.pid, (SELECT name FROM people WHERE id = o.pid)
		FROM (SELECT `+TaskOwnerOf+` AS pid FROM tasks t WHERE t.id = $1) o`, taskID).Scan(&ownerID, &ownerName); err != nil {
		return false, err
	}
	if actor.Person == "" || ownerID != nil && *ownerID != actor.Person {
		who := "the task's owner"
		if ownerName != nil {
			who = firstName(*ownerName)
		}
		return false, ChoiceRefusal{"not_owner", fmt.Sprintf("Only %s can change it.", who)}
	}
	opened, err := PullRequestOpenedTx(ctx, tx, taskID)
	if err != nil {
		return false, err
	}
	if opened {
		return false, ChoiceRefusal{"pull_request_open", PullRequestOpenText}
	}
	was := ""
	if current != nil {
		was = *current
	}
	if was == choice {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET pull_request_choice = NULLIF($2, ''), updated_at = now() WHERE id = $1`,
		taskID, choice); err != nil {
		return false, err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestChoiceChanged, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, ActorType: actor.ActorType, ActorID: actor.ActorID, Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: map[string]any{"from": nullable(was), "to": nullable(choice)}})
	return true, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IsChoiceRefusal says whether err is a ChoiceRefusal, and which.
func IsChoiceRefusal(err error) (ChoiceRefusal, bool) {
	var r ChoiceRefusal
	ok := errors.As(err, &r)
	return r, ok
}
