package orchestrator_test

// Task relations' start gating (docs/design/task-relations.md, "Gating:
// every start path"): each guarded path refuses a blocked task with the
// same words, and lets the same task start once its blocker has merged.

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// keyOf is a task's key in the world's project (P-n).
func (w *world) keyOf(task string) string {
	w.t.Helper()
	return w.str(`SELECT p.key_prefix || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, task)
}

// relPerson is a person of the world's organisation, for the edges a test
// writes as theirs.
func (w *world) relPerson() string {
	id := "per_rel_" + w.org
	mustExec(w.t, w.owner, `INSERT INTO people (id, organization_id, name) VALUES ($1, $2, 'Rel') ON CONFLICT DO NOTHING`, id, w.org)
	return id
}

// blocks writes the edge blocker → task as a person's, past the writer's checks.
func (w *world) blocks(blocker, task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $2, $3, 'blocks', $4)`, w.org, blocker, task, w.relPerson())
}

// mergeByHand makes a task done with a merged pull request, by the owner
// role: a merge as the database records it, without the sync that
// releases dependants (the tests of that drive a real merge).
func (w *world) mergeByHand(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, task)
	mustExec(w.t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
			head_branch, base_branch, head_sha, title, state)
		VALUES ('pr_m_' || $1, $2, $3, $1, $4, 9000 + (SELECT number FROM tasks WHERE id = $1), 'u', 'h', 'main', 's', 't', 'merged')`,
		task, w.org, w.project, w.repoID)
}

// blockedPair is a blocker and the task it blocks, neither started.
func (w *world) blockedPair() (blocker, task, want string) {
	w.t.Helper()
	blocker, task = w.task(), w.task()
	w.blocks(blocker, task)
	return blocker, task, w.keyOf(task) + " is blocked by " + w.keyOf(blocker) + " (not started): it can start once that's merged."
}

// refusedBlocked checks an API answer is the 409 blocked with the words want.
func refusedBlocked(t *testing.T, what string, status int, body map[string]any, want string) {
	t.Helper()
	e, _ := body["error"].(map[string]any)
	if status != 409 || e["code"] != "blocked" || e["message"] != want {
		t.Fatalf("%s on a blocked task: %d %v, want 409 blocked %q", what, status, body, want)
	}
	details, _ := e["details"].(map[string]any)
	if bs, _ := details["blockers"].([]any); len(bs) == 0 {
		t.Fatalf("%s: the refusal names no blockers: %v", what, body)
	}
}

func (w *world) startRefusals(task, path string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.start_refused' AND payload->>'path' = $2`, task, path)
}

// Path 1: Deliver.
func TestDeliverRefusesABlockedTaskUntilItsBlockerMerges(t *testing.T) {
	w := newWorld(t)
	blocker, task, want := w.blockedPair()
	status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{})
	refusedBlocked(t, "deliver", status, body, want)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("a refused deliver made %d deliveries", n)
	}
	if w.startRefusals(task, "deliver") != 1 {
		t.Fatalf("the refusal was not recorded")
	}
	w.mergeByHand(blocker)
	if status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{}); status != 201 {
		t.Fatalf("deliver once the blocker merged: %d %v", status, body)
	}
}

// A blocker done with nothing merged still blocks: only merged work
// releases what waits on it.
func TestABlockerDoneWithNothingMergedStillBlocks(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, blocker)
	status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{})
	refusedBlocked(t, "deliver", status, body,
		w.keyOf(task)+" is blocked by "+w.keyOf(blocker)+" (done, nothing merged): it can start once that's merged.")
	status, body = w.call("/internal/tasks/"+task+"/talk", map[string]any{})
	if status != 409 {
		t.Fatalf("talk past a blocker done with nothing merged: %d %v", status, body)
	}
}

// Path 2: Talk it through.
func TestTalkItThroughRefusesABlockedTaskUntilItsBlockerMerges(t *testing.T) {
	w := conductorWorld(t)
	blocker, task, want := w.blockedPair()
	status, body := w.call("/internal/tasks/"+task+"/talk", map[string]any{})
	refusedBlocked(t, "talk", status, body, want)
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("a refused talk started %d runs", n)
	}
	if w.startRefusals(task, "talk") != 1 {
		t.Fatalf("the refusal was not recorded")
	}
	w.mergeByHand(blocker)
	if status, body := w.call("/internal/tasks/"+task+"/talk", map[string]any{}); status != 201 {
		t.Fatalf("talk once the blocker merged: %d %v", status, body)
	}
}

// Path 3: a first message in an unstarted task's Chat.
func TestAFirstChatMessageRefusesABlockedTaskUntilItsBlockerMerges(t *testing.T) {
	w := conductorWorld(t)
	blocker, task, want := w.blockedPair()
	status, body := w.call("/internal/tasks/"+task+"/chat", map[string]any{"text": "Let's start"})
	refusedBlocked(t, "a first message", status, body, want)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message'`, task); n != 0 {
		t.Fatalf("a refused message was kept")
	}
	if w.startRefusals(task, "chat") != 1 {
		t.Fatalf("the refusal was not recorded")
	}
	w.mergeByHand(blocker)
	if status, body := w.call("/internal/tasks/"+task+"/chat", map[string]any{"text": "Let's start"}); status != 201 {
		t.Fatalf("a first message once the blocker merged: %d %v", status, body)
	}
}

// Path 4: the conductor's first start_phase implement (and decide next at
// the start), on a task that gained a blocker while it planned.
func TestTheConductorsFirstImplementerIsRefusedWhileBlocked(t *testing.T) {
	w := conducting(t)
	blocker, task := w.task(), w.task()
	w.talk(task)
	w.blocks(blocker, task)
	want := w.keyOf(task) + " is blocked by " + w.keyOf(blocker) + " (not started): it can start once that's merged." +
		" You can't start it anyway; a person can. Tell the person, and end your turn: you are woken when it can start."
	w.refused(task, "start_phase", `{"phase":"implement"}`, want)
	w.refused(task, "decide", `{"action":"next"}`, want)
	if n := w.phaseRuns(task, "implement"); n != 0 {
		t.Fatalf("a refused start made %d implementers", n)
	}
	w.mergeByHand(blocker)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
}

// Path 5: the workflow's own implement step, the backstop for an edge
// added between the start and the step: it parks, the task goes back to
// queued saying what it waits on, and nothing fails.
func TestTheImplementStepParksABlockedDelivery(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.deliver(task)
	w.blocks(blocker, task)
	w.until("the delivery to park on its blocker", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'
			AND awaiting_signals ? 'relations.unblocked'`, task) == 1
	})
	if s := w.taskStatus(task); s != "queued" {
		t.Fatalf("a parked delivery's task is %s", s)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
		AND payload->>'reason' = $2`, task, "blocked by "+w.keyOf(blocker)); n != 1 {
		t.Fatalf("the park did not say what it waits on")
	}
	if n := w.phaseRuns(task, "implement"); n != 0 || w.startRefusals(task, "implement") != 1 {
		t.Fatalf("parked: %d implementers, %d refusals", n, w.startRefusals(task, "implement"))
	}
	// Woken while still blocked, it parks again.
	var wf string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM workflow_runs WHERE task_id = $1`, task).Scan(&wf)
	if err := w.runtime.Signal(context.Background(), w.org, wf, delivery.SignalRelationsUnblock, map[string]any{}, ""); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if n := w.phaseRuns(task, "implement"); n != 0 {
		t.Fatalf("a still-blocked delivery started an implementer")
	}
	w.mergeByHand(blocker)
	if err := w.runtime.Signal(context.Background(), w.org, wf, delivery.SignalRelationsUnblock, map[string]any{}, ""); err != nil {
		t.Fatal(err)
	}
	w.until("the implementer once the blocker merged", func() bool { return w.phaseRuns(task, "implement") == 1 })
}

// Path 6: Start over is a fresh attempt from main, so it is a start; Try
// again is not (path 7).
func TestStartOverRefusesABlockedTaskButTryAgainDoesNot(t *testing.T) {
	w := newWorld(t)
	task, _ := w.aborted()
	blocker := w.task()
	w.blocks(blocker, task)
	want := w.keyOf(task) + " is blocked by " + w.keyOf(blocker) + " (not started): it can start once that's merged."
	status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": "restart"})
	refusedBlocked(t, "start over", status, body, want)
	if w.startRefusals(task, "start_over") != 1 {
		t.Fatalf("the refusal was not recorded")
	}
	if s := w.taskStatus(task); s != "aborted" {
		t.Fatalf("a refused start over left the task %s", s)
	}
	w.mergeByHand(blocker)
	if status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": "restart"}); status != 200 {
		t.Fatalf("start over once the blocker merged: %d %v", status, body)
	}
}

// Path 7 stays unguarded: Try again carries on the same attempt.
func TestTryAgainIsNotGuarded(t *testing.T) {
	w := newWorld(t)
	task, _ := w.aborted()
	blocker := w.task()
	w.blocks(blocker, task)
	if status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("try again on a task that gained a blocker: %d %v", status, body)
	}
	w.until("the implementer again", func() bool { return w.phaseRuns(task, "implement") == 2 })
}

// Start anyway: a person may start a blocked task, recorded with the
// blockers and their note; a request with no person is refused 403.
func TestOnlyAPersonStartsABlockedTaskAnyway(t *testing.T) {
	w := newWorld(t)
	_, task, _ := w.blockedPair()
	status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{"override": true})
	if e, _ := body["error"].(map[string]any); status != 403 || e["code"] != "override_forbidden" ||
		e["message"] != "only a person can start a blocked task" {
		t.Fatalf("an override with no person: %d %v", status, body)
	}
	ana := w.person("Ana")
	status, body = w.callAs(ana, "/internal/tasks/"+task+"/deliver", map[string]any{"override": true, "note": "the sync is enough"})
	if status != 201 {
		t.Fatalf("a person's Start anyway: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.start_overridden'
		AND payload->>'how' = 'deliver' AND payload->>'note' = 'the sync is enough' AND jsonb_array_length(payload->'blockers') = 1
		AND actor_id = $2`, task, ana); n != 1 {
		t.Fatalf("the override was not recorded")
	}
	// Overridden, the implement step does not park it.
	w.until("the implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })

	// Start over anyway, likewise a person's.
	w2 := newWorld(t)
	t2, _ := w2.aborted()
	b2 := w2.task()
	w2.blocks(b2, t2)
	if status, body := w2.call("/internal/tasks/"+t2+"/recover", map[string]any{"action": "restart", "override": true}); status != 403 {
		t.Fatalf("start over anyway with no person: %d %v", status, body)
	}
	if status, body := w2.callAs(w2.person("Bo"), "/internal/tasks/"+t2+"/recover", map[string]any{"action": "restart", "override": true}); status != 200 {
		t.Fatalf("a person's start over anyway: %d %v", status, body)
	}
	if n := w2.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.start_overridden'`, t2); n != 1 {
		t.Fatalf("start over anyway was not recorded")
	}
}

// Start anyway on a delivery parked at its implement step: it goes on.
func TestStartAnywayReleasesAParkedDelivery(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.deliver(task)
	w.blocks(blocker, task)
	w.until("the delivery to park", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'`, task) == 1
	})
	if status, body := w.callAs(w.person("Ana"), "/internal/tasks/"+task+"/deliver", map[string]any{"override": true}); status != 200 {
		t.Fatalf("Start anyway on a parked delivery: %d %v", status, body)
	}
	w.until("the implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
}
