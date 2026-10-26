package orchestrator_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A session started from chat can never see a project hidden from that
// chat's provider: a member linking it by hand is refused, saying why; a
// session started in dude links it.
func TestAMemberCannotLinkAProjectHiddenFromTheChatASessionCameFrom(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.webProject)
	link := []map[string]any{{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}
	status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": link})
	if status != 409 || out["error"].(map[string]any)["code"] != "hidden_from_chat" {
		t.Fatalf("linking a hidden project: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, id, s.webProject); n != 0 {
		t.Fatal("the hidden project was linked")
	}
	// The same link on a session started in dude.
	other := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+other+"/link", map[string]any{"projects": link})
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, other, s.webProject); n != 1 {
		t.Fatal("a session not from chat could not link it")
	}
}

// one is the query's single text value, "" for NULL.
func (s *sessionWorld) one(sql string, args ...any) string {
	s.t.Helper()
	var out *string
	if err := s.owner.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	if out == nil {
		return ""
	}
	return *out
}

// fileFromSession files a proposed task in BL (the session's linked
// project) and an epic, through the session's card, and returns the task's id.
func fileFromSession(s *sessionWorld, session string) (task, epic string) {
	s.t.Helper()
	proposal := s.proposal(session, []delivery.ProposalItem{
		{Kind: "epic", Project: "BL", Title: "Nightly failures"},
		{Kind: "task", Project: "BL", Title: "Fix the export timeout", Goal: "The nightly export test times out after 30 seconds."},
	})
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1}})
	for _, r := range out["results"].([]any) {
		if m := r.(map[string]any); m["status"] != "filed" {
			s.t.Fatalf("item %v: %v", m["item"], m)
		}
	}
	task = s.one(`SELECT id FROM tasks WHERE project_id = $1 AND title = 'Fix the export timeout'`, s.project)
	epic = s.one(`SELECT id FROM epics WHERE project_id = $1 AND title = 'Nightly failures'`, s.project)
	return task, epic
}

// toolsAs is a tool caller for an implementer Run on task, over the tool server's JSON API.
func toolsAs(s *sessionWorld, task, run string) func(tool, args string) (int, string) {
	s.t.Helper()
	tools := httptest.NewServer((&agenttools.Server{DB: s.app, Log: s.log}).Handler())
	s.t.Cleanup(tools.Close)
	token, hash := agenttools.NewToken()
	mustExec(s.t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash)
		VALUES ($1, $2, $3, $4, 1, 'running', 'implement', 'implementer', $5)`, run, s.org, s.project, task, hash)
	return func(tool, args string) (int, string) {
		req, _ := http.NewRequest("POST", tools.URL+"/tools/"+tool, strings.NewReader(args))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			s.t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
}

// A task (and epic) filed from a chat-born session's card carries the
// session's provider, marked as through the session; its agents are fenced
// when its project is hidden from that provider, and a task its agent
// makes inherits the same. The task page does not say it came from Slack.
func TestATaskFiledFromAChatBornSessionIsFencedAsTheSessionIs(t *testing.T) {
	s := newSessionWorld(t)
	session := s.session()
	mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, session)
	task, epic := fileFromSession(s, session)
	if o := s.one(`SELECT concat_ws('|', origin->>'provider', origin->>'via', origin->>'from', origin->>'conversationId') FROM tasks WHERE id = $1`,
		task); o != "slack|session|"+session {
		t.Fatalf("task origin %s", o)
	}
	if o := s.one(`SELECT origin->>'via' FROM epics WHERE id = $1`, epic); o != "session" {
		t.Fatalf("epic origin via %q", o)
	}
	call := toolsAs(s, task, "run_filed_impl")
	if status, body := call("list_tasks", `{}`); status != 200 {
		t.Fatalf("before hiding: %d %s", status, body)
	}
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.project)
	for _, c := range [][2]string{{"list_tasks", `{}`}, {"list_repositories", `{}`}, {"search_memory", `{"query":"export"}`},
		{"create_task", `{"title":"More","goal":"Another piece of the nightly export work."}`}} {
		status, body := call(c[0], c[1])
		if status != 422 || !strings.Contains(body, "this task's project is hidden from what is started from slack") {
			t.Errorf("%s: %d %s", c[0], status, body)
		}
	}
	// A task its agent made before the hiding carries the provider, through the task.
	mustExec(t, s.owner, `DELETE FROM chat_hidden_projects WHERE organization_id = $1`, s.org)
	if status, body := call("create_task", `{"title":"Follow-up","goal":"Measure the nightly export after the fix."}`); status != 200 {
		t.Fatalf("create_task: %d %s", status, body)
	}
	if o := s.one(`SELECT concat_ws('|', origin->>'provider', origin->>'via', origin->>'from') FROM tasks WHERE title = 'Follow-up'`); o != "slack|task|"+task {
		t.Fatalf("an agent-made task's origin %s", o)
	}
}

// A task filed from a session started in dude has no origin and is not fenced.
func TestATaskFiledFromASessionNotFromChatIsUnaffected(t *testing.T) {
	s := newSessionWorld(t)
	session := s.session()
	task, _ := fileFromSession(s, session)
	if o := s.one(`SELECT origin::text FROM tasks WHERE id = $1`, task); o != "" {
		t.Fatalf("task origin %s", o)
	}
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.project)
	call := toolsAs(s, task, "run_web_impl")
	for _, tool := range []string{"list_tasks", "list_repositories"} {
		if status, body := call(tool, `{}`); status != 200 {
			t.Errorf("%s: %d %s", tool, status, body)
		}
	}
}
