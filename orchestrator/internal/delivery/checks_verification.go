package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// gatedJudgement is one gated Run's checks judgement: its newest that is not
// unchanged, else its newest. Unchanged: the Run's turn changed nothing it
// could push, so no report was judged.
type gatedJudgement struct {
	Heads     map[string]RunHead `json:"-"`
	Role      string
	Verified  bool
	Skipped   bool
	Waived    bool
	Unchanged bool
	Carried   bool
	// Some reported failure or not-run check passed on the judge's word:
	// CheckPreexisting or CheckDeferred.
	JudgePassed bool
	Message     string
	Checks      []gatedCheck `json:"checks"`
}

type gatedCheck struct {
	Command, Result, Decision, Reason, CallID, WaiverReason, JudgeReason string
	Waiver                                                               *CarriedWaiver
}

// status is a gated Run's outcome as its row in the pull request names it,
// from what its verdicts show. A waived, carried, judge-passed, skipped or
// unchanged Run is never called verified.
func (j gatedJudgement) status() string {
	switch {
	case j.Skipped:
		return "not model-checked"
	case j.Waived:
		return "waived"
	case j.Unchanged:
		return "no changes; checks not needed"
	case !j.Verified:
		return "not backed"
	}
	var backed, carried, judged bool
	for _, c := range j.Checks {
		switch c.Result {
		case CheckPass:
			// Missing's answer backs no reported check.
			backed = backed || c.Command != MissingChecks
		case CheckPreexisting, CheckDeferred:
			judged = true
		case CheckWaived:
			carried = true
		}
	}
	switch {
	case carried && judged:
		return "waiver carried; passed by the judge"
	case carried:
		return "waiver carried"
	case judged && backed:
		return "backed; the rest passed by the judge"
	case judged:
		return "passed by the judge"
	}
	return "backed"
}

// carriedFrom is the waiver a carried Run's first waived check rests on.
func (j gatedJudgement) carriedFrom() *CarriedWaiver {
	for _, c := range j.Checks {
		if c.Waiver != nil {
			return c.Waiver
		}
	}
	return nil
}

// gatedRuns is each gated Run of the current attempt with its newest
// judgement that is not unchanged (else its newest), oldest Run first. A
// later unchanged turn pushed nothing new, so it never displaces the Run's
// earlier judged, waived or skipped turn; a Run with only unchanged turns
// reads as unchanged.
func gatedRuns(ctx context.Context, tx pgx.Tx, st *State) ([]gatedJudgement, error) {
	rows, err := tx.Query(ctx, `SELECT phase, heads, payload FROM (
		SELECT DISTINCT ON (r.id) r.id, r.created_at, r.phase::text AS phase, r.heads, e.payload
		FROM events e JOIN runs r ON r.id=e.run_id
		WHERE r.task_id=$1 AND r.phase IN ('implement','fix','simplify') AND e.event_type=$3
		 AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1))
		ORDER BY r.id, (e.payload->>'unchanged')::boolean IS TRUE, e.cursor DESC) latest
		ORDER BY created_at, id`, st.TaskID, st.Attempt, EvRunChecksJudged)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (gatedJudgement, error) {
		var phase string
		var heads, payload []byte
		var j gatedJudgement
		if err := row.Scan(&phase, &heads, &payload); err != nil {
			return j, err
		}
		if err := json.Unmarshal(payload, &j); err != nil {
			return j, err
		}
		// Heads in another shape only lose the Run its heads, never the
		// account or the tester's prompt.
		if json.Unmarshal(heads, &j.Heads) != nil {
			j.Heads = nil
		}
		j.Role = RoleName(PromptRoleForPhase[phase])
		return j, nil
	})
}

var (
	checksCompactTmpl     = mustTemplate("notices/checks/account-compact")
	checksOmittedTmpl     = mustTemplate("notices/checks/account-omitted")
	checksCarriedTmpl     = mustTemplate("notices/checks/account-carried")
	checksCarriedFromTmpl = mustTemplate("notices/checks/account-carried-from")
	checksJudgedTmpl      = mustTemplate("notices/checks/account-judged")
)

// checksAccountText is the checks gate's compact account of runs (oldest
// first) for a PR, in at most room characters as long as its headline fits:
// the headline is the weakest of them all (a skip, else any waiver with each
// reason, else verified), then the newest Run, compactly if its full row does
// not fit, then older Runs newest-first while they fit. "" for no Runs. A
// Run that changed nothing weakens nothing and backs nothing: with only such
// Runs the headline says no checks were needed.
func checksAccountText(runs []gatedJudgement, room int) string {
	if len(runs) == 0 {
		return ""
	}
	var skipped, unverified, judgePassed bool
	var waivers []gatedJudgement
	judged := 0
	for _, j := range runs {
		switch {
		case j.Skipped:
			skipped = true
		case j.Waived, j.Carried && j.Verified:
			waivers = append(waivers, j)
		case j.Unchanged:
			continue
		case !j.Verified:
			unverified = true
		}
		judgePassed = judgePassed || j.JudgePassed && j.Verified
		judged++
	}
	newest := runs[len(runs)-1]
	compact := render(checksCompactTmpl, map[string]any{"Role": newest.Role, "Status": newest.status(), "Count": len(newest.Checks)}) + "\n"
	var b strings.Builder
	b.WriteString("### Checks\n")
	switch {
	case skipped:
		b.WriteString("Checks not model-checked, set a judgement tier in the organisation's settings.\n")
	case len(waivers) > 0:
		b.WriteString("Checks were waived by a person:\n")
		// Each reason clipped, as the test gate's are; the whole one is in
		// the ledger. The newest waivers that fit beside a compact newest Run.
		shown := make([]string, len(waivers))
		for i, j := range waivers {
			if w := j.carriedFrom(); !j.Waived && w != nil {
				shown[i] = render(checksCarriedTmpl, map[string]any{"Role": j.Role, "From": w.Role, "Note": checksField(w.Note)}) + "\n"
				continue
			}
			shown[i] = fmt.Sprintf("- %s: %s\n", j.Role, checksField(j.Message))
		}
		from := newestThatFit(shown, min(checksWaiversMax, room-checksFixedMax-utf8.RuneCountInString(compact)))
		writeOmitted(&b, from, "waiver")
		for _, line := range shown[from:] {
			b.WriteString(line)
		}
	case unverified:
		b.WriteString("Checks were not verified; the work was sent back or awaits a person.\n")
	case judged == 0:
		b.WriteString("No gated Run changed anything; no checks were needed.\n")
	case judgePassed:
		b.WriteString(render(checksJudgedTmpl, nil) + "\n")
	default:
		b.WriteString("Checks were verified against the Run's recorded commands.\n")
	}
	b.WriteByte('\n')
	rowsRoom := min(checksRowsMax, room-utf8.RuneCountInString(b.String())-checksOmittedMax)
	lines := make([]string, len(runs))
	for i, j := range runs {
		lines[i] = checksRow(j)
	}
	// The newest Run decided publishing, so it is always shown: compactly
	// when its full row would not fit. With no room even for that, the
	// headline stands alone and says the account is omitted.
	last := len(lines) - 1
	if utf8.RuneCountInString(lines[last]) > rowsRoom {
		lines[last] = compact
	}
	if utf8.RuneCountInString(lines[last]) > rowsRoom {
		b.WriteString(render(checksOmittedTmpl, nil))
		return b.String()
	}
	from := newestThatFit(lines[:last], rowsRoom-utf8.RuneCountInString(lines[last]))
	writeOmitted(&b, from, "Run")
	for _, row := range lines[from:] {
		b.WriteString(row)
	}
	return strings.TrimRight(b.String(), "\n")
}

// The checks account takes at most checksWaiversMax for the waivers' lines,
// checksRowsMax for the Runs' rows, and checksFixedMax for its heading,
// headline sentence and the two omitted lines (checksOmittedMax each). A
// pull request gives it less when the rest of its body leaves less
// (verificationParts.fit). Each field is clipped to checksFieldMax before
// it is put in a code span.
const (
	checksWaiversMax = 8_000
	checksRowsMax    = 40_000
	checksFixedMax   = 200
	checksOmittedMax = 30
	checksFieldMax   = 300
)

// newestThatFit is where the newest of lines that fit max characters
// together start; it stops at the first that does not fit.
func newestThatFit(lines []string, max int) int {
	from, size := len(lines), 0
	for from > 0 && size+utf8.RuneCountInString(lines[from-1]) <= max {
		from--
		size += utf8.RuneCountInString(lines[from])
	}
	return from
}

// writeOmitted says how many earlier things, if any, were left out.
func writeOmitted(b *strings.Builder, n int, thing string) {
	switch n {
	case 0:
	case 1:
		fmt.Fprintf(b, "1 earlier %s omitted.\n", thing)
	default:
		fmt.Fprintf(b, "%d earlier %ss omitted.\n", n, thing)
	}
}

// checksRow is a gated Run's line and one line per check.
func checksRow(j gatedJudgement) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **%s** — %s\n", j.Role, j.status())
	for _, c := range j.Checks {
		fmt.Fprintf(&b, "  - %s — **%s**", codeSpan(checksField(c.Command)), checksField(valueOr(c.Result, c.Decision)))
		if c.Reason != "" {
			fmt.Fprintf(&b, " — %s", checksField(c.Reason))
		}
		if c.WaiverReason != "" {
			fmt.Fprintf(&b, " — %s", checksField(c.WaiverReason))
		}
		if c.JudgeReason != "" {
			fmt.Fprintf(&b, " — %s", checksField(c.JudgeReason))
		}
		if c.Waiver != nil {
			fmt.Fprintf(&b, " — %s", render(checksCarriedFromTmpl, map[string]any{"Role": c.Waiver.Role, "Run": codeSpan(checksField(c.Waiver.RunID)),
				"By": checksField(valueOr(c.Waiver.PersonName, "a person"))}))
		}
		if c.CallID != "" {
			fmt.Fprintf(&b, " (%s)", codeSpan(checksField(c.CallID)))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// checksField is a recorded value as one line of at most checksFieldMax.
func checksField(s string) string { return clip(oneLine(s), checksFieldMax) }

// codeSpan is s as a CommonMark code span: fenced by one backtick more than
// its longest run of them, and padded with a space where s starts or ends
// with one, so nothing in s closes it early. s never starts or ends with a
// space (it is a checksField), so CommonMark's padding strip loses nothing.
func codeSpan(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

var (
	writerChecksTmpl      = mustTemplate("contracts/writer-checks")
	writerCarriedFromTmpl = mustTemplate("notices/checks/writer-carried-from")
)

// writerChecksMax bounds the tester's account of the writers' checks; the
// newest Runs that fit are kept, as the pull request's account keeps them.
const writerChecksMax = 12_000

// writerCheck is one gated Run as the tester reads it: its role, the
// commits it published (repository@short sha), its outcome and its checks,
// each field one clipped line.
type writerCheck struct {
	Role, Heads, Status, Message string
	Checks                       []writerCheckLine
}

// writerCheckLine is one check as the tester reads it. Why: why it was not
// passed, a person's waiver not covering it or the judge not passing it.
type writerCheckLine struct{ Command, Result, Reason, Why, Waiver string }

// WriterChecksTx is what a tester of the task's attempt is told about the
// checks its writers reported (contracts/writer-checks), from the same
// judgements the pull request's account reads (gatedRuns): "" with none.
func WriterChecksTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) (string, error) {
	runs, err := gatedRuns(ctx, tx, &State{TaskID: taskID, Attempt: attempt})
	if err != nil || len(runs) == 0 {
		return "", err
	}
	return writerChecksText(runs, writerChecksMax), nil
}

// writerChecksText renders runs (oldest first) in at most room characters,
// the newest Runs that fit; with none fitting, the newest alone without its
// checks.
func writerChecksText(runs []gatedJudgement, room int) string {
	items := make([]writerCheck, len(runs))
	for i, j := range runs {
		var heads []string
		for _, repo := range slices.Sorted(maps.Keys(j.Heads)) {
			heads = append(heads, repo+"@"+short(j.Heads[repo].SHA))
		}
		c := writerCheck{Role: j.Role, Heads: strings.Join(heads, ", "), Status: j.status()}
		if j.Waived {
			c.Message = checksField(j.Message)
		}
		for _, v := range j.Checks {
			waiver := ""
			if w := v.Waiver; w != nil {
				waiver = checksField(render(writerCarriedFromTmpl, map[string]any{"By": valueOr(w.PersonName, "a person"),
					"Role": w.Role, "Run": w.RunID, "Turn": w.Turn, "Note": w.Note}))
			}
			c.Checks = append(c.Checks, writerCheckLine{checksField(v.Command),
				checksField(valueOr(v.Result, v.Decision)), checksField(v.Reason), checksField(strings.Join(slices.DeleteFunc([]string{v.WaiverReason, v.JudgeReason}, func(s string) bool { return s == "" }), " — ")), waiver})
		}
		items[i] = c
	}
	for from := range items {
		if text := render(writerChecksTmpl, items[from:]); utf8.RuneCountInString(text) <= room {
			return text
		}
	}
	last := items[len(items)-1]
	last.Checks = nil
	return render(writerChecksTmpl, []writerCheck{last})
}
