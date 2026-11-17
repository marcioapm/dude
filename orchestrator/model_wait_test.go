package orchestrator_test

// A worker Run whose turns the model provider keeps refusing is never
// failed for it: dude waits and retries in the same session as long as the
// refusals last, up to the role's no-progress limit (2 h by default) of
// continuous refusals. Past it the Run is parked, resumable, and its
// conductor told once; at 30 minutes the conductor is told once that dude
// is waiting. The stall detector leaves a waiting Run alone, and a
// person's pause still acts at once. A simplifier that fails for any
// other reason is a decision, not a silent skip to the test.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// refusing is n copies of the proxy's 429.
func refusing(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = rateLimited
	}
	return out
}

// refusedFor back-dates the start of the Run's current wait (its streak's
// first run.turn.retried) to ago: the test clock passing that long.
func (w *world) refusedFor(run string, ago time.Duration) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE events SET occurred_at = now() - make_interval(secs => $2)
		WHERE id = (SELECT id FROM events WHERE run_id = $1 AND event_type = 'run.turn.retried' AND payload->>'attempt' = '1'
			ORDER BY cursor DESC LIMIT 1)`, run, ago.Seconds())
}

func (w *world) runStatus(run string) string {
	return w.str(`SELECT status::text || '/' || COALESCE(dude_pause, '') FROM runs WHERE id = $1`, run)
}

// Nine refusals in a row, then the model answers: the implementer, and a
// reviewer, finish in the same lux Run and session, never failed.
func TestManyTemporaryRefusalsInARowAreWaitedOut(t *testing.T) {
	for _, phase := range []string{"implement", "review"} {
		t.Run(phase, func(t *testing.T) {
			w := newWorld(t)
			w.syncer.RetryAhead = 20 * time.Minute
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				labels, _ := spec["labels"].(map[string]any)
				b := fakelux.Behaviour{Reply: "Looks good."}
				if labels["dude.phase"] == "implement" {
					b = fakelux.Behaviour{Commit: map[string]string{"hello.go": "package hello\n"}, Message: "work", Reply: "Done."}
				}
				if labels["dude.phase"] == phase {
					b.FirstTurnErrors = refusing(9)
				}
				return b
			}
			task := w.task()
			w.deliver(task)
			var run string
			w.until("the Run to complete", func() bool {
				run = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = $2 AND status = 'completed'
					ORDER BY created_at LIMIT 1), '')`, task, phase)
				return run != "" || w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = $2 AND status = 'failed'`, task, phase) > 0
			})
			if run == "" {
				t.Fatalf("the %s Run failed:\n%s", phase, w.describeRuns())
			}
			if n := w.turnRetries(run); n != 9 {
				t.Errorf("%d retries, want 9", n)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.failed'`, task); n != 0 {
				t.Errorf("%d run.failed", n)
			}
			if lr := w.luxRunFor(run); lr == nil || lr.Resumed != 0 || len(lr.Inputs) != 9 {
				t.Fatalf("not the same lux Run and session: %+v", lr)
			}
			if r := w.escalationReason(task); r != "" {
				t.Errorf("escalated for %q", r)
			}
		})
	}
}

// modelRefusingWorld is a conducted task whose implementer's first n turns
// the provider refuses, its retries held (RetryAhead 0): the task and the
// implementer once its first retry waits.
func modelRefusingWorld(t *testing.T, n int, hang bool) (*world, string, string) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{FirstTurnErrors: refusing(n), Hang: hang,
				Commit: map[string]string{"hello.go": "package hello\n"}, Message: "work", Reply: "Done."}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.syncer.RetryAhead = 0
	w.must(task, "start_phase", `{"phase":"implement"}`)
	var run string
	w.until("the implementer's first retry held", func() bool {
		run = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' LIMIT 1), '')`, task)
		return run != "" && w.turnRetries(run) == 1
	})
	return w, task, run
}

// Refused continuously past the role's limit (2 h), the implementer is
// parked, not failed: its conductor is told once, in plain words; its
// steer resumes the same lux Run, and the agent finishes its work there.
func TestATransientFailurePastTheBoundParksTheRun(t *testing.T) {
	w, task, run := modelRefusingWorld(t, 2, false)
	luxID := w.luxRunOf(run)
	w.refusedFor(run, 2*time.Hour+time.Minute)
	w.retryDue(run)
	w.until("the implementer parked", func() bool { return w.runStatus(run) == "paused/idle" })
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'`, run); n != 0 {
		t.Fatalf("%d run.failed for a Run the provider refused", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key LIKE 'model-parked:'||$2||':%'
		AND line LIKE '%model provider has refused%for 2h%the Run is parked%resume it when the provider is back%'`, task, run); n != 1 {
		t.Fatalf("%d parked wakes, want 1", n)
	}
	if got := w.taskStatus(task); got == "awaiting_input" {
		t.Fatal("a conducted park asked the person")
	}
	if r := w.escalationReason(task); r != "" {
		t.Fatalf("escalated for %q", r)
	}
	w.must(task, "steer", fmt.Sprintf(`{"run":%q,"text":"The provider is back: carry on."}`, run))
	w.until("the implementer to finish in the same lux Run", func() bool {
		return w.runStatus(run) == "completed/"
	})
	if got := w.luxRunOf(run); got != luxID {
		t.Fatalf("resumed on lux Run %s, want %s", got, luxID)
	}
	if lr := w.luxRunFor(run); lr == nil || lr.Resumed < 1 {
		t.Fatalf("the lux Run was not resumed: %+v", lr)
	}
}

// At 30 minutes of continuous refusals the conductor is told once that
// dude is waiting: not again at the next retry. A turn that ends without
// failing starts a new wait, told once again at its own 30 minutes.
func TestTheModelWaitNoticeIsSentOncePerWait(t *testing.T) {
	w, task, run := modelRefusingWorld(t, 3, true)
	notices := func() int {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key LIKE 'model-waiting:'||$2||':%'
			AND line LIKE '%model provider has refused%dude keeps waiting%'`, task, run)
	}
	w.refusedFor(run, 31*time.Minute)
	for attempt := 2; attempt <= 3; attempt++ {
		w.retryDue(run)
		w.until(fmt.Sprintf("retry %d held", attempt), func() bool { return w.turnRetries(run) == attempt })
	}
	if n := notices(); n != 1 {
		t.Fatalf("%d notices after two retries past 30 minutes, want 1", n)
	}
	// The retried turn goes through (and ends holding on a question, so
	// the Run waits rather than completes): the wait is over.
	w.retryDue(run)
	luxID := w.luxRunOf(run)
	w.until("the retried turn working", func() bool {
		return len(w.lux.Inputs(luxID)) == 3 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_busy_at IS NOT NULL
			AND turn_done_at IS NULL`, run) == 1
	})
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status)
		VALUES ('q_'||$1, $2, $3, $1, 'Which?', 'open')`, run, w.org, task)
	w.lux.EndTurn(luxID)
	w.until("the streak reset", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL
			AND COALESCE((harness_state->>'turnRetries')::int, 0) = 0`, run) == 1
	})
	// A new wait: refused again, past 30 minutes again.
	mustExec(t, w.owner, `UPDATE questions SET status = 'answered', answer = 'that', answered_at = now() WHERE id = 'q_'||$1`, run)
	if status, body := w.call("/internal/runs/"+run+"/steer", map[string]any{"text": "go on"}); status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	busy := func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL
			AND waiting_since IS NULL`, run) == 1 && w.lux.State(luxID) == "running"
	}
	w.until("a new turn working", busy)
	before := w.turnRetries(run)
	w.until("the new turn refused", func() bool { w.lux.FailTurn(luxID, rateLimited); return w.turnRetries(run) == before+1 })
	if n := notices(); n != 1 {
		t.Fatalf("%d notices at a new wait's start, want still 1", n)
	}
	w.refusedFor(run, 31*time.Minute)
	w.retryDue(run)
	w.until("the new wait's retried turn working", busy)
	w.until("refused again", func() bool { w.lux.FailTurn(luxID, rateLimited); return w.turnRetries(run) == before+2 })
	if n := notices(); n != 2 {
		t.Fatalf("%d notices, want one per wait (2)", n)
	}
}

// A person's Pause during a long wait (past the four retries dude once
// allowed) acts at once.
func TestAPauseActsAtOnceDuringALongModelWait(t *testing.T) {
	w := newWorld(t)
	w.syncer.RetryAhead = 0
	w.implementerFailing(func(b *fakelux.Behaviour) { b.FirstTurnErrors = refusing(9); b.Hang = true })
	task := w.task()
	w.deliver(task)
	var run string
	w.until("the first retry held", func() bool {
		run = w.implementer(task)
		return run != "" && w.turnRetries(run) == 1
	})
	for attempt := 2; attempt <= 6; attempt++ {
		w.retryDue(run)
		w.until(fmt.Sprintf("retry %d held (Run %s)", attempt, w.runStatus(run)), func() bool { return w.turnRetries(run) == attempt })
	}
	w.refusedFor(run, 90*time.Minute)
	if status, body := w.call("/internal/runs/"+run+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	for range 5 {
		w.pump()
	}
	if got := w.runStatus(run); !strings.HasPrefix(got, "paused/") {
		t.Fatalf("the Run is %s five pumps after the pause, want paused:\n%s", got, w.describeRuns())
	}
}

// A Run waiting out the provider's refusals for hours, silent all along,
// is not reported as making no progress, nudged, interrupted or failed:
// while its retry is held, and while the retried turn is in flight.
func TestTheStallDetectorsLeaveARunWaitingOnTheModel(t *testing.T) {
	w := newWorld(t)
	w.syncer.RetryAhead = 0
	w.syncer.IdleAfter = 300 * time.Millisecond
	w.implementerFailing(func(b *fakelux.Behaviour) { b.FirstTurnErrors = refusing(1); b.Hang = true })
	task := w.task()
	w.deliver(task)
	var run string
	w.until("the retry held", func() bool {
		run = w.implementer(task)
		return run != "" && w.turnRetries(run) == 1
	})
	silent := func(what string) {
		t.Helper()
		mustExec(t, w.owner, `UPDATE runs SET agent_active_at = now() - interval '3 hours', agent_busy_at = now() - interval '3 hours',
			files_changed_at = now() - interval '3 hours', started_at = now() - interval '3 hours' WHERE id = $1`, run)
		for range 3 {
			w.sweep()
			w.pump()
		}
		if n := w.stalls(run); n != 0 {
			t.Fatalf("%s: %d stall reports", what, n)
		}
		if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt`, run); n != 0 {
			t.Fatalf("%s: %d interrupts", what, n)
		}
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND idle_nudged_at IS NOT NULL`, run); n != 0 {
			t.Fatalf("%s: nudged", what)
		}
		if got := w.runStatus(run); got != "running/" {
			t.Fatalf("%s: the Run is %s", what, got)
		}
	}
	silent("retry held")
	w.retryDue(run)
	w.until("the retried turn in flight", func() bool {
		return len(w.lux.Inputs(w.luxRunOf(run))) == 1
	})
	silent("retried turn in flight")
}

// A simplifier that fails (not on a temporary model error) is a decision
// for whoever decides the delivery: the delivery does not go on to the
// test without it.
func TestAFailedSimplifierIsADecisionNotASkip(t *testing.T) {
	w := newWorld(t)
	w.crashSimplifiers(1)
	task := w.task()
	w.deliver(task)
	w.until("the simplifier's failure decided", func() bool {
		return w.escalationReason(task) != "" || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
			AND step IN ('test', 'awaitTest', 'openPullRequest', 'awaitPullRequest')`, task) > 0
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'test'`, task); n != 0 {
		t.Fatalf("%d test Runs after the simplifier failed", n)
	}
	if r := w.escalationReason(task); r != "simplify_failed" {
		t.Fatalf("escalated %q, want simplify_failed; workflow at %s", r,
			w.str(`SELECT step FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, task))
	}
}
