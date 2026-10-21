package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
// {scope: "attempt", person, turn, covers} for a waiver later Runs of the
// attempt may carry). directiveId is the directive carrying a send-back's
// message to the agent.
const (
	EvRunChecksJudged  = "run.checks.judged"
	EvRunChecksDecided = "run.checks.decided"
)

// EscalateChecksTx stops the delivery for a person at the step waiting on
// runID, whose report the gate will not pass: the Run keeps its session
// and its unpushed work, and the workflow resumes that wait once decided.
// A delivery already waiting on another escalation keeps it.
func EscalateChecksTx(ctx context.Context, tx pgx.Tx, org, taskID, runID, message string, rounds int) error {
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if d == nil {
		return fmt.Errorf("checks gate has no delivery for Run %s", runID)
	}
	st := d.State
	if st.Escalation != nil {
		return nil
	}
	detail := map[string]any{"runId": runID, "message": message, "rounds": rounds}
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
	st.Escalation = &Escalation{Reason: ReasonChecksUnverified, At: d.Step, Detail: detail}
	if st.conducted() {
		st.Escalations++
		if _, err := RecordWakeTx(ctx, tx, org, taskID, "escalation", fmt.Sprintf("%s:escalation:%d", d.WorkflowID, st.Escalations),
			escalatedLine(ReasonChecksUnverified, st.Escalation.Actions())); err != nil {
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
	if _, err = SetTaskStatusTx(ctx, tx, org, st.ProjectID, taskID, "", "awaiting_input", ReasonChecksUnverified); err != nil {
		return err
	}
	return emitTx(ctx, tx, org, &st, EvQuestionAsked, map[string]any{"kind": "escalation", "reason": ReasonChecksUnverified,
		"detail": st.Escalation.Detail, "actions": st.Escalation.Actions()})
}

// settleChecksTx carries a person's decision to the held Run, in the
// decision's transaction: a waiver passes the turn it was held at, so the
// gate lets its work be pushed with no further agent turn; a retry sends
// the report back once more, with the configured rounds again; stop ends
// the Run, its work unpushed. A waiver of ScopeAttempt also records the
// held report's reported failures (covers), which a later Run of the
// attempt reporting the same command failing may carry once the judge
// says it is the same failure (phases.judgeChecks).
func settleChecksTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, e *Escalation, action, note, person, scope string) error {
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
		decided["scope"], decided["person"], decided["turn"], decided["covers"] = scope, person, turn, covers
	}
	if action == "retry" {
		var detail struct {
			Message string `json:"message"`
		}
		e.decode(&detail)
		directiveID, _, err := QueueDirective(ctx, tx, ref, Directive{Text: detail.Message, Scope: "run"})
		if err != nil {
			return err
		}
		decided["directiveId"] = directiveID
	}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvRunChecksDecided, ledger.ActorHuman, decided)); err != nil {
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
		_, err := tx.Exec(ctx, `UPDATE runs SET control = 'abort', control_requested_at = now(), control_reason = $2
			WHERE id = $1 AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, id, "a person stopped it at the checks gate")
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
	_ = json.Unmarshal(raw, &verdicts)
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
	RunID   string `json:"runId"`
	Role    string `json:"role"`
	Turn    int64  `json:"turn"`
	Person  string `json:"person,omitempty"`
	Note    string `json:"note"`
	Cursor  int64  `json:"cursor"`
	Covered Check  `json:"-"`
}

// InheritableWaiverTx is the newest attempt-scoped checks waiver of the
// judged Run's task and attempt, decided before the turn that ended at
// cursor before, covering a reported failure of the same command
// (trimmed). It is only a candidate for the judge, never a pass by
// itself; nil for none.
func InheritableWaiverTx(ctx context.Context, tx pgx.Tx, runID string, before int64, failed Check) (*CarriedWaiver, error) {
	if failed.Result != CheckFail {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT e.run_id, src.phase::text, e.cursor, e.payload
		FROM runs judged JOIN runs src ON src.task_id = judged.task_id AND src.attempt = judged.attempt
		JOIN events e ON e.run_id = src.id
		WHERE judged.id = $1 AND e.event_type = $2 AND e.payload->>'action' = $3 AND e.payload->>'scope' = $4
		  AND e.cursor < $5
		ORDER BY e.cursor DESC`, runID, EvRunChecksDecided, ActionWaive, ScopeAttempt, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w CarriedWaiver
		var phase string
		var p struct {
			Note   string  `json:"note"`
			Person string  `json:"person"`
			Turn   int64   `json:"turn"`
			Covers []Check `json:"covers"`
		}
		if err := rows.Scan(&w.RunID, &phase, &w.Cursor, &p); err != nil {
			return nil, err
		}
		for _, c := range p.Covers {
			if c.Result == failed.Result && strings.TrimSpace(c.Command) == strings.TrimSpace(failed.Command) {
				w.Role, w.Turn, w.Person, w.Note, w.Covered = RoleName(PromptRoleForPhase[phase]), p.Turn, p.Person, p.Note, c
				return &w, nil
			}
		}
	}
	return nil, rows.Err()
}
