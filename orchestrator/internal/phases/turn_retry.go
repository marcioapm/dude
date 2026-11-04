package phases

import (
	"context"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Retrying an agent's turn that failed on a temporary model error: a rate
// limit, an overloaded or unreachable upstream, a response cut off. The
// LLM proxy's blips last minutes; failing the Run for one escalates to a
// person what waiting would have fixed. The same Run's agent, in the same
// session, is told to carry on once the back-off is over: its work and
// conversation stay. maxTurnRetries in a row, then the failure is the
// Run's as any other (turnFailed), its reason naming the retries.

// evTurnRetried is a failed turn dude retries. Payload: {attempt, after
// (seconds), reason (the agent's error), retryAfter (true when the error
// said how long to wait)}.
const evTurnRetried = "run.turn.retried"

// maxTurnRetries bounds the retries of one turn: the fifth temporary
// failure in a row is the Run's.
const maxTurnRetries = 4

// Back-off between retries when the error says nothing of how long to
// wait: turnRetryBase doubling, up to turnRetryMax, with jitter, so the
// Runs a proxy-wide blip failed together do not retry together.
const (
	turnRetryBase = 15 * time.Second
	turnRetryMax  = 5 * time.Minute
	// A retry_after longer than this is waited out only this long: the
	// next failure says again how long.
	turnRetryAfterMax = 15 * time.Minute
)

var turnRetryNudge = delivery.MustText("notices/turn-retry").Render(nil)

// What a harness says of a temporary model failure, as each relays it:
//
//   - Claude Code's result line: "API Error: 529 overloaded",
//     "API Error: Request rejected (429) · {...\"retry_after\":143}",
//     "API Error: Connection closed mid-response".
//   - Codex's error notification (willRetry false, after its own retries):
//     "unexpected status 503 Service Unavailable", "stream disconnected
//     before completion: ...".
//   - OpenCode through lux's ACP adapter: "session/prompt: <message>
//     (<code>)", the provider's message, e.g. "rate limited (429)".
var (
	// An HTTP status of a temporary failure where an error names one: after
	// "API Error", "status" or "HTTP", or in parentheses.
	transientStatus = regexp.MustCompile(`(?i)(?:api error:?\s*(?:request rejected\s*)?\(?|\bstatus:?\s*|\bhttp/?[\d.]*\s*|\()(429|502|503|504|529)\b`)
	// The same failures said in words.
	transientWords = regexp.MustCompile(`(?i)rate[ _-]?limit|too many requests|overloaded|service unavailable|bad gateway|` +
		`gateway time-?out|connection closed mid-response|stream disconnected|stream (?:was )?reset|econnreset|connection reset`)
	// How long the error asks to be waited out: retry_after in a JSON body
	// (quoted, or escaped once more), or a Retry-After header.
	retryAfterSecs = regexp.MustCompile(`(?i)retry[_-]after\\?"?\s*[:=]?\s*"?(\d+(?:\.\d+)?)`)
)

// transientTurnError says whether a turn's error is a temporary model
// failure worth retrying, and how long it asks to be waited out (0 when it
// does not say).
func transientTurnError(agentErr string) (bool, time.Duration) {
	if !transientStatus.MatchString(agentErr) && !transientWords.MatchString(agentErr) {
		return false, 0
	}
	var after time.Duration
	if m := retryAfterSecs.FindStringSubmatch(agentErr); m != nil {
		if secs, err := strconv.ParseFloat(m[1], 64); err == nil && secs > 0 {
			after = min(time.Duration(secs*float64(time.Second)), turnRetryAfterMax)
		}
	}
	return true, after
}

// turnRetryWait is how long before retry number attempt (1-based): what
// the error asked, never less; otherwise turnRetryBase doubling, up to
// turnRetryMax, jittered by ±20 % (jitter in [0, 1)).
func turnRetryWait(attempt int, retryAfter time.Duration, jitter float64) time.Duration {
	if retryAfter > 0 {
		// Never sooner than asked; spread by up to a tenth after.
		return retryAfter + time.Duration(float64(retryAfter)*0.1*jitter)
	}
	wait := min(turnRetryBase<<min(attempt-1, 10), turnRetryMax)
	return min(time.Duration(float64(wait)*(0.8+0.4*jitter)), turnRetryMax)
}

// retryTurn retries a turn that failed on a temporary model error, when
// it is not past maxTurnRetries: the agent is told to carry on, queued as
// a directive held until the back-off is over (deliver_after), and sent
// the usual way then. The Run itself is not backed off: a pause, an abort
// or a park is acted on meanwhile. A Run lux stopped meanwhile is resumed
// by the usual paths (a talker parked by parkStopped, its directive
// unsent). Says whether it retried; false leaves the failure to
// turnFailed.
func (t *translator) retryTurn(ctx context.Context, tx pgx.Tx, s *Syncer, agentErr string) (bool, error) {
	transient, retryAfter := transientTurnError(agentErr)
	if !transient || t.TurnRetries >= maxTurnRetries {
		return false, nil
	}
	var live bool
	// Locked: a Stop or pause committing meanwhile (StopPhaseRunsTx fails
	// what is unread) is seen here, not raced by a retry queued after it.
	if err := tx.QueryRow(ctx, `SELECT status IN ('scheduled', 'starting', 'running') AND lux_stop_reason IS NULL
		AND control = 'none' FROM runs WHERE id = $1 FOR UPDATE`, t.run.ID).Scan(&live); err != nil || !live {
		// A Run dude is stopping, pausing or has ended is not retried: what
		// stops it decides.
		return false, err
	}
	attempt := t.TurnRetries + 1
	wait := turnRetryWait(attempt, retryAfter, rand.Float64())
	id, _, err := delivery.QueueDirective(ctx, tx, t.run.ref(), delivery.Directive{Text: turnRetryNudge, Scope: "turn"})
	if err != nil {
		return false, err
	}
	// Only the retry waits out the back-off (deliver_after, HeldBack): the
	// Run's own back-off (next_attempt_at) would hold a person's pause, a
	// child's abort and every other sweep action as long. RetryAhead, for
	// tests, takes it as due that much earlier, as it does next_attempt_at.
	if _, err := tx.Exec(ctx, `UPDATE directives SET deliver_after = now() + make_interval(secs => $2) WHERE id = $1`,
		id, (wait - s.RetryAhead).Seconds()); err != nil {
		return false, err
	}
	red, err := ledger.Redactor(ctx, tx, t.run.ID)
	if err != nil {
		return false, err
	}
	t.TurnRetries = attempt
	// The turn is over, not done: its idle must not complete the Run.
	t.LastFailed = true
	return true, s.event(ctx, tx, t.run, evTurnRetried, ledger.ActorSystem, map[string]any{
		"attempt": attempt, "maxAttempts": maxTurnRetries, "after": wait.Round(time.Second).Seconds(),
		"reason": red.String(agentErr), "retryAfter": retryAfter > 0})
}

// retriedReason names the retries a failure came after, if any.
func retriedReason(reason string, retries int) string {
	if retries == 0 {
		return reason
	}
	times := "times"
	if retries == 1 {
		times = "time"
	}
	return fmt.Sprintf("%s (after dude retried a temporary model error %d %s)", strings.TrimSpace(reason), retries, times)
}
