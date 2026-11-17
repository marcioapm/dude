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

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Retrying an agent's turn that failed on a temporary model error: a rate
// limit, an overloaded or unreachable upstream, a response cut off. The
// same Run's agent, in the same session, is told to carry on once the
// back-off is over: its work and conversation stay. There is no count of
// retries: dude waits as long as the refusals last, up to the Run's
// role's no-progress limit (roleTimeLimit) of continuous refusals. Past
// it a phase Run is parked, resumable, its session and work kept, and its
// conductor told; a talker is parked as for any failed turn (parkFailed).
// A conducted phase Run's conductor is told once when a wait passes
// modelWaitNotice.

// evTurnRetried is a failed turn dude retries. Payload: {attempt, after
// (seconds), reason (the agent's error), retryAfter (true when the error
// said how long to wait), waitedSecs (how long the refusals have lasted)}.
const evTurnRetried = "run.turn.retried"

// modelWaitNotice is how long a phase Run's continuous wait on temporary
// model errors lasts before its conductor is told, once per wait.
const modelWaitNotice = 30 * time.Minute

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

// retryTurn retries a turn that failed on a temporary model error: the
// agent is told to carry on, queued as a directive held until the
// back-off is over (deliver_after), and sent the usual way then. The Run
// itself is not backed off: a pause, an abort or a park is acted on
// meanwhile. A Run lux stopped meanwhile is resumed by the usual paths (a
// talker parked by parkStopped, its directive unsent). Once the refusals
// have lasted the role's limit (roleTimeLimit) a phase Run is parked
// instead (parkForModel). Says whether it handled the failure; false
// leaves it to turnFailed (a talker past the limit: parkFailed).
func (t *translator) retryTurn(ctx context.Context, tx pgx.Tx, s *Syncer, agentErr string) (bool, error) {
	transient, retryAfter := transientTurnError(agentErr)
	if !transient {
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
	// A retried turn that went on to work (a tool call, a plan, a
	// thought after the streak's last retry) ended the wait: this refusal
	// starts a new one. Not its messages: a harness may relay the refusal
	// itself as one (Claude Code's error line).
	if t.TurnRetries > 0 {
		var worked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events e WHERE e.run_id = $1 AND e.event_type IN ($3, $4, $5)
			AND e.cursor > (SELECT COALESCE(max(l.cursor), 0) FROM events l WHERE l.run_id = $1 AND l.event_type = $2))`,
			t.run.ID, evTurnRetried, evToolCalled, evPlanUpdated, evAgentThought).Scan(&worked); err != nil {
			return false, err
		}
		if worked {
			t.TurnRetries = 0
		}
	}
	// How long the refusals have lasted: since this streak's first retry
	// (attempt 1), on the database's clock against the time the
	// orchestrator stamped that event with (their skew is seconds); and
	// the limit they may last.
	var waitedSecs, limitSecs float64
	var since int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(extract(epoch FROM now() - w.occurred_at)::float8, 0), COALESCE(w.cursor, 0), `+roleTimeLimit+`
		FROM runs r LEFT JOIN projects p ON p.id = r.project_id JOIN organizations o ON o.id = r.organization_id
		LEFT JOIN LATERAL (SELECT e.occurred_at, e.cursor FROM events e WHERE $2 AND e.run_id = r.id AND e.event_type = $3
			AND e.payload->>'attempt' = '1' ORDER BY e.cursor DESC LIMIT 1) w ON true
		WHERE r.id = $1`, t.run.ID, t.TurnRetries > 0, evTurnRetried).Scan(&waitedSecs, &since, &limitSecs); err != nil {
		return false, err
	}
	waited := time.Duration(waitedSecs * float64(time.Second))
	if waited >= time.Duration(limitSecs*float64(time.Second)) {
		if t.run.talker() {
			return false, nil
		}
		return true, t.parkForModel(ctx, tx, s, waited)
	}
	if waited >= modelWaitNotice && !t.run.talker() && t.run.TaskID != "" {
		// Once per wait: keyed by its first retry.
		if err := t.tellConductor(ctx, tx, fmt.Sprintf("model-waiting:%s:%d", t.run.ID, since), waited, false); err != nil {
			return false, err
		}
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
		"attempt": attempt, "after": wait.Round(time.Second).Seconds(), "waitedSecs": waited.Round(time.Second).Seconds(),
		"reason": red.String(agentErr), "retryAfter": retryAfter > 0})
}

// parkForModel parks a phase Run the model provider has refused for its
// role's limit, as an idle park is (dude_pause 'idle'): its lux Run
// stopped and kept, its session and work with it, resumed by a person's
// Resume or its conductor's steer, handed to the person if the conductor
// leaves it (IdleParksDueSQL). Not failed: nothing it did was wrong.
func (t *translator) parkForModel(ctx context.Context, tx pgx.Tx, s *Syncer, waited time.Duration) error {
	why := "parked: the model provider has refused for " + waitWords(waited)
	var parkedAt time.Time
	err := tx.QueryRow(ctx, `UPDATE runs r SET control = 'pause_graceful', control_requested_at = now(), control_reason = $2,
			dude_pause = 'idle', turn_done_at = NULL
		WHERE r.id = $1 AND `+delivery.Unasked+` RETURNING control_requested_at`, t.run.ID, why).Scan(&parkedAt)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	t.TurnRetries = 0
	t.LastFailed = true
	var taskStatus string
	if t.run.TaskID != "" && t.run.SourceKind == "" {
		if err := t.tellConductor(ctx, tx, "model-parked:"+t.run.ID+":"+parkedAt.UTC().Format(time.RFC3339Nano), waited, true); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, t.run.TaskID).Scan(&taskStatus); err != nil {
			return err
		}
	}
	return s.event(ctx, tx, t.run, evParked, ledger.ActorSystem,
		map[string]any{"reason": "idle", "message": why, "taskStatus": db.Nullable(taskStatus), "parkedAt": parkedAt})
}

// tellConductor says the model provider has refused the Run for waited:
// its task's conductor, when one takes it (a stalled wake, keyed key);
// parked and with none, the person, as an idle park with none does.
func (t *translator) tellConductor(ctx context.Context, tx pgx.Tx, key string, waited time.Duration, parked bool) error {
	ref := t.run.ref()
	takes, err := delivery.ConductorTakesTx(ctx, tx, t.run.TaskID)
	if err != nil {
		return err
	}
	if takes {
		_, err := delivery.RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, delivery.WakeStalled, key, delivery.MustText("wakes/model-waiting").Render(
			struct {
				Phase, RunID, For string
				Parked            bool
			}{t.run.Phase, t.run.ID, waitWords(waited), parked}))
		return err
	}
	if !parked {
		return nil
	}
	why := delivery.WhyNoConductor
	if d, err := delivery.ReadDelivery(ctx, tx, t.run.TaskID); err != nil {
		return err
	} else if d != nil && d.State.Decider == delivery.DeciderConductor {
		why = delivery.WhyHandover
	}
	_, err = delivery.ToPersonTx(ctx, tx, delivery.ToPerson{Ref: ref, Why: why,
		Reason: "the model provider has refused for " + waitWords(waited)})
	return err
}

// waitWords is a wait as a person reads it: "2h0m", "35m".
func waitWords(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
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
