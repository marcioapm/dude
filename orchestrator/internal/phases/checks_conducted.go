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
// non-pass is rejected (only a person passes it, after the rounds), with
// no task bases, so its send-back offers no way to pass it, and a report of passes only passes with the skip notice. Never
// does a non-pass pass without a decision.
func noConductor(d checksDecision, reason, skip delivery.Text) checksDecision {
	if len(d.Verdicts) > 0 {
		d.Verdicts = append([]checksVerdict{}, d.Verdicts...)
		for i := range d.Verdicts {
			d.Verdicts[i].JudgeReason = reason.Render(nil)
		}
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
//
// The ask shows the conductor the Run's evidence (delivery.ChecksEvidence):
// its commits as read at this turn (read, nil when they could not be read)
// and the shell calls it and its subagents started after the latest of
// them was made. dude does not decide whether a call proves a check.
func (s *Syncer) askConductor(ctx context.Context, r phaseRun, d checksDecision, checks []delivery.Check, read *checkoutRead) (checksDecision, error) {
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
		// left is not read here: judgeChecks found the delivery conducted
		// just before (conductedDelivery); only the pending shortcut
		// (checksAskOpenTx) yields on it.
		asked, verdict, overdue, unavailable, _, err = checksAskTx(ctx, tx, r, turn)
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
		a.Evidence = &delivery.ChecksEvidence{}
		if read != nil {
			if a.Evidence, err = delivery.EvidenceTx(ctx, tx, r.ID, read.Checkouts, read.ReadStart); err != nil {
				return err
			}
		}
		return delivery.AskChecksTx(ctx, tx, r.ref(), a, checksWakeLine(r, a, bases))
	})
	d.pending = true
	return d, err
}

// checksAskTx reads where the gate's ask about r's turn stands: what was
// asked (nil for nothing), the conductor's verdict on the turn (nil for
// none), whether the ask's deadline passed, and, with no verdict, whether
// the delivery left its conductor (no longer Conducted: decided by the
// policy since) and whether the conductor is unavailable.
func checksAskTx(ctx context.Context, tx pgx.Tx, r phaseRun, turn int64) (asked *delivery.ChecksAsked, verdict *delivery.ConductedChecks, overdue, unavailable, left bool, err error) {
	if asked, overdue, err = delivery.ChecksAskedTx(ctx, tx, r.ID, turn); err != nil {
		return
	}
	if verdict, err = delivery.ConductedChecksTx(ctx, tx, r.ID, turn); err != nil || verdict != nil {
		return
	}
	d, err := delivery.ReadDelivery(ctx, tx, r.TaskID)
	if err != nil {
		return
	}
	left = !d.Conducted()
	unavailable, err = delivery.ChecksConductorUnavailableTx(ctx, tx, r.TaskID)
	return
}

// checksAskOpenTx says the gate asked the conductor about r's turn and is
// still waiting on that ask: no verdict on the turn, its deadline not
// passed, the conductor not unavailable, and the delivery still its
// conductor's to decide (not left to the policy since). askConductor would
// find it pending; checksBeforePush answers so before inspecting anything.
func checksAskOpenTx(ctx context.Context, tx pgx.Tx, r phaseRun, turn int64) (bool, error) {
	asked, verdict, overdue, unavailable, left, err := checksAskTx(ctx, tx, r, turn)
	return err == nil && asked != nil && verdict == nil && !overdue && !left && !unavailable, err
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

// checksWakeLine is the conductor's wake: the Run, its turn, the task's
// bases and the waivers a failure may carry (first: the line is clipped,
// and a later check's question loses only its evidence), then one question
// per reported check, then whether one is missing, then the evidence every
// pass is decided by (a.Evidence): the Run's commits at the report, whether
// its tree was clean, and its calls at the latest commit, newest first.
// The calls come last, so a clipped wake loses the oldest call first;
// run_calls lists them all.
func checksWakeLine(r phaseRun, a delivery.ChecksAsked, bases []taskBase) string {
	type question struct {
		Command, Result, Evidence string
		Waiver                    bool
	}
	type waiver struct {
		N             int
		Command, Role string
		Cursor        int64
	}
	type call struct {
		N                    int
		Command, Exit, Child string
		Tail                 string
	}
	qs := make([]question, len(a.Check))
	var ws []waiver
	for i, c := range a.Check {
		// The agent's evidence is its own words: quoted as external text,
		// cut so every check's question fits the wake. Its command is its
		// own words too, shown %q-quoted: defanged, so it cannot write a
		// marker and forge a call line below; and clipped as a call's is,
		// so the questions cannot take the wake's whole line from the calls.
		qs[i] = question{delivery.Defanged(clipTo(oneLine(c.Command), checksWakeCommandMax)), c.Result, delivery.ExternalText(r.ID, clipTo(oneLine(c.Evidence), checksWakeEvidenceMax)), a.Waivers[i] != nil}
		if w := a.Waivers[i]; w != nil {
			// Clipped, as evidence is: the waivers come first, and a long
			// command must not push a later waiver's cursor out of the
			// clipped line. Check N names the check below.
			ws = append(ws, waiver{i + 1, delivery.Defanged(clipTo(oneLine(c.Command), checksWakeEvidenceMax)), w.Role, w.Cursor})
		}
	}
	var heads string
	var clean, read, more bool
	var calls []call
	if e := a.Evidence; e != nil && e.Read {
		read, clean, more = true, e.Clean, e.More
		heads = delivery.Checkouts{Heads: e.Heads}.Headline()
		for i, c := range e.Calls {
			// A call's command and output are the agent's and its tools':
			// quoted as external text.
			out := call{N: i + 1, Command: delivery.ExternalText(r.ID, clipTo(oneLine(c.Command), checksWakeCommandMax)), Exit: c.ExitWords(), Child: c.Child}
			if c.Tail != "" {
				out.Tail = delivery.ExternalText(r.ID, c.Tail)
			}
			calls = append(calls, out)
		}
	}
	type wake struct {
		Phase, RunID, Bases string
		Turn                int64
		Waivers             []waiver
		Checks              []question
		TestPhase           bool
		Evidence            bool
		Read, Clean, More   bool
		Heads               string
		Calls               []call
		Max, Dropped        int
	}
	data := wake{r.Phase, r.ID, joinBases(bases), a.Turn, ws, qs, a.TestPhase, a.Evidence != nil, read, clean, more, heads, calls, delivery.EvidenceCalls, 0}
	// The calls are bounded twice: by count (EvidenceCalls, read so), and
	// by the wake's line: the oldest go first until it fits, and the wake
	// says how many went, so a clipped line never cuts one mid-way.
	line := checksWake.Render(data)
	for len([]rune(line)) > delivery.ChecksLineChars && len(data.Calls) > 0 {
		data.Calls = data.Calls[:len(data.Calls)-1]
		data.Dropped++
		line = checksWake.Render(data)
	}
	return line
}

// checksWakeCommandMax bounds each call's command in the wake: the call's
// whole command is in the Run's Chat and in run_calls.
const checksWakeCommandMax = 120

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
