package orchestrator_test

import (
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A member's write that waits on the session's lock is decided by the
// membership the lock's holder leaves behind: demoted to reader or
// removed while their chat or filing waits, they are refused and nothing
// they sent lands — no Run, no message, no filed task, no event. The
// test waits for Ana's request itself to block on that session's lock; an
// unrelated advisory waiter in the same database does not count.
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
				// Another backend already waits on an unrelated advisory lock: it
				// must not pass for Ana's request.
				unrelated := s.advisoryWaiter(8675309)
				if pids := blockedOnSession(t, holder, id); len(pids) != 0 {
					t.Fatalf("before Ana's %s, %v counted as waiting on the session's lock", what, pids)
				}
				result := make(chan int, 1)
				go func() {
					status, _ := s.as(s.ana, "POST", "/internal/sessions/"+id+wr.path, body)
					result <- status
				}()
				// Each probe is a round trip; no sleep.
				for deadline := time.Now().Add(10 * time.Second); ; {
					pids := blockedOnSession(t, holder, id)
					if slices.Contains(pids, unrelated) {
						t.Fatalf("the unrelated waiter %d counted as waiting on the session's lock", unrelated)
					}
					if len(pids) == 1 {
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

// blockedOnSession is the backends waiting, ungranted, on session's
// advisory lock and blocked by holder's backend. The key is
// delivery.LockSession's: hashtext('session:' || id) as one bigint, which
// pg_locks shows split into classid (high 32 bits), objid (low) and
// objsubid 1.
func blockedOnSession(t *testing.T, holder pgx.Tx, session string) []int32 {
	t.Helper()
	rows, err := holder.Query(t0(), `WITH k AS (SELECT hashtext('session:' || $1)::bigint AS key)
		SELECT l.pid FROM pg_locks l, k
		WHERE l.locktype = 'advisory' AND NOT l.granted
			AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
			AND l.classid = ((k.key >> 32) & 4294967295)::oid AND l.objid = (k.key & 4294967295)::oid AND l.objsubid = 1
			AND pg_backend_pid() = ANY(pg_blocking_pids(l.pid))
		ORDER BY l.pid`, session)
	if err != nil {
		t.Fatal(err)
	}
	pids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		t.Fatal(err)
	}
	return pids
}

// advisoryWaiter has one connection hold advisory lock key and a second
// wait on it, until the test ends; returns the waiting backend's pid once
// Postgres shows it waiting.
func (s *sessionWorld) advisoryWaiter(key int64) int32 {
	s.t.Helper()
	connect := func() *pgx.Conn {
		c, err := pgx.ConnectConfig(t0(), s.owner.Config().Copy())
		if err != nil {
			s.t.Fatal(err)
		}
		return c
	}
	holder, waiter := connect(), connect()
	if _, err := holder.Exec(t0(), `SELECT pg_advisory_lock($1)`, key); err != nil {
		s.t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(t0(), `SELECT pg_advisory_lock($1)`, key)
		waited <- err
	}()
	s.t.Cleanup(func() {
		_, _ = holder.Exec(t0(), `SELECT pg_advisory_unlock($1)`, key)
		<-waited
		_ = waiter.Close(t0())
		_ = holder.Close(t0())
	})
	pid := waiter.PgConn().PID()
	for deadline := time.Now().Add(10 * time.Second); ; {
		if s.count(`SELECT count(*) FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted`, pid) == 1 {
			return int32(pid)
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("the unrelated advisory waiter %d never waited", pid)
		}
	}
}
