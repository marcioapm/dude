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
