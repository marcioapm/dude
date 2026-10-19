package orchestrator_test

// Lifecycle's start rule (docs/design/tracking-work.md, "Start paths: one
// rule"): every path refuses an archived task, then a closed one, then a
// blocked one; the first two whatever a person's override says, and each
// lets the same task start again once it is unarchived and reopened.

import (
	"strings"
	"testing"
)

// closeByHand leaves the task as Close does: aborted, with a resolution.
func (w *world) closeByHand(task, resolution string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE tasks SET status = 'aborted', resolution = $2::task_resolution WHERE id = $1`, task, resolution)
}

// archiveByHand closes the task as Won't do when it hasn't ended, then archives it.
func (w *world) archiveByHand(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE tasks SET status = CASE WHEN status = 'done' THEN status ELSE 'aborted' END,
		resolution = CASE WHEN status = 'done' THEN resolution ELSE COALESCE(resolution, 'wont_do') END,
		archived_at = now(), archived_by = 'per_test' WHERE id = $1`, task)
}

// reopenByHand undoes both, as Unarchive then Reopen leave it.
func (w *world) reopenByHand(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE tasks SET archived_at = NULL, archived_by = NULL, resolution = NULL, status = 'received' WHERE id = $1`, task)
}

func (w *world) lifecycleRefusals(task, path, reason string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.start_refused'
		AND payload->>'path' = $2 AND payload->>'reason' = $3`, task, path, reason)
}

// refusedAs checks an API answer is the 409 code with the words want.
func refusedFor(t *testing.T, what string, status int, body map[string]any, code, want string) {
	t.Helper()
	e, _ := body["error"].(map[string]any)
	if status != 409 || e["code"] != code || e["message"] != want {
		t.Fatalf("%s: %d %v, want 409 %s %q", what, status, body, code, want)
	}
}

func closedWords(key string) string   { return key + " was closed as Won't do: reopen it to start it." }
func archivedWords(key string) string { return key + " is archived: unarchive it to start it." }

// Path 1: Deliver. Closed and archived are refused before blockers, a
// person's Start anyway passes neither, and the task delivers once reopened.
func TestDeliverRefusesAClosedOrArchivedTaskEvenOverridden(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	w.blocks(blocker, task)
	ana := w.person("Ana")
	w.closeByHand(task, "wont_do")
	status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{})
	refusedFor(t, "deliver on a closed task", status, body, "closed", closedWords(w.keyOf(task)))
	status, body = w.callAs(ana, "/internal/tasks/"+task+"/deliver", map[string]any{"override": true})
	refusedFor(t, "Start anyway on a closed task", status, body, "closed", closedWords(w.keyOf(task)))
	w.archiveByHand(task)
	status, body = w.callAs(ana, "/internal/tasks/"+task+"/deliver", map[string]any{"override": true})
	refusedFor(t, "Start anyway on an archived task", status, body, "archived", archivedWords(w.keyOf(task)))
	if w.lifecycleRefusals(task, "deliver", "closed") != 2 || w.lifecycleRefusals(task, "deliver", "archived") != 1 {
		t.Fatal("the refusals were not recorded with their reasons")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.start_overridden'`, task); n != 0 {
		t.Fatalf("an override of a closed task was recorded %d times", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("a refused deliver made %d deliveries", n)
	}
	w.reopenByHand(task)
	status, body = w.call("/internal/tasks/"+task+"/deliver", map[string]any{})
	refusedBlocked(t, "deliver once reopened, still blocked", status, body,
		w.keyOf(task)+" is blocked by "+w.keyOf(blocker)+" (not started): it can start once that's merged.")
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/deliver", map[string]any{"override": true}); status != 201 {
		t.Fatalf("Start anyway once reopened: %d %v", status, body)
	}
}

// Path 2: Talk it through.
func TestTalkItThroughRefusesAClosedOrArchivedTask(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	w.closeByHand(task, "wont_do")
	status, body := w.callAs(w.person("Ana"), "/internal/tasks/"+task+"/talk", map[string]any{"override": true})
	refusedFor(t, "talk on a closed task", status, body, "closed", closedWords(w.keyOf(task)))
	w.archiveByHand(task)
	status, body = w.call("/internal/tasks/"+task+"/talk", map[string]any{})
	refusedFor(t, "talk on an archived task", status, body, "archived", archivedWords(w.keyOf(task)))
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("a refused talk started %d runs", n)
	}
	if w.lifecycleRefusals(task, "talk", "archived") != 1 {
		t.Fatal("the refusal was not recorded")
	}
	w.reopenByHand(task)
	if status, body := w.call("/internal/tasks/"+task+"/talk", map[string]any{}); status != 201 {
		t.Fatalf("talk once reopened: %d %v", status, body)
	}
}

// Path 3: a first message in a closed task's Chat starts nothing, and no
// conductor: its owner reopens it first.
func TestAFirstChatMessageRefusesAClosedOrArchivedTask(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	w.closeByHand(task, "wont_do")
	status, body := w.call("/internal/tasks/"+task+"/chat", map[string]any{"text": "Let's start"})
	refusedFor(t, "a first message on a closed task", status, body, "closed", closedWords(w.keyOf(task)))
	w.archiveByHand(task)
	status, body = w.call("/internal/tasks/"+task+"/chat", map[string]any{"text": "Let's start"})
	refusedFor(t, "a first message on an archived task", status, body, "archived", archivedWords(w.keyOf(task)))
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("a refused message made %d runs", n)
	}
	if w.lifecycleRefusals(task, "chat", "closed") != 1 {
		t.Fatal("the refusal was not recorded")
	}
	w.reopenByHand(task)
	if status, body := w.call("/internal/tasks/"+task+"/chat", map[string]any{"text": "Let's start"}); status != 201 {
		t.Fatalf("a first message once reopened: %d %v", status, body)
	}
}

// Path 4: the conductor's first implementer on a task closed while it
// waited at the start is refused with the rule's words, overridden or not,
// and starts once the task is reopened.
func TestTheConductorsFirstImplementerIsRefusedOnAClosedTask(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.closeByHand(task, "wont_do")
	// A person's Start anyway marked the delivery: it does not pass Close.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"overridden": true}'::jsonb WHERE task_id = $1`, task)
	want := closedWords(w.keyOf(task)) + " Nobody can start it anyway until a person does that."
	w.refused(task, "start_phase", `{"phase":"implement"}`, want)
	w.refused(task, "decide", `{"action":"next"}`, want)
	w.archiveByHand(task)
	w.refused(task, "start_phase", `{"phase":"implement"}`, archivedWords(w.keyOf(task)))
	if n := w.phaseRuns(task, "implement"); n != 0 {
		t.Fatalf("a refused start made %d implementers", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.start_refused'
		AND payload->>'path' = 'start_phase' AND actor_type = 'agent' AND payload->>'reason' IN ('closed', 'archived')`, task); n != 3 {
		t.Fatalf("%d task.start_refused {path: start_phase, reason}, want one per refusal", n)
	}
	w.reopenByHand(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer", func() bool { return w.phaseRuns(task, "implement") == 1 })
}

// Path 6: Start over, anyway or not, passes neither; once reopened the
// task delivers again, as a new attempt.
func TestStartOverRefusesAClosedOrArchivedTaskEvenAnyway(t *testing.T) {
	w := newWorld(t)
	task, _ := w.aborted()
	w.closeByHand(task, "wont_do")
	bo := w.person("Bo")
	status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": "restart"})
	refusedFor(t, "start over on a closed task", status, body, "closed", closedWords(w.keyOf(task)))
	status, body = w.callAs(bo, "/internal/tasks/"+task+"/recover", map[string]any{"action": "restart", "override": true})
	refusedFor(t, "start over anyway on a closed task", status, body, "closed", closedWords(w.keyOf(task)))
	w.archiveByHand(task)
	status, body = w.callAs(bo, "/internal/tasks/"+task+"/recover", map[string]any{"action": "restart", "override": true})
	refusedFor(t, "start over anyway on an archived task", status, body, "archived", archivedWords(w.keyOf(task)))
	if s := w.taskStatus(task); s != "aborted" {
		t.Fatalf("a refused start over left the task %s", s)
	}
	if w.lifecycleRefusals(task, "start_over", "closed") != 2 || w.lifecycleRefusals(task, "start_over", "archived") != 1 {
		t.Fatal("the refusals were not recorded")
	}
	w.reopenByHand(task)
	if status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{}); status != 201 || body["alreadyRunning"] == true {
		t.Fatalf("deliver once reopened: %d %v", status, body)
	}
	w.until("the new attempt's implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND attempt = 2`, task) == 1
	})
}

// Path 7: Resume and Try again on a closed task are refused (the page
// offers Reopen instead); blockers still don't hold them.
func TestResumeAndTryAgainRefuseAClosedOrArchivedTask(t *testing.T) {
	w := newWorld(t)
	task, _ := w.aborted()
	if got, _ := w.options(task); strings.Join(got, " ") != "resume retry restart" {
		t.Fatalf("before the close: %v", got)
	}
	w.closeByHand(task, "wont_do")
	_, raw := w.get("/internal/tasks/"+task+"/recover", w.org)
	if got, _ := w.options(task); len(got) != 0 || !strings.Contains(raw, `"refused":"closed"`) {
		t.Fatalf("a closed task offers %v: %s", got, raw)
	}
	want := w.keyOf(task) + " was closed: reopen it to pick it back up."
	for _, action := range []string{"resume", "retry"} {
		status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": action})
		refusedFor(t, action+" on a closed task", status, body, "closed", want)
		if w.lifecycleRefusals(task, action, "closed") != 1 {
			t.Fatalf("%s's refusal was not recorded", action)
		}
	}
	w.archiveByHand(task)
	status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": "retry"})
	refusedFor(t, "try again on an archived task", status, body, "archived", archivedWords(w.keyOf(task)))
	if s := w.taskStatus(task); s != "aborted" {
		t.Fatalf("a refused pick-up left the task %s", s)
	}
	// Unarchived and the resolution cleared on the stopped task, it can be tried again.
	mustExec(t, w.owner, `UPDATE tasks SET archived_at = NULL, resolution = NULL WHERE id = $1`, task)
	if status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("try again once neither: %d %v", status, body)
	}
}

// Path 11: a start when unblocked that comes due on a closed or archived
// task starts nothing and ends, saying why.
func TestStartWhenUnblockedEndsOnAClosedOrArchivedTask(t *testing.T) {
	for _, put := range []string{"closed", "archived"} {
		t.Run(put, func(t *testing.T) {
			w := newWorld(t)
			task := w.task()
			w.autoStart(task, "deliver")
			mustExec(t, w.owner, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, task)
			w.closeByHand(task, "wont_do")
			if put == "archived" {
				w.archiveByHand(task)
			}
			if w.fireAutoStarts() != 1 {
				t.Fatal("the worker took no request")
			}
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 0 {
				t.Fatalf("a %s task was started by its request", put)
			}
			if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, task); n != 0 {
				t.Fatal("the request outlived the refusal")
			}
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_start_cancelled'
				AND payload->>'why' = $2`, task, put); n != 1 {
				t.Fatalf("the request's end does not say %s", put)
			}
			if w.lifecycleRefusals(task, "auto_start", put) != 1 {
				t.Fatal("the refusal was not recorded")
			}
		})
	}
}
