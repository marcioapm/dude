package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A session made with no title is untitled: no title in its row, its list
// line or its detail, and its agent's briefing says it is not named yet
// and tells it to name it.
func TestASessionStartsUntitled(t *testing.T) {
	s := newSessionWorld(t)
	out := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{})
	id := out["id"].(string)
	if out["title"] != nil {
		t.Errorf("created with title %v", out["title"])
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND title IS NULL AND titled_by IS NULL`, id); n != 1 {
		t.Errorf("the row has a title")
	}
	detail := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["session"].(map[string]any)
	if detail["title"] != nil || detail["titledBy"] != nil {
		t.Errorf("detail title %v by %v", detail["title"], detail["titledBy"])
	}
	list := s.ok(s.marcio, "GET", "/internal/sessions", nil)["sessions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["title"] != nil {
		t.Errorf("list %v", list)
	}
	s.withTools()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where would metering live?"})
	run := s.started(id)
	var prompt string
	_ = s.owner.QueryRow(context.Background(), `SELECT prompt FROM runs WHERE id = $1`, run).Scan(&prompt)
	if !strings.HasPrefix(prompt, "Brainstorm, this is a new session, not named yet.") {
		t.Errorf("the briefing does not say it is untitled:\n%s", prompt)
	}
}

// A file the brainstorm publishes mid-conversation is recorded on its
// Run, its artifact.created on the session (so only members see it) and on
// no task. (Who may read its bytes: TestASessionsArtifactsAreItsMembersAlone.)
func TestABrainstormsPublishedFileIsItsSessions(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "metering"})
	run := s.started(id)
	// The scripted agent does its own work in the container from a later message.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "write it up\n" +
		`tool: artifact {"path":"design.md","content":"# Metering\n\nCount each run once.","description":"How metering counts runs"}`})
	// Recorded while the session's Run goes on: nothing pauses it.
	var artifact string
	s.until("the file recorded", func() bool {
		s.pump()
		_ = s.owner.QueryRow(context.Background(), `SELECT id FROM artifacts WHERE run_id = $1 AND name = 'design.md'
			AND description = 'How metering counts runs'`, run).Scan(&artifact)
		return artifact != ""
	})
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, run); n != 1 {
		t.Error("the session's Run stopped before its file was recorded")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'artifact.created' AND session_id = $1 AND run_id = $2
		AND task_id IS NULL AND payload->>'artifactId' = $3`, id, run, artifact); n != 1 {
		t.Errorf("artifact.created on the session: %d, want 1", n)
	}
	if status, out := s.as(s.marcio, "GET", "/internal/artifacts/"+artifact+"/content", nil); status != 200 {
		t.Errorf("its owner reading the file: %d %v", status, out)
	}
}

// A member who can chat renames the session: one session.renamed by them,
// and the agent's name_session refuses from then on. A reader cannot, and
// someone not in it is told it does not exist.
func TestAPersonsRenameWinsAndAReaderCannotRename(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{})["id"].(string)
	s.join(id, s.ana, "chat")
	s.join(id, s.joao, "read")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "metering"})
	run := s.started(id)

	if status, out := s.tool(run, "name_session", `{"title":"Usage metering"}`); status != 200 {
		t.Fatalf("the agent naming it: %d %v", status, out)
	}
	if status, out := s.as(s.joao, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Mine"}); status != 403 {
		t.Errorf("a reader renamed: %d %v", status, out)
	}
	for _, who := range []string{s.outsider, s.admin} {
		if status, out := s.as(who, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Mine"}); status != 404 {
			t.Errorf("a non-member renamed: %d %v", status, out)
		}
	}
	if status, _ := s.as(s.ana, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "  "}); status != 400 {
		t.Errorf("an empty title: %d", status)
	}
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Billing\nv2"})
	var title, by string
	_ = s.owner.QueryRow(context.Background(), `SELECT title, titled_by FROM sessions WHERE id = $1`, id).Scan(&title, &by)
	if title != "Billing v2" || by != "person" {
		t.Errorf("title %q by %q", title, by)
	}
	rows, _ := s.owner.Query(context.Background(), `SELECT payload->>'title' || '/' || (payload->>'by') || '/' || actor_id
		FROM events WHERE session_id = $1 AND event_type = 'session.renamed' ORDER BY cursor`, id)
	var got []string
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		got = append(got, line)
	}
	want := []string{"Usage metering/agent/" + run, "Billing v2/" + s.ana + "/" + s.ana}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("session.renamed %v, want %v", got, want)
	}

	status, out := s.tool(run, "name_session", `{"title":"Something else"}`)
	if status != 422 || !strings.Contains(out["error"].(string), "a member named this session") {
		t.Errorf("name_session after a person renamed: %d %v", status, out)
	}
	_ = s.owner.QueryRow(context.Background(), `SELECT title FROM sessions WHERE id = $1`, id).Scan(&title)
	if title != "Billing v2" {
		t.Errorf("the agent's refused rename changed the title to %q", title)
	}
	// Seen only by its members, as every session event is.
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'session.renamed' AND session_id = $1
		AND session_visible(session_id, $2)`, id, s.outsider); n != 0 {
		t.Errorf("an outsider may see %d renames", n)
	}
}

// A person's rename and the agent's name_session, at the same moment, are
// settled one after the other on the session's row, and the person's name
// wins either way: when theirs commits first the agent's, waiting on the
// row, is refused; when the agent's commits first, the person's, waiting,
// replaces it. The first rename is held open in a transaction; the test
// waits for the second's backend to block on a lock held by that one.
func TestAPersonsRenameAndTheAgentsAreSettledInTurnAndThePersonWins(t *testing.T) {
	for _, first := range []string{"person", "agent"} {
		t.Run(first+" first", func(t *testing.T) {
			s := newSessionWorld(t)
			s.withTools()
			id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{})["id"].(string)
			s.join(id, s.ana, "chat")
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "metering"})
			run := s.started(id)

			holder, err := s.owner.Begin(t0())
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(t0())
			type outcome struct {
				status int
				out    map[string]any
			}
			result := make(chan outcome, 1)
			if first == "person" {
				if _, err := delivery.RenameSession(t0(), holder, delivery.SessionRef(s.org, id), "Billing v2", s.ana, "person", s.ana); err != nil {
					t.Fatal(err)
				}
				go func() {
					status, out := s.tool(run, "name_session", `{"title":"Usage metering"}`)
					result <- outcome{status, out}
				}()
			} else {
				ref := delivery.RunRef{Org: s.org, RunID: run, SessionID: id}
				if _, err := delivery.RenameSession(t0(), holder, ref, "Usage metering", delivery.ByAgent, "agent", run); err != nil {
					t.Fatal(err)
				}
				go func() {
					status, out := s.as(s.ana, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Billing v2"})
					result <- outcome{status, out}
				}()
			}
			// Each probe is a round trip; no sleep.
			for deadline := time.Now().Add(10 * time.Second); ; {
				var waiting int
				if err := holder.QueryRow(t0(), `SELECT count(*) FROM pg_stat_activity
					WHERE datname = current_database() AND wait_event_type = 'Lock'
						AND pg_backend_pid() = ANY(pg_blocking_pids(pid))`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting == 1 {
					break
				}
				select {
				case got := <-result:
					t.Fatalf("the second rename finished (%d %v) without waiting on the first", got.status, got.out)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("the second rename never waited on the first")
				}
			}
			if err := holder.Commit(t0()); err != nil {
				t.Fatal(err)
			}
			var got outcome
			select {
			case got = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("the second rename never finished")
			}

			want := []string{"Billing v2/" + s.ana}
			if first == "person" {
				if msg, _ := got.out["error"].(string); got.status != 422 || !strings.Contains(msg, "a member named this session") {
					t.Errorf("the agent's waiting name_session: %d %v, want 422", got.status, got.out)
				}
			} else {
				if got.status != 200 {
					t.Errorf("Ana's waiting rename: %d %v, want 200", got.status, got.out)
				}
				want = []string{"Usage metering/agent", "Billing v2/" + s.ana}
			}
			var title, by string
			_ = s.owner.QueryRow(t0(), `SELECT title, titled_by FROM sessions WHERE id = $1`, id).Scan(&title, &by)
			if title != "Billing v2" || by != "person" {
				t.Errorf("title %q by %q, want Ana's", title, by)
			}
			rows, _ := s.owner.Query(t0(), `SELECT (payload->>'title') || '/' || (payload->>'by')
				FROM events WHERE session_id = $1 AND event_type = 'session.renamed' ORDER BY cursor`, id)
			renamed, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(renamed, ",") != strings.Join(want, ",") {
				t.Errorf("session.renamed %v, want %v", renamed, want)
			}
		})
	}
}
