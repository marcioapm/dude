package orchestrator_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
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

// A link the session's rules refuse answers with its code and changes
// nothing the session links.
func TestASessionLinkTheRulesRefuseChangesNothing(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
	hidden := "prj_hid_" + s.org
	mustExec(t, s.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Hidden', $1, 'HID')`, hidden, s.org)
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, hidden)
	bl := map[string]any{"projectId": s.project, "repositoryIds": []string{s.repoID}}
	many := []map[string]any{bl}
	for i := range delivery.LinksMax {
		p := fmt.Sprintf("prj_many%02d_%s", i, s.org)
		mustExec(t, s.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, $1, $1, $3)`, p, s.org,
			fmt.Sprintf("M%02d", i))
		many = append(many, map[string]any{"projectId": p, "repositoryIds": []string{}})
	}
	links := func() string {
		return s.one(`SELECT (SELECT string_agg(project_id, ',' ORDER BY project_id) FROM session_projects WHERE session_id = $1) || ' / ' ||
			(SELECT string_agg(repository_id, ',' ORDER BY repository_id) FROM session_repositories WHERE session_id = $1)`, id)
	}
	before := links()
	if before != s.project+" / "+s.repoID {
		t.Fatalf("linked before %q", before)
	}
	for _, c := range []struct {
		name     string
		projects []map[string]any
		status   int
		code     string
	}{
		{"the same project twice", []map[string]any{bl, {"projectId": s.project, "repositoryIds": []string{}}}, 400, "bad_request"},
		{"51 projects", many, 400, "bad_request"},
		{"a repository of another project", []map[string]any{{"projectId": s.project, "repositoryIds": []string{s.repoID, s.webRepo}}}, 404, "not_found"},
		{"an unknown project", []map[string]any{bl, {"projectId": "prj_nope", "repositoryIds": []string{}}}, 404, "not_found"},
		{"a project hidden from the session's chat", []map[string]any{bl, {"projectId": hidden, "repositoryIds": []string{}}}, 409, "hidden_from_chat"},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": c.projects})
			e, _ := out["error"].(map[string]any)
			if status != c.status || e["code"] != c.code {
				t.Fatalf("%d %v", status, out)
			}
			if after := links(); after != before {
				t.Fatalf("links %q, were %q", after, before)
			}
		})
	}
	// The same body less what each case added links.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": many[:delivery.LinksMax]})
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1`, id); n != delivery.LinksMax {
		t.Fatalf("%d linked", n)
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
// session's provider, marked as through the session, and a task its agent
// makes inherits the same. Once its project is hidden from that provider,
// the task keeps working on its own project: hiding applies to new work.
func TestATaskFiledFromAChatBornSessionKeepsItsOwnProjectOnceHidden(t *testing.T) {
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
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.project)
	for _, c := range [][2]string{{"list_tasks", `{}`}, {"list_repositories", `{}`}, {"search_memory", `{"query":"export"}`},
		{"create_task", `{"title":"Follow-up","goal":"Measure the nightly export after the fix."}`}} {
		if status, body := call(c[0], c[1]); status != 200 {
			t.Errorf("%s on its own hidden project: %d %s", c[0], status, body)
		}
	}
	if o := s.one(`SELECT concat_ws('|', origin->>'provider', origin->>'via', origin->>'from') FROM tasks WHERE title = 'Follow-up'`); o != "slack|task|"+task {
		t.Fatalf("an agent-made task's origin %s", o)
	}
	// Another project hidden from Slack is still out of its reach: a
	// relation into it shows nothing of it.
	other := s.taskIn(s.webProject, "Console export", s.marcio)
	mustExec(t, s.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $2, $3, 'blocks', $4)`, s.org, other, task, s.marcio)
	if status, body := call("list_tasks", `{}`); status != 200 || !strings.Contains(body, "WC-") {
		t.Fatalf("list_tasks before hiding WC: %d %s", status, body)
	}
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.webProject)
	if status, body := call("list_tasks", `{}`); status != 200 || strings.Contains(body, "WC-") {
		t.Errorf("list_tasks shows another hidden project's task: %d %s", status, body)
	}
}

// A chat-born session's agent parked holding a checkout of a project an
// admin hid from that chat since is not resumed with it (lux cannot take a
// repository out of a Run): the next message ends it and a fresh agent,
// without the checkout, takes the message.
func TestAParkedSessionAgentHoldingAHiddenProjectsCheckoutIsReplaced(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look at billing"})
	run := s.started(id)
	s.linkWeb(id)
	s.untilWebCheckedOut(run)
	mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.webProject)
	s.parkSession(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	var next string
	s.until("a fresh agent took the message", func() bool {
		next, _ = s.brainstorm(id)
		return next != run && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id IS NOT NULL`, next) == 1
	})
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, run); n != 1 {
		t.Errorf("the agent holding the hidden checkout was not ended")
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE '%carry on%'`, next); n == 0 &&
		!strings.Contains(s.one(`SELECT prompt FROM runs WHERE id = $1`, next), "carry on") {
		t.Errorf("the fresh agent was not given the message")
	}
	v := s.luxRun(next)
	if v == nil {
		t.Fatalf("no lux Run for the fresh agent")
	}
	if v.spec.Git != nil {
		for _, r := range v.spec.Git.Repositories {
			if r.Name == lux.SpecName("WC-web") {
				t.Errorf("the fresh agent checks out the hidden project's repository")
			}
		}
	}
	if strings.Contains(v.raw, "WC-web") || strings.Contains(v.raw, "web-console") {
		t.Errorf("the fresh agent's spec names the hidden project: %s", v.raw)
	}
}

// A chat-born session's cards are not filed into a project hidden from its
// chat since it was linked: a new task there, an edit of one of its tasks
// and a relation naming one are each refused, and nothing is written. Once
// the project is no longer hidden, the same card files.
func TestASessionsCardsAreNotFiledIntoAProjectHiddenSinceItWasLinked(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
	s.linkWeb(id)
	wc := s.taskIn(s.webProject, "Console export", s.marcio)
	bl := s.taskIn(s.project, "Billing export", s.marcio)
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "WC", Title: "Console usage page", Goal: "Show the usage per day on the console."},
		{Kind: "edit", Task: s.keyOf(wc), After: &delivery.TaskText{Goal: ptr("A goal long enough to keep, edited from chat")}},
		{Kind: "relation", From: s.keyOf(wc), To: s.keyOf(bl), RelationKind: "blocks", Why: "the console reads it"},
	})
	written := func() int {
		return s.count(`SELECT count(*) FROM tasks WHERE title = 'Console usage page'`) +
			s.count(`SELECT count(*) FROM tasks WHERE id = $1 AND goal LIKE '%edited from chat'`, wc) +
			s.count(`SELECT count(*) FROM task_relations WHERE from_task_id = $1`, wc)
	}
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.webProject)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1, 2}})
	results, _ := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("file: %v", out)
	}
	for i, r := range results {
		m := r.(map[string]any)
		why, _ := m["why"].(string)
		if want := []string{"project WC is not linked", "is not a task of a linked project", "is not a task of a linked project"}[i]; m["status"] != "refused" || !strings.Contains(why, want) {
			t.Errorf("item %v into a hidden project: %v, want refused saying %q", m["item"], m, want)
		}
	}
	if n := written(); n != 0 {
		t.Fatalf("%d rows written into the hidden project", n)
	}
	mustExec(t, s.owner, `DELETE FROM chat_hidden_projects WHERE project_id = $1`, s.webProject)
	out = s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1, 2}})
	for _, r := range out["results"].([]any) {
		if m := r.(map[string]any); m["status"] != "filed" {
			t.Errorf("item %v once no longer hidden: %v", m["item"], m)
		}
	}
	if n := written(); n != 3 {
		t.Fatalf("%d of the 3 writes once no longer hidden", n)
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
