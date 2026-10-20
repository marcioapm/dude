package agenttools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
)

// session makes a session with a live brainstorm Run on it, titled as
// given ("" for untitled, by "agent" or "person"), and returns the Run's token.
func (f *fixture) session(t *testing.T, id, title, titledBy string) string {
	t.Helper()
	mustExec(t, f.owner, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''))`,
		id, f.org, title, titledBy)
	token, hash := agenttools.NewToken()
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, mcp_token_hash)
		VALUES ($1, $2, $3, 1, 'running', 'agent', 'brainstorm', $4)`, "run_"+id, f.org, id, hash)
	return token
}

func (f *fixture) title(t *testing.T, session string) (title, by string) {
	t.Helper()
	_ = f.owner.QueryRow(context.Background(), `SELECT COALESCE(title, ''), COALESCE(titled_by, '') FROM sessions WHERE id = $1`,
		session).Scan(&title, &by)
	return title, by
}

// The agent names its untitled session, one line, and may name it again;
// each time is one session.renamed by the agent.
func TestASessionsAgentNamesItsSession(t *testing.T) {
	f := setup(t)
	token := f.session(t, "ssn_named", "", "")
	if status, out := f.post(t, token, "name_session", `{"title":"  Usage-based\nbilling  "}`); status != 200 || out["title"] != "Usage-based billing" {
		t.Fatalf("name_session: %d %v", status, out)
	}
	if title, by := f.title(t, "ssn_named"); title != "Usage-based billing" || by != "agent" {
		t.Errorf("title %q by %q", title, by)
	}
	if status, out := f.post(t, token, "name_session", `{"title":"Metering retries"}`); status != 200 {
		t.Fatalf("renaming: %d %v", status, out)
	}
	rows, err := f.owner.Query(context.Background(), `SELECT payload->>'title', payload->>'by', session_id FROM events
		WHERE event_type = 'session.renamed' ORDER BY cursor`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var title, by, session string
		_ = rows.Scan(&title, &by, &session)
		got = append(got, title+"/"+by+"/"+session)
	}
	if strings.Join(got, ",") != "Usage-based billing/agent/ssn_named,Metering retries/agent/ssn_named" {
		t.Errorf("renamed events: %v", got)
	}
	for _, bad := range []string{`{"title":"   "}`, `{"title":"` + strings.Repeat("x", 61) + `"}`} {
		if status, _ := f.post(t, token, "name_session", bad); status != 422 {
			t.Errorf("%s: %d, want refused", bad, status)
		}
	}
	// Sixty characters, counted as characters, not bytes.
	if status, out := f.post(t, token, "name_session", `{"title":"`+strings.Repeat("é", 60)+`"}`); status != 200 {
		t.Errorf("sixty accented characters: %d %v", status, out)
	}
}

// Once a person has named the session, the agent's name_session refuses,
// saying why, and the person's title stays.
func TestNameSessionIsRefusedOnceAPersonNamedIt(t *testing.T) {
	f := setup(t)
	token := f.session(t, "ssn_mine", "Ana's title", "person")
	status, out := f.post(t, token, "name_session", `{"title":"Something else"}`)
	if status != 422 || !strings.Contains(out["error"].(string), "a member named this session") {
		t.Fatalf("name_session after a person: %d %v", status, out)
	}
	if title, by := f.title(t, "ssn_mine"); title != "Ana's title" || by != "person" {
		t.Errorf("title %q by %q after the refusal", title, by)
	}
	var n int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE session_id = 'ssn_mine' AND event_type = 'session.renamed'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d session.renamed after a refusal", n)
	}
	// A task's agent has no such tool.
	if status, _ := f.post(t, f.run(t, "run_impl_name", "implementer", "running"), "name_session", `{"title":"x"}`); status != 404 {
		t.Errorf("an implementer names sessions: %d", status)
	}
}

func TestBrainstormStartsOnlyInvestigatorChildren(t *testing.T) {
	f := setup(t)
	token := f.session(t, "ssn_child_tools", "Research", "person")

	var started struct{ RunID string }
	if status := f.postAs(t, token, "start_agent", `{"profile":"investigator","key":"same"}`, &started); status != 200 || started.RunID == "" {
		t.Fatalf("investigator start: %d %+v", status, started)
	}
	var again struct{ RunID string }
	if status := f.postAs(t, token, "start_agent", `{"profile":"investigator","key":"same"}`, &again); status != 200 || again.RunID != started.RunID {
		t.Fatalf("idempotent start: %d %+v", status, again)
	}
	for _, profile := range []string{"reviewer", "qa_browser", "implementer", "fixer", "simplifier", "conductor", "brainstorm", "unknown"} {
		if status, out := f.post(t, token, "start_agent", fmt.Sprintf(`{"profile":%q,"key":%q}`, profile, profile)); status != 422 {
			t.Fatalf("%s: %d %v", profile, status, out)
		}
	}
	if status, _ := f.post(t, token, "start_agent", `{"key":"profile-less"}`); status != 422 {
		t.Fatalf("profile-less child: %d", status)
	}
	if status, _ := f.post(t, token, "start_agent", `{"profile":"investigator","sessionId":"ssn_child_tools","key":"forged"}`); status != 422 {
		t.Fatalf("forged parent arguments: %d", status)
	}
	var session, task, source, parent string
	if err := f.owner.QueryRow(context.Background(), `SELECT COALESCE(session_id,''), COALESCE(task_id,''), source_kind, parent_run_id FROM runs WHERE id=$1`, started.RunID).
		Scan(&session, &task, &source, &parent); err != nil {
		t.Fatal(err)
	}
	if session != "ssn_child_tools" || task != "" || source != "session" || parent != "run_ssn_child_tools" {
		t.Fatalf("child ownership: %q %q %q %q", session, task, source, parent)
	}

	childToken := f.tokenForRun(t, started.RunID)
	tools := f.getTools(t, childToken)
	for _, name := range []string{"list_tasks", "list_epics", "list_repositories", "search_memory", "get_memory", "run_diff", "emit_event"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("child lacks %s", name)
		}
	}
	for _, name := range []string{"start_agent", "github_check_log", "update_task", "relate", "create_task", "publish"} {
		if _, ok := tools[name]; ok {
			t.Errorf("child exposes %s", name)
		}
	}
	if status, out := f.post(t, childToken, "start_agent", `{"profile":"investigator"}`); status != 404 && status != 422 {
		t.Fatalf("child recursion: %d %v", status, out)
	}
}

func (f *fixture) tokenForRun(t *testing.T, runID string) string {
	t.Helper()
	token, hash := agenttools.NewToken()
	mustExec(t, f.owner, `UPDATE runs SET mcp_token_hash=$2 WHERE id=$1`, runID, hash)
	return token
}
