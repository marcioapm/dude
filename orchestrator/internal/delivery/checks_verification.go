package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// checksVerificationText is the checks gate's compact account for a PR:
// the latest gated Run verdicts on this attempt, including the no-tier advice.
func checksVerificationText(ctx context.Context, tx pgx.Tx, st *State) (string, error) {
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT e.payload FROM events e JOIN runs r ON r.id=e.run_id
  WHERE r.task_id=$1 AND r.phase IN ('implement','fix','simplify') AND e.event_type=$3
   AND ($2=0 OR r.attempt=$2) ORDER BY e.cursor DESC LIMIT 1`, st.TaskID, st.Attempt, EvRunChecksJudged).Scan(&payload)
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var p struct {
		Verified bool
		Skipped  bool
		Waived   bool
		Checks   []struct{ Command, Result, Decision, Reason, CallID string } `json:"checks"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("### Checks\n")
	switch {
	case p.Skipped:
		b.WriteString("Checks not model-checked, set a judgement tier in the organisation's settings.\n\n")
	case p.Waived:
		b.WriteString("Checks were waived by a person.\n\n")
	case p.Verified:
		b.WriteString("Checks were verified against the Run's recorded commands.\n\n")
	default:
		b.WriteString("Checks were not verified; the work was sent back or awaits a person.\n\n")
	}
	for _, c := range p.Checks {
		fmt.Fprintf(&b, "- `%s` — **%s**", c.Command, valueOr(c.Result, c.Decision))
		if c.Reason != "" {
			fmt.Fprintf(&b, " — %s", oneLine(c.Reason))
		}
		if c.CallID != "" {
			fmt.Fprintf(&b, " (`%s`)", c.CallID)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
