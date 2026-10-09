package agenttools_test

import (
	"context"
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
