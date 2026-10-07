package orchestrator_test

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A member's write that waits on the session's lock is decided by the
// membership the lock's holder leaves behind: demoted to reader or
// removed while their chat or filing waits, they are refused and nothing
// they sent lands — no Run, no message, no filed task, no event.
func TestAWriteWaitingOnTheSessionIsDecidedByWhoTheyAreAfter(t *testing.T) {
	type write struct {
		path   string
		body   func(s *sessionWorld, id string) map[string]any
		landed string // what the write would leave, counted for the session ($1)
	}
	writes := map[string]write{
		"chat": {
			path: "/chat",
			body: func(*sessionWorld, string) map[string]any { return map[string]any{"text": "must not reach the agent"} },
			landed: `SELECT (SELECT count(*) FROM runs WHERE session_id = $1)
				+ (SELECT count(*) FROM events WHERE session_id = $1 AND payload::text LIKE '%must not reach the agent%')`,
		},
		"file": {
			path: "/file",
			body: func(s *sessionWorld, id string) map[string]any {
				prop := s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Must not be filed",
					Goal: "Count experiment runs per org per day"}})
				return map[string]any{"proposalId": prop, "items": []int{0}}
			},
			landed: `SELECT (SELECT count(*) FROM session_filings WHERE session_id = $1)
				+ (SELECT count(*) FROM tasks WHERE title = 'Must not be filed')
				+ (SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.filed')`,
		},
	}
	changes := map[string]struct {
		sql    string
		status int
	}{
		"demoted to reader": {`UPDATE session_people SET role = 'read' WHERE session_id = $1 AND person_id = $2`, 403},
		"removed":           {`DELETE FROM session_people WHERE session_id = $1 AND person_id = $2`, 404},
	}
	for what, wr := range writes {
		for how, change := range changes {
			t.Run(what+", "+how, func(t *testing.T) {
				s := newSessionWorld(t)
				id := s.session()
				s.join(id, s.ana, "chat")
				body := wr.body(s, id)
				before := s.count(wr.landed, id)

				holder, err := s.owner.Begin(t0())
				if err != nil {
					t.Fatal(err)
				}
				defer holder.Rollback(t0())
				if err := delivery.LockSession(t0(), holder, id); err != nil {
					t.Fatal(err)
				}
				result := make(chan int, 1)
				go func() {
					status, _ := s.as(s.ana, "POST", "/internal/sessions/"+id+wr.path, body)
					result <- status
				}()
				// Ana's request is waiting on the session's lock: another backend
				// waits on an advisory lock. Each probe is a round trip; no sleep.
				for deadline := time.Now().Add(10 * time.Second); ; {
					var waiting int
					if err := holder.QueryRow(t0(), `SELECT count(*) FROM pg_stat_activity
						WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event = 'advisory'`).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting > 0 {
						break
					}
					select {
					case status := <-result:
						t.Fatalf("Ana's %s finished (%d) without waiting on the session's lock", what, status)
					default:
					}
					if time.Now().After(deadline) {
						t.Fatalf("Ana's %s never waited on the session's lock", what)
					}
				}
				if _, err := holder.Exec(t0(), change.sql, id, s.ana); err != nil {
					t.Fatal(err)
				}
				if err := holder.Commit(t0()); err != nil {
					t.Fatal(err)
				}
				select {
				case status := <-result:
					if status != change.status {
						t.Errorf("Ana's waiting %s, %s meanwhile: %d, want %d", what, how, status, change.status)
					}
				case <-time.After(10 * time.Second):
					t.Fatalf("Ana's %s never finished", what)
				}
				if after := s.count(wr.landed, id); after != before {
					t.Errorf("Ana's refused %s left %d rows behind", what, after-before)
				}
			})
		}
	}
}
