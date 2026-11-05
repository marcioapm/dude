package orchestrator_test

// A turn that fails on a temporary model error — a rate limit, an
// overloaded upstream, a stream cut off — is waited out and retried in the
// same Run, any role's: no escalation for a blip. Past the bound the
// failure is the Run's; any other failure is not retried. A
// quiet conductor retrying keeps its decision from defaulting at its
// deadline. Through the real syncer, workflow and API, the fake lux
// speaking each harness's protocol.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// The temporary failures as the harnesses report them (internal/phases
// turn_retry.go).
const (
	rateLimited = `API Error: Request rejected (429) · {"error":"All provider keys are rate limited","retry_after":143}`
	cutOff      = "API Error: Connection closed mid-response"
	overloaded  = "API Error: 529 overloaded"
	acpReset    = "session/prompt: Internal error: read ECONNRESET (-32603)"
	acpLimited  = "session/prompt: rate limited (429)"
	codexDown   = "unexpected status 503 Service Unavailable"
	codexStream = "stream disconnected before completion: error decoding response body"
	badGateway  = "API Error: 502 Bad Gateway"
	gwTimeout   = "API Error: 504 Gateway Timeout"
)

// implementerFailing has the implementer's turns fail as the behaviour
// built by fail says, and every other phase find nothing.
func (w *world) implementerFailing(fail func(*fakelux.Behaviour)) {
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			b := fakelux.Behaviour{Commit: map[string]string{"hello.go": "package hello\n"}, Message: "work", Reply: "Done."}
			fail(&b)
			return b
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
}

// implementer is the task's implementer Run.
func (w *world) implementer(task string) string {
	return w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' ORDER BY created_at LIMIT 1`, task)
}

func (w *world) turnRetries(run string) int {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.turn.retried'`, run)
}

// Each shape of temporary failure, on each harness, is retried — the wait
// a 429 asks for honoured — and the implementer then finishes its work:
// no run.failed, no escalation, its work published.
func TestATransientTurnFailureIsRetriedAndTheRunGoesOn(t *testing.T) {
	for harness, errs := range map[string][]string{
		"opencode":    {acpLimited, acpReset, badGateway, gwTimeout},
		"claude-code": {rateLimited, cutOff, overloaded, codexDown},
		"codex":       {codexStream, codexDown, rateLimited, cutOff},
	} {
		t.Run(harness, func(t *testing.T) {
			w := newWorld(t)
			if harness != "opencode" {
				w.setHarness("implementer", harness)
			}
			// Each back-off counts as over: the test does not wait it out.
			w.syncer.RetryAhead = 20 * time.Minute
			w.implementerFailing(func(b *fakelux.Behaviour) { b.FirstTurnErrors = errs })
			task := w.task()
			w.deliver(task)
			w.until("the implementer to finish", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, task) == 1
			})
			run := w.implementer(task)
			if n := w.turnRetries(run); n != len(errs) {
				t.Fatalf("%d retries recorded, want %d", n, len(errs))
			}
			for i, e := range errs {
				var reason string
				var after float64
				var attempt int
				if err := w.owner.QueryRow(context.Background(), `SELECT payload->>'reason', (payload->>'after')::float8,
					(payload->>'attempt')::int FROM events WHERE run_id = $1 AND event_type = 'run.turn.retried' ORDER BY cursor OFFSET $2 LIMIT 1`,
					run, i).Scan(&reason, &after, &attempt); err != nil {
					t.Fatal(err)
				}
				if attempt != i+1 || !strings.Contains(e, strings.TrimPrefix(reason, "the agent's turn failed: ")) {
					t.Errorf("retry %d: attempt %d for %q, want %d for %q", i, attempt, reason, i+1, e)
				}
				if e == rateLimited && after < 143 {
					t.Errorf("a 429 asking for 143 s was retried after %v s", after)
				}
				if e != rateLimited && (after < 12 || after > 300) {
					t.Errorf("retry %d after %v s, want the 15 s-based back-off", i, after)
				}
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'`, run); n != 0 {
				t.Errorf("%d run.failed for a retried turn", n)
			}
			if r := w.escalationReason(task); r != "" {
				t.Errorf("escalated for %q", r)
			}
			// The same lux Run went on: the agent had its session, and was
			// told each time to carry on.
			lr := w.luxRunFor(run)
			if lr == nil || len(lr.Inputs) != len(errs) || !strings.Contains(lr.Inputs[0], "temporary error from the model provider") {
				t.Fatalf("the agent's inputs: %v", lr.Inputs)
			}
			if lr.Resumed != 0 {
				t.Errorf("the lux Run was resumed %d times; a retry goes on in the running one", lr.Resumed)
			}
		})
	}
}

// Past maxTurnRetries the failure is the Run's, as any other: failed
// with the agent's error, saying it was retried, and escalated as
// implement_failed.
func TestATransientFailurePastTheBoundFailsTheRunAsBefore(t *testing.T) {
	w := newWorld(t)
	w.syncer.RetryAhead = 20 * time.Minute
	w.implementerFailing(func(b *fakelux.Behaviour) { b.TurnError = acpLimited })
	task := w.task()
	w.deliver(task)
	w.until("the task to need a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
	run := w.implementer(task)
	if n := w.turnRetries(run); n != 4 {
		t.Errorf("%d retries before failing, want 4", n)
	}
	errText := w.str(`SELECT status::text || ': ' || COALESCE(error, '') FROM runs WHERE id = $1`, run)
	if !strings.HasPrefix(errText, "failed: the agent's turn failed: "+acpLimited) || !strings.Contains(errText, "retried a temporary model error 4 times") {
		t.Errorf("the Run ended %q", errText)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'
		AND payload->>'error' LIKE '%retried a temporary model error 4 times%'`, run); n != 1 {
		t.Errorf("%d run.failed naming the retries, want 1", n)
	}
	if r := w.escalationReason(task); r != "implement_failed" {
		t.Errorf("escalated for %q, want implement_failed", r)
	}
}

// A failure that is not temporary — refused credentials, a bad request —
// is not retried: the Run fails at once.
func TestANonTransientTurnFailureIsNotRetried(t *testing.T) {
	for name, e := range map[string]string{
		"auth": "session/prompt: Unauthorized: invalid x-api-key (401)",
		"400":  `session/prompt: Bad Request: {"type":"invalid_request_error"} (400)`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.syncer.RetryAhead = 20 * time.Minute
			w.implementerFailing(func(b *fakelux.Behaviour) { b.TurnError = e })
			task := w.task()
			w.deliver(task)
			w.until("the task to need a person", func() bool { return w.taskStatus(task) == "awaiting_input" })
			run := w.implementer(task)
			if n := w.turnRetries(run); n != 0 {
				t.Errorf("%d retries of %q", n, e)
			}
			if r := w.escalationReason(task); r != "implement_failed" {
				t.Errorf("escalated for %q, want implement_failed", r)
			}
			if got := w.str(`SELECT COALESCE(error, '') FROM runs WHERE id = $1`, run); strings.Contains(got, "retried") {
				t.Errorf("error %q names retries", got)
			}
		})
	}
}

// A reviewer's turn failing on the proxy's 429 is retried too: the review
// completes, and nothing escalates as review_failed.
func TestAReviewersTransientFailureIsRetried(t *testing.T) {
	w := newWorld(t)
	w.syncer.RetryAhead = 20 * time.Minute
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Commit: map[string]string{"hello.go": "package hello\n"}, Message: "work", Reply: "Done."}
		case "review":
			return fakelux.Behaviour{Reply: "Looks good.", FirstTurnErrors: []string{acpLimited}}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	task := w.task()
	w.deliver(task)
	w.until("a review to complete", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'`, task) > 0
	})
	if n := w.count(`SELECT count(*) FROM events e JOIN runs r ON r.id = e.run_id WHERE r.task_id = $1 AND r.phase = 'review'
		AND e.event_type = 'run.turn.retried'`, task); n < 1 {
		t.Errorf("no reviewer retry recorded")
	}
	if r := w.escalationReason(task); r != "" {
		t.Errorf("escalated for %q", r)
	}
}

// conductorRetrying has a quiet conductor's first turn fail on a 429
// asking for an hour, and returns once the retry is queued and the
// decision is waited on.
func conductorRetrying(t *testing.T) (w *world, task, conductor string) {
	w = conducting(t)
	w.waitingFor(time.Hour)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.role"] == delivery.RoleConductor {
			// Longer than any test waits: the retry is pending until made due.
			b.FirstTurnErrors = []string{`API Error: Request rejected (429) · {"error":"All provider keys are rate limited","retry_after":3600}`}
			b.Hang = true
		}
		return b
	}
	task = w.task()
	w.startQuiet(task)
	w.until("the conductor's turn retried", func() bool {
		id, _, _ := w.conductor(task)
		return id != "" && w.turnRetries(id) == 1
	})
	conductor, _, _ = w.conductor(task)
	w.until("the decision waited on", func() bool { return w.decisionAt(task) != "" })
	return w, task, conductor
}

// The actual syncer queues a retry after a transient model error. Expiring
// a conductor-held escalation while that retry waits, then runs, must not
// ask a person; finishing the turn restores the original deadline.
func TestRetryingConductorKeepsItsEscalationThroughBackoffAndBusyTurn(t *testing.T) {
	w, task, conductor := conductorRetrying(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET step='decide',wake_at=now(),
		state=(state-'decision') || jsonb_build_object('escalation',jsonb_build_object(
		'reason','implement_failed','step','implement','holder','conductor',
		'raisedAt',now()-interval '1 hour'))
		WHERE task_id=$1 AND workflow_type='task.delivery' AND status IN ('waiting','running')`, task)
	check := func() {
		t.Helper()
		mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at=now() WHERE task_id=$1 AND workflow_type='task.delivery'`, task)
		for range 3 {
			w.pump()
		}
		if got := w.str(`SELECT state->'escalation'->>'holder' FROM workflow_runs WHERE task_id=$1 AND workflow_type='task.delivery' ORDER BY created_at DESC LIMIT 1`, task); got != delivery.HolderConductor {
			t.Fatalf("retrying escalation holder=%s", got)
		}
		if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked' AND payload->>'kind'='escalation'`, task); n != 0 {
			t.Fatalf("asked person %d times during legitimate retry", n)
		}
		var wait float64
		if err := w.owner.QueryRow(context.Background(), `SELECT extract(epoch FROM wake_at-now())::float8 FROM workflow_runs WHERE task_id=$1 AND workflow_type='task.delivery' ORDER BY created_at DESC LIMIT 1`, task).Scan(&wait); err != nil {
			t.Fatal(err)
		}
		if wait <= 0 || wait > 31 {
			t.Fatalf("recheck in %v seconds; want bounded 30s", wait)
		}
	}
	check()
	w.retryDue(conductor)
	w.until("the retry sent and busy", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND deliver_after IS NOT NULL AND sent_at IS NOT NULL`, conductor) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id=$1 AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL`, conductor) == 1
	})
	check()
	w.lux.EndTurn(w.luxRunFor(conductor).ID)
	w.until("the retry count reset", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND COALESCE((harness_state->>'turnRetries')::int,0)=0`, conductor) == 1
	})
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at=now() WHERE task_id=$1 AND workflow_type='task.delivery'`, task)
	w.until("the expired escalation handed over", func() bool { return w.taskStatus(task) == "awaiting_input" })
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked' AND payload->>'kind'='escalation'`, task); n != 1 {
		t.Fatalf("handover count=%d, want exactly one", n)
	}
}

// A quiet conductor whose turn failed on a 429 is retrying it: its
// decision's deadline passing does not default the decision while the
// retry waits. Once the retried turn goes through, the conductor takes
// its decision itself.
func TestAConductorRetryingATransientFailureKeepsItsDecision(t *testing.T) {
	w, task, conductor := conductorRetrying(t)
	at := w.decisionAt(task)
	w.lapse(task)
	for range 5 {
		w.pump()
	}
	if n := w.defaulted(task, ""); n != 0 {
		t.Fatalf("%d decisions defaulted while the conductor retried", n)
	}
	if got := w.decisionAt(task); got != at {
		t.Fatalf("the decision at %q moved to %q while the conductor retried", at, got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, conductor); n != 1 {
		t.Fatalf("the retrying conductor is not running: %s", w.describeRuns())
	}
	// Sending the retry does not end its protection: the retried turn
	// must have time to decide, even if the deadline lapses mid-turn.
	w.retryDue(conductor)
	w.until("the retry running", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND deliver_after IS NOT NULL AND sent_at IS NOT NULL`, conductor) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL`, conductor) == 1
	})
	w.lapse(task)
	for range 5 {
		w.pump()
	}
	if n := w.defaulted(task, ""); n != 0 {
		t.Fatalf("%d decisions defaulted while the retried turn ran", n)
	}
	// Once the turn succeeds the deadline applies again as it stood.
	w.lux.EndTurn(w.luxRunFor(conductor).ID)
	w.until("the retried turn through", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND COALESCE((harness_state->>'turnRetries')::int, 0) = 0`, conductor) == 1
	})
	w.lapse(task)
	w.until("the decision defaulted at its deadline", func() bool { return w.defaulted(task, delivery.DefaultedDeadline) == 1 })
}

// A conductor paused while it waits out a retry's back-off is not
// retrying any more: its count left behind does not hold the decision,
// whose deadline defaults it.
func TestAConductorPausedMidRetryLetsItsDeadlineDefault(t *testing.T) {
	w, task, conductor := conductorRetrying(t)
	if status, body := w.call("/internal/runs/"+conductor+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	w.until("the conductor paused", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, conductor) == 1
	})
	w.lapse(task)
	w.until("the decision defaulted at its deadline", func() bool { return w.defaulted(task, delivery.DefaultedDeadline) == 1 })
}

func TestALongRetryBackOffDoesNotInterruptASilentConductor(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.role"] == delivery.RoleConductor {
			b.FirstTurnErrors = []string{`API Error: Request rejected (429) · {"retry_after":900}`}
			b.Hang = true
		}
		return b
	}
	task := w.task()
	w.startQuiet(task)
	w.until("the conductor retry held", func() bool {
		id, _, _ := w.conductor(task)
		return id != "" && w.turnRetries(id) == 1
	})
	run, _, _ := w.conductor(task)
	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = now() - interval '11 minutes',
		agent_busy_at = now() - interval '11 minutes' WHERE id = $1`, run)
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt`, run); n != 0 {
		t.Fatalf("%d interrupts while the retry was held", n)
	}
	if in := w.lux.Inputs(w.luxRunFor(run).ID); len(in) != 0 {
		t.Fatalf("inputs during retry back-off: %v", in)
	}
}

func TestASuccessfulTurnCancelsItsHeldRetry(t *testing.T) {
	w := conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	task, _ := w.delivered()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{FirstTurnErrors: []string{rateLimited}, Hang: true}
	}
	status, out := w.chat(task, "start")
	if status != 201 {
		t.Fatalf("start: %d %v", status, out)
	}
	run, _ := out["runId"].(string)
	w.until("the retry held", func() bool { return w.turnRetries(run) == 1 })
	if status, out := w.chat(task, "message"); status != 200 {
		t.Fatalf("message: %d %v", status, out)
	}
	w.until("the message delivered during back-off", func() bool {
		return len(w.lux.Inputs(w.luxRunFor(run).ID)) == 1
	})
	w.lux.EndTurn(w.luxRunFor(run).ID)
	w.until("the successful turn settled", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NOT NULL
			AND COALESCE((harness_state->>'turnRetries')::int, 0) = 0`, run) == 1
	})
	w.syncer.ConductorWarm = time.Millisecond
	w.until("the conductor parked", func() bool {
		_, status, _ := w.conductor(task)
		return status == "paused"
	})
	w.syncer.ConductorWarm = time.Hour
	w.retryDue(run)
	for range 5 {
		w.pump()
	}
	if _, status, _ := w.conductor(task); status != "paused" {
		t.Fatalf("stale retry resumed the conductor: %s", status)
	}
	if status, out := w.chat(task, "wake"); status != 200 {
		t.Fatalf("wake: %d %v", status, out)
	}
	w.until("the wake delivered", func() bool { return len(w.lux.Inputs(w.luxRunFor(run).ID)) >= 2 })
	for range 5 {
		w.pump()
	}
	in := w.lux.Inputs(w.luxRunFor(run).ID)
	if len(in) != 2 || !strings.Contains(in[0], "message") || !strings.Contains(in[1], "wake") {
		t.Fatalf("inputs after due: %v, want [message, wake]", in)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND deliver_after IS NOT NULL
		AND sent_at IS NULL AND failed_at IS NOT NULL AND error IS NOT NULL`, run); n != 1 {
		t.Fatalf("%d held retries cancelled with a reason, want 1", n)
	}
}

// retryDue makes the Run's pending turn retry due now, as the test clock
// passing its back-off.
func (w *world) retryDue(run string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE directives SET deliver_after = now() WHERE run_id = $1 AND deliver_after IS NOT NULL
		AND sent_at IS NULL`, run)
}

// retryPending has the implementer's first turn fail on the 429 asking for
// 143 s, and returns the task and implementer once the retry is queued:
// its back-off not taken as over (RetryAhead 0), the retry waits.
func (w *world) retryPending() (task, run string) {
	w.t.Helper()
	w.syncer.RetryAhead = 0
	w.implementerFailing(func(b *fakelux.Behaviour) { b.FirstTurnErrors = []string{rateLimited}; b.Hang = true })
	task = w.task()
	w.deliver(task)
	w.until("the implementer's turn retried", func() bool {
		run = w.implementer(task)
		return run != "" && w.turnRetries(run) == 1
	})
	return task, run
}

// A person's Pause while a turn retry waits out its back-off is acted on
// at once: only the retry waits, not the Run (the Run's own back-off
// would hold every sweep action as long).
func TestAPauseIsNotHeldByATurnRetrysBackOff(t *testing.T) {
	w := newWorld(t)
	_, run := w.retryPending()
	if status, body := w.call("/internal/runs/"+run+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	for range 5 {
		w.pump()
	}
	if got := w.str(`SELECT status::text FROM runs WHERE id = $1`, run); got != "paused" {
		t.Fatalf("the Run is %s five pumps after the pause, want paused:\n%s", got, w.describeRuns())
	}
}

// An abort of the Run (a child's, a checks escalation's: control 'abort'
// on a live Run) while a turn retry waits is acted on at once.
func TestAnAbortIsNotHeldByATurnRetrysBackOff(t *testing.T) {
	w := newWorld(t)
	_, run := w.retryPending()
	mustExec(t, w.owner, `UPDATE runs SET control = 'abort', control_requested_at = now(), control_reason = 'aborted', keep = false
		WHERE id = $1`, run)
	for range 5 {
		w.pump()
	}
	// The abort is acted on: lux is told to cancel the Run.
	if lr := w.luxRunFor(run); lr == nil || lr.State == "running" {
		t.Fatalf("the lux Run is still running five pumps after the abort:\n%s", w.describeRuns())
	}
}

// The retry itself is held for its back-off: not sent before the time the
// 429 asked for, sent once the clock passes it.
func TestATurnRetryIsHeldUntilItsBackOffIsOver(t *testing.T) {
	w := newWorld(t)
	_, run := w.retryPending()
	var wait float64
	if err := w.owner.QueryRow(context.Background(), `SELECT extract(epoch FROM deliver_after - now())::float8 FROM directives
		WHERE run_id = $1 AND deliver_after IS NOT NULL`, run).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait < 140 || wait > 160 {
		t.Fatalf("the retry is held %v s, want the 143 s the 429 asked (+ up to 10 %%)", wait)
	}
	retried := func() bool {
		lr := w.luxRunFor(run)
		return lr != nil && len(w.lux.Inputs(lr.ID)) > 0
	}
	// The clock just short of the back-off's end: still held.
	mustExec(t, w.owner, `UPDATE directives SET deliver_after = now() + interval '30 seconds' WHERE run_id = $1
		AND deliver_after IS NOT NULL`, run)
	for range 5 {
		w.pump()
	}
	if retried() {
		t.Fatalf("the retry was sent before its back-off was over")
	}
	if got := w.str(`SELECT status::text FROM runs WHERE id = $1`, run); got != "running" {
		t.Fatalf("the Run is %s while its retry waits", got)
	}
	// Past it: sent.
	w.retryDue(run)
	w.until("the retry sent", retried)
	if in := w.lux.Inputs(w.luxRunFor(run).ID); !strings.Contains(in[0], "temporary error from the model provider") {
		t.Fatalf("the agent was given %q", in[0])
	}
}

// A Pause or Stop pending as a 429 turn ends is not raced by a retry: the
// Run is not retried, and ends as the control decides.
func TestATurnFailingUnderAPendingControlIsNotRetried(t *testing.T) {
	for _, c := range []struct{ name, path, want string }{
		{"pause", "/pause", "paused failed"},
		{"stop", "/abort", "aborted"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.syncer.RetryAhead = 20 * time.Minute
			w.implementerFailing(func(b *fakelux.Behaviour) { b.Hang = true })
			task := w.task()
			w.deliver(task)
			var run string
			w.until("the implementer working", func() bool {
				run = w.implementer(task)
				lr := w.luxRunFor(run)
				return run != "" && lr != nil && lr.State == "running" &&
					w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_busy_at IS NOT NULL`, run) == 1
			})
			if status, body := w.call("/internal/runs/"+run+c.path, map[string]any{}); status != 200 {
				t.Fatalf("%s: %d %v", c.name, status, body)
			}
			w.lux.FailTurn(w.luxRunFor(run).ID, rateLimited)
			w.until("the Run to end as its control decides", func() bool {
				return strings.Contains(c.want, w.str(`SELECT status::text FROM runs WHERE id = $1`, run))
			})
			for range 5 {
				w.pump()
			}
			if n := w.turnRetries(run); n != 0 {
				t.Fatalf("%d run.turn.retried under a pending %s", n, c.name)
			}
			if got := w.str(`SELECT status::text FROM runs WHERE id = $1`, run); !strings.Contains(c.want, got) {
				t.Fatalf("the Run is %s, want %s", got, c.want)
			}
		})
	}
}
