package orchestrator_test

import (
	"context"
	"strings"
	"testing"
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

// A file the brainstorm writes into $LUX_ARTIFACTS mid-conversation is
// collected when its container stops, as any agent's: recorded on its
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
		`tool: artifact {"path":"design.md","content":"# Metering\n\nCount each run once."}`})
	s.until("the second turn", func() bool {
		v := s.luxRun(run)
		return v != nil && len(v.inputs) > 0 && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NOT NULL`, run) == 1
	})
	mustExec(t, s.owner, `UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, run)
	var artifact string
	s.until("the file recorded", func() bool {
		s.pump()
		_ = s.owner.QueryRow(context.Background(), `SELECT id FROM artifacts WHERE run_id = $1 AND name = 'design.md'`, run).Scan(&artifact)
		return artifact != ""
	})
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
