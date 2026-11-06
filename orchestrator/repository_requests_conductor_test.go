package orchestrator_test

// Conductor-first repository requests (design A11, owner decision Q3), end
// to end through the fake agent, lux and GitHub: a conducted writer's
// request is its conductor's (no repository.requested, nothing pushed, the
// task running); the conductor's read approval brings the repository to
// the waiting Run and resumes it once; write is the owner's alone, never an
// admin's who is not the owner.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// heldRequest is a conducted task whose implementer asked for the
// project's "web" repository (write, when write) and waits on it, parked:
// the world, the task, the implementer's Run and the request.
func heldRequest(t *testing.T, write bool) (w *world, task, runID, reqID string) {
	t.Helper()
	w = conducting(t)
	w.syncer.ParkAfter = 300 * time.Millisecond
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	scripted := w.lux.Decide
	args := `{"repository":"web","reason":"the client","wait":true}`
	if write {
		args = `{"repository":"web","write":true,"reason":"the client","wait":true}`
	}
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{CallTools: [][2]string{{"request_repository", args}},
				Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
		}
		return scripted(spec)
	}
	task = w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer parked on its request", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND dude_pause = 'person'`, task).Scan(&runID)
		return runID != ""
	})
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM repository_requests WHERE run_id = $1`, runID).Scan(&reqID)
	if n := w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND addressee = 'conductor'`, reqID); n != 1 {
		t.Fatalf("the request is not the conductor's")
	}
	if n := w.events(task, "repository.requested"); n != 0 {
		t.Fatalf("%d repository.requested for a request its conductor holds", n)
	}
	if s := w.taskStatus(task); s == "awaiting_input" {
		t.Fatalf("the task waits on a person for its conductor's request")
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = $2`, task, "repository-request:"+reqID); n != 1 {
		t.Fatalf("%d conductor wakes for the request, want one", n)
	}
	return w, task, runID, reqID
}

// The conductor approves read access to a repository of the project: it
// joins the task, the parked Run is resumed once with it checked out, and
// the approval is recorded as the conductor's.
func TestTheConductorApprovesReadAndTheRunResumesOnce(t *testing.T) {
	w, task, runID, reqID := heldRequest(t, false)
	w.must(task, "decide_repository_request", `{"id":"`+reqID+`","action":"approve"}`)
	w.refused(task, "decide_repository_request", `{"id":"`+reqID+`","action":"deny"}`, "held by you")
	w.until("the clone and the implementer finishing", func() bool {
		return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, reqID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	lr := w.luxRunOf(runID)
	if got := w.lux.ResumeInputs(lr); len(got) != 1 || !strings.Contains(got[0], "web is checked out at") {
		t.Errorf("resumed with %q, want once with web", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'repository.approved' AND payload->>'by' = 'conductor'`, task); n != 1 {
		t.Errorf("%d approvals recorded as the conductor's, want one", n)
	}
	if n := w.count(`SELECT count(*) FROM task_repositories WHERE task_id = $1 AND repository_id = $2 AND access = 'read'`, task, "repo_web_"+w.org); n != 1 {
		t.Errorf("the repository did not join the task read-only")
	}
	if n := w.events(task, "repository.requested"); n != 0 {
		t.Errorf("%d repository.requested: the person was asked", n)
	}
}

// Write access is the owner's: the conductor's approval is refused, and it
// forwards it (one repository.requested); an admin who is not the owner is
// refused (403) as any non-owner; the owner approves.
func TestWriteAccessIsTheOwnersAlone(t *testing.T) {
	w, task, runID, reqID := heldRequest(t, true)
	ana, cy := w.ownedBy(task, "Ana"), w.person("Cy")
	mustExec(t, w.owner, `UPDATE people SET role = 'admin' WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, cy)
	w.refused(task, "decide_repository_request", `{"id":"`+reqID+`","action":"approve"}`, "only the task's owner approves write access")
	w.must(task, "decide_repository_request", `{"id":"`+reqID+`","action":"forward"}`)
	if n := w.events(task, "repository.requested"); n != 1 {
		t.Fatalf("%d repository.requested after the forward, want one", n)
	}
	if status, body := w.callAs(cy, "/internal/repository-requests/"+reqID+"/decide", map[string]any{"approve": true}); status != 403 {
		t.Fatalf("an admin who is not the owner approved write: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'pending'`, reqID); n != 1 {
		t.Fatalf("the refused approval changed the request")
	}
	if status, body := w.callAs(ana, "/internal/repository-requests/"+reqID+"/decide", map[string]any{"approve": true}); status != 200 {
		t.Fatalf("the owner's approval: %d %v", status, body)
	}
	w.until("the implementer resumed with web", func() bool {
		return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, reqID) == 1
	})
	if got := w.lux.ResumeInputs(w.luxRunOf(runID)); len(got) != 1 {
		t.Errorf("resumed %d times, want once", len(got))
	}
}

// The owner may decide a request its conductor still holds (an override).
func TestTheOwnerDecidesARequestTheConductorHolds(t *testing.T) {
	w, task, _, reqID := heldRequest(t, false)
	ana := w.ownedBy(task, "Ana")
	if status, body := w.callAs(ana, "/internal/repository-requests/"+reqID+"/decide", map[string]any{"approve": false, "note": "no"}); status != 200 {
		t.Fatalf("the owner's decision: %d %v", status, body)
	}
	w.refused(task, "decide_repository_request", `{"id":"`+reqID+`","action":"approve"}`, "held by you")
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'repository.denied' AND payload->>'by' = 'person'`, task); n != 1 {
		t.Errorf("%d denials by the person, want one", n)
	}
}
