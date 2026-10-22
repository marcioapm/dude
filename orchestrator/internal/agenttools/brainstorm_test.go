package agenttools_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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

// A member naming the session while the agent's name_session is on its way
// (granted when the caller was resolved, renamed before the tool's
// transaction) is refused, saying why, and the person's title stays.
func TestNameSessionIsRefusedOnceAPersonNamedIt(t *testing.T) {
	var f *fixture
	srv := &agenttools.Server{BeforeCall: func(tool string) {
		if tool == "name_session" {
			mustExec(t, f.owner, `UPDATE sessions SET title = 'Ana''s title', titled_by = 'person' WHERE id = 'ssn_mine'`)
		}
	}}
	f = setupWith(t, srv)
	token := f.session(t, "ssn_mine", "", "")
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

// A session's agent reads a task's feedback in full through its own
// pull_requests, naming the task; without one it is told to name it.
func TestASessionReadsFeedbackInFullThroughPullRequests(t *testing.T) {
	f, _ := pullRequestFixture(t)
	token := f.session(t, "ssn_prs", "", "")
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_prs', $1, $2)`, f.project, f.org)
	long := strings.Repeat("x", 300) + " the part past the excerpt"
	f.comment(t, "issue-comment-3", "sdk", 88, "tiago", long, "")
	var out fullFeedbackAnswer
	if status := f.postAs(t, token, "pull_requests", `{"task":"TEXT-1","feedback":["issue-comment-3","issue-comment-404"]}`, &out); status != 200 {
		t.Fatalf("pull_requests: %d %+v", status, out)
	}
	if len(out.Feedback) != 1 || delivery.Unquoted(out.Feedback[0].Body) != long || !slices.Equal(out.UnknownFeedback, []string{"issue-comment-404"}) {
		t.Errorf("a session's read: %+v", out)
	}
	if status, refused := f.post(t, token, "pull_requests", `{"feedback":["issue-comment-3"]}`); status != 422 ||
		!strings.Contains(fmt.Sprint(refused), "name the task to read its feedback in full") {
		t.Errorf("without a task: %d %v", status, refused)
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

// A brainstorm whose session a member named is not given name_session on
// any surface (GET /tools, MCP tools/list, a call): a call is answered as
// one to a tool the Run does not have. Untitled, or named by the agent, it
// is given it.
func TestABrainstormIsNotGivenNameSessionOnceAMemberNamedTheSession(t *testing.T) {
	f := setup(t)
	for _, s := range []struct {
		id, title, by string
		granted       bool
	}{
		{"ssn_untitled", "", "", true},
		{"ssn_agents", "Metering", "agent", true},
		{"ssn_members", "Dry run", "person", false},
	} {
		session, want := s.id, s.granted
		token := f.session(t, session, s.title, s.by)
		if _, ok := f.getTools(t, token)["name_session"]; ok != want {
			t.Errorf("%s: GET /tools lists name_session %v, want %v", session, ok, want)
		}
		cs, err := f.connect(t, token)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := cs.ListTools(context.Background(), nil)
		cs.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.ContainsFunc(listed.Tools, func(tl *mcp.Tool) bool { return tl.Name == "name_session" }); got != want {
			t.Errorf("%s: MCP lists name_session %v, want %v", session, got, want)
		}
		if !want {
			status, out := f.post(t, token, "name_session", `{"title":"Something else"}`)
			if status != 404 || out["error"] != "no tool name_session for this Run" {
				t.Errorf("%s: name_session called: %d %v", session, status, out)
			}
			if got, by := f.title(t, session); got != "Dry run" || by != "person" {
				t.Errorf("%s: title %q by %q", session, got, by)
			}
			if _, ok := f.getTools(t, token)["propose"]; !ok {
				t.Errorf("%s: propose went with name_session", session)
			}
		}
	}
}

func (f *fixture) tokenForRun(t *testing.T, runID string) string {
	t.Helper()
	token, hash := agenttools.NewToken()
	mustExec(t, f.owner, `UPDATE runs SET mcp_token_hash=$2 WHERE id=$1`, runID, hash)
	return token
}
