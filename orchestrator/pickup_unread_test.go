package orchestrator_test

// A pick-up gives a resumed Run what its stop left unread (a fix round's
// brief, stopped by a person before its agent read it), ahead of the
// pick-up note; and a Run's open question cancelled because the Run was
// replaced leaves the task waiting on nobody.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// tickUntil pumps as pump does, but looks at cond after each of the
// delivery's steps, before the sweep: what a step created is caught
// before lux is told of it.
func (w *world) tickUntil(what string, cond func() bool) {
	w.t.Helper()
	ctx := context.Background()
	for range 2000 {
		for range 5 {
			n, err := w.runtime.Tick(ctx, 1)
			if err != nil {
				w.t.Fatal(err)
			}
			if cond() {
				return
			}
			if n == 0 {
				break
			}
		}
		if _, err := w.syncer.Sweep(ctx); err != nil {
			w.t.Fatal(err)
		}
		if _, err := w.syncer.SettlePublishes(ctx); err != nil {
			w.t.Fatal(err)
		}
		if _, err := w.syncer.RetireCompleted(ctx); err != nil {
			w.t.Fatal(err)
		}
		if _, err := w.artifacts.Sweep(ctx); err != nil {
			w.t.Fatal(err)
		}
		if _, err := phases.NotifyFinished(ctx, w.app, func(ctx context.Context, org, wf, runID, status, key string) error {
			return w.runtime.Signal(ctx, org, wf, delivery.SignalPhaseFinished, map[string]string{"runId": runID, "status": status}, key)
		}); err != nil {
			w.t.Fatal(err)
		}
		time.Sleep(untilPoll)
	}
	w.t.Fatalf("timed out waiting for %s\nruns:\n%s", what, w.describeRuns())
}

// fixRoundCreated starts a resuming delivery whose review finds
// something, and returns as the step creates the fix round, before the
// sweep resumes the implementer's lux Run with its brief; ana owns the
// task.
func fixRoundCreated(t *testing.T) (w *world, task, ana, fix string) {
	w = resuming(t)
	w.onModel("reviewer", fakeagent.StuckModel)
	task = w.task()
	ana = w.ownedBy(task, "Ana")
	w.names(task, w.repoID)
	w.start(task, resumingPolicy())
	w.tickUntil("the fix round created", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task) == 1
	})
	return w, task, ana, w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
}

// implementerAsking delivers a task whose implementer asks a person
// something, every other phase hanging, and returns once the task waits
// on the answer.
func implementerAsking(t *testing.T) (w *world, task, run string) {
	w = newWorld(t)
	w.withTools()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: fakeagent.Question, Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	task = w.task()
	w.deliver(task)
	w.until("the implementer to ask", func() bool { return w.taskStatus(task) == "awaiting_input" })
	return w, task, w.implementer(task)
}

// resumedInputs waits for the task's implementer, resumed, to be given
// the pick-up note, and returns everything its lux Run was given.
func (w *world) resumedInputs(task, note string) []string {
	w.t.Helper()
	implID := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	w.until("the fixer to take up the note", func() bool {
		lr := w.luxRunFor(implID)
		if lr == nil {
			return false
		}
		for _, in := range w.lux.Inputs(lr.ID) {
			if strings.Contains(in, note) {
				return true
			}
		}
		return false
	})
	return w.lux.Inputs(w.luxRunFor(implID).ID)
}

// The fix round continues the implementer
// (run.continues how=resume) with the findings queued as its brief; the
// owner's Stop comes before the agent reads it, failing the brief; Resume
// with a note. The agent is given the findings, then the note.
func TestAResumedFixRoundIsGivenTheBriefItNeverRead(t *testing.T) {
	w, task, ana, fix := fixRoundCreated(t)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'how' = $3`,
		fix, delivery.EvRunContinues, delivery.ContinuesResume); n != 1 {
		t.Fatalf("the fix round does not continue the implementer's session")
	}
	brief := w.str(`SELECT id FROM directives WHERE run_id = $1 ORDER BY created_at LIMIT 1`, fix)
	if text := w.str(`SELECT text FROM directives WHERE id = $1`, brief); !strings.Contains(text, "## Review findings") {
		t.Fatalf("the fix round's brief: %q", text)
	}
	// Stopped before its agent read it.
	w.stop(ana, task, map[string]any{"note": "hold on"})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.failed'
		AND payload->>'directiveId' = $2 AND payload->>'error' = $3`, fix, brief, delivery.UnreadRunAborted); n != 1 {
		t.Fatalf("the brief did not fail as unread on the Stop:\n%s", w.describeRuns())
	}
	w.until("the fix round kept", func() bool { return w.keptRun(fix) })
	w.pickUp(ana, task, map[string]any{"action": "resume", "note": "go on with the findings"})
	inputs := w.resumedInputs(task, "go on with the findings")
	findings, note := -1, -1
	for i, in := range inputs {
		if strings.Contains(in, "## Review findings") && findings < 0 {
			findings = i
		}
		if strings.Contains(in, "go on with the findings") {
			note = i
		}
	}
	if findings < 0 || note < 0 || findings > note {
		t.Fatalf("the resumed fixer was given (findings at %d, note at %d):\n%s", findings, note, strings.Join(inputs, "\n---\n"))
	}
	// The ledger says it was given again.
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND resends = $2`, fix, brief); n != 1 {
		t.Errorf("%d re-deliveries of the brief recorded, want 1", n)
	}
}

// A Run asked a person something and is restarted; its question is
// cancelled with it. Nothing else waits on a person: the task leaves
// awaiting_input for what the delivery is doing.
func TestARestartedRunsCancelledQuestionLeavesTheTaskNotWaiting(t *testing.T) {
	w, task, run := implementerAsking(t)
	if n := w.count(`SELECT count(*) FROM questions WHERE run_id = $1 AND status = 'open'`, run); n != 1 {
		t.Fatalf("%d open questions on the implementer", n)
	}
	status, out := w.call("/internal/runs/"+run+"/restart", map[string]any{"note": "start again"})
	if status != 200 && status != 201 {
		t.Fatalf("restart: %d %v", status, out)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE run_id = $1 AND status = 'open'`, run); n != 0 {
		t.Fatalf("the restarted Run's question is still open")
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND status = 'open'`, task); n == 0 {
		if got := w.taskStatus(task); got == "awaiting_input" {
			t.Fatalf("the task is %s with no open question", got)
		}
	}
	if got := w.taskStatus(task); got != "running" {
		t.Errorf("task %s after the restart, want running", got)
	}
}

// A restarted Run's question cancelled with another open on the task: the restart cancels the
// Run's own, but the task still waits on a person, and stays
// awaiting_input.
func TestARestartedRunsCancelledQuestionLeavesAnotherQuestionWaiting(t *testing.T) {
	w, task, run := implementerAsking(t)
	// Another Run of the task asked a person something, still open.
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, ended_at)
		SELECT 'run_other_asker', organization_id, project_id, task_id, attempt, 'completed', 'review', now() FROM runs WHERE id = $1`, run)
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, items)
		SELECT 'q_other', organization_id, task_id, 'run_other_asker', 'Which one?', '[]', jsonb_build_array(jsonb_build_object('prompt', 'Which one?', 'options', '[]'::jsonb)) FROM runs WHERE id = $1`, run)
	status, out := w.call("/internal/runs/"+run+"/restart", map[string]any{"note": "start again"})
	if status != 200 && status != 201 {
		t.Fatalf("restart: %d %v", status, out)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE run_id = $1 AND status = 'open'`, run); n != 0 {
		t.Fatalf("the restarted Run's question is still open")
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE id = 'q_other' AND status = 'open'`); n != 1 {
		t.Fatalf("the other question is no longer open")
	}
	for range 3 {
		w.pump()
	}
	if got := w.taskStatus(task); got != "awaiting_input" {
		t.Errorf("task %s after the restart with another question open, want awaiting_input", got)
	}
}

// Several directives the Stop left unread are given back in the order
// they were queued, all before the pick-up note: the findings, then a
// person's steer sent after the fix round was created, then the note.
func TestAResumedFixRoundIsGivenWhatItNeverReadInOrder(t *testing.T) {
	w, task, ana, fix := fixRoundCreated(t)
	if status, body := w.call("/internal/runs/"+fix+"/steer", map[string]any{"text": "also check the README"}); status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	w.stop(ana, task, map[string]any{"note": "hold on"})
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND error = $2`, fix, delivery.UnreadRunAborted); n != 2 {
		t.Fatalf("%d directives failed unread on the Stop, want the brief and the steer:\n%s", n, w.describeRuns())
	}
	w.until("the fix round kept", func() bool { return w.keptRun(fix) })
	w.pickUp(ana, task, map[string]any{"action": "resume", "note": "go on with the findings"})
	inputs := w.resumedInputs(task, "go on with the findings")
	findings, steer, note := -1, -1, -1
	for i, in := range inputs {
		if strings.Contains(in, "## Review findings") && findings < 0 {
			findings = i
		}
		if strings.Contains(in, "also check the README") && steer < 0 {
			steer = i
		}
		if strings.Contains(in, "go on with the findings") {
			note = i
		}
	}
	if findings < 0 || steer < 0 || note < 0 || !(findings < steer && steer < note) {
		t.Fatalf("the resumed fixer was given findings at %d, steer at %d, note at %d:\n%s", findings, steer, note,
			strings.Join(inputs, "\n---\n"))
	}
}
