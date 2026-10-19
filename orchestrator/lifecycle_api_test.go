package orchestrator_test

// Close, Reopen, Archive and Unarchive through the orchestrator's API
// (docs/design/tracking-work.md, Epic 1, "Data, API, events"): each
// resolution, the duplicate's handover, Close and archive in one
// transaction, every refusal by its code, and who may.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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
	// The canonical task can't be closed as a duplicate in turn: what waits
	// on it through its duplicate would wait on a closed task.
	third := w.task()
	w.assignOwner(canonical, ana)
	status, body = w.callAs(ana, "/internal/tasks/"+canonical+"/close", map[string]any{"resolution": "duplicate", "duplicateOf": w.keyOf(third)})
	e, _ := body["error"].(map[string]any)
	msg, _ := e["message"].(string)
	if want := w.keyOf(canonical) + " is what " + w.keyOf(dup) + " duplicates: reopen " + w.keyOf(dup) + " first."; status != 409 || e["code"] != "conflict" || msg != want {
		t.Fatalf("close a canonical task as a duplicate: %d %v", status, body)
	}
	if got := w.str(`SELECT string_agg(id, ',') FROM task_blocked_by($1)`, waiting); got != canonical || w.resolutionOf(canonical) != "" {
		t.Fatalf("a refused close changed something: blocked by %q, resolution %q", got, w.resolutionOf(canonical))
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
	task, a, b, c, x := w.task(), w.task(), w.task(), w.task(), w.task()
	w.blocks(task, a)
	w.blocks(task, b)
	w.blocks(x, task)
	mustExec(t, w.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, LEAST($2, $3), GREATEST($2, $3), 'related', $4)`, w.org, task, c, w.relPerson())
	ana := w.ownedBy(task, "Ana")
	status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do", "removeRelations": true})
	removed, _ := body["removedRelations"].([]any)
	if status != 200 || len(removed) != 2 {
		t.Fatalf("close and remove: %d %v", status, body)
	}
	// Only this task's own blocks edges went: what blocks it, and its related link, stay.
	rows, err := w.owner.Query(context.Background(), `SELECT from_task_id || ' ' || kind || ' ' || to_task_id FROM task_relations
		WHERE organization_id = $1 ORDER BY 1`, w.org)
	if err != nil {
		t.Fatal(err)
	}
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(left)
	want := []string{x + " blocks " + task, min(task, c) + " related " + max(task, c)}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("edges left: %v, want %v", left, want)
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
	// Queued, its status says nothing of the agent that works on it.
	working := w.task()
	w.assignOwner(working, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'queued' WHERE id = $1`, working)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_working_' || $3, $1, $2, $3, 1, 'running', 'implement', 'implementer')`, w.org, w.project, working)
	// Stopped, with its pull request still open: any open one refuses.
	stoppedPR := w.task()
	w.assignOwner(stoppedPR, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'aborted' WHERE id = $1`, stoppedPR)
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
			head_branch, base_branch, head_sha, title, state)
		VALUES ('pr_o_' || $1, $2, $3, $1, $4, 8000 + (SELECT number FROM tasks WHERE id = $1), 'u', 'h', 'main', 's', 't', 'open')`,
		stoppedPR, w.org, w.project, w.repoID)
	for _, c := range []struct{ task, code, words string }{{running, "running", "Stop its agents first"}, {review, "review", "It has a pull request open"},
		{working, "running", "Stop its agents first"}, {stoppedPR, "review", "It has a pull request open: close it on GitHub first."}} {
		for _, route := range []string{"close", "archive"} {
			status, body := w.callAs(ana, "/internal/tasks/"+c.task+"/"+route, map[string]any{"resolution": "wont_do"})
			e, _ := body["error"].(map[string]any)
			if status != 409 || e["code"] != c.code || !strings.HasPrefix(e["message"].(string), c.words) {
				t.Fatalf("%s on a %s task: %d %v", route, c.code, status, body)
			}
		}
		if w.resolutionOf(c.task) != "" || w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NOT NULL`, c.task) != 0 {
			t.Fatal("a refused close or archive wrote something")
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
	rows, err := w.owner.Query(context.Background(), `SELECT event_type FROM events WHERE task_id = $1
		AND event_type IN ('task.closed', 'task.archived') ORDER BY cursor`, open)
	if err != nil {
		t.Fatal(err)
	}
	order, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"task.closed", "task.archived"}) {
		t.Fatalf("order %v", order)
	}
	// One writing transaction: both events carry its id.
	if n := w.count(`SELECT count(DISTINCT xmin::text) FROM events WHERE task_id = $1
		AND event_type IN ('task.closed', 'task.archived')`, open); n != 1 {
		t.Fatalf("Close and archive wrote in %d transactions", n)
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

// A merge of the canonical task releases what its duplicate blocked: the
// blocking it was handed is released by its merge, as its own would be.
func TestTheCanonicalTasksMergeReleasesItsDuplicatesDependant(t *testing.T) {
	w := newWorld(t)
	canonical, dup, waiting := w.task(), w.task(), w.task()
	w.blocks(dup, waiting)
	ana := w.ownedBy(dup, "Ana")
	if status, body := w.callAs(ana, "/internal/tasks/"+dup+"/close", map[string]any{"resolution": "duplicate", "duplicateOf": w.keyOf(canonical)}); status != 200 {
		t.Fatalf("close as duplicate: %d %v", status, body)
	}
	w.mergeFor(canonical)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.unblocked' AND payload->>'by' = $2`,
		waiting, w.keyOf(canonical)); n != 1 {
		t.Fatalf("task.unblocked on the duplicate's dependant: %d", n)
	}
	if status, body := w.call("/internal/tasks/"+waiting+"/deliver", map[string]any{}); status != 201 {
		t.Fatalf("deliver once the canonical task merged: %d %v", status, body)
	}
}

// A Close and archive whose archive half fails, after the close half has
// written, leaves the task as it was: the close is rolled back with it.
func TestCloseAndArchiveRollsBackTheCloseWhenTheArchiveFails(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	// This database's tasks refuse any archived_by: only the archive's write sets one.
	mustExec(t, w.owner, `ALTER TABLE tasks ADD CONSTRAINT test_no_archiver CHECK (archived_by IS NULL) NOT VALID`)
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/archive", map[string]any{"resolution": "wont_do"}); status != 500 {
		t.Fatalf("close and archive with a failing archive: %d %v", status, body)
	}
	if r, s := w.resolutionOf(task), w.taskStatus(task); r != "" || s != "received" {
		t.Fatalf("the close half outlived the failed archive: %q %s", r, s)
	}
	if n := w.events(task, "task.closed"); n != 0 {
		t.Fatalf("%d task.closed after the rollback", n)
	}
}

// A task closed and reopened twice, its first fresh attempt parked on a
// blocker without a phase Run, delivers a new attempt each time: it never
// joins the delivery that ended.
func TestEachReopenDeliversAFreshAttempt(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	ana := w.ownedBy(task, "Ana")
	w.deliver(task)
	w.blocks(blocker, task)
	parkedOnBlocker := func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'
			AND awaiting_signals ? 'relations.unblocked'`, task) == 1
	}
	w.until("the first delivery to park on its blocker", parkedOnBlocker)
	for cycle := 1; cycle <= 2; cycle++ {
		if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
			t.Fatalf("close %d: %d %v", cycle, status, body)
		}
		if status, body := w.callAs(ana, "/internal/tasks/"+task+"/reopen", map[string]any{}); status != 200 {
			t.Fatalf("reopen %d: %d %v", cycle, status, body)
		}
		status, body := w.callAs(ana, "/internal/tasks/"+task+"/deliver", map[string]any{"override": true})
		if status != 201 || body["alreadyRunning"] == true {
			t.Fatalf("deliver after reopen %d: %d %v", cycle, status, body)
		}
		if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, task); n != 1 {
			t.Fatalf("after reopen %d: %d live deliveries", cycle, n)
		}
		if cycle == 1 {
			// Its override cleared, it parks on the blocker again before any phase Run.
			mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'overridden' WHERE task_id = $1 AND status IN ('running', 'waiting')`, task)
			w.until("the fresh attempt to park on its blocker", parkedOnBlocker)
		}
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, task); n != 3 {
		t.Fatalf("%d deliveries, want one per start", n)
	}
}

// A delivery parked at implement on a blocker, closed through Close: the
// blocker's merge (its signal) makes no implementer on the closed task.
func TestAClosedParkedDeliveryStartsNoImplementerOnTheUnblock(t *testing.T) {
	w := newWorld(t)
	blocker, task := w.task(), w.task()
	ana := w.ownedBy(task, "Ana")
	w.deliver(task)
	w.blocks(blocker, task)
	w.until("the delivery to park on its blocker", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'implementRun' AND status = 'waiting'`, task) == 1
	})
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close a parked delivery's task: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, task); n != 1 {
		t.Fatal("Close did not end the parked delivery")
	}
	w.mergeFor(blocker)
	w.pump()
	if n := w.phaseRuns(task, "implement"); n != 0 {
		t.Fatalf("%d implementers on a closed task", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, task); n != 1 {
		t.Fatal("the closed task's delivery came back")
	}
	// The race: a poller claimed the step the unblock woke before Close
	// committed. The step, run now, parks without an error and makes no Run.
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'running', step = 'implementRun' WHERE task_id = $1`, task)
	w.pump()
	if n := w.phaseRuns(task, "implement"); n != 0 {
		t.Fatalf("the claimed step made %d implementers on a closed task", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'waiting' AND last_error IS NULL`, task); n != 1 {
		t.Fatal("the claimed step on a closed task did not park")
	}
	// The same, for an overridden step, which reads no blockers.
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'running', step = 'implementRun',
		state = state || '{"overridden": true}'::jsonb WHERE task_id = $1`, task)
	w.pump()
	if n := w.phaseRuns(task, "implement"); n != 0 {
		t.Fatalf("an overridden claimed step made %d implementers on a closed task", n)
	}
	// Close committing after the step's own check: its insert refuses, in
	// the transaction that holds the delivery.
	store := &delivery.Store{DB: w.app}
	wf := w.str(`SELECT id FROM workflow_runs WHERE task_id = $1`, task)
	_, err := store.CreatePhaseRun(context.Background(), w.org,
		delivery.PhaseRun{TaskID: task, WorkflowID: wf, Phase: delivery.PhaseImplement, Key: "late-implement"})
	if !errors.Is(err, delivery.ErrDeliveryEnded) || w.phaseRuns(task, "implement") != 0 {
		t.Fatalf("an implementer created on a closed task: %v, %d Runs", err, w.phaseRuns(task, "implement"))
	}
	// Close and Reopen both commit after the step's own check: the task is
	// open again, but the delivery the step belongs to has ended. The step
	// above parked it, so it is ended again as Close leaves it.
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'completed', wake_at = NULL, awaiting_signals = '[]'::jsonb,
		locked_by = NULL, locked_until = NULL WHERE task_id = $1`, task)
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/reopen", map[string]any{}); status != 200 {
		t.Fatalf("reopen: %d %v", status, body)
	}
	_, err = store.CreatePhaseRun(context.Background(), w.org,
		delivery.PhaseRun{TaskID: task, WorkflowID: wf, Phase: delivery.PhaseImplement, Key: "late-implement-reopened"})
	if !errors.Is(err, delivery.ErrDeliveryEnded) || w.phaseRuns(task, "implement") != 0 {
		t.Fatalf("an implementer for an ended delivery after reopen: %v, %d Runs", err, w.phaseRuns(task, "implement"))
	}
	// A new delivery started since (B): the first one's step still makes
	// no implementer, and B's Runs are its own.
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/deliver", map[string]any{"override": true}); status != 201 {
		t.Fatalf("deliver after reopen: %d %v", status, body)
	}
	b := w.str(`SELECT id FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, task)
	before := w.phaseRuns(task, "implement")
	_, err = store.CreatePhaseRun(context.Background(), w.org,
		delivery.PhaseRun{TaskID: task, WorkflowID: wf, Phase: delivery.PhaseImplement, Key: "late-implement-replaced"})
	if !errors.Is(err, delivery.ErrDeliveryEnded) || w.phaseRuns(task, "implement") != before {
		t.Fatalf("an implementer for a replaced delivery: %v, %d Runs, want %d", err, w.phaseRuns(task, "implement"), before)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE id = $1 AND status IN ('running', 'waiting')`, b); n != 1 {
		t.Fatal("the refused insert touched the live delivery")
	}
}

// An epic's archive is an admin's, or the owner's of every task it
// archives; its unarchive likewise, the owner read as taskOwner reads it
// (the first active person).
func TestArchiveAnEpicIsItsTasksOwnersOrAnAdmins(t *testing.T) {
	w := newWorld(t)
	epic := "epc_" + w.org
	mustExec(t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Webhooks')`, epic, w.org, w.project)
	anas, bos := w.task(), w.task()
	mustExec(t, w.owner, `UPDATE tasks SET epic_id = $1, status = 'done' WHERE id IN ($2, $3)`, epic, anas, bos)
	ana := w.ownedBy(anas, "Ana")
	bo := w.ownedBy(bos, "Bo")
	mustExec(t, w.owner, `UPDATE people SET role = 'member' WHERE id IN (SELECT person_id FROM api_keys WHERE id IN ($1, $2))`, ana, bo)
	for _, body := range []map[string]any{{}, {"withEndedTasks": true}} {
		status, out := w.callAs(ana, "/internal/epics/"+epic+"/archive", body)
		if s, code := w.refusal(status, out); s != 403 || code != "not_owner" {
			t.Fatalf("a member archives an epic with another's task (%v): %d %v", body, status, out)
		}
	}
	if w.count(`SELECT count(*) FROM tasks WHERE epic_id = $1 AND archived_at IS NOT NULL`, epic) != 0 ||
		w.count(`SELECT count(*) FROM epics WHERE id = $1 AND archived_at IS NOT NULL`, epic) != 0 || w.events(anas, "task.archived") != 0 {
		t.Fatal("a refused epic archive changed a row")
	}
	cy := w.person("Cy")
	mustExec(t, w.owner, `UPDATE people SET role = 'admin' WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, cy)
	if status, out := w.callAs(cy, "/internal/epics/"+epic+"/archive", map[string]any{}); status != 200 || out["archived"] != true {
		t.Fatalf("an admin archives the epic: %d %v", status, out)
	}
	status, out := w.callAs(ana, "/internal/epics/"+epic+"/unarchive", map[string]any{})
	if s, code := w.refusal(status, out); s != 403 || code != "not_owner" {
		t.Fatalf("a member unarchives an epic with another's task: %d %v", status, out)
	}
	if w.count(`SELECT count(*) FROM epics WHERE id = $1 AND archived_at IS NOT NULL`, epic) != 1 {
		t.Fatal("a refused unarchive changed the epic")
	}
	// Bo leaves; Ana, next in Bo's task, is its owner now and may unarchive.
	mustExec(t, w.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		SELECT $1, person_id, organization_id, 1 FROM api_keys WHERE id = $2`, bos, ana)
	mustExec(t, w.owner, `UPDATE people SET removed_at = now() WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, bo)
	if status, out := w.callAs(ana, "/internal/epics/"+epic+"/unarchive", map[string]any{}); status != 200 {
		t.Fatalf("the effective owner unarchives: %d %v", status, out)
	}
}

// Who: the owner or an admin; another member 403 in the menu's words; a
// request with no person (an agent's, a service's) 403.
func TestOnlyTheOwnerOrAnAdminClosesOrArchives(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	w.ownedBy(task, "Ana")
	bo := w.person("Bo")
	for _, route := range []string{"close", "archive", "reopen", "unarchive"} {
		status, body := w.callAs(bo, "/internal/tasks/"+task+"/"+route, map[string]any{"resolution": "wont_do"})
		e, _ := body["error"].(map[string]any)
		if status != 403 || e["message"] != "Only Ana, the owner, or an admin can do this" {
			t.Fatalf("another member's %s: %d %v", route, status, body)
		}
	}
	if w.resolutionOf(task) != "" || w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NULL`, task) != 1 {
		t.Fatal("a member's refused request wrote something")
	}
	if status, _ := w.call("/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 403 {
		t.Fatalf("no person closes: %d", status)
	}
	mustExec(t, w.owner, `UPDATE people SET role = 'admin' WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, bo)
	if status, body := w.callAs(bo, "/internal/tasks/"+task+"/archive", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("an admin closes and archives: %d %v", status, body)
	}
}

// Another organisation's task and epic do not exist here, for an admin too.
func TestLifecycleRoutesDoNotReachAnotherOrganisation(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	mustExec(t, w.owner, `UPDATE people SET role = 'admin' WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, ana)
	other := "org_other_" + w.org
	mustExec(t, w.owner, `INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, other)
	mustExec(t, w.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_o_' || $1, $1, 'O', 'o', 'O')`, other)
	mustExec(t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ('epc_o_' || $1, $1, 'prj_o_' || $1, 'Theirs')`, other)
	mustExec(t, w.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status, resolution)
		VALUES ('wi_o_' || $1, $1, 'prj_o_' || $1, 1, 'Theirs', 'x', '[]'::jsonb, 'done', 'nothing_to_merge')`, other)
	for _, path := range []string{"/internal/tasks/wi_o_" + other + "/close", "/internal/tasks/wi_o_" + other + "/archive",
		"/internal/tasks/wi_o_" + other + "/reopen", "/internal/tasks/wi_o_" + other + "/unarchive",
		"/internal/epics/epc_o_" + other + "/archive", "/internal/epics/epc_o_" + other + "/unarchive"} {
		if status, body := w.callAs(ana, path, map[string]any{"resolution": "wont_do"}); status != 404 {
			t.Fatalf("%s: %d %v", path, status, body)
		}
	}
	if n := w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1 AND (archived_at IS NOT NULL OR resolution IS DISTINCT FROM 'nothing_to_merge')`, other); n != 0 {
		t.Fatal("another organisation's task was archived or reopened")
	}
	if n := w.count(`SELECT count(*) FROM epics WHERE organization_id = $1 AND archived_at IS NOT NULL`, other); n != 0 {
		t.Fatal("another organisation's epic was archived")
	}
}

// Two Closes of one task at once: one closes it, the other is refused as
// ended (409), never a 500.
func TestTwoClosesAtOnce(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	ana := w.ownedBy(task, "Ana")
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			b, _ := json.Marshal(map[string]any{"resolution": "wont_do"})
			req, _ := http.NewRequest("POST", w.api+"/internal/tasks/"+task+"/close", bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer svc")
			req.Header.Set("X-Dude-Organization", w.org)
			req.Header.Set("X-Dude-Actor", ana)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			res.Body.Close()
			statuses <- res.StatusCode
		}()
	}
	got := []int{<-statuses, <-statuses}
	slices.Sort(got)
	if !slices.Equal(got, []int{200, 409}) {
		t.Fatalf("two Closes: %v", got)
	}
	if n := w.events(task, "task.closed"); n != 1 {
		t.Fatalf("%d task.closed", n)
	}
}

// An epic's unarchive and the unarchive of one of its tasks, at once, in
// either interleaving: each ends with its answer or a refusal, never a
// deadlock. A third transaction holds the row the first of them queues on,
// so both are waiting before either goes on.
func TestUnarchiveAnEpicAndItsTaskAtOnce(t *testing.T) {
	for _, c := range []struct {
		name, fence string
		taskFirst   bool
	}{
		// The epic's unarchive holds the epic and waits on the task's row.
		{"the epic's first", `SELECT 1 FROM epics WHERE id = $1 FOR UPDATE`, false},
		// The task's unarchive gets as far as it can before the epic's starts.
		{"the task's first", `SELECT 1 FROM tasks WHERE epic_id = $1 FOR UPDATE`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			ctx := context.Background()
			epic := "epc_" + w.org
			task := w.task()
			mustExec(t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title, archived_at) VALUES ($1, $2, $3, 'Webhooks', now())`,
				epic, w.org, w.project)
			mustExec(t, w.owner, `UPDATE tasks SET epic_id = $1, status = 'done', archived_at = (SELECT archived_at FROM epics WHERE id = $1)
				WHERE id = $2`, epic, task)
			a := delivery.Actor{ActorType: "human", ActorID: "test"}
			fence, err := w.owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Rollback(ctx)
			if _, err := fence.Exec(ctx, c.fence, epic); err != nil {
				t.Fatal(err)
			}
			// Until n of this database's sessions wait on a lock.
			waitFor := func(n int) {
				deadline := time.Now().Add(10 * time.Second)
				for got := 0; got != n; time.Sleep(10 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("%d sessions waiting on a lock, want %d", got, n)
					}
					if err := w.app.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
						WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&got); err != nil {
						t.Fatal(err)
					}
				}
			}
			taskErr, epicErr := make(chan error, 1), make(chan error, 1)
			unarchiveTask := func() {
				taskErr <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error { return delivery.UnarchiveTaskTx(ctx, tx, w.org, task, a) })
			}
			unarchiveEpic := func() {
				epicErr <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
					_, err := delivery.UnarchiveEpicTx(ctx, tx, w.org, epic, a)
					return err
				})
			}
			first, second := unarchiveEpic, unarchiveTask
			if c.taskFirst {
				first, second = unarchiveTask, unarchiveEpic
			}
			go first()
			waitFor(1)
			go second()
			waitFor(2)
			if err := fence.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var refusal delivery.LifecycleRefusal
			if err := <-epicErr; err != nil {
				t.Fatalf("the epic's unarchive: %v", err)
			}
			if err := <-taskErr; err != nil && !errors.As(err, &refusal) {
				t.Fatalf("the task's unarchive: %v", err)
			}
			if n := w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND archived_at IS NULL`, task); n != 1 {
				t.Fatal("the task is still archived")
			}
		})
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
	// Unless a pull request merged: then it is plain done.
	merged := w.task()
	w.mergedPR(merged)
	if status, body := w.call("/internal/tasks/"+merged+"/done", map[string]any{}); status != 200 {
		t.Fatalf("mark done with a merged pull request: %d %v", status, body)
	}
	if r := w.resolutionOf(merged); r != "" || w.taskStatus(merged) != "done" {
		t.Fatalf("done with a merge: resolution %q", r)
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
