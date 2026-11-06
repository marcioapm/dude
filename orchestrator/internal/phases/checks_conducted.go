package phases

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

var (
	checksWake           = delivery.MustText("wakes/checks")
	checksNoConductor    = delivery.MustText("notices/checks/no-conductor")
	checksUnconducted    = delivery.MustText("notices/checks/unconducted")
	checksUnconductedRej = delivery.MustText("notices/checks/unconducted-reject")
	checksConductorSaid  = delivery.MustText("notices/checks/conductor-rejected")
)

// conductedDelivery says the Run's task has a live delivery its conductor
// decides: its checks reports are the conductor's to decide.
func (s *Syncer) conductedDelivery(ctx context.Context, r phaseRun) (bool, error) {
	var conducted bool
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		d, err := delivery.ReadDelivery(ctx, tx, r.TaskID)
		conducted = err == nil && d.Conducted()
		return err
	})
	return conducted, err
}

// noConductor is the gate's decision when no conductor decides: every
// non-pass is rejected as untiered (only a person passes it, after the
// rounds), and a report of passes only passes with the skip notice. Never
// does a non-pass pass without a decision.
func noConductor(d checksDecision, reason, skip delivery.Text) checksDecision {
	if len(d.Verdicts) > 0 {
		d.Verdicts = append([]checksVerdict{}, d.Verdicts...)
		for i := range d.Verdicts {
			d.Verdicts[i].JudgeReason = reason.Render(nil)
		}
		d.untiered = true
		return d
	}
	d.Verdicts = []checksVerdict{}
	d.Verified, d.Skipped = true, true
	d.Message = skip.Render(nil)
	return d
}

// askConductor is the conducted gate: the conductor's verdict on the
// Run's latest turn decides it (verdictDecision). With none yet, the gate
// asks once (delivery.AskChecksTx: an event, a wake keyed on the turn and
// the Run's wait) and is pending; past the wait, or with the conductor
// unavailable, it falls back (noConductor): passes skip, non-passes are
// rejected for a person after the rounds.
func (s *Syncer) askConductor(ctx context.Context, r phaseRun, d checksDecision, checks []delivery.Check) (checksDecision, error) {
	var asked *delivery.ChecksAsked
	var verdict *delivery.ConductedChecks
	var overdue, unavailable bool
	var turn int64
	var epoch int
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		if turn, err = delivery.LatestTurnTx(ctx, tx, r.ID); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT agent_session_epoch FROM runs WHERE id = $1`, r.ID).Scan(&epoch); err != nil {
			return err
		}
		if asked, overdue, err = delivery.ChecksAskedTx(ctx, tx, r.ID, turn); err != nil {
			return err
		}
		if verdict, err = delivery.ConductedChecksTx(ctx, tx, r.ID, turn); err != nil || verdict != nil {
			return err
		}
		unavailable, err = delivery.ChecksConductorUnavailableTx(ctx, tx, r.TaskID)
		return err
	})
	if err != nil {
		return d, err
	}
	if verdict != nil {
		return verdictDecision(d, checks, asked, verdict), nil
	}
	if overdue || unavailable {
		return noConductor(d, checksUnconductedRej, checksUnconducted), nil
	}
	if asked != nil {
		d.pending = true
		return d, nil
	}
	// What the conductor is asked: the report, each failure's carried
	// waiver candidate, and the task's bases when a failure is reported.
	a := delivery.ChecksAsked{Turn: turn, Epoch: epoch, Check: checks, Waivers: map[int]*delivery.CarriedWaiver{}}
	var bases []taskBase
	if hasResult(checks, delivery.CheckFail) {
		if bases, err = s.taskBases(ctx, r); err != nil {
			return d, err
		}
	}
	for _, b := range bases {
		a.Bases = append(a.Bases, delivery.ChecksBase{Repo: b.Repo, Branch: b.Branch, SHA: b.SHA})
	}
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		current, err := delivery.LatestTurnTx(ctx, tx, r.ID)
		if err != nil || current != turn {
			return err
		}
		candidates, err := delivery.AttemptWaiversTx(ctx, tx, r.ID, turn)
		if err != nil {
			return err
		}
		for i, c := range checks {
			if w := candidates.For(c); w != nil {
				a.Waivers[i] = w
			}
		}
		if a.TestPhase, err = deliveryTests(ctx, tx, r.TaskID); err != nil {
			return err
		}
		calls, err := recordedCalls(ctx, tx, r.ID, checks, bases)
		if err != nil {
			return err
		}
		return delivery.AskChecksTx(ctx, tx, r.ref(), a, checksWakeLine(r, a, bases, calls))
	})
	d.pending = true
	return d, err
}

// checksAskOpenTx says the gate asked the conductor about r's turn and is
// still waiting on that ask: no verdict on the turn, its deadline not
// passed, and the conductor not unavailable. askConductor would find it
// pending; checksBeforePush answers so before inspecting anything.
func checksAskOpenTx(ctx context.Context, tx pgx.Tx, r phaseRun, turn int64) (bool, error) {
	asked, overdue, err := delivery.ChecksAskedTx(ctx, tx, r.ID, turn)
	if err != nil || asked == nil || overdue {
		return false, err
	}
	verdict, err := delivery.ConductedChecksTx(ctx, tx, r.ID, turn)
	if err != nil || verdict != nil {
		return false, err
	}
	unavailable, err := delivery.ChecksConductorUnavailableTx(ctx, tx, r.TaskID)
	return !unavailable, err
}

func hasResult(checks []delivery.Check, result string) bool {
	for _, c := range checks {
		if c.Result == result {
			return true
		}
	}
	return false
}

// checksWakeEvidenceMax bounds each check's quoted evidence in the wake;
// the whole report is in the Run's Chat (run_chat).
const checksWakeEvidenceMax = 80

func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutRunes(s[:n], false) + "…"
}

// recordedCalls says, per reported pass, how the Run's latest recorded
// shell call of that command ended: "exit N", "completed" (no exit code
// recorded), "error" or "failed" (no exit code), or "none recorded"; and,
// per reported failure when the task has bases, how its latest call at one
// of them ended: a call naming a base's commit whose last step (after its
// last " && ") is the command. It is dude's own reading of the ledger, not
// the agent's words: the conductor backs a pass, or a failure as
// pre-existing, by it (or by reading the calls with run_chat).
func recordedCalls(ctx context.Context, tx pgx.Tx, runID string, checks []delivery.Check, bases []taskBase) ([]string, error) {
	out := make([]string, len(checks))
	wanted := false
	for i, c := range checks {
		if c.Result == delivery.CheckPass || c.Result == delivery.CheckFail && len(bases) > 0 {
			out[i], wanted = "none recorded", true
		}
	}
	if !wanted {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.payload->'input'->>'command', d.payload->>'status', d.payload->'exitCode'
		FROM events c JOIN events d ON d.run_id = c.run_id AND d.event_type = $3 AND d.payload->>'callId' = c.payload->>'callId'
		WHERE c.run_id = $1 AND c.event_type = $2 AND c.payload->'input' ? 'command'
		ORDER BY d.cursor`, runID, evToolCalled, evToolCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var command, status *string
		var exit *float64
		if err := rows.Scan(&command, &status, &exit); err != nil {
			return nil, err
		}
		if command == nil {
			continue
		}
		ran, atBase := ofCommand(*command), atBase(*command, bases)
		for i, c := range checks {
			switch {
			case c.Result == delivery.CheckPass && ran == oneLine(c.Command):
			case c.Result == delivery.CheckFail && atBase == oneLine(c.Command):
			default:
				continue
			}
			switch {
			case exit != nil:
				out[i] = fmt.Sprintf("exit %d", int(*exit))
			case status != nil && *status != "":
				out[i] = *status
			default:
				out[i] = "completed"
			}
		}
	}
	return out, rows.Err()
}

// atBase is the command a recorded call ran at one of the task's bases:
// its last step (after its last " && "), one-lined, when the call names a
// base's commit (its full SHA, or the first 12 characters); "" otherwise.
func atBase(command string, bases []taskBase) string {
	ran := oneLine(command)
	for _, b := range bases {
		if len(b.SHA) >= 12 && strings.Contains(ran, b.SHA[:12]) {
			if i := strings.LastIndex(ran, " && "); i >= 0 {
				return ran[i+len(" && "):]
			}
		}
	}
	return ""
}

// ofCommand is the command a recorded shell call ran, one-lined, for
// recordedCalls to compare with a reported one: the call as it was, less a
// single leading "cd DIR && " (the command run in a directory). A call that
// only contains the command (piped, ORed, echoed, grepped for, with more
// arguments) is not a call of it.
func ofCommand(command string) string {
	ran := oneLine(command)
	if rest, ok := strings.CutPrefix(ran, "cd "); ok {
		if dir, after, ok := strings.Cut(rest, " && "); ok && dir != "" && !strings.ContainsAny(dir, " ;|&") {
			return after
		}
	}
	return ran
}

// checksWakeLine is the conductor's wake: the Run, its turn, the task's
// bases and the waivers a failure may carry (first: the line is clipped,
// and a later check's question loses only its evidence), then one question
// per reported check, then whether one is missing. calls is recordedCalls
// of the report (nil: none read).
func checksWakeLine(r phaseRun, a delivery.ChecksAsked, bases []taskBase, calls []string) string {
	type question struct {
		Command, Result, Evidence, Call string
		Waiver                          bool
	}
	type waiver struct {
		N             int
		Command, Role string
		Cursor        int64
	}
	qs := make([]question, len(a.Check))
	var ws []waiver
	for i, c := range a.Check {
		// The agent's evidence is its own words: quoted as external text,
		// cut so every check's question fits the wake.
		qs[i] = question{oneLine(c.Command), c.Result, delivery.ExternalText(r.ID, clipTo(oneLine(c.Evidence), checksWakeEvidenceMax)), "", a.Waivers[i] != nil}
		if i < len(calls) {
			qs[i].Call = calls[i]
		}
		if w := a.Waivers[i]; w != nil {
			ws = append(ws, waiver{i + 1, oneLine(c.Command), w.Role, w.Cursor})
		}
	}
	return checksWake.Render(struct {
		Phase, RunID, Bases string
		Turn                int64
		Waivers             []waiver
		Checks              []question
		TestPhase           bool
	}{r.Phase, r.ID, joinBases(bases), a.Turn, ws, qs, a.TestPhase})
}

// verdictDecision is the gate's decision from the conductor's verdict, as
// the judge's was: passes backed and nothing missing verify; a failure
// passes as the waived one, pre-existing or deferred; anything else
// rejects. Each verdict cites the decision's cursor and by "conductor".
// asked may be nil for a verdict whose ask is lost; then nothing the ask
// carried (waivers, bases) passes a failure.
func verdictDecision(d checksDecision, checks []delivery.Check, asked *delivery.ChecksAsked, v *delivery.ConductedChecks) checksDecision {
	d.Verdicts = []checksVerdict{}
	verified := true
	var rejected []checksVerdict
	for i, c := range checks {
		cv := delivery.ConductedCheck{Verdict: delivery.VerdictReject}
		if i < len(v.Checks) && strings.TrimSpace(v.Checks[i].Command) == strings.TrimSpace(c.Command) {
			cv = v.Checks[i]
		}
		out := checksVerdict{Command: c.Command, Decision: cv.Verdict, Reason: cv.Reason, DecisionCursor: v.Cursor, By: "conductor"}
		var waiver *delivery.CarriedWaiver
		if asked != nil {
			waiver = asked.Waivers[i]
		}
		switch {
		case c.Result == delivery.CheckPass && cv.Verdict == delivery.VerdictBacked:
			out.Result = delivery.CheckPass
		case c.Result == delivery.CheckFail && cv.Verdict == delivery.VerdictSameAsWaived && waiver != nil && cv.WaiverCursor == waiver.Cursor:
			out.Result, out.Waiver = delivery.CheckWaived, waiver
			d.Carried = true
		case c.Result == delivery.CheckFail && cv.Verdict == delivery.VerdictPreexisting && asked != nil && len(asked.Bases) > 0:
			out.Result = delivery.CheckPreexisting
			d.ConductorPassed = true
		case c.Result == delivery.CheckNotRun && cv.Verdict == delivery.VerdictDeferred:
			out.Result = delivery.CheckDeferred
			d.ConductorPassed = true
		case c.Result == delivery.CheckPass:
			out.Result = "unverified"
			verified = false
		default:
			// The agent's own result and evidence stay, with the
			// conductor's reason beside them.
			out.Result, out.Reason = c.Result, c.Evidence
			out.JudgeReason = checksConductorSaid.Render(cmp.Or(strings.TrimSpace(cv.Reason), "no reason given"))
			verified = false
			rejected = append(rejected, out)
			continue
		}
		d.Verdicts = append(d.Verdicts, out)
	}
	missing := checksVerdict{Command: delivery.MissingChecks, Decision: v.Missing.Verdict, Reason: v.Missing.Reason,
		DecisionCursor: v.Cursor, By: "conductor", Result: delivery.CheckPass}
	if v.Missing.Verdict != delivery.MissingNone {
		missing.Result = "unverified"
		verified = false
	}
	d.Verdicts = append(append(d.Verdicts, rejected...), missing)
	if asked != nil {
		for _, b := range asked.Bases {
			d.bases = append(d.bases, taskBase{Repo: b.Repo, Branch: b.Branch, SHA: b.SHA})
		}
	}
	d.Verified = verified
	if !verified {
		d.Carried, d.ConductorPassed = false, false
	}
	return d
}
