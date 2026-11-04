package orchestrator_test

// A turn that fails on a temporary model error — a rate limit, an
// overloaded upstream, a stream cut off — is waited out and retried in the
// same Run, any role's: no escalation for a blip. Past the bound the
// failure is the Run's, as before; any other failure is not retried. A
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

// Past maxTurnRetries the failure is the Run's, exactly as before: failed
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
// is not retried: the Run fails at once, as before.
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

// A quiet conductor whose turn failed on a 429 is retrying it: its
// decision's deadline passing does not default the decision while the
// retry waits. Once the retried turn goes through, the conductor takes
// its decision itself.
func TestAConductorRetryingATransientFailureKeepsItsDecision(t *testing.T) {
	w := conducting(t)
	w.waitingFor(time.Hour)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.role"] == delivery.RoleConductor {
			// Longer than any test waits: the retry is pending until made due.
			b.FirstTurnErrors = []string{`API Error: Request rejected (429) · {"error":"All provider keys are rate limited","retry_after":3600}`}
		}
		return b
	}
	task := w.task()
	w.startQuiet(task)
	w.until("the conductor's turn retried", func() bool {
		id, _, _ := w.conductor(task)
		return id != "" && w.turnRetries(id) == 1
	})
	conductor, _, _ := w.conductor(task)
	w.until("the decision waited on", func() bool { return w.decisionAt(task) != "" })
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
	// The retry is due and its turn goes through: the retries are over,
	// and the deadline applies again as it stood.
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = now() WHERE id = $1`, conductor)
	w.until("the retried turn through", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND COALESCE((harness_state->>'turnRetries')::int, 0) = 0`, conductor) == 1
	})
	w.lapse(task)
	w.until("the decision defaulted at its deadline", func() bool { return w.defaulted(task, delivery.DefaultedDeadline) == 1 })
}
