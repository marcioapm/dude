package orchestrator_test

// Close, Reopen, Archive and Unarchive through the orchestrator's API
// (docs/design/tracking-work.md, Epic 1, "Data, API, events"): each
// resolution, the duplicate's handover, Close and archive in one
// transaction, every refusal by its code, and who may.

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// ownedBy makes a person and gives them the task.
func (w *world) ownedBy(task, name string) string {
	w.t.Helper()
	key := w.person(name)
	w.assignOwner(task, key)
	return key
}

func (w *world) refusal(status int, body map[string]any) (int, string) {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return status, code
}

func (w *world) resolutionOf(task string) string {
	return w.str(`SELECT COALESCE(resolution::text, '') FROM tasks WHERE id = $1`, task)
}

// Won't do: aborted with its resolution, who closed it, the status move's
// reason, a pending start when unblocked ended, and task.closed with the note.
func TestCloseAsWontDo(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	w.autoStart(task, "deliver")
	status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do", "note": "not needed"})
	if status != 200 || body["resolution"] != "wont_do" {
		t.Fatalf("close: %d %v", status, body)
	}
	if s, r := w.taskStatus(task), w.resolutionOf(task); s != "aborted" || r != "wont_do" {
		t.Fatalf("closed: %s %s", s, r)
	}
	if n := w.count(`SELECT count(*) FROM tasks t JOIN api_keys k ON k.person_id = t.closed_by WHERE t.id = $1 AND k.id = $2`, task, ana); n != 1 {
		t.Fatal("closed_by is not the person")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
		AND payload->>'status' = 'aborted' AND payload->>'reason' = 'closed as won''t do by Ana'`, task); n != 1 {
		t.Fatal("the status move's reason is not said")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.closed' AND payload->>'resolution' = 'wont_do'
		AND payload->>'note' = 'not needed' AND actor_id = $2`, task, ana); n != 1 {
		t.Fatal("task.closed was not recorded")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_start_cancelled' AND payload->>'why' = 'closed'`, task); n != 1 {
		t.Fatal("the pending start when unblocked was not ended as closed")
	}
	// Already ended: refused.
	status, body = w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"})
	if s, code := w.refusal(status, body); s != 409 || code != "conflict" {
		t.Fatalf("close an ended task: %d %v", status, body)
	}
}

// Can't reproduce closes any task in this epic (the kind check is Epic 2's);
// Nothing to merge is not Close's; a resolution must be one of the three.
func TestCloseResolutions(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	for _, r := range []string{"nothing_to_merge", "obsolete", ""} {
		status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": r})
		if s, code := w.refusal(status, body); s != 400 || code != "bad_request" {
			t.Fatalf("close as %q: %d %v", r, status, body)
		}
	}
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "cant_reproduce"}); status != 200 {
		t.Fatalf("close as can't reproduce: %d %v", status, body)
	}
	if r := w.resolutionOf(task); r != "cant_reproduce" {
		t.Fatalf("resolution %q", r)
	}
}

// Duplicate: the edge, and what waited on the duplicate now waits on the
// task it duplicates, said in task.closed; Reopen takes it back.
func TestCloseAsDuplicateHandsItsBlockingOver(t *testing.T) {
	w := newWorld(t)
	canonical, dup, waiting := w.task(), w.task(), w.task()
	w.blocks(dup, waiting)
	ana := w.ownedBy(dup, "Ana")
	status, body := w.callAs(ana, "/internal/tasks/"+dup+"/close", map[string]any{"resolution": "duplicate", "duplicateOf": w.keyOf(canonical)})
	if status != 200 || body["duplicateOf"] != w.keyOf(canonical) {
		t.Fatalf("close as duplicate: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM task_relations WHERE from_task_id = $1 AND to_task_id = $2 AND kind = 'duplicates'`, dup, canonical); n != 1 {
		t.Fatal("no duplicates edge")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.closed' AND payload->>'duplicateOf' = $2
		AND payload->'handedOver' = jsonb_build_array($3::text)`, dup, w.keyOf(canonical), w.keyOf(waiting)); n != 1 {
		t.Fatal("task.closed does not say what was handed over")
	}
	// The waiting task is blocked by the canonical task now.
	status, body = w.call("/internal/tasks/"+waiting+"/deliver", map[string]any{})
	refusedBlocked(t, "deliver on a task waiting on a duplicate", status, body,
		w.keyOf(waiting)+" is blocked by "+w.keyOf(canonical)+" (not started): it can start once that's merged.")
	// Refusals of the picker: itself, and a duplicate.
	other := w.task()
	w.assignOwner(other, ana)
	for ref, want := range map[string]string{w.keyOf(other): "itself", w.keyOf(dup): "pick that one"} {
		status, body := w.callAs(ana, "/internal/tasks/"+other+"/close", map[string]any{"resolution": "duplicate", "duplicateOf": ref})
		e, _ := body["error"].(map[string]any)
		if msg, _ := e["message"].(string); status/100 != 4 || !strings.Contains(msg, want) {
			t.Fatalf("a duplicate of %s: %d %v", ref, status, body)
		}
	}
	// Reopen: the edge goes, and the waiting task waits on the duplicate itself again.
	if status, body := w.callAs(ana, "/internal/tasks/"+dup+"/reopen", map[string]any{}); status != 200 {
		t.Fatalf("reopen: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM task_relations WHERE from_task_id = $1 AND kind = 'duplicates'`, dup); n != 0 {
		t.Fatal("the duplicates edge outlived Reopen")
	}
	if s, r := w.taskStatus(dup), w.resolutionOf(dup); s != "received" || r != "" {
		t.Fatalf("reopened: %s %q", s, r)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.reopened' AND payload->>'was' = 'duplicate'`, dup); n != 1 {
		t.Fatal("task.reopened was not recorded")
	}
	status, body = w.call("/internal/tasks/"+waiting+"/deliver", map[string]any{})
	refusedBlocked(t, "deliver after the reopen", status, body,
		w.keyOf(waiting)+" is blocked by "+w.keyOf(dup)+" (not started): it can start once that's merged.")
}

// A duplicate whose handover would close a loop is refused, naming it.
func TestCloseAsDuplicateRefusesALoop(t *testing.T) {
	w := newWorld(t)
	canonical, dup, waiting := w.task(), w.task(), w.task()
	w.blocks(dup, waiting)
	w.blocks(waiting, canonical)
	ana := w.ownedBy(dup, "Ana")
	status, body := w.callAs(ana, "/internal/tasks/"+dup+"/close", map[string]any{"resolution": "duplicate", "duplicateOf": w.keyOf(canonical)})
	e, _ := body["error"].(map[string]any)
	details, _ := e["details"].(map[string]any)
	loop, _ := details["loop"].([]any)
	if status != 409 || e["code"] != "loop" || len(loop) == 0 || !strings.Contains(e["message"].(string), w.keyOf(canonical)) {
		t.Fatalf("a loop through the handover: %d %v", status, body)
	}
	if w.taskStatus(dup) != "received" || w.count(`SELECT count(*) FROM task_relations WHERE kind = 'duplicates' AND from_task_id = $1`, dup) != 0 {
		t.Fatal("a refused close wrote something")
	}
}

// Remove the relation: the blocks edges from the closed task go in the
// same transaction, one task.relation_removed each, and what waited can start.
func TestCloseRemovesItsRelationsWhenAsked(t *testing.T) {
	w := newWorld(t)
	task, a, b := w.task(), w.task(), w.task()
	w.blocks(task, a)
	w.blocks(task, b)
	ana := w.ownedBy(task, "Ana")
	status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do", "removeRelations": true})
	removed, _ := body["removedRelations"].([]any)
	if status != 200 || len(removed) != 2 {
		t.Fatalf("close and remove: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM task_relations WHERE from_task_id = $1`, task); n != 0 {
		t.Fatalf("%d edges left", n)
	}
	for _, d := range []string{a, b} {
		if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.relation_removed'`, d); n != 1 {
			t.Fatalf("task.relation_removed on the dependant: %d", n)
		}
	}
	if status, body := w.call("/internal/tasks/"+a+"/deliver", map[string]any{}); status != 201 {
		t.Fatalf("the dependant could not start: %d %v", status, body)
	}
	// Without it, a Won't do keeps blocking.
	w2 := newWorld(t)
	t2, d2 := w2.task(), w2.task()
	w2.blocks(t2, d2)
	if status, _ := w2.callAs(w2.ownedBy(t2, "Ana"), "/internal/tasks/"+t2+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatal("close")
	}
	if status, _ := w2.call("/internal/tasks/"+d2+"/deliver", map[string]any{}); status != 409 {
		t.Fatal("a Won't do blocker released its dependant")
	}
}

// Running and in review are refused in the menu's words, Close and
// Archive alike.
func TestCloseAndArchiveRefuseRunningAndReview(t *testing.T) {
	w := newWorld(t)
	task, _ := w.aborted()
	ana := w.ownedBy(task, "Ana")
	// A task whose agents are working.
	running := w.task()
	w.assignOwner(running, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'running' WHERE id = $1`, running)
	review := w.task()
	w.assignOwner(review, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'review' WHERE id = $1`, review)
	for _, c := range []struct{ task, code, words string }{{running, "running", "Stop its agents first"}, {review, "review", "It has a pull request open"}} {
		for _, route := range []string{"close", "archive"} {
			status, body := w.callAs(ana, "/internal/tasks/"+c.task+"/"+route, map[string]any{"resolution": "wont_do"})
			e, _ := body["error"].(map[string]any)
			if status != 409 || e["code"] != c.code || !strings.HasPrefix(e["message"].(string), c.words) {
				t.Fatalf("%s on a %s task: %d %v", route, c.code, status, body)
			}
		}
	}
	// A stopped task (aborted by a person, no resolution) can be closed.
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close a stopped task: %d %v", status, body)
	}
}

// Archive: an ended task at once; one not ended needs a resolution
// (not_ended), and with one is closed and archived in one transaction:
// task.closed {andArchived} then task.archived {viaClose}, caused by it.
func TestArchiveAndCloseAndArchive(t *testing.T) {
	w := newWorld(t)
	open := w.task()
	ana := w.ownedBy(open, "Ana")
	status, body := w.callAs(ana, "/internal/tasks/"+open+"/archive", map[string]any{})
	if s, code := w.refusal(status, body); s != 409 || code != "not_ended" {
		t.Fatalf("archive an open task without a resolution: %d %v", status, body)
	}
	// A refused close inside the archive leaves nothing: one transaction.
	status, body = w.callAs(ana, "/internal/tasks/"+open+"/archive", map[string]any{"resolution": "duplicate", "duplicateOf": w.keyOf(open)})
	if status != 400 || w.taskStatus(open) != "received" || w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NOT NULL`, open) != 0 {
		t.Fatalf("a refused close and archive: %d %v", status, body)
	}
	status, body = w.callAs(ana, "/internal/tasks/"+open+"/archive", map[string]any{"resolution": "wont_do", "note": "no longer"})
	if status != 200 {
		t.Fatalf("close and archive: %d %v", status, body)
	}
	if w.resolutionOf(open) != "wont_do" || w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NOT NULL`, open) != 1 {
		t.Fatal("not closed and archived")
	}
	var closedID, archivedCause string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM events WHERE task_id = $1 AND event_type = 'task.closed'
		AND (payload->>'andArchived')::boolean`, open).Scan(&closedID)
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(causation_id, '') FROM events WHERE task_id = $1 AND event_type = 'task.archived'
		AND (payload->>'viaClose')::boolean`, open).Scan(&archivedCause)
	if closedID == "" || archivedCause != closedID {
		t.Fatalf("one batch: task.closed %q, task.archived caused by %q", closedID, archivedCause)
	}
	var order []string
	rows, _ := w.owner.Query(context.Background(), `SELECT event_type FROM events WHERE task_id = $1
		AND event_type IN ('task.closed', 'task.archived') ORDER BY cursor`, open)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		order = append(order, s)
	}
	rows.Close()
	if !slices.Equal(order, []string{"task.closed", "task.archived"}) {
		t.Fatalf("order %v", order)
	}
	// Archived: Reopen refused (archived), Close refused (archived), archive again refused.
	for route, code := range map[string]string{"reopen": "archived", "close": "archived", "archive": "conflict"} {
		status, body := w.callAs(ana, "/internal/tasks/"+open+"/"+route, map[string]any{"resolution": "wont_do"})
		if s, c := w.refusal(status, body); s != 409 || c != code {
			t.Fatalf("%s on an archived task: %d %v, want %s", route, status, body, code)
		}
	}
	// Unarchive, then Reopen: two steps.
	if status, body := w.callAs(ana, "/internal/tasks/"+open+"/unarchive", map[string]any{}); status != 200 {
		t.Fatalf("unarchive: %d %v", status, body)
	}
	if w.resolutionOf(open) != "wont_do" || w.events(open, "task.unarchived") != 1 {
		t.Fatal("unarchive changed the resolution, or said nothing")
	}
	if status, body := w.callAs(ana, "/internal/tasks/"+open+"/reopen", map[string]any{}); status != 200 {
		t.Fatalf("reopen once unarchived: %d %v", status, body)
	}
	// An ended task archives with no body.
	done := w.task()
	w.assignOwner(done, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done', resolution = 'nothing_to_merge' WHERE id = $1`, done)
	if status, body := w.callAs(ana, "/internal/tasks/"+done+"/archive", map[string]any{}); status != 200 {
		t.Fatalf("archive an ended task: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.archived' AND NOT payload ? 'viaClose'`, done); n != 1 {
		t.Fatal("plain task.archived was not recorded")
	}
}

// Who: the owner or an admin; another member 403 in the menu's words; a
// request with no person (an agent's, a service's) 403.
func TestOnlyTheOwnerOrAnAdminClosesOrArchives(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	w.ownedBy(task, "Ana")
	bo := w.person("Bo")
	status, body := w.callAs(bo, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"})
	e, _ := body["error"].(map[string]any)
	if status != 403 || e["message"] != "Only Ana, the owner, or an admin can do this" {
		t.Fatalf("another member closes: %d %v", status, body)
	}
	if status, _ := w.call("/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 403 {
		t.Fatalf("no person closes: %d", status)
	}
	mustExec(t, w.owner, `UPDATE people SET role = 'admin' WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, bo)
	if status, body := w.callAs(bo, "/internal/tasks/"+task+"/archive", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("an admin closes and archives: %d %v", status, body)
	}
}

// Mark done names "done, nothing merged": resolution nothing_to_merge.
func TestMarkDoneWritesNothingToMerge(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET status = 'review' WHERE id = $1`, task)
	if status, body := w.call("/internal/tasks/"+task+"/done", map[string]any{}); status != 200 {
		t.Fatalf("mark done: %d %v", status, body)
	}
	if r := w.resolutionOf(task); r != "nothing_to_merge" {
		t.Fatalf("resolution %q", r)
	}
	// It is not "closed": Reopen refuses it.
	ana := w.ownedBy(task, "Ana")
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/reopen", map[string]any{}); status != 409 {
		t.Fatalf("reopen a done task: %d %v", status, body)
	}
}

// An epic with unended tasks is refused, naming them; withEndedTasks
// archives those that ended and leaves the epic; once all have, the epic
// and its tasks go together, and Unarchive brings back those it took.
func TestArchiveAnEpic(t *testing.T) {
	w := newWorld(t)
	epic := "epc_" + w.org
	mustExec(t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Webhooks')`, epic, w.org, w.project)
	open, done, alone := w.task(), w.task(), w.task()
	mustExec(t, w.owner, `UPDATE tasks SET epic_id = $1 WHERE id IN ($2, $3, $4)`, epic, open, done, alone)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, done)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'aborted', resolution = 'wont_do', archived_at = now() - interval '1 day' WHERE id = $1`, alone)
	ana := w.person("Ana")
	for _, task := range []string{open, done, alone} {
		w.assignOwner(task, ana)
	}
	status, body := w.callAs(ana, "/internal/epics/"+epic+"/archive", map[string]any{})
	e, _ := body["error"].(map[string]any)
	details, _ := e["details"].(map[string]any)
	tasks, _ := details["tasks"].([]any)
	if status != 409 || e["code"] != "unfinished" || len(tasks) != 1 || tasks[0].(map[string]any)["key"] != w.keyOf(open) {
		t.Fatalf("archive an epic with an open task: %d %v", status, body)
	}
	status, body = w.callAs(ana, "/internal/epics/"+epic+"/archive", map[string]any{"withEndedTasks": true})
	if status != 200 || body["archived"] != false {
		t.Fatalf("archive its ended tasks: %d %v", status, body)
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NOT NULL`, done) != 1 ||
		w.count(`SELECT count(*) FROM epics WHERE id = $1 AND archived_at IS NULL`, epic) != 1 {
		t.Fatal("the ended task should be archived and the epic not")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.archived' AND payload->>'viaEpic' = $2`, done, epic); n != 1 {
		t.Fatal("task.archived {viaEpic} missing")
	}
	// Close the last one, then the epic goes with it.
	if status, body := w.callAs(ana, "/internal/tasks/"+open+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	status, body = w.callAs(ana, "/internal/epics/"+epic+"/archive", map[string]any{})
	if status != 200 || body["archived"] != true {
		t.Fatalf("archive the epic: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE event_type = 'epic.archived' AND payload->>'epicId' = $1 AND (payload->>'tasks')::int = 1`, epic); n != 1 {
		t.Fatal("epic.archived missing")
	}
	// A task of an archived epic can't be unarchived alone.
	status, body = w.callAs(ana, "/internal/tasks/"+open+"/unarchive", map[string]any{})
	if s, c := w.refusal(status, body); s != 409 || c != "archived" {
		t.Fatalf("unarchive a task of an archived epic: %d %v", status, body)
	}
	status, body = w.callAs(ana, "/internal/epics/"+epic+"/unarchive", map[string]any{})
	restored, _ := body["tasks"].([]any)
	if status != 200 || len(restored) != 1 || restored[0] != w.keyOf(open) {
		t.Fatalf("unarchive the epic: %d %v", status, body)
	}
	// The task archived on its own the day before, and the one archived by withEndedTasks, stay archived.
	if w.count(`SELECT count(*) FROM tasks WHERE id IN ($1, $2) AND archived_at IS NOT NULL`, alone, done) != 2 {
		t.Fatal("unarchiving the epic brought back tasks archived apart from it")
	}
}
