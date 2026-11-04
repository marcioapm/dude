package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Events of conductor-first escalation, declared ahead of their writers
// (docs: conductor-first, PR2 and PR4a). Nothing emits them yet.
const (
	// A writer's question routed to its conductor rather than a person
	// (ask_conductor): the task keeps running and nobody is notified.
	EvQuestionRouted = "question.routed"
	// An escalation raised with its conductor holding it (Holder
	// "conductor"): payload {reason, detail, actions, holder, personOnly}.
	EvEscalationRaised = "escalation.raised"
)

// Who holds an escalation (Escalation.Holder), or who a question is for
// (questions.addressee). "" reads as HolderPerson: everything raised before
// conductor-first was a person's.
const (
	HolderPerson    = "person"
	HolderConductor = "conductor"
)

// Why a person is asked (ToPerson.Why). Recorded for the reader of the
// call, not written anywhere yet.
const (
	// The conductor's own question: ask_person, decide ask_person,
	// request_limit.
	WhyConductor = "conductor"
	// A delivery no conductor decides (Deliver's policy).
	WhyNoConductor = "no_conductor"
	// Handed over from the conductor: it did not decide in time, failed,
	// or has no model.
	WhyHandover = "handover"
	// Something only the person can do: connect GitHub.
	WhyCredential = "credential"
	// The person's own gate: "Ask me first", an adopted branch's hold.
	WhyPersonGate = "person_gate"
	// A writer's (implementer, fixer, investigator) ask_person.
	// TODO(conductor-first PR4a): routed to the conductor (ask_conductor).
	WhyWriter = "writer"
	// dude stopped the delivery: escalate, the checks gate's bound, a
	// promotion stopped at boot.
	// TODO(conductor-first PR2): conductor-held first (raiseEscalationTx);
	// the person's only through WhyNoConductor or WhyHandover.
	WhyEscalation = "escalation"
)

// ToPerson is something that waits on a person: exactly one of
//
//   - Items, a question on Ref's Run (Ref.RunID);
//   - Escalation, the delivery's (State.Escalation) stopping for them;
//   - neither, a hold: the task waits on the person for Reason.
type ToPerson struct {
	Ref        RunRef
	Items      []QuestionItem
	State      *State
	Escalation *Escalation
	// The task's status reason, for an escalation or a hold.
	Reason string
	Why    string
}

// ToPersonTx is the one way anything on a task reaches a person: it
// inserts an open question addressed to a person, moves the task to
// awaiting_input, and emits question.asked
// (TestNothingReachesAPersonButThroughToPersonTx). It takes the task's row
// (SetTaskStatusTx) after whatever the caller locked, in
// LockEscalationTx's order. Returns the question's id, "" for none.
func ToPersonTx(ctx context.Context, tx pgx.Tx, p ToPerson) (string, error) {
	switch p.Why {
	case WhyConductor, WhyNoConductor, WhyHandover, WhyCredential, WhyPersonGate, WhyWriter, WhyEscalation:
	default:
		return "", fmt.Errorf("to person: unknown why %q", p.Why)
	}
	if len(p.Items) > 0 {
		if p.Escalation != nil {
			return "", fmt.Errorf("to person: a question and an escalation at once")
		}
		return askPersonTx(ctx, tx, p.Ref, p.Items)
	}
	// An escalation or a hold: the task waits on the person for Reason. An
	// escalation's question.asked of kind escalation says so (the API's
	// escalation).
	st, e := p.State, p.Escalation
	switch {
	case e != nil && (st == nil || st.Escalation != e):
		return "", fmt.Errorf("to person: an escalation that is not its delivery's")
	case e != nil && e.HeldBy() != HolderPerson:
		return "", fmt.Errorf("to person: an escalation held by %s", e.Holder)
	case st == nil:
		return "", fmt.Errorf("to person: a hold with no delivery")
	}
	if _, err := SetTaskStatusTx(ctx, tx, p.Ref.Org, st.ProjectID, st.TaskID, "", "awaiting_input", p.Reason); err != nil || e == nil {
		return "", err
	}
	return "", emitTx(ctx, tx, p.Ref.Org, st, EvQuestionAsked,
		map[string]any{"kind": "escalation", "reason": e.Reason, "detail": e.Detail, "actions": e.Actions()})
}

// heldBy marks the escalation raised now, held by holder. Called where it
// is raised, before the state that carries it is written.
func (e *Escalation) heldBy(holder string) *Escalation {
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.Holder, e.RaisedAt = holder, &now
	return e
}

// HeldBy is who holds it: "" reads as HolderPerson.
func (e *Escalation) HeldBy() string {
	if e.Holder == "" {
		return HolderPerson
	}
	return e.Holder
}

// askPersonTx records a Run's question for a person, and the task waiting
// on it. When the question is what moved the task, its event keeps the
// status the task had before (taskStatus) and the move's own event
// (waitCursor): the mark an answer to a conductor checks before putting
// the task back (EndConductorWait). Items are normalized (NormalizeItems):
// prompt and options keep what their readers show.
func askPersonTx(ctx context.Context, tx pgx.Tx, r RunRef, items []QuestionItem) (string, error) {
	id := ids.New(ids.Question)
	prompt, options := AskPrompt(items), AskOptions(items)
	opts, _ := json.Marshal(options)
	rawItems, _ := json.Marshal(items)
	if _, err := tx.Exec(ctx, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, items, addressee)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, 'person')`, id, r.Org, r.TaskID, r.RunID, prompt, opts, rawItems); err != nil {
		return "", err
	}
	var before string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, r.TaskID).Scan(&before); err != nil {
		return "", err
	}
	moved, err := SetTaskStatusTx(ctx, tx, r.Org, r.ProjectID, r.TaskID, "", "awaiting_input", "the agent asked a question")
	if err != nil {
		return "", err
	}
	payload := map[string]any{"kind": "agent", "questionId": id, "prompt": prompt, "options": options, "items": items}
	if moved {
		// The move's event is this transaction's latest for the task, whose
		// row the move holds.
		var cursor int64
		if err := tx.QueryRow(ctx, `SELECT max(cursor) FROM events WHERE task_id = $1 AND event_type = $2`,
			r.TaskID, EvTaskStatusChanged).Scan(&cursor); err != nil {
			return "", err
		}
		payload["taskStatus"], payload["waitCursor"] = before, cursor
	}
	// A Run the conductor started asking a person wakes the conductor,
	// which may answer for what it knows, or explain in Chat.
	var phase string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(phase::text, '') FROM runs WHERE id = $1 AND conductor_run_id IS NOT NULL`,
		r.RunID).Scan(&phase); err == nil {
		if _, err := RecordWakeTx(ctx, tx, r.Org, r.TaskID, "question", "question:"+id, runAsksLine(phase, r.RunID, id)); err != nil {
			return "", err
		}
	} else if !db.IsNotFound(err) {
		return "", err
	}
	_, err = ledger.Append(ctx, tx, r.Event(EvQuestionAsked, ledger.ActorAgent, payload))
	return id, err
}

// PersonOnly says only a person may take the action on an escalation of
// the reason: the conductor may take any other, and must ask the person
// (one question, its recommendation marked) for these. A superset of
// NeedsReason, whose actions an organization admin may take as well as the
// task's owner (mayDecideNoted); the rest are the owner's.
//
//   - shipping past what a review or test did not pass: accept on
//     review_failed, stuck, exhausted, not_converging, test_failed_criteria;
//     waive on test_blocked and checks_unverified (NeedsReason);
//   - changing the acceptance criteria: amend on not_converging;
//   - declaring the merged part the task: done on pull_request_closed;
//   - resetting the organisation's fix budgets: retry on pr_loop_exhausted;
//   - everything on conductor_unavailable, which no conductor holds.
//
// Retry and stop on stuck, exhausted and not_converging are the
// conductor's: it may let the loop go more rounds, with its direction.
func PersonOnly(reason, action string) bool {
	if NeedsReason(reason, action) {
		return true
	}
	switch reason {
	case ReasonConductorUnavailable:
		return true
	case "review_failed", "stuck", "exhausted":
		return action == "accept"
	case ReasonNotConverging:
		return action == "accept" || action == "amend"
	case "pull_request_closed":
		return action == "done"
	case "pr_loop_exhausted":
		return action == "retry"
	}
	return false
}

// RepositoryAccessOwnerOnly says only the task's owner may approve an
// agent's request for a repository with that access: write. Not an
// organization admin who is not the owner, nor the conductor, which may
// deny a request, or approve read access.
func RepositoryAccessOwnerOnly(access string) bool { return access != "read" }
