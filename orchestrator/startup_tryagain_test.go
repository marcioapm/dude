package orchestrator_test

// Try again on a talker that failed (delivery.RetryBrainstorm,
// RetryConductor): a session's agent whose resume failed, or a task's
// conductor whose start failed, is replaced by a fresh one that takes what
// it never read, once, and never beside a second live agent; a Try again
// after the sweep already handed the messages on is told so.

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// settled waits, without sweeping, for cond: the Run's own follower records
// what lux said, and a sweep meanwhile would hand its messages on first.
func (w *world) settled(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			w.t.Fatalf("%s never happened:\n%s", what, w.describeRuns())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// liveTalkers counts the live agents of a session or a task (col names which).
func (w *world) liveTalkers(col, id string) int {
	return w.count(fmt.Sprintf(`SELECT count(*) FROM runs WHERE %s = $1 AND kind = 'agent' AND role IN ('brainstorm', 'conductor')
		AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, col), id)
}

// retryRefusal is the code and message of a refused Try again.
func retryRefusal(status int, out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	return fmt.Sprintf("%d %v: %v", status, e["code"], e["message"])
}

const taken = "409 retry_taken: a new agent already took the later messages"

// failedResume is a session whose agent ran, was parked, and whose resume
// for "and the tests?" lux ended for good while it waited for a host.
func failedResume(t *testing.T) (s *sessionWorld, id, run string) {
	s = newSessionWorld(t)
	s.syncer.ConductorWarm = time.Hour
	// Only the resume's wait for a host is held; every other start runs on.
	reached, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	s.lux.OnStage = func(epoch int, stage string) {
		if epoch == 2 && stage == "waiting" {
			hold.Do(func() {
				close(reached)
				<-release
			})
		}
	}
	id = s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where would metering live?"})
	run = s.started(id)
	s.lux.StopOnItsOwn(s.luxRunOf(run))
	s.until("the Run parked", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, run) == 1
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "and the tests?"})
	// Swept until the resume is asked of lux and followed, held waiting.
	s.until("the resume waiting for a host", func() bool {
		select {
		case <-reached:
			return true
		default:
			return false
		}
	})
	s.until("the resume followed", func() bool {
		return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.stage' AND payload->>'stage' = 'waiting'`, run) == 2
	})
	s.lux.CancelInLux(s.luxRunOf(run))
	close(release)
	s.settled("the resume to fail", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run) == 1
	})
	return s, id, run
}

func TestTryAgainOnASessionWhoseResumeFailedHandsTheMessageToAFreshAgentOnce(t *testing.T) {
	s, id, run := failedResume(t)
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed' AND payload->>'stage' = 'waiting'`, run); n != 1 {
		t.Errorf("the failed resume's run.failed names waiting %d times, want once", n)
	}
	status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/retry", nil)
	next, _ := out["runId"].(string)
	if status != 201 || next == "" || next == run {
		t.Fatalf("Try again: %d %v, want a fresh agent", status, out)
	}
	// Chat reads the fresh agent as Try again on the failed one, not a newer agent that took its messages.
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'retries' = $2`, next, run); n != 1 {
		t.Errorf("the fresh agent's run.created names the one it retries %d times, want 1", n)
	}
	if n := s.liveTalkers("session_id", id); n != 1 {
		t.Errorf("%d live agents after Try again, want 1", n)
	}
	// Try again itself hands the message on, before any sweep: the failed
	// Run's copy is settled naming the fresh agent, none left to hand on.
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE '%and the tests?'
		AND failed_at IS NOT NULL AND error LIKE '%' || $2 || '%'`, run, next); n != 1 {
		t.Errorf("the failed Run's copy settled naming %s %d times, want once", next, n)
	}
	if n := s.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE r.session_id = $1 AND d.delivered_at IS NULL AND d.failed_at IS NULL AND r.id = $2`, id, run); n != 0 {
		t.Errorf("%d messages left unsettled on the failed Run, want none", n)
	}
	s.until("the fresh agent to answer the message", func() bool {
		said := s.said(next)
		return len(said) == 1 && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NOT NULL`, next) == 1
	})
	tk := &talking{world: s.world}
	tk.answeredOnce(next, "and the tests?")
	// Used: the latest agent is live, the failed one's messages taken.
	if status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/retry", nil); retryRefusal(status, out) != taken {
		t.Errorf("a second Try again: %s, want %s", retryRefusal(status, out), taken)
	}
}

func TestTryAgainAfterTheSweepHandedASessionsMessagesOnSaysANewAgentTookThem(t *testing.T) {
	s, id, run := failedResume(t)
	var next string
	s.until("the sweep to hand the message to a fresh agent", func() bool {
		next, _ = s.brainstorm(id)
		return next != run && len(s.said(next)) == 1
	})
	if status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/retry", nil); retryRefusal(status, out) != taken {
		t.Errorf("Try again after the sweep: %s, want %s", retryRefusal(status, out), taken)
	}
	if n := s.liveTalkers("session_id", id); n != 1 {
		t.Errorf("%d live agents, want 1", n)
	}
	(&talking{world: s.world}).answeredOnce(next, "and the tests?")
}

// failedConductor is a task whose conductor, started for "what changed?",
// failed to start, with "and now?" queued for it meanwhile; no sweep has
// run since it failed.
func failedConductor(t *testing.T) (w *world, task, run string) {
	w = conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	w.lux.FailStarts("dude.role=conductor", 1)
	// The start is held at its container until it is followed, so its
	// failure is recorded by its own follower with no sweep after it.
	reached, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	w.lux.OnStage = func(epoch int, stage string) {
		if stage == "container" {
			hold.Do(func() {
				close(reached)
				<-release
			})
		}
	}
	task = w.task()
	if status, out := w.chat(task, "what changed?"); status != 201 {
		t.Fatalf("the first message: %d %v", status, out)
	}
	run, _, _ = w.conductor(task)
	if status, out := w.chat(task, "and now?"); status != 200 || out["runId"] != run {
		t.Fatalf("the second message: %d %v, want it queued for %s", status, out, run)
	}
	w.until("the conductor's start followed at its container", func() bool {
		select {
		case <-reached:
		default:
			return false
		}
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.stage' AND payload->>'stage' = 'container'`, run) == 1
	})
	close(release)
	w.settled("the conductor's start to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run) == 1
	})
	return w, task, run
}

func TestTryAgainOnAConductorThatDidNotStartStartsAFreshOneWithItsMessagesOnce(t *testing.T) {
	w, task, run := failedConductor(t)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed' AND payload ? 'stage'`, run); n != 1 {
		t.Errorf("the failed start's run.failed names no stage")
	}
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	next, _ := out["runId"].(string)
	if status != 201 || next == "" || next == run {
		t.Fatalf("Try again: %d %v, want a fresh conductor", status, out)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'retries' = $2`, next, run); n != 1 {
		t.Errorf("the fresh conductor's run.created names the one it retries %d times, want 1", n)
	}
	if n := w.liveTalkers("task_id", task); n != 1 {
		t.Errorf("%d live conductors after Try again, want 1", n)
	}
	// Try again itself hands the queued message on, before any sweep: the
	// failed Run's copy is settled naming the fresh conductor.
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE '%and now?'
		AND failed_at IS NOT NULL AND error LIKE '%' || $2 || '%'`, run, next); n != 1 {
		t.Errorf("the failed conductor's queued message settled naming %s %d times, want once", next, n)
	}
	w.until("the fresh conductor to answer both messages", func() bool {
		said := w.said(next)
		return len(said) == 2
	})
	tk := &talking{world: w}
	tk.answeredOnce(next, "what changed?")
	tk.answeredOnce(next, "and now?")
	// The first message is shown once in Chat, on the failed Run.
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message' AND payload->>'text' = 'what changed?'`, task); n != 1 {
		t.Errorf("%d chat.message for the first message, want 1", n)
	}
	if status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{}); retryRefusal(status, out) != taken {
		t.Errorf("a second Try again: %s, want %s", retryRefusal(status, out), taken)
	}
}

func TestTryAgainAfterTheSweepHandedAConductorsMessagesOnSaysANewAgentTookThem(t *testing.T) {
	w, task, run := failedConductor(t)
	var next string
	w.until("the sweep to hand the queued message to a fresh conductor", func() bool {
		next, _, _ = w.conductor(task)
		return next != run && len(w.said(next)) == 1
	})
	if status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{}); retryRefusal(status, out) != taken {
		t.Errorf("Try again after the sweep: %s, want %s", retryRefusal(status, out), taken)
	}
	if n := w.liveTalkers("task_id", task); n != 1 {
		t.Errorf("%d live conductors, want 1", n)
	}
	(&talking{world: w}).answeredOnce(next, "and now?")
}

// failedConductorResume is a task whose conductor ran, was parked, and
// whose resume for "and now?" lux ended for good while it waited for a
// host; no sweep has run since it failed.
func failedConductorResume(t *testing.T) (w *world, task, run string) {
	w = conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	reached, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	w.lux.OnStage = func(epoch int, stage string) {
		if epoch == 2 && stage == "waiting" {
			hold.Do(func() {
				close(reached)
				<-release
			})
		}
	}
	task = w.task()
	if status, out := w.chat(task, "what changed?"); status != 201 {
		t.Fatalf("the first message: %d %v", status, out)
	}
	run, _, _ = w.conductor(task)
	w.until("the conductor to answer", func() bool { return len(w.said(run)) == 1 })
	w.lux.StopOnItsOwn(w.luxRunOf(run))
	w.until("the conductor parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, run) == 1
	})
	if status, out := w.chat(task, "and now?"); status != 200 || out["runId"] != run {
		t.Fatalf("the second message: %d %v, want it queued for %s", status, out, run)
	}
	w.until("the resume waiting for a host", func() bool {
		select {
		case <-reached:
			return true
		default:
			return false
		}
	})
	w.until("the resume followed", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.stage' AND payload->>'stage' = 'waiting'`, run) == 2
	})
	w.lux.CancelInLux(w.luxRunOf(run))
	close(release)
	w.settled("the resume to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run) == 1
	})
	return w, task, run
}

func TestTryAgainOnAConductorWhoseResumeFailedHandsTheMessageToAFreshOneOnce(t *testing.T) {
	w, task, run := failedConductorResume(t)
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	next, _ := out["runId"].(string)
	if status != 201 || next == "" || next == run {
		t.Fatalf("Try again: %d %v, want a fresh conductor", status, out)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'retries' = $2`, next, run); n != 1 {
		t.Errorf("the fresh conductor's run.created names the one it retries %d times, want 1", n)
	}
	// Briefed with the message the failed one never read, not the failed one's old briefing.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND strpos(prompt, 'and now?') > 0`, next); n != 1 {
		t.Errorf("the fresh conductor is not briefed with the unread message")
	}
	if n := w.liveTalkers("task_id", task); n != 1 {
		t.Errorf("%d live conductors after Try again, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE '%and now?'
		AND failed_at IS NOT NULL AND error LIKE '%' || $2 || '%'`, run, next); n != 1 {
		t.Errorf("the failed conductor's message settled naming %s %d times, want once", next, n)
	}
	w.until("the fresh conductor to answer the message", func() bool { return len(w.said(next)) == 1 })
	(&talking{world: w}).answeredOnce(next, "and now?")
	if status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{}); retryRefusal(status, out) != taken {
		t.Errorf("a second Try again: %s, want %s", retryRefusal(status, out), taken)
	}
}

// wokenLine is the reason dude wakes the conductor for in failedWokenConductor.
const wokenLine = "the decision the failed conductor never heard"

// failedWokenConductor is a task with no conductor that dude woke for a
// reason: the conductor it started, briefed with the wake note and no
// person's message, failed to start. No sweep has run since it failed.
func failedWokenConductor(t *testing.T) (w *world, task, run string) {
	w = conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	w.syncer.WakeWindow = 50 * time.Millisecond
	w.lux.FailStarts("dude.role=conductor", 1)
	reached, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	w.lux.OnStage = func(epoch int, stage string) {
		if stage == "container" {
			hold.Do(func() {
				close(reached)
				<-release
			})
		}
	}
	task = w.task()
	w.recordReason(task, "test:woken", wokenLine)
	w.until("the woken conductor's start followed at its container", func() bool {
		select {
		case <-reached:
		default:
			return false
		}
		run, _, _ = w.conductor(task)
		return run != "" && w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.stage' AND payload->>'stage' = 'container'`, run) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'conductor.woken' AND (payload->>'started')::bool`, run); n != 1 {
		t.Fatalf("the conductor was not started by a wake")
	}
	close(release)
	w.settled("the woken conductor's start to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run) == 1
	})
	return w, task, run
}

// toldWoken counts the notes that told the task's conductors wokenLine.
func (w *world) toldWoken(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'
		AND strpos(payload->>'text', $2) > 0`, task, wokenLine)
}

func TestTryAgainOnAWokenConductorThatDidNotStartCarriesItsReasonsOnce(t *testing.T) {
	w, task, run := failedWokenConductor(t)
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	next, _ := out["runId"].(string)
	if status != 201 || next == "" || next == run {
		t.Fatalf("Try again: %d %v, want a fresh conductor", status, out)
	}
	// The note's attempt is the fresh conductor's to hear, not failed on the old one.
	if n := w.count(`SELECT count(*) FROM conductor_wake_attempts WHERE conductor_run_id = $1 AND directive_id IS NULL
		AND heard_at IS NULL AND failed_at IS NULL`, next); n != 1 {
		t.Errorf("%d unheard, unfailed wake attempts on the fresh conductor, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wake_attempts WHERE conductor_run_id = $1`, run); n != 0 {
		t.Errorf("%d wake attempts left on the failed conductor, want none", n)
	}
	w.until("the fresh conductor to hear its briefing", func() bool {
		return len(w.said(next)) >= 1 &&
			w.count(`SELECT count(*) FROM conductor_wake_attempts WHERE conductor_run_id = $1 AND heard_at IS NOT NULL`, next) == 1
	})
	for range 3 {
		w.pump()
	}
	if n := w.toldWoken(task); n != 1 {
		t.Errorf("the reason was told %d times, want once", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, task); n != 0 {
		t.Errorf("%d reasons pending again, want none", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 2 {
		t.Errorf("%d conductors, want the failed one and the fresh one", n)
	}
	if n := w.liveTalkers("task_id", task); n != 1 {
		t.Errorf("%d live conductors, want 1", n)
	}
}

func TestTryAgainAfterTheSweepPutAWokenConductorsReasonsBackSaysANewAgentTakesThem(t *testing.T) {
	w, task, run := failedWokenConductor(t)
	// The sweep hands the failed conductor over, its reasons pending again,
	// and holds them for the window before it wakes the next conductor.
	w.syncer.WakeWindow = time.Hour
	w.until("the failed conductor's reasons put back", func() bool {
		return w.count(`SELECT count(*) FROM conductor_wake_attempts WHERE conductor_run_id = $1 AND failed_at IS NOT NULL`, run) == 1 &&
			w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, task) == 1
	})
	if status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{}); retryRefusal(status, out) != taken {
		t.Errorf("Try again after the sweep: %s, want %s", retryRefusal(status, out), taken)
	}
	if n := w.liveTalkers("task_id", task); n != 0 {
		t.Errorf("%d live conductors before the wake, want none", n)
	}
	w.syncer.WakeWindow = 50 * time.Millisecond
	w.until("the next conductor woken for the reasons", func() bool {
		next, _, _ := w.conductor(task)
		return next != run && len(w.said(next)) >= 1
	})
	for range 3 {
		w.pump()
	}
	if n := w.liveTalkers("task_id", task); n != 1 {
		t.Errorf("%d live conductors, want 1", n)
	}
	// Told once by the failed conductor's note, once by the next one's.
	if n := w.toldWoken(task); n != 2 {
		t.Errorf("the reason was told %d times, want twice (the failed note, then the next conductor's)", n)
	}
}
