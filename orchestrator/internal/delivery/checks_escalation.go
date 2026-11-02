package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ReasonChecksUnverified: a gated phase's checks report was sent back as
// many times as the organisation allows and is still not verified. Its
// Run is held, its work unpushed, until a person retries, waives or stops.
const ReasonChecksUnverified = "checks_unverified"

// Ledger events of the checks gate, on the Run: each judgement of a
// turn's report (payload {turn, round, verified, checks, message,
// skipped?, escalated?, waived?, carried?, directiveId?}), and each
// person's decision on a held report ({action, note, directiveId?}, with
// {scope: "attempt", person, personName?, turn, covers} for a waiver later Runs of the
// attempt may carry). directiveId is the directive carrying a send-back's
// message to the agent.
const (
	EvRunChecksJudged  = "run.checks.judged"
	EvRunChecksDecided = "run.checks.decided"
)

// UnverifiedCheck is one check a held report did not pass, as the
// escalation's detail keeps it ("unverified"): its command and why, each
// one clipped line.
type UnverifiedCheck struct {
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// EscalateChecksTx stops the delivery for a person at the step waiting on
// runID, whose report the gate will not pass: the Run keeps its session
// and its unpushed work, and the workflow resumes that wait once decided.
// A delivery already waiting on another escalation keeps it. unverified
// are the checks the held send-back names.
func EscalateChecksTx(ctx context.Context, tx pgx.Tx, org, taskID, runID, message string, rounds int, unverified []UnverifiedCheck) error {
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if d == nil {
		return fmt.Errorf("checks gate has no delivery for Run %s", runID)
	}
	st := d.State
	if st.Escalation != nil {
		// Held again after a person steered it during the hold: the steer's
		// turn cleared the wait, so it is set again for the park.
		if e := st.Escalation; e.Reason == ReasonChecksUnverified && e.Decided == nil && e.RunID() == runID {
			return waitOnPersonTx(ctx, tx, runID)
		}
		return nil
	}
	detail := map[string]any{"runId": runID, "message": message, "rounds": rounds}
	if len(unverified) > 0 {
		clipped := make([]UnverifiedCheck, len(unverified))
		for i, c := range unverified {
			clipped[i] = UnverifiedCheck{clip(oneLine(c.Command), escalatedItemMax), clip(oneLine(c.Reason), escalatedItemMax)}
		}
		detail["unverified"] = clipped
	}
	// The reported failures an attempt waiver could carry (settleChecksTx),
	// for a person to be offered that scope only when there are some.
	turn, err := LatestTurnTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	failures, err := heldFailuresTx(ctx, tx, runID, turn)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		detail["failures"] = failures
	}
	var atSignals []string
	if err := tx.QueryRow(ctx, `SELECT awaiting_signals FROM workflow_runs WHERE id = $1`, d.WorkflowID).Scan(&atSignals); err != nil {
		return err
	}
	st.Escalation = &Escalation{Reason: ReasonChecksUnverified, At: d.Step, AtSignals: atSignals, Detail: detail}
	if st.conducted() {
		if _, err := tellEscalationTx(ctx, tx, org, d.WorkflowID, &st); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = 'decide', status = 'waiting',
		awaiting_signals = '["human.decision"]', wake_at = NULL WHERE id = $1`, d.WorkflowID, raw); err != nil {
		return err
	}
	if err = waitOnPersonTx(ctx, tx, runID); err != nil {
		return err
	}
	if _, err = SetTaskStatusTx(ctx, tx, org, st.ProjectID, taskID, "", "awaiting_input", ReasonChecksUnverified); err != nil {
		return err
	}
	return emitTx(ctx, tx, org, &st, EvQuestionAsked, map[string]any{"kind": "escalation", "reason": ReasonChecksUnverified,
		"detail": st.Escalation.Detail, "actions": st.Escalation.Actions()})
}

// waitOnPersonTx starts the held Run's wait on a person (checksHeld), kept
// if one is already running: it is parked like one asking a question once
// its project's grace is over.
func waitOnPersonTx(ctx context.Context, tx pgx.Tx, runID string) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET waiting_since = COALESCE(waiting_since, now()) WHERE id = $1`, runID)
	return err
}

// settleChecksTx carries a person's decision to the held Run, in the
// decision's transaction: a waiver passes the turn it was held at, so the
// gate lets its work be pushed with no further agent turn; a retry sends
// the report back once more, with the configured rounds again; stop ends
// the Run, its work unpushed. A waiver of ScopeAttempt also records the
// held report's reported failures (covers), which a later Run of the
// attempt reporting the same command failing may carry once the judge
// says it is the same failure (phases.judgeChecks). said is the person's
// note marked as their words (PersonSaidTx), "" for none: a retry's
// send-back leads with it, so the agent acts on what the person said.
func settleChecksTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, e *Escalation, action, note, said, person, scope string) error {
	id := e.RunID()
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: id}
	decided := map[string]any{"action": action, "note": note}
	turn, err := LatestTurnTx(ctx, tx, id)
	if err != nil {
		return err
	}
	var covers []Check
	if scope == ScopeAttempt {
		if covers, err = heldFailuresTx(ctx, tx, id, turn); err != nil {
			return err
		}
		if len(covers) == 0 {
			return escalationErr("bad_request", "%s", mNothingToCarryInAttempt)
		}
		name, err := PersonName(ctx, tx, person)
		if err != nil {
			return err
		}
		decided["scope"], decided["person"], decided["turn"], decided["covers"] = scope, person, turn, covers
		if name != "" {
			decided["personName"] = name
		}
	}
	if action == "retry" {
		var detail struct {
			Message string `json:"message"`
		}
		e.decode(&detail)
		directiveID, _, err := QueueDirective(ctx, tx, ref, Directive{Text: withPersonSaid(said, detail.Message), Scope: "run"})
		if err != nil {
			return err
		}
		decided["directiveId"] = directiveID
	}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvRunChecksDecided, ledger.ActorHuman, decided)); err != nil {
		return err
	}
	// Decided: the Run no longer waits on this person (checksHeld ends with
	// this transaction). Another wait that holds it keeps its start: OpenAsk
	// would still count checksHeld here.
	if _, err := tx.Exec(ctx, `UPDATE runs r SET waiting_since = CASE WHEN `+HoldsTurn+` THEN r.waiting_since END
		WHERE r.id = $1`, id); err != nil {
		return err
	}
	switch action {
	case ActionWaive:
		judged := map[string]any{"turn": turn, "verified": true, "waived": true, "message": note, "checks": []any{}}
		if scope == ScopeAttempt {
			judged["scope"], judged["person"], judged["covers"] = scope, person, covers
		}
		_, err = ledger.Append(ctx, tx, ref.Event(EvRunChecksJudged, ledger.ActorHuman, judged))
		return err
	case "stop":
		// A Run parked while it was held is aborted here, as the API's stop
		// aborts a paused one, but not kept: a paused Run is resumed once
		// nothing holds it, before its control is read.
		const why = "a person stopped it at the checks gate"
		var status string
		if err := tx.QueryRow(ctx, `SELECT status::text FROM runs WHERE id = $1 FOR UPDATE`, id).Scan(&status); db.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET control = 'abort', control_requested_at = now(), control_reason = $2,
			status = CASE WHEN status = 'paused' THEN 'aborted' ELSE status END,
			ended_at = CASE WHEN status = 'paused' THEN now() ELSE ended_at END
			WHERE id = $1 AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, id, why); err != nil {
			return err
		}
		if status != "paused" {
			return nil
		}
		if err := FailUnreadTx(ctx, tx, ref, time.Time{}, UnreadRunAborted); err != nil {
			return err
		}
		ev := ref.Event("run.aborted", ledger.ActorHuman, map[string]any{"reason": why})
		if person != "" {
			ev.ActorID = person
		}
		_, err := ledger.Append(ctx, tx, ev)
		return err
	}
	return nil
}

// Scopes of a checks waiver (EscalationDecision.Scope). A recorded waiver
// with no scope, as every one before scopes, is the Run's alone.
const (
	ScopeRun     = "run"
	ScopeAttempt = "attempt"
)

// heldFailuresTx are the checks the Run's report held at turn reported as
// failing, as its judgement recorded them; nil for none. Only a reported
// fail is carried: not-run says nothing about what a later run shows.
func heldFailuresTx(ctx context.Context, tx pgx.Tx, runID string, turn int64) ([]Check, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT payload->'checks' FROM events WHERE run_id = $1 AND event_type = $2
		AND (payload->>'turn')::bigint = $3 AND payload->>'verified' = 'false' ORDER BY cursor DESC LIMIT 1`,
		runID, EvRunChecksJudged, turn).Scan(&raw)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var verdicts []struct{ Command, Result, Reason string }
	if err := json.Unmarshal(raw, &verdicts); err != nil {
		return nil, fmt.Errorf("Run %s's held checks judgement at turn %d: %w", runID, turn, err)
	}
	var out []Check
	for _, v := range verdicts {
		if v.Result == CheckFail {
			out = append(out, Check{Command: v.Command, Result: v.Result, Evidence: v.Reason})
		}
	}
	return out, nil
}

// CarriedWaiver is a person's attempt-scoped waiver a later report's
// failure of Covered may be the same as: who waived it, why, and the Run
// and turn it was held at.
type CarriedWaiver struct {
	RunID  string `json:"runId"`
	Role   string `json:"role"`
	Turn   int64  `json:"turn"`
	Person string `json:"person,omitempty"`
	// The person's name when they waived, for the pull request's account.
	PersonName string `json:"personName,omitempty"`
	Note       string `json:"note"`
	Cursor     int64  `json:"cursor"`
	Covered    Check  `json:"-"`
}

// AttemptWaivers are the attempt-scoped checks waivers a judged turn may
// carry, newest first, each with what it covers.
type AttemptWaivers []attemptWaiver

type attemptWaiver struct {
	CarriedWaiver
	covers []Check
}

// AttemptWaiversTx are the attempt-scoped checks waivers of the judged
// Run's task and attempt decided before cursor before (the end of the
// judged turn), read once per judgement.
func AttemptWaiversTx(ctx context.Context, tx pgx.Tx, runID string, before int64) (AttemptWaivers, error) {
	// event_type is a literal so generic plans can prove events_run_checks_decided_idx's predicate.
	rows, err := tx.Query(ctx, `SELECT e.run_id, src.phase::text, e.cursor, e.payload
		FROM runs judged JOIN runs src ON src.task_id = judged.task_id AND src.attempt = judged.attempt
		JOIN events e ON e.run_id = src.id
		WHERE judged.id = $1 AND e.event_type = 'run.checks.decided' AND e.payload->>'action' = $2 AND e.payload->>'scope' = $3
		  AND e.cursor < $4
		ORDER BY e.cursor DESC`, runID, ActionWaive, ScopeAttempt, before)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (attemptWaiver, error) {
		var w attemptWaiver
		var phase string
		var p struct {
			Note       string  `json:"note"`
			Person     string  `json:"person"`
			PersonName string  `json:"personName"`
			Turn       int64   `json:"turn"`
			Covers     []Check `json:"covers"`
		}
		if err := row.Scan(&w.RunID, &phase, &w.Cursor, &p); err != nil {
			return w, err
		}
		w.Role, w.Turn, w.Person, w.PersonName, w.Note, w.covers = RoleName(PromptRoleForPhase[phase]), p.Turn, p.Person, p.PersonName, p.Note, p.Covers
		return w, nil
	})
}

// For is the newest waiver covering a reported failure of failed's
// command (trimmed). It is only a candidate for the judge, never a pass
// by itself; nil for none, and for anything but a reported fail.
func (ws AttemptWaivers) For(failed Check) *CarriedWaiver {
	if failed.Result != CheckFail {
		return nil
	}
	for _, w := range ws {
		for _, c := range w.covers {
			if c.Result == failed.Result && strings.TrimSpace(c.Command) == strings.TrimSpace(failed.Command) {
				carried := w.CarriedWaiver
				carried.Covered = c
				return &carried
			}
		}
	}
	return nil
}
