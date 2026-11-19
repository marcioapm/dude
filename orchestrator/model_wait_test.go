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
// dude is waiting: not at 29, and not again at the next retry. A turn that
// ends without failing starts a new wait, told once again at its own 30
// minutes.
func TestTheModelWaitNoticeIsSentOncePerWait(t *testing.T) {
	w, task, run := modelRefusingWorld(t, 4, true)
	notices := func() int {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key LIKE 'model-waiting:'||$2||':%'
			AND line LIKE '%model provider has refused%dude keeps waiting%'`, task, run)
	}
	w.refusedFor(run, 29*time.Minute)
	w.retryDue(run)
	w.until("retry 2 held", func() bool { return w.turnRetries(run) == 2 })
	if n := notices(); n != 0 {
		t.Fatalf("%d notices at 29 minutes of refusals, want none before 30", n)
	}
	w.refusedFor(run, 31*time.Minute)
	for attempt := 3; attempt <= 4; attempt++ {
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
		return len(w.lux.Inputs(luxID)) == 4 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_busy_at IS NOT NULL
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
	// A retried turn in flight past the longest back-off (15 minutes) is
	// no longer waiting on the model: a real hang is reported again.
	mustExec(t, w.owner, `UPDATE directives SET sent_at = now() - interval '16 minutes'
		WHERE run_id = $1 AND deliver_after IS NOT NULL`, run)
	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = now() - interval '3 hours', agent_busy_at = now() - interval '3 hours',
		files_changed_at = now() - interval '3 hours', started_at = now() - interval '3 hours' WHERE id = $1`, run)
	w.until("the hung retried turn reported", func() bool {
		w.sweep()
		return w.stalls(run) > 0
	})
}

// harnessRetries is the Run's current streak of retries (turnRetries).
func (w *world) harnessRetries(run string) int {
	return w.count(`SELECT COALESCE((harness_state->>'turnRetries')::int, 0) FROM runs WHERE id = $1`, run)
}

// modelWakes are the conductor's wakes about the Run's model wait.
func (w *world) modelWakes(task, run string) int {
	return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1
		AND (key LIKE 'model-waiting:'||$2||':%' OR key LIKE 'model-parked:'||$2||':%')`, task, run)
}

// modelParks is how many times run was parked for the model's refusals.
func (w *world) modelParks(run string) int {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'
		AND (payload->>'model')::boolean`, run)
}

// retriedTurn sends the Run's held retry and waits for the retried turn
// in flight; refused then fails that turn on agentErr, until dude has
// retried it again or parked it. Read from the Run's events, not its
// status: lux's lifecycle events trail the agent's records, so a Run
// whose refusal is recorded may not read running yet.
func (w *world) retriedTurn(run string) (refused func(agentErr string)) {
	w.t.Helper()
	luxID, inputs, parked := w.luxRunOf(run), w.turnRetries(run), w.modelParks(run)
	w.retryDue(run)
	w.until("the retried turn in flight", func() bool {
		return len(w.lux.Inputs(luxID)) == inputs && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_busy_at IS NOT NULL
			AND turn_done_at IS NULL`, run) == 1
	})
	return func(agentErr string) {
		w.t.Helper()
		w.until("the retried turn refused", func() bool {
			w.lux.FailTurn(luxID, agentErr)
			return w.turnRetries(run) == inputs+1 || w.modelParks(run) > parked
		})
		for range 3 {
			w.pump()
		}
	}
}

// A retried turn that went on to work for hours, then met one refusal, is
// not a Run refused for hours: the work ended the wait, and the refusal
// starts a new one. Not parked, its conductor not told. The work ends
// only the streak it came after: the new streak, refused past the role's
// limit with no work, parks the Run.
func TestAWorkingRetriedTurnEndsTheModelWait(t *testing.T) {
	w, task, run := modelRefusingWorld(t, 1, true)
	refused := w.retriedTurn(run)
	// It works: a tool call, two hours of it on the test clock.
	w.lux.AgentSays(w.luxRunOf(run), map[string]any{"sessionUpdate": "tool_call", "toolCallId": "work-1", "title": "bash",
		"kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": "make"}})
	w.until("the work recorded", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.tool.called'`, run) > 0
	})
	w.refusedFor(run, 2*time.Hour+time.Minute)
	refused(rateLimited)
	if w.modelParks(run) != 0 {
		t.Fatalf("the Run is %s after one refusal following its work, want not parked", w.runStatus(run))
	}
	if n := w.harnessRetries(run); n != 1 {
		t.Errorf("streak of %d retries, want a new one (1)", n)
	}
	if n := w.modelWakes(task, run); n != 0 {
		t.Errorf("%d model wakes for a Run that was working, want none", n)
	}
	refused = w.retriedTurn(run)
	w.refusedFor(run, 2*time.Hour+time.Minute)
	refused(rateLimited)
	if w.modelParks(run) == 0 {
		t.Fatalf("refused past the limit in a new streak with no work, the Run is %s, want parked", w.runStatus(run))
	}
	w.until("the Run parked", func() bool { return w.runStatus(run) == "paused/idle" })
}

// A retried turn that only thinks, then is refused (a thinking block
// streamed before a 529), did no work: the thought may be the refused
// response's own. The wait goes on, and past the role's limit the Run is
// parked, its conductor told once.
func TestAThinkingThenRefusedTurnDoesNotEndTheModelWait(t *testing.T) {
	w, task, run := modelRefusingWorld(t, 1, true)
	thoughts := func() int {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.thought'`, run)
	}
	// A fixed number of cycles, not while the Run reads running: the first
	// refusal can be seen before lux's lifecycle event makes it running.
	for cycle := 1; cycle <= 3 && w.modelParks(run) == 0; cycle++ {
		refused := w.retriedTurn(run)
		w.lux.AgentSays(w.luxRunOf(run), map[string]any{"sessionUpdate": "agent_thought_chunk",
			"content": map[string]any{"type": "text", "text": "Let me look at the code."}})
		w.refusedFor(run, 2*time.Hour+time.Minute)
		refused(overloaded)
		// Flushed at the turn's end, before the refusal is translated.
		w.until(fmt.Sprintf("thought %d recorded", cycle), func() bool { return thoughts() >= cycle })
		if n := w.count(`SELECT count(*) FROM events t WHERE t.run_id = $1 AND t.event_type = 'agent.thought'
			AND t.cursor > (SELECT max(cursor) FROM events WHERE run_id = $1 AND event_type IN ('run.turn.retried', 'run.parked'))`, run); n > 0 {
			t.Fatalf("cycle %d: the thought was recorded after its refusal", cycle)
		}
	}
	if w.modelParks(run) == 0 {
		t.Fatalf("refused past the limit after each thought, the Run is %s, want parked", w.runStatus(run))
	}
	w.until("the Run parked", func() bool { return w.runStatus(run) == "paused/idle" })
	if n := w.modelWakes(task, run); n != 1 {
		t.Fatalf("%d model wakes, want the one model-parked", n)
	}
}

// After a model park and the conductor's steer, the next refusal starts a
// new wait (attempt 1): the Run is not parked again at once.
func TestARefusalAfterAModelParkIsANewWait(t *testing.T) {
	w, task, run := modelRefusingWorld(t, 3, false)
	w.refusedFor(run, 2*time.Hour+time.Minute)
	w.retryDue(run)
	w.until("the implementer parked", func() bool { return w.runStatus(run) == "paused/idle" })
	w.must(task, "steer", fmt.Sprintf(`{"run":%q,"text":"The provider is back: carry on."}`, run))
	w.until("refused once more after the resume", func() bool {
		return w.turnRetries(run) == 2 || (w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`, run) > 1)
	})
	for range 3 {
		w.pump()
	}
	if got := w.runStatus(run); got != "running/" {
		t.Fatalf("the Run is %s after one refusal past its resume, want running", got)
	}
	if n := w.harnessRetries(run); n != 1 {
		t.Errorf("streak of %d retries after the resume, want 1", n)
	}
}

// A model park its conductor leaves is handed to the person as the
// provider's refusals, not as an agent that went quiet. The latest park
// decides: one parked as quiet after it (resumed, then quiet) is handed
// over as an agent that went quiet.
func TestALeftModelParkIsHandedOverAsTheProvidersRefusals(t *testing.T) {
	for _, quietAfter := range []bool{false, true} {
		t.Run(fmt.Sprintf("quietAfter=%v", quietAfter), func(t *testing.T) {
			w, task, run := modelRefusingWorld(t, 2, false)
			w.syncer.HeldAskWait = time.Hour
			w.refusedFor(run, 2*time.Hour+time.Minute)
			w.retryDue(run)
			w.until("the implementer parked", func() bool { return w.runStatus(run) == "paused/idle" })
			if quietAfter {
				// A later park as quiet: the model park's event again, without model.
				mustExec(t, w.owner, `INSERT INTO events (id, organization_id, project_id, task_id, run_id, event_type, actor_type, actor_id,
					source, payload) SELECT id||'q', organization_id, project_id, task_id, run_id, event_type, actor_type, actor_id, source,
					payload - 'model' FROM events WHERE run_id = $1 AND event_type = 'run.parked'`, run)
			}
			w.syncer.HeldAskWait = 500 * time.Millisecond
			w.until("the task to wait on the person", func() bool { return w.taskStatus(task) == "awaiting_input" })
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.idle_handed_over'
				AND (payload->>'model')::boolean = $2`, run, !quietAfter); n != 1 {
				t.Fatalf("%d hand-overs marked model %v, want 1", n, !quietAfter)
			}
			got := w.str(`SELECT payload->>'reason' FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
				ORDER BY cursor DESC LIMIT 1`, task)
			if want := map[bool]string{false: "model provider", true: "went quiet"}[quietAfter]; !strings.Contains(got, want) {
				t.Fatalf("handed over as %q, want %q", got, want)
			}
		})
	}
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
