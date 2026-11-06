package phases

import (
	"cmp"
	"context"
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
		if verdict, err = delivery.ConductedChecksTx(ctx, tx, r.ID, turn); err != nil || verdict != nil {
			return err
		}
		if asked, overdue, err = delivery.ChecksAskedTx(ctx, tx, r.ID, turn); err != nil {
			return err
		}
		unavailable, err = delivery.ChecksConductorUnavailableTx(ctx, tx, r.TaskID)
		return err
	})
	if err != nil {
		return d, err
	}
	if verdict != nil {
		if asked, _, err = s.checksAsked(ctx, r, turn); err != nil {
			return d, err
		}
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
		return delivery.AskChecksTx(ctx, tx, r.ref(), a, checksWakeLine(r, a, bases))
	})
	d.pending = true
	return d, err
}

// checksAsked is what the gate asked the conductor about turn.
func (s *Syncer) checksAsked(ctx context.Context, r phaseRun, turn int64) (asked *delivery.ChecksAsked, overdue bool, err error) {
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		asked, overdue, err = delivery.ChecksAskedTx(ctx, tx, r.ID, turn)
		return err
	})
	return asked, overdue, err
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

// checksWakeLine is the conductor's wake: the Run, its turn, and one
// question per reported check, then whether one is missing.
func checksWakeLine(r phaseRun, a delivery.ChecksAsked, bases []taskBase) string {
	type question struct {
		Command, Result, Evidence string
		Waiver                    *delivery.CarriedWaiver
	}
	qs := make([]question, len(a.Check))
	for i, c := range a.Check {
		// The agent's evidence is its own words: quoted as external text,
		// cut so every check's question fits the wake.
		qs[i] = question{oneLine(c.Command), c.Result, delivery.ExternalText(r.ID, clipTo(oneLine(c.Evidence), checksWakeEvidenceMax)), a.Waivers[i]}
	}
	return checksWake.Render(struct {
		Phase, RunID, Bases string
		Turn                int64
		Checks              []question
		TestPhase           bool
	}{r.Phase, r.ID, joinBases(bases), a.Turn, qs, a.TestPhase})
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
