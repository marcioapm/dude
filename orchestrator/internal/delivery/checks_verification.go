package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// gatedJudgement is one gated Run's latest checks judgement.
type gatedJudgement struct {
	Role     string
	Verified bool
	Skipped  bool
	Waived   bool
	Message  string
	Checks   []struct{ Command, Result, Decision, Reason, CallID string } `json:"checks"`
}

// status is a gated Run's outcome as its row in the pull request names it.
// A waived or skipped Run is never called verified.
func (j gatedJudgement) status() string {
	switch {
	case j.Skipped:
		return "not model-checked"
	case j.Waived:
		return "waived"
	case j.Verified:
		return "backed"
	}
	return "not backed"
}

// gatedRuns is each gated Run of the current attempt with its latest
// judgement, oldest Run first.
func gatedRuns(ctx context.Context, tx pgx.Tx, st *State) ([]gatedJudgement, error) {
	rows, err := tx.Query(ctx, `SELECT phase, payload FROM (
		SELECT DISTINCT ON (r.id) r.id, r.created_at, r.phase::text AS phase, e.payload
		FROM events e JOIN runs r ON r.id=e.run_id
		WHERE r.task_id=$1 AND r.phase IN ('implement','fix','simplify') AND e.event_type=$3
		 AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1)) ORDER BY r.id, e.cursor DESC) latest
		ORDER BY created_at, id`, st.TaskID, st.Attempt, EvRunChecksJudged)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (gatedJudgement, error) {
		var phase string
		var payload []byte
		var j gatedJudgement
		if err := row.Scan(&phase, &payload); err != nil {
			return j, err
		}
		err := json.Unmarshal(payload, &j)
		j.Role = RoleName(PromptRoleForPhase[phase])
		return j, err
	})
}

var (
	checksCompactTmpl = mustTemplate("notices/checks/account-compact")
	checksOmittedTmpl = mustTemplate("notices/checks/account-omitted")
)

// checksAccountText is the checks gate's compact account of runs (oldest
// first) for a PR, in at most room characters as long as its headline fits:
// the headline is the weakest of them all (a skip, else any waiver with each
// reason, else verified), then the newest Run, compactly if its full row does
// not fit, then older Runs newest-first while they fit. "" for no Runs.
func checksAccountText(runs []gatedJudgement, room int) string {
	if len(runs) == 0 {
		return ""
	}
	var skipped, unverified bool
	var waivers []gatedJudgement
	for _, j := range runs {
		switch {
		case j.Skipped:
			skipped = true
		case j.Waived:
			waivers = append(waivers, j)
		case !j.Verified:
			unverified = true
		}
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
			shown[i] = fmt.Sprintf("- %s: %s\n", j.Role, checksField(j.Message))
		}
		from := newestThatFit(shown, min(checksWaiversMax, room-checksFixedMax-utf8.RuneCountInString(compact)))
		writeOmitted(&b, from, "waiver")
		for _, line := range shown[from:] {
			b.WriteString(line)
		}
	case unverified:
		b.WriteString("Checks were not verified; the work was sent back or awaits a person.\n")
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
