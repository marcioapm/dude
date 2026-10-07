package orchestrator_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// memoryEvents are the ledger events about a memory after cursor that
// viewer may read, by the ledger's own visibility predicate (the one the
// public history and the live stream apply: session_visible).
func (s *sessionWorld) memoryEvents(viewer, memoryID string, after int64) []string {
	s.t.Helper()
	var out []string
	if err := s.app.InOrg(t0(), s.org, func(tx pgx.Tx) error {
		rows, err := tx.Query(t0(), `SELECT event_type || ' ' || payload::text FROM events
			WHERE cursor > $1 AND payload->>'memoryId' = $2 AND session_visible(session_id, $3) ORDER BY cursor`, after, memoryID, viewer)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *sessionWorld) cursor() int64 {
	s.t.Helper()
	var c int64
	if err := s.owner.QueryRow(t0(), `SELECT coalesce(max(cursor), 0) FROM events`).Scan(&c); err != nil {
		s.t.Fatal(err)
	}
	return c
}

// A member changing a session's memory — its title, archiving it,
// restoring it — records that on the session: its members read the event,
// nobody else does (an invitee, someone else, an admin not in it), and
// the private title goes nowhere else. An organisation memory's changes
// stay everyone's, as before.
func TestChangingASessionMemoryIsRecordedOnTheSession(t *testing.T) {
	for _, action := range []string{"update", "archive", "restore"} {
		t.Run(action, func(t *testing.T) {
			s := newSessionWorld(t)
			s.withTools()
			id := s.session()
			// The organisation's admin, as an accepted member, makes the change;
			// a second admin is not in the session.
			s.join(id, s.admin, "chat")
			otherAdmin := "per_admin2_" + s.org
			mustExec(t, s.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, 'Bea Boss', 'admin')`, otherAdmin, s.org)
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}, "role": "chat"})
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan"})
			run := s.started(id)
			status, out := s.tool(run, "remember", `{"title":"Confidential zephyr acquisition","content":"Private terms."}`)
			if status != 200 {
				t.Fatalf("remember: %d %v", status, out)
			}
			mid := out["id"].(string)
			if action == "restore" {
				s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/archive", nil)
			}
			after := s.cursor()
			if action == "update" {
				s.ok(s.admin, "PATCH", "/internal/memory/memories/"+mid, map[string]any{"title": "Confidential zephyr acquisition, signed"})
			} else {
				s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/"+action, nil)
			}
			want := map[string]string{"update": "memory.updated", "archive": "memory.archived", "restore": "memory.restored"}[action]
			if n := s.count(`SELECT count(*) FROM events WHERE cursor > $1 AND event_type = $2 AND payload->>'memoryId' = $3 AND session_id = $4`,
				after, want, mid, id); n != 1 {
				t.Errorf("%s on the session: %d events, want 1", want, n)
			}
			for _, member := range []string{s.marcio, s.admin} {
				if got := s.memoryEvents(member, mid, after); len(got) != 1 {
					t.Errorf("member %s reads %d events, want the %s: %v", member, len(got), want, got)
				}
			}
			// A pending invitee, someone else, an admin not in it.
			for _, who := range []string{s.ana, s.outsider, otherAdmin} {
				if got := s.memoryEvents(who, mid, after); len(got) != 0 {
					t.Errorf("%s reads the session memory's change: %v", who, got)
				}
				if n := s.count(`SELECT count(*) FROM events WHERE cursor > $1 AND session_visible(session_id, $2)
					AND payload::text ILIKE '%zephyr%'`, after, who); n != 0 {
					t.Errorf("%s reads the private title in %d events", who, n)
				}
			}
		})
	}

	t.Run("an organisation memory's changes stay everyone's", func(t *testing.T) {
		s := newSessionWorld(t)
		created := s.ok(s.admin, "POST", "/internal/memory/memories", map[string]any{"title": "Meter windows", "content": "24h"})
		mid := created["id"].(string)
		after := s.cursor()
		s.ok(s.admin, "PATCH", "/internal/memory/memories/"+mid, map[string]any{"title": "Meter windows, 48h"})
		s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/archive", nil)
		s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/restore", nil)
		if n := s.count(`SELECT count(*) FROM events WHERE cursor > $1 AND payload->>'memoryId' = $2 AND session_id IS NULL`, after, mid); n != 3 {
			t.Errorf("%d session-less events, want 3", n)
		}
		if got := s.memoryEvents(s.outsider, mid, after); len(got) != 3 {
			t.Errorf("someone else reads %d of the organisation memory's changes, want 3: %v", len(got), got)
		}
	})
}
