package phases

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

var (
	checksWake           = delivery.MustText("wakes/checks")
	checksNoConductor    = delivery.MustText("notices/checks/no-conductor")
	checksUnconducted    = delivery.MustText("notices/checks/unconducted")
	checksUnconductedRej = delivery.MustText("notices/checks/unconducted-reject")
	checksConductorSaid  = delivery.MustText("notices/checks/conductor-rejected")
	checksUnbacked       = delivery.MustText("notices/checks/unbacked")
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
		calls, closest, err := recordedCalls(ctx, tx, r.ID, checks, bases)
		if err != nil {
			return err
		}
		a.Closest = closest
		return delivery.AskChecksTx(ctx, tx, r.ref(), a, checksWakeLine(r, a, bases, calls))
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

// recordedCalls says, per reported pass, how the Run's latest recorded
// shell call of that command (sameRead) ended: "exit N", "completed" (no
// exit code recorded), "error" or "failed" (no exit code), or "none
// recorded"; and, per reported failure when the task has bases, how its
// latest call at one of them (atBase) ended, as baseReading says it: "exit
// N" only when the exit is the command's own. It is dude's own reading of
// the ledger, not the agent's words: the conductor backs a pass, or a
// failure as pre-existing, by it (or by reading the calls with run_chat).
//
// The calls read are the Run's own shell calls and its subagents' (a Task
// or spawned agent's agent.child.tool.* calls), under the same rules.
// closest names, per reported pass no call matched, up to
// closestCallsMax of the recorded calls nearest it (closestCalls), each
// with how it ended, so the agent can report one exactly; only calls whose
// exit status may be their command's are named (a call that backs nothing
// is no call to report), and all of them together within
// closestCallsTotal bytes, so they never crowd out the wake's questions.
// A pass whose nearest calls did not fit has a nil entry (elided), one
// with no call near it an empty one.
func recordedCalls(ctx context.Context, tx pgx.Tx, runID string, checks []delivery.Check, bases []taskBase) (out []string, closest map[int][]string, err error) {
	cr := newCallReader(checks, bases)
	if !cr.wanted {
		return cr.out, nil, nil
	}
	// The output is read only to attribute an exit 1 at the base
	// (baseReading), so only with a base: it would make the sort spill;
	// last is the end of its stdout (or merged output), where an echoed
	// exit status is (echoedExit).
	rows, err := tx.Query(ctx, `SELECT c.payload->'input'->>'command', d.payload->>'status', d.payload->'exitCode',
			CASE WHEN $6 THEN concat_ws(E'\n', d.payload->'output'->>'head', d.payload->'output'->>'tail', d.payload->'stdout'->>'head',
				d.payload->'stdout'->>'tail', d.payload->'stderr'->>'head', d.payload->'stderr'->>'tail') ELSE '' END,
			coalesce(d.payload->'output'->>'tail', d.payload->'output'->>'head', d.payload->'stdout'->>'tail', d.payload->'stdout'->>'head', '')
		FROM events c JOIN events d ON d.run_id = c.run_id AND d.payload->>'callId' = c.payload->>'callId'
			AND d.event_type = CASE c.event_type WHEN $2 THEN $3 ELSE $5 END
			AND d.payload->>'childId' IS NOT DISTINCT FROM c.payload->>'childId'
		WHERE c.run_id = $1 AND c.event_type IN ($2, $4) AND c.payload->'input' ? 'command'
		ORDER BY d.cursor`, runID, evToolCalled, evToolCompleted, evChildToolCalled, evChildToolCompleted, cr.baseFails)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var command, status *string
		var exit *float64
		var output, last string
		if err := rows.Scan(&command, &status, &exit, &output, &last); err != nil {
			return nil, nil, err
		}
		if command != nil {
			cr.read(*command, status, exit, output, last)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return cr.out, cr.closest(), nil
}

// callReader is recordedCalls' reading of a report against the recorded
// calls, one call at a time, in the order they completed. Each report's
// command is read once (wants), each call once (shellLex, parseCall).
type callReader struct {
	checks    []delivery.Check
	wants     []shellCall
	bases     []taskBase
	wanted    bool // a check is read at all
	baseFails bool // a failure is read at a base
	out       []string
	matched   []bool
	// seen: the distinct calls that may back a pass, by command (index),
	// each with its latest reading and when (at).
	seen  []readCall
	index map[string]int
	n     int
}

func newCallReader(checks []delivery.Check, bases []taskBase) *callReader {
	cr := &callReader{checks: checks, bases: bases, out: make([]string, len(checks)), matched: make([]bool, len(checks)),
		wants: make([]shellCall, len(checks)), index: map[string]int{}}
	for i, c := range checks {
		pass, fail := c.Result == delivery.CheckPass, c.Result == delivery.CheckFail && len(bases) > 0
		if pass || fail {
			cr.out[i], cr.wanted, cr.wants[i] = "none recorded", true, shellOf(c.Command, true)
		}
		cr.baseFails = cr.baseFails || fail
	}
	return cr
}

// read reads one recorded call: its command, the status and exit its
// completion recorded, its output and the end of it (last).
func (cr *callReader) read(command string, status *string, exit *float64, output, last string) {
	cr.n++
	toks, why := shellLex(strings.Trim(command, " \t\n"))
	call := parseCall(toks, why, false)
	// Rule E: an echoed status is read only from a call that completed or
	// exited 0. A call that ended in error, was killed or timed out with
	// no exit code never ran its echo, and a non-zero exit is not echo's.
	// Under set -e a failing command exits the shell before its echo,
	// so a call that completed with no exit code proves nothing there;
	// with exit 0 recorded every step ran, the echo too. Under pipefail
	// a non-zero status is some stage's.
	reading := "completed"
	switch {
	case call.echoed && exit == nil && call.set == "-euo" && status != nil && *status == "completed":
		reading = "unclear: under set -e its echo may not have run"
	case call.echoed && (exit != nil && *exit == 0 || exit == nil && status != nil && *status == "completed"):
		reading = echoedExit(last)
	case exit != nil:
		reading = fmt.Sprintf("exit %d", int(*exit))
	case status != nil && *status != "":
		reading = *status
	case call.echoed:
		reading = "unclear: no exit or status recorded, so its echo may not have run"
	}
	if call.piped && strings.HasPrefix(reading, "exit ") && reading != "exit 0" {
		reading = "its pipe failed (" + reading + "), not proof of which command failed"
	}
	if call.why == "" {
		// Only a call that may back a pass is offered as one to report
		// (closestCalls), once, with its latest reading.
		key := strings.TrimSpace(command)
		if j, ok := cr.index[key]; ok {
			cr.seen[j].reading, cr.seen[j].at = reading, cr.n
		} else {
			cr.index[key] = len(cr.seen)
			cr.seen = append(cr.seen, readCall{command: key, reading: reading, at: cr.n, words: callWords(call)})
		}
	}
	var steps [][]string
	if cr.baseFails {
		steps = baseSteps(toks, why)
	}
	for i, c := range cr.checks {
		switch {
		case c.Result == delivery.CheckPass:
			if sameRead(cr.wants[i], call) {
				cr.out[i], cr.matched[i] = reading, true
			}
		case c.Result == delivery.CheckFail && steps != nil:
			if base := atBase(steps, cr.wants[i], cr.bases); base != nil {
				if exit != nil {
					cr.out[i] = base.reading(int(*exit), output)
				} else {
					cr.out[i] = "completed"
					if status != nil && *status != "" {
						cr.out[i] = *status
					}
				}
			}
		}
	}
}

// closest is recordedCalls' closest of the calls read.
func (cr *callReader) closest() map[int][]string {
	var closest map[int][]string
	latest := slices.Clone(cr.seen)
	slices.SortFunc(latest, func(a, b readCall) int { return cmp.Compare(b.at, a.at) })
	budget := closestCallsTotal
	for i, c := range cr.checks {
		if c.Result != delivery.CheckPass || cr.matched[i] {
			continue
		}
		if closest == nil {
			closest = map[int][]string{}
		}
		near := closestCalls(callWords(cr.wants[i]), latest)
		closest[i] = []string{} // none near
		for _, n := range near {
			if budget -= len(n); budget < 0 {
				break
			}
			closest[i] = append(closest[i], n)
		}
		if len(near) > 0 && len(closest[i]) == 0 {
			closest[i] = nil // elided for space
		}
	}
	return closest
}

// unbackedHint is dude's word on an unbacked pass no recorded call
// matched (asked says closest has an entry for it only then): the
// recorded calls nearest it, so the agent can report one exactly, or that
// they were omitted for space (closest nil). "" for a pass a call matched,
// whose recorded exit the wake already named.
func unbackedHint(command string, closest []string, asked bool) string {
	if !asked {
		return ""
	}
	return checksUnbacked.Render(struct {
		Command string
		Closest []string
		Elided  bool
	}{clipTo(command, closestCallMax), closest, closest == nil})
}

// readCall is a recorded shell call that may back a pass, how it last
// ended, when (the how-manyth call read), and its words as closestCalls
// compares them, read once.
type readCall struct {
	command, reading string
	at               int
	words            map[string]bool
}

// callWords are a call's command words (no environment, no plumbing),
// as closestCalls compares them.
func callWords(c shellCall) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(c.cmd) {
		m[w] = true
	}
	return m
}

// closestCallsMax bounds the recorded calls an unmatched pass names,
// closestCallMax each one's length, and closestCallsTotal all of a
// report's together.
const (
	closestCallsMax   = 3
	closestCallMax    = 160
	closestCallsTotal = 600
)

// closestCalls are the recorded calls (distinct, latest first) nearest a
// reported command's words (callWords) no call matched, nearest first: by
// the share of the command's words they have in common (any word in
// common, the latest of equals first), each quoted as recorded with how it
// ended.
func closestCalls(want map[string]bool, calls []readCall) []string {
	type near struct {
		line  string
		score float64
	}
	var nears []near
	for _, c := range calls {
		common := 0
		for w := range c.words {
			if want[w] {
				common++
			}
		}
		if common > 0 {
			nears = append(nears, near{fmt.Sprintf("%q (%s)", clipTo(c.command, closestCallMax), c.reading),
				float64(common) / float64(len(want)+len(c.words)-common)})
		}
	}
	slices.SortStableFunc(nears, func(a, b near) int { return cmp.Compare(b.score, a.score) })
	out := []string{}
	for _, n := range nears[:min(len(nears), closestCallsMax)] {
		out = append(out, n.line)
	}
	return out
}

// plainDir is a cd's argument dude reads as a directory: one word, nothing
// the shell would expand or chain, no option ("-" goes back to the
// previous directory, "-P" and "-L" to the home one), and no ".."
// component, nor a "." one but a bare ".". The word is compared as
// written: bash's cd resolves "x/../api" lexically, to ./api, but dash's
// (and bash's under set -P) physically, through x when it is a symlink,
// so where it lands is not the word's to say.
func plainDir(dir string) bool {
	if dir == "" || strings.HasPrefix(dir, "-") || strings.ContainsAny(dir, " ;|&<>$`'\"\\*?~(){}#!") {
		return false
	}
	if dir == "." {
		return true
	}
	for part := range strings.SplitSeq(dir, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return true
}

// baseDir is a directory a call at the base names (a worktree it adds, a
// cd into it): leading ".." components, as a worktree beside the Run's
// checkout is named, then a plainDir (or nothing). atBase follows the
// path under it itself.
func baseDir(dir string) bool {
	rest := dir
	for rest == ".." || strings.HasPrefix(rest, "../") {
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, ".."), "/")
	}
	return rest == "" && dir != "" || rest != "" && plainDir(rest)
}

// sameCommand says a recorded shell call ran the reported command, with an
// exit status that is the command's own; the call read (shellOf) says
// where that status is. It is sameRead of both, each read by shellOf.
func sameCommand(reported, recorded string) (shellCall, bool) {
	got := shellOf(recorded, false)
	return got, sameRead(shellOf(reported, true), got)
}

// sameRead says the recorded call got, as shellOf read it, ran the
// reported command want with an exit status that is the command's own.
//
// dude reads an allowlist grammar (shellCall) and a call it does not read
// backs nothing; nor does a report it does not read. What both run must be
// the same words, quoted text as written: a call that only contains the
// command (echoed, grepped for, with more, fewer or other arguments or
// steps) is not a call of it.
//   - The directory: a report naming one ("cd api && make test", or "make
//     test (api)") is backed only by a call run there; one naming none, by
//     a call run in any.
//   - The environment: both must make the same assignments in the same
//     order. "env A=1 cmd" and "A=1 cmd" are the same only when no value
//     expands anything (expands): env's values are expanded before any is
//     assigned, and dash expands "~" in an assignment, not in env's
//     argument.
//   - A report setting -u (set -euo pipefail) with an expansion is backed
//     only by a call that set it too: unset, the expansion fails there.
func sameRead(want, got shellCall) bool {
	return got.why == "" && want.why == "" && want.cmd != "" && want.cmd == got.cmd &&
		(want.dir == "" || want.dir == got.dir) && sameEnv(want, got) &&
		(want.set != "-euo" || got.set == "-euo" || !strings.Contains(want.simple, "$"))
}

// sameEnv says two reads make the same assignments (sameRead).
func sameEnv(want, got shellCall) bool {
	if want.viaEnv == got.viaEnv && slices.Equal(want.pre, got.pre) && slices.Equal(want.env, got.env) {
		return true
	}
	return !expands(want) && !expands(got) &&
		slices.Equal(slices.Concat(want.pre, want.env), slices.Concat(got.pre, got.env))
}

// expands says an assignment of c's expands something, or may: "$", "`",
// "~" or a glob.
func expands(c shellCall) bool {
	return slices.ContainsFunc(slices.Concat(c.pre, c.env), func(a string) bool { return strings.ContainsAny(a, "$`~*?[") })
}

// shellCall is a shell command as dude reads it: a call backs a pass only
// when it is, exactly, one of the shapes of this grammar, and a report
// must read under it too, without PIPE and ECHO:
//
//	CALL     := [PIPEFAIL SEP] [CD "&&"] SIMPLE [PIPE] [ECHO]
//	PIPEFAIL := "set -o pipefail" | "set -euo pipefail"   (the call's first step)
//	SEP      := "&&" | ";"
//	CD       := "cd" DIR                                   (plainDir)
//	SIMPLE   := ASSIGN* ["env" ASSIGN+] WORD+ REDIR*
//	PIPE     := ("|" WORD+)+                               (only after PIPEFAIL)
//	ECHO     := ";" "echo" "exit=$?"                       (last)
//	REDIR    := (">" | ">>" | "2>" | "2>>") WORD | "2>&1" | "1>&2"
//
// shellLex says what a WORD is. Spaces and tabs separate words; nothing
// else does. A call spanning lines is not read: a newline separates
// commands.
//
// Why each shape backs what it does: a cd that fails, or a pipefail set,
// stops the && after it; a set that is the call's first step runs, or the
// shell exits (a special builtin); redirects leave the exit the command's;
// under pipefail exit 0 is every stage's, the command's too, while a
// non-zero exit is some stage's, not proof of which; and the echo prints
// the command's (or the pipe's) exit status as the call's last line, read
// only from a call that completed or exited 0 (echoedExit). Nothing else
// may run between the set and the command, so nothing can turn pipefail
// off, change directory or the environment unseen.
//
// Every shape means the same in bash and in POSIX sh (dash, the agent's
// /bin/sh on layered images): bash's "&>" is not one, as dash reads it as
// a background job.
type shellCall struct {
	dir string // a leading cd's (or a report's annotation's)
	// set is the leading pipefail set's option word ("-o", "-euo"; "" for
	// none).
	set string
	// pre: assignments before the command (or before env), in order; env:
	// env's, viaEnv: the command is run by env.
	pre, env []string
	viaEnv   bool
	cmd      string // the command's words, quoted as written, redirects dropped
	simple   string // its assignments, env and words
	echoed   bool   // a trailing "; echo exit=$?"
	piped    bool   // a pipe after the command (under pipefail)
	// why the call's exit status is not the command's, or dude does not
	// read it ("" when it is the command's).
	why string
}

// readAnnotation splits a report's trailing "(DIR)", "make test (api)",
// into the command and the directory, the word as written (plainDir); ok
// false with none. Whether the command has a cd of its own too is
// shellOf's to say, on the lexed command.
func readAnnotation(s string) (cmd, dir string, ok bool) {
	if !strings.HasSuffix(s, ")") {
		return "", "", false
	}
	open := strings.LastIndexByte(s, '(')
	if open < 1 || (s[open-1] != ' ' && s[open-1] != '\t') {
		return "", "", false
	}
	dir = s[open+1 : len(s)-1]
	cmd = strings.TrimRight(s[:open], " \t")
	if cmd == "" || !plainDir(dir) {
		return "", "", false
	}
	return cmd, dir, true
}

// shellOf reads command under shellCall's grammar; reported reads a
// report's: with its directory annotation, without PIPE or ECHO. Only
// leading and trailing spaces, tabs and newlines are trimmed: nothing
// inside is collapsed before it is read. A report that names a directory
// both ways ("cd api && make test (web)", however spaced, after a leading
// set or not) is not read: it names two.
func shellOf(command string, reported bool) shellCall {
	s := strings.Trim(command, " \t\n")
	annotation, annotated := "", false
	if reported {
		if cmd, dir, ok := readAnnotation(s); ok {
			s, annotation, annotated = cmd, dir, true
		}
	}
	toks, why := shellLex(s)
	c := parseCall(toks, why, reported)
	switch {
	case !annotated || c.why != "":
	case c.dir != "" || slices.ContainsFunc(toks, func(t shTok) bool { return plainWord(t, "cd") }):
		c.why = "it names a directory twice, with a cd and an annotation"
	default:
		c.dir = annotation
	}
	return c
}

// isWord: t is a word (not an operator, not the echo's exit=$?).
func isWord(t shTok) bool { return !t.op && !t.status }

// plainWord: t is the unquoted word w.
func plainWord(t shTok, w string) bool { return isWord(t) && !t.quoted && t.text == w }

// parseCall parses a lexed call (shellLex's tokens, and why it refused it)
// under shellCall's grammar.
func parseCall(toks []shTok, why string, reported bool) shellCall {
	var c shellCall
	if c.why = why; why != "" {
		return c
	}
	i := 0
	if len(toks) >= 4 && plainWord(toks[0], "set") && (plainWord(toks[1], "-o") || plainWord(toks[1], "-euo")) &&
		plainWord(toks[2], "pipefail") && toks[3].op && (toks[3].text == "&&" || toks[3].text == ";") {
		c.set, i = toks[1].text, 4
	}
	if len(toks) >= i+3 && plainWord(toks[i], "cd") && isWord(toks[i+1]) && plainDir(toks[i+1].text) &&
		toks[i+2].op && toks[i+2].text == "&&" {
		c.dir, i = toks[i+1].text, i+3
	}
	end := len(toks)
	if n := end; n-i >= 4 && toks[n-3].op && toks[n-3].text == ";" && plainWord(toks[n-2], "echo") && toks[n-1].status {
		c.echoed, end = true, n-3
	}
	// SIMPLE
	start := i
	for i < end && isWord(toks[i]) && assignment.MatchString(toks[i].text) {
		c.pre, i = append(c.pre, toks[i].text), i+1
	}
	if i+1 < end && plainWord(toks[i], "env") && isWord(toks[i+1]) && assignment.MatchString(toks[i+1].text) {
		c.viaEnv, i = true, i+1
		for i < end && isWord(toks[i]) && assignment.MatchString(toks[i].text) {
			c.env, i = append(c.env, toks[i].text), i+1
		}
	}
	var words []string
	for i < end && isWord(toks[i]) {
		words, i = append(words, toks[i].text), i+1
	}
	simple := make([]string, 0, i-start)
	for _, t := range toks[start:i] {
		simple = append(simple, t.text)
	}
	c.cmd, c.simple = strings.Join(words, " "), strings.Join(simple, " ")
	if len(words) == 0 {
		c.why = "it runs no command dude reads"
		return c
	}
	for i < end && toks[i].redirect {
		if toks[i].target {
			if i+1 >= end || !isWord(toks[i+1]) {
				c.why = "dude cannot read where it redirects its output"
				return c
			}
			i++
		}
		i++
	}
	// PIPE
	for i < end && toks[i].op && toks[i].text == "|" {
		i++
		if i >= end || !isWord(toks[i]) {
			c.why = "dude cannot read its pipe"
			return c
		}
		for i < end && isWord(toks[i]) {
			i++
		}
		c.piped = true
	}
	switch {
	case i < end:
		c.why = "it has a step, operator or word dude does not read there"
	case c.piped && c.set == "" && !reported:
		c.why = "its exit status is the last command's in its pipe (no leading set -o pipefail)"
	}
	return c
}

// echoedExit is a call's exit as it echoed it (shellCall's ECHO): the
// last non-empty line of its recorded output is exit= and a number.
func echoedExit(last string) string {
	lines := strings.Split(strings.TrimRight(last, " \t\r\n"), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if n, ok := strings.CutPrefix(line, "exit="); ok && n != "" && strings.Trim(n, "0123456789") == "" {
		if exit, err := strconv.Atoi(n); err == nil {
			return fmt.Sprintf("exit %d", exit)
		}
	}
	return "unclear: its echoed exit status is not the end of its recorded output"
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shTok is a shell word, quoted as written, or an operator: a control
// operator (&&, ;, |) or an output redirect (>, 2>>, 2>&1, …).
type shTok struct {
	text string
	op   bool
	// quoted: the word has quoting or an escape in it.
	quoted bool
	// status: the word is exactly exit=$?, the echo's.
	status bool
	// redirect: the operator is an output redirect; target: one to the
	// next word.
	redirect, target bool
}

// redirects are the output redirects dude reads, and whether each takes a
// target word.
var redirects = map[string]bool{">": true, ">>": true, "2>": true, "2>>": true, "2>&1": false, "1>&2": false}

// shellLex splits a one-line command into words and operators, or says
// why dude does not read it. A word is plain characters, a backslash and
// the character it escapes, and single- or double-quoted strings, none
// holding $(, a backtick, $', $", <( or >(; "$" outside single quotes
// only as $NAME or ${NAME}, and $? only in the echo's exit=$?. Outside
// quotes, any #, newline, carriage return, &, ;, |, (, ), {, }, <, > or !
// other than in the operators shellCall reads makes the call unread.
func shellLex(s string) ([]shTok, string) {
	const (
		quoting   = "dude cannot read its quoting"
		expansion = "it expands or substitutes something dude does not read"
	)
	if strings.ContainsAny(s, "\n\r") {
		return nil, "it spans lines, which dude does not read"
	}
	var toks []shTok
	var b strings.Builder
	quoted, status := false, false
	flush := func() {
		if b.Len() > 0 {
			toks = append(toks, shTok{text: b.String(), quoted: quoted, status: status})
		}
		b.Reset()
		quoted, status = false, false
	}
	op := func(text string) {
		flush()
		toks = append(toks, shTok{text: text, op: true})
	}
	// dollar reads a "$" at s[i] (outside single quotes): the length of
	// $NAME or ${NAME}, 0 for anything else.
	dollar := func(s string, i int) int {
		j := i + 1
		brace := j < len(s) && s[j] == '{'
		if brace {
			j++
		}
		k := j
		for k < len(s) && (s[k] == '_' || 'a' <= s[k] && s[k] <= 'z' || 'A' <= s[k] && s[k] <= 'Z' || k > j && '0' <= s[k] && s[k] <= '9') {
			k++
		}
		switch {
		case k == j:
			return 0
		case brace && (k >= len(s) || s[k] != '}'):
			return 0
		case brace:
			k++
		}
		return k - i
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t':
			flush()
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, quoting
			}
			in := s[i+1 : i+1+j]
			for _, bad := range []string{"$(", "`", "$'", `$"`, "<(", ">("} {
				if strings.Contains(in, bad) {
					return nil, expansion
				}
			}
			b.WriteString(s[i : i+2+j])
			quoted, i = true, i+1+j
		case '"':
			end := i + 1
			for ; end < len(s) && s[end] != '"'; end++ {
				if s[end] == '\\' {
					end++
				}
			}
			if end >= len(s) {
				return nil, quoting
			}
			for _, bad := range []string{"$(", "`", "$'", `$"`, "<(", ">("} {
				if strings.Contains(s[i:end+1], bad) {
					return nil, expansion
				}
			}
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				switch s[j] {
				case '\\':
					j++
				case '$':
					n := dollar(s, j)
					if n == 0 {
						return nil, expansion
					}
					j += n - 1
				}
			}
			if j >= len(s) {
				return nil, quoting
			}
			b.WriteString(s[i : j+1])
			quoted, i = true, j
		case '\\':
			if i+1 >= len(s) {
				return nil, quoting
			}
			b.WriteString(s[i : i+2])
			quoted, i = true, i+1
		case '$':
			if n := dollar(s, i); n > 0 {
				b.WriteString(s[i : i+n])
				i += n - 1
				continue
			}
			// The echo's exit=$?, a word of its own.
			if b.String() == "exit=" && !quoted && strings.HasPrefix(s[i:], "$?") && (i+2 == len(s) || s[i+2] == ' ' || s[i+2] == '\t') {
				b.WriteString("$?")
				status, i = true, i+1
				continue
			}
			return nil, expansion
		case '>':
			// An fd before it ("2>") is part of the redirect.
			fd := ""
			if w := b.String(); w != "" && !quoted && strings.Trim(w, "0123456789") == "" {
				fd = w
				b.Reset()
			}
			r := fd + ">"
			if i+1 < len(s) && s[i+1] == '>' {
				r, i = r+">", i+1
			}
			if i+1 < len(s) && s[i+1] == '&' {
				if i+2 >= len(s) || i+3 < len(s) && !strings.ContainsRune(" \t;|&", rune(s[i+3])) {
					return nil, "it redirects in a way dude does not read"
				}
				r, i = r+s[i+1:i+3], i+2
			}
			target, ok := redirects[r]
			if !ok {
				return nil, "it redirects in a way dude does not read"
			}
			flush()
			toks = append(toks, shTok{text: r, op: true, redirect: true, target: target})
		case '&':
			// "&>" and "&>>" are bash's redirects of both streams, but
			// POSIX sh (dash, /bin/sh on layered images) reads "cmd &>
			// f" as "cmd &" then "> f": a background job, refused alike.
			if i+1 >= len(s) || s[i+1] != '&' {
				return nil, "it runs something in the background, whose exit is not waited for"
			}
			op("&&")
			i++
		case '|':
			if i+1 < len(s) && (s[i+1] == '|' || s[i+1] == '&') {
				return nil, "its exit status is a later step's"
			}
			op("|")
		case ';':
			if i+1 < len(s) && s[i+1] == ';' {
				return nil, "dude does not read ;;"
			}
			op(";")
		case '#':
			return nil, "it has a comment, which dude does not read"
		case '`', '(', ')', '{', '}', '<', '!':
			return nil, "it has a " + string(c) + ", which dude does not read"
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return toks, ""
}

// baseCall is a recorded call that ran a reported command at a task's
// base (atBase).
type baseCall struct {
	// others: the call has a step other than checking the base out and
	// entering a directory before the command, whose exit may be the
	// call's.
	others bool
}

// baseSteps reads a recorded call (shellLex's tokens, and why it refused
// it) as atBase does: steps of words joined by "&&", and nothing else (no
// redirect, pipe, ";" or echo). nil when it is not so.
func baseSteps(toks []shTok, why string) [][]string {
	if why != "" {
		return nil
	}
	steps := [][]string{nil}
	for _, t := range toks {
		last := &steps[len(steps)-1]
		switch {
		case isWord(t):
			*last = append(*last, t.text)
		case t.op && t.text == "&&" && len(*last) > 0:
			steps = append(steps, nil)
		default:
			return nil
		}
	}
	if len(steps[len(steps)-1]) == 0 {
		return nil
	}
	return steps
}

// atBase reads a recorded call, as baseSteps read it, as the reported
// command (want, shellOf's reading of the report) run at one of the
// task's bases, nil when it is not one. It is one when:
//   - a step checks a base's commit out by its SHA (the full one, or a
//     prefix of 12 or more): "git worktree add [options] DIR SHA",
//     "git checkout [options] SHA" or "git switch --detach [options] SHA";
//   - its last step is the reported command (its assignments and words),
//     without its directory, and the report has no set, pipe or echo;
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
// (git diff SHA && make test, run at the head) is not at the base. One
// grammar judges the ledger: a call shellLex does not read (over lines,
// with a comment, a pipe, a background job, ANSI-C quoting) is not at the
// base, nor is a report shellOf does not read.
func atBase(steps [][]string, want shellCall, bases []taskBase) *baseCall {
	if want.why != "" || want.set != "" || want.piped || want.echoed || path.IsAbs(want.dir) || len(steps) < 2 ||
		strings.Join(steps[len(steps)-1], " ") != want.simple {
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
		if step[0] != "cd" {
			call.others = true
			continue
		}
		if len(step) != 2 || !baseDir(step[1]) {
			return nil
		}
		dir := path.Clean(step[1])
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
	if want.dir != "" && path.Clean(want.dir) != sub {
		return nil
	}
	return call
}

// checksOutBase says a step's words check out one of bases by its SHA,
// and the directory of the worktree it adds ("" for a checkout in place).
func checksOutBase(f []string, bases []taskBase) (worktree string, ok bool) {
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
	case sub == "worktree" && len(args) == 2 && baseDir(args[0]):
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
// directory"), dash ("sh: 1: cd: can't cd to api") or zsh ("zsh:cd:1: no
// such file or directory: api"), any of dash's own ("sh: 1: "), and git's
// fatal lines and the error lines of a checkout or switch (a checkout
// mid-merge prints "f: needs merge" to stdout before its error).
var stepFailing = regexp.MustCompile(`(?m)(^|: )cd: |can't cd|zsh:cd:\d+: |^sh: \d+: |^fatal: |^error: (pathspec|Your local changes|you need to resolve|unknown switch)`)

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
		Command, Result, Evidence, Call, Closest string
		Waiver                                   bool
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
		qs[i] = question{oneLine(c.Command), c.Result, delivery.ExternalText(r.ID, clipTo(oneLine(c.Evidence), checksWakeEvidenceMax)), "", "", a.Waivers[i] != nil}
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
	render := func() string {
		return checksWake.Render(struct {
			Phase, RunID, Bases string
			Turn                int64
			Waivers             []waiver
			Checks              []question
			TestPhase           bool
		}{r.Phase, r.ID, joinBases(bases), a.Turn, ws, qs, a.TestPhase})
	}
	// The nearest calls are a convenience: each check's are named only
	// while the whole line still fits the wake, so they never cut a later
	// check's question off.
	line := render()
	for i := range qs {
		if len(a.Closest[i]) == 0 {
			continue
		}
		qs[i].Closest = closestLine(r.ID, a.Closest[i])
		if with := render(); utf8.RuneCountInString(delivery.FlatLine(with)) <= delivery.ChecksLineChars {
			line = with
		} else {
			qs[i].Closest = ""
		}
	}
	return line
}

// closestLine is a pass's nearest calls in the wake. Each names a
// recorded command, the agent's text (%q-quoted, so its control
// characters, line breaks too, are escaped and its spaces kept as
// recorded), so each is quoted as external text whole, with dude's
// reading of it inside the quote.
func closestLine(runID string, closest []string) string {
	parts := make([]string, len(closest))
	for i, n := range closest {
		parts[i] = delivery.ExternalText(runID, n)
	}
	return strings.Join(parts, "; ")
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
			if asked != nil {
				closest, ok := asked.Closest[i]
				out.JudgeReason = unbackedHint(c.Command, closest, ok)
			}
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
