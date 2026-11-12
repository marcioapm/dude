package api_test

import (
	"context"
	"strings"
	"testing"
)

// A person pins their own project fact and unpins it, each recorded in
// the ledger with who did it; another member may not pin it, an admin
// may. What may not be pinned is refused with the reason.
func TestAPersonPinsAndUnpinsTheirFact(t *testing.T) {
	m := setupMemory(t)
	status, created := m.do(t, "POST", "/internal/memory/memories",
		`{"projectId": "prj_m", "title": "The tools mirror goes down sometimes", "content": "Retry the install once."}`)
	if status != 201 {
		t.Fatalf("create: %d %v", status, created)
	}
	id := created["id"].(string)
	path := "/internal/memory/memories/" + id

	if status, _ := m.do(t, "POST", path+"/pin", "", "X-Dude-Person", "per_bo"); status != 403 {
		t.Errorf("another member pinned Ana's fact: %d", status)
	}
	status, out := m.do(t, "POST", path+"/pin", "")
	if status != 200 || out["pinnedAt"] == nil || out["pinnedBy"] != "Ana Ribeiro" {
		t.Fatalf("Ana's pin: %d %v", status, out)
	}
	status, out = m.do(t, "POST", path+"/unpin", "")
	if status != 200 || out["pinnedAt"] != nil {
		t.Errorf("Ana's unpin: %d %v", status, out)
	}
	if _, err := m.owner.Exec(context.Background(), `UPDATE people SET role = 'admin' WHERE id = 'per_bo'`); err != nil {
		t.Fatal(err)
	}
	if status, out := m.do(t, "POST", path+"/pin", "", "X-Dude-Person", "per_bo", "X-Dude-Role", "admin"); status != 200 || out["pinnedBy"] != "Bo Lind" {
		t.Errorf("an admin's pin: %d %v", status, out)
	}

	rows, err := m.owner.Query(context.Background(), `SELECT event_type || ' ' || actor_id FROM events
		WHERE event_type IN ('memory.pinned', 'memory.unpinned') AND payload->>'memoryId' = $1 ORDER BY cursor`, id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	rows.Close()
	if want := "memory.pinned per_ana,memory.unpinned per_ana,memory.pinned per_bo"; strings.Join(got, ",") != want {
		t.Errorf("ledger %q, want %q", strings.Join(got, ","), want)
	}

	// A pinned fact's edit that makes it unpinnable is refused.
	if status, out := m.do(t, "PATCH", path, `{"kind": "note"}`, "X-Dude-Person", "per_bo", "X-Dude-Role", "admin"); status != 422 || !strings.Contains(message(out), "unpin it first") {
		t.Errorf("a pinned fact made a note: %d %v", status, out)
	}
}

// What is not a project's fact about no task is refused a pin: the
// organization's, a note, a fact about a task, an archived one.
func TestOnlyAProjectsOwnFactIsPinned(t *testing.T) {
	m := setupMemory(t)
	for body, want := range map[string]string{
		`{"title": "Org-wide", "content": "x"}`:                                                                   "only a project's memory",
		`{"projectId": "prj_m", "title": "A note", "content": "x", "kind": "note"}`:                               "only a fact",
		`{"projectId": "prj_m", "title": "On a task", "content": "x", "about": [{"type": "task", "id": "wi_m"}]}`: "about a task",
	} {
		status, created := m.do(t, "POST", "/internal/memory/memories", body)
		if status != 201 {
			t.Fatalf("create %s: %d %v", body, status, created)
		}
		status, out := m.do(t, "POST", "/internal/memory/memories/"+created["id"].(string)+"/pin", "")
		if msg := message(out); status != 422 || !strings.Contains(msg, want) {
			t.Errorf("%s pinned: %d %v, want a refusal saying %q", body, status, out, want)
		}
	}
	_, created := m.do(t, "POST", "/internal/memory/memories", `{"projectId": "prj_m", "title": "Gone", "content": "x"}`)
	id := created["id"].(string)
	m.do(t, "POST", "/internal/memory/memories/"+id+"/archive", "")
	if status, out := m.do(t, "POST", "/internal/memory/memories/"+id+"/pin", ""); status != 422 {
		t.Errorf("an archived fact pinned: %d %v", status, out)
	}
	var n int
	if err := m.owner.QueryRow(context.Background(), `SELECT count(*) FROM memories WHERE pinned_at IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d pinned (%v)", n, err)
	}
}

// message is an API refusal's message.
func message(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["message"].(string)
	return s
}

// A brainstorm session's memory is its members' alone: a member who may
// change it is still refused a pin.
func TestASessionsMemoryIsNotPinned(t *testing.T) {
	m := setupMemory(t)
	for _, q := range []string{
		`INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ses_p', $1, 'S', 'person')`,
		`INSERT INTO session_people (session_id, person_id, organization_id, role, invited_at, accepted_at) VALUES ('ses_p', 'per_ana', $1, 'owner', now(), now())`,
		`INSERT INTO memories (id, organization_id, session_id, title, content, author_kind, author_person_id) VALUES ('mem_ses', $1, 'ses_p', 'S', 'x', 'person', 'per_ana')`,
	} {
		if _, err := m.owner.Exec(context.Background(), q, m.org); err != nil {
			t.Fatal(err)
		}
	}
	if status, out := m.do(t, "POST", "/internal/memory/memories/mem_ses/pin", ""); status != 422 || !strings.Contains(message(out), "session's memory is not pinned") {
		t.Errorf("a session's memory pinned: %d %v", status, out)
	}
}

// A pinned fact's edit that would make it one Pin refuses — about a task,
// or moved to the whole organization — is refused, and it stays pinned
// and as it was.
func TestAPinnedFactsEditCannotMakeItUnpinnable(t *testing.T) {
	m := setupMemory(t)
	_, created := m.do(t, "POST", "/internal/memory/memories", `{"projectId": "prj_m", "title": "Pinned", "content": "x"}`)
	id := created["id"].(string)
	path := "/internal/memory/memories/" + id
	if status, out := m.do(t, "POST", path+"/pin", ""); status != 200 {
		t.Fatalf("pin: %d %v", status, out)
	}
	for _, body := range []string{
		`{"about": [{"type": "task", "id": "wi_m"}]}`,
		`{"projectId": ""}`,
	} {
		if status, out := m.do(t, "PATCH", path, body); status != 422 || !strings.Contains(message(out), "unpin it first") {
			t.Errorf("PATCH %s on a pinned fact: %d %v", body, status, out)
		}
		var pinned bool
		var project string
		var refs int
		if err := m.owner.QueryRow(context.Background(), `SELECT pinned_at IS NOT NULL, coalesce(project_id, ''),
				(SELECT count(*) FROM memory_refs WHERE memory_id = $1) FROM memories WHERE id = $1`, id).Scan(&pinned, &project, &refs); err != nil {
			t.Fatal(err)
		}
		if !pinned || project != "prj_m" || refs != 0 {
			t.Errorf("after PATCH %s: pinned %v, project %q, %d refs", body, pinned, project, refs)
		}
	}
}

// Pinning what is pinned changes nothing: one memory.pinned event, the
// same pinned_at.
func TestPinningTwiceIsOnePin(t *testing.T) {
	m := setupMemory(t)
	_, created := m.do(t, "POST", "/internal/memory/memories", `{"projectId": "prj_m", "title": "Pinned", "content": "x"}`)
	id := created["id"].(string)
	path := "/internal/memory/memories/" + id
	pinnedAt := func() string {
		var at string
		if err := m.owner.QueryRow(context.Background(), `SELECT pinned_at::text FROM memories WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if status, out := m.do(t, "POST", path+"/pin", ""); status != 200 {
		t.Fatalf("pin: %d %v", status, out)
	}
	first := pinnedAt()
	if status, out := m.do(t, "POST", path+"/pin", ""); status != 200 || out["pinnedAt"] == nil {
		t.Fatalf("the second pin: %d %v", status, out)
	}
	if again := pinnedAt(); again != first {
		t.Errorf("pinned_at moved from %s to %s", first, again)
	}
	var n int
	if err := m.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = 'memory.pinned' AND payload->>'memoryId' = $1`, id).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d memory.pinned events (%v), want 1", n, err)
	}
}
