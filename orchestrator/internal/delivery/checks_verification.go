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

// checksVerificationText is the checks gate's compact account for a PR:
// each gated Run of the current attempt with its latest judgement, oldest Run
// first, the earliest left out past checksRowsMax. The headline is the weakest
// of them all: a skip, else any waiver with each reason, else verified.
func checksVerificationText(ctx context.Context, tx pgx.Tx, st *State) (string, error) {
	rows, err := tx.Query(ctx, `SELECT phase, payload FROM (
		SELECT DISTINCT ON (r.id) r.id, r.created_at, r.phase::text AS phase, e.payload
		FROM events e JOIN runs r ON r.id=e.run_id
		WHERE r.task_id=$1 AND r.phase IN ('implement','fix','simplify') AND e.event_type=$3
		 AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1)) ORDER BY r.id, e.cursor DESC) latest
		ORDER BY created_at, id`, st.TaskID, st.Attempt, EvRunChecksJudged)
	if err != nil {
		return "", err
	}
	var runs []gatedJudgement
	for rows.Next() {
		var phase string
		var payload []byte
		if err := rows.Scan(&phase, &payload); err != nil {
			rows.Close()
			return "", err
		}
		var j gatedJudgement
		if err := json.Unmarshal(payload, &j); err != nil {
			rows.Close()
			return "", err
		}
		role := RoleForPhase[phase]
		if phase == PhaseFix {
			role = "fixer"
		}
		j.Role = RoleName(role)
		runs = append(runs, j)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", nil
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
	var b strings.Builder
	b.WriteString("### Checks\n")
	switch {
	case skipped:
		b.WriteString("Checks not model-checked, set a judgement tier in the organisation's settings.\n")
	case len(waivers) > 0:
		b.WriteString("Checks were waived by a person:\n")
		// Each reason clipped, as the test gate's are; the whole one is in
		// the ledger. The newest waivers that fit checksWaiversMax.
		shown := make([]string, len(waivers))
		for i, j := range waivers {
			shown[i] = fmt.Sprintf("- %s: %s\n", j.Role, clip(oneLine(j.Message), checksFieldMax))
		}
		from := newestThatFit(shown, checksWaiversMax)
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
	lines := make([]string, len(runs))
	for i, j := range runs {
		lines[i] = checksRow(j)
	}
	from := newestThatFit(lines, checksRowsMax)
	writeOmitted(&b, from, "Run")
	for _, row := range lines[from:] {
		b.WriteString(row)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// A pull request body holds at most 65,536 characters. The checks account
// takes at most checksWaiversMax for the waivers' lines, checksRowsMax for
// the Runs' rows, and under 200 for its heading, headline sentence and the
// two omitted lines: 48,200 in all, leaving over 17,000 for the markers,
// the test gate's section and the task's description. Each command and
// reason is clipped to checksFieldMax, so one Run's row (at most
// checksCountMax+1 checks) fits.
const (
	checksWaiversMax = 8_000
	checksRowsMax    = 40_000
	checksFieldMax   = 300
)

// newestThatFit is where the newest of lines that fit max characters
// together start; the newest is always kept.
func newestThatFit(lines []string, max int) int {
	from, size := len(lines), 0
	for from > 0 && (from == len(lines) || size+utf8.RuneCountInString(lines[from-1]) <= max) {
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
		fmt.Fprintf(&b, "  - `%s` — **%s**", clip(c.Command, checksFieldMax), valueOr(c.Result, c.Decision))
		if c.Reason != "" {
			fmt.Fprintf(&b, " — %s", clip(oneLine(c.Reason), checksFieldMax))
		}
		if c.CallID != "" {
			fmt.Fprintf(&b, " (`%s`)", c.CallID)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
