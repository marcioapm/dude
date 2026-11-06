package phases

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"regexp"
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
// passed, the conductor not unavailable, and the delivery still its
// conductor's to decide (not left to the policy since). askConductor would
// find it pending; checksBeforePush answers so before inspecting anything.
func checksAskOpenTx(ctx context.Context, tx pgx.Tx, r phaseRun, turn int64) (bool, error) {
	asked, overdue, err := delivery.ChecksAskedTx(ctx, tx, r.ID, turn)
	if err != nil || asked == nil || overdue {
		return false, err
	}
	verdict, err := delivery.ConductedChecksTx(ctx, tx, r.ID, turn)
	if err != nil || verdict != nil {
		return false, err
	}
	if d, err := delivery.ReadDelivery(ctx, tx, r.TaskID); err != nil || !d.Conducted() {
		return false, err
	}
	unavailable, err := delivery.ChecksConductorUnavailableTx(ctx, tx, r.TaskID)
	return !unavailable, err
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

// recordedCalls says, per reported pass, how the Run's latest recorded
// shell call of that command (sameCommand) ended: "exit N", "completed" (no
// exit code recorded), "error" or "failed" (no exit code), or "none
// recorded"; and, per reported failure when the task has bases, how its
// latest call at one of them (atBase) ended, as baseReading says it: "exit
// N" only when the exit is the command's own. It is dude's own reading of
// the ledger, not the agent's words: the conductor backs a pass, or a
// failure as pre-existing, by it (or by reading the calls with run_chat).
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
	// The output is read only to attribute an exit 1 at the base
	// (baseReading).
	rows, err := tx.Query(ctx, `SELECT c.payload->'input'->>'command', d.payload->>'status', d.payload->'exitCode',
			concat_ws(E'\n', d.payload->'output'->>'head', d.payload->'output'->>'tail', d.payload->'stdout'->>'head',
				d.payload->'stdout'->>'tail', d.payload->'stderr'->>'head', d.payload->'stderr'->>'tail')
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
		var output string
		if err := rows.Scan(&command, &status, &exit, &output); err != nil {
			return nil, err
		}
		if command == nil {
			continue
		}
		for i, c := range checks {
			var base *baseCall
			switch {
			case c.Result == delivery.CheckPass && sameCommand(c.Command, *command):
			case c.Result == delivery.CheckFail && len(bases) > 0:
				if base = atBase(*command, c.Command, bases); base == nil {
					continue
				}
			default:
				continue
			}
			switch {
			case exit != nil && base != nil:
				out[i] = base.reading(int(*exit), output)
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

// inDir splits a command, one-lined, into the directory a single leading
// "cd DIR && " runs it in ("" with none) and the rest.
func inDir(command string) (dir, rest string) {
	ran := oneLine(command)
	if after, ok := strings.CutPrefix(ran, "cd "); ok {
		if dir, rest, ok := strings.Cut(after, " && "); ok && plainDir(dir) {
			return path.Clean(dir), rest
		}
	}
	return "", ran
}

// plainDir is a cd's argument dude reads as a directory: one word, nothing
// the shell would expand or chain.
func plainDir(dir string) bool {
	return dir != "" && !strings.ContainsAny(dir, " ;|&<>$`'\"\\*?~(){}")
}

// sameCommand says a recorded shell call ran the reported command: both
// one-lined and read the same way (inDir), the same command, in the
// reported directory when the report names one (any, or none, when it
// does not). So "cd api && make test" is a call of "make test" and of
// itself, not of "cd web && make test". A call that only contains the
// command (piped, ORed, echoed, grepped for, with more arguments or steps)
// is not a call of it.
func sameCommand(reported, recorded string) bool {
	wantDir, want := inDir(reported)
	dir, ran := inDir(recorded)
	return ran == want && (wantDir == "" || wantDir == dir)
}

// baseCall is a recorded call that ran a reported command at a task's
// base (atBase).
type baseCall struct {
	// others: the call has a step other than checking the base out and
	// entering a directory before the command, whose exit may be the
	// call's.
	others bool
}

// atBase reads a recorded call as the reported command run at one of the
// task's bases, nil when it is not one. The call, one-lined, is steps
// joined by " && " (a step with any other operator is read as one word
// and matches nothing), and is one when:
//   - a step checks a base's commit out by its SHA (the full one, or a
//     prefix of 12 or more): "git worktree add [options] DIR SHA",
//     "git checkout [options] SHA" or "git switch --detach [options] SHA";
//   - its last step is the reported command, without its "cd DIR && ";
//   - the directory it runs in is inside that checkout: after a worktree
//     add, a later "cd" enters DIR (or a directory under it), and further
//     relative "cd"s go down from there; after a checkout in place, any
//     later "cd"s are relative and go down. No "cd" may leave it again.
//   - when the report names a directory ("cd api && make test"), it is the
//     one the command ran in, relative to the checkout's root:
//     "cd ../task-base/api && make test" after adding ../task-base is a
//     call of it, "cd ../task-base/web && make test" is not. The worktree's
//     own path differs from the Run's checkout by design, so it is the path
//     under it that is compared. A report naming an absolute directory
//     matches no call at the base.
//
// Only a SHA in such a step counts: a call that merely names the commit
// (git diff SHA && make test, run at the head) is not at the base.
func atBase(command, reported string, bases []taskBase) *baseCall {
	wantDir, want := inDir(reported)
	if path.IsAbs(wantDir) {
		return nil
	}
	steps := strings.Split(oneLine(command), " && ")
	if len(steps) < 2 || steps[len(steps)-1] != want {
		return nil
	}
	call := &baseCall{}
	// at is the checkout's directory relative to the shell's ("" before
	// a step checked a base out); sub the command's under it.
	at, sub, checkedOut := "", ".", false
	for _, step := range steps[:len(steps)-1] {
		if wt, ok := checksOutBase(step, bases); ok {
			// A second checkout, or one inside another, is not read.
			if checkedOut {
				return nil
			}
			checkedOut = true
			if wt == "" {
				at = "."
			} else {
				at = "\x00" + wt // not entered yet
			}
			continue
		}
		dir, ok := strings.CutPrefix(step, "cd ")
		if !ok {
			call.others = true
			continue
		}
		if !plainDir(dir) {
			return nil
		}
		dir = path.Clean(dir)
		switch {
		case !checkedOut:
			// A cd before the checkout: into the clone it checks out.
		case strings.HasPrefix(at, "\x00"):
			wt := path.Clean(at[1:])
			if dir != wt && !strings.HasPrefix(dir, wt+"/") {
				return nil
			}
			at, sub = wt, path.Clean("./"+strings.TrimPrefix(strings.TrimPrefix(dir, wt), "/"))
		default:
			if path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") {
				return nil
			}
			sub = path.Join(sub, dir)
		}
	}
	if !checkedOut || strings.HasPrefix(at, "\x00") {
		return nil
	}
	if wantDir != "" && path.Clean(wantDir) != sub {
		return nil
	}
	return call
}

// checksOutBase says step checks out one of bases by its SHA, and the
// directory of the worktree it adds ("" for a checkout in place).
func checksOutBase(step string, bases []taskBase) (worktree string, ok bool) {
	f := strings.Fields(step)
	if len(f) < 3 || f[0] != "git" {
		return "", false
	}
	var args []string
	detached := false
	sub := f[1]
	rest := f[2:]
	if sub == "worktree" {
		if f[2] != "add" {
			return "", false
		}
		rest = f[3:]
	}
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; {
		case a == "-b" || a == "-B" || a == "--reason":
			i++
		case a == "--detach" || a == "-d" && sub == "switch":
			detached = true
		case strings.HasPrefix(a, "-"):
		default:
			args = append(args, a)
		}
	}
	var sha string
	switch {
	case sub == "worktree" && len(args) == 2 && plainDir(args[0]):
		worktree, sha = args[0], args[1]
	case sub == "checkout" && len(args) == 1:
		sha = args[0]
	case sub == "switch" && detached && len(args) == 1:
		sha = args[0]
	default:
		return "", false
	}
	sha = strings.ToLower(sha)
	for _, b := range bases {
		if len(sha) >= 12 && strings.HasPrefix(strings.ToLower(b.SHA), sha) {
			return worktree, true
		}
	}
	return "", false
}

// stepFailing matches a line a step before the command prints when it
// fails: a cd's in bash ("bash: line 1: cd: api: No such file or
// directory") or dash ("sh: 1: cd: can't cd to api"), any of dash's own
// ("sh: 1: "), and git's fatal lines and the error lines of a checkout or
// switch.
var stepFailing = regexp.MustCompile(`(?m)(^|: )cd: |can't cd|^sh: \d+: |^fatal: |^error: (pathspec|Your local changes|unknown switch)`)

// stepFailed says output shows a step before the command failing: a line
// stepFailing matches, or an "error: " line first, before the command
// printed anything. A test's "error: expected 2, got 3" later in the
// output is the command's.
func stepFailed(output string) bool {
	if stepFailing.MatchString(output) {
		return true
	}
	for line := range strings.Lines(output) {
		if line = strings.TrimSpace(line); line != "" {
			return strings.HasPrefix(line, "error: ")
		}
	}
	return false
}

// baseReading: reading is how a base call that exited exit ran, for the conductor:
// "exit N" only when the exit is the command's own. A call ends with the
// exit of the first step that fails, so:
//   - 0: every step, the command too, succeeded: "exit 0";
//   - 126 and up (not executable, not found, git's fatal 128, or a signal):
//     the command did not run, or was stopped; never a failure at the base;
//   - with a step other than checking out and entering a directory before
//     the command (others), any other non-zero exit may be that step's:
//     unclear;
//   - 1 to 125 is the command's only when the call's recorded output is
//     there and shows no step before it failing (stepFailed); unclear
//     otherwise. A cd fails with 1 in bash but 2 in dash (/bin/sh on the
//     runtime image), git checkout and switch with 1, so no exit in the
//     range is the command's alone.
//
// Only "exit N" is dude's word that the command failed at the base.
func (b *baseCall) reading(exit int, output string) string {
	unclear := fmt.Sprintf("unclear: exit %d, which a step before it may have returned", exit)
	switch {
	case exit == 0:
		return "exit 0"
	case exit >= 126 || exit < 0:
		return fmt.Sprintf("did not run or was stopped: exit %d", exit)
	case b.others:
		return unclear
	case strings.TrimSpace(output) == "" || stepFailed(output):
		return unclear
	}
	return fmt.Sprintf("exit %d", exit)
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
			// Clipped, as evidence is: the waivers come first, and a long
			// command must not push a later waiver's cursor out of the
			// clipped line. Check N names the check in full below.
			ws = append(ws, waiver{i + 1, clipTo(oneLine(c.Command), checksWakeEvidenceMax), w.Role, w.Cursor})
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
