package orchestrator_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
)

// A session with no linked projects still searches its organisation's
// memories: an organisation memory is found, a project's is not.
func TestASessionWithNothingLinkedSearchesOrganisationMemory(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	orgMem, prjMem := "mem_org_"+s.org, "mem_prj_"+s.org
	mustExec(t, s.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, kind, author_kind)
		VALUES ($1, $3, NULL, 'Meter windows', 'The meter keys expire after 24h.', 'fact', 'person'),
		       ($2, $3, $4, 'Meter project note', 'The meter lives in billing.', 'fact', 'person')`, orgMem, prjMem, s.org, s.project)
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Nothing linked"})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "search memory"})
	run := s.started(id)
	s.until("the memories to be indexed", func() bool {
		return s.count(`SELECT count(*) FROM search_documents WHERE source_id = ANY($1)`, []string{orgMem, prjMem}) == 2
	})

	status, body := s.toolRaw(run, "search_memory", `{"query":"meter"}`)
	if status != 200 {
		t.Fatalf("search_memory with nothing linked: %d %s", status, body)
	}
	if !strings.Contains(body, orgMem) {
		t.Errorf("the organisation's memory was not found: %s", body)
	}
	if strings.Contains(body, prjMem) {
		t.Errorf("a project's memory was found by a session that links no project: %s", body)
	}
}

// What a session's agent remembers is the session's: its accepted members
// read it (and see it was the brainstorm's), its own agent finds it; nobody
// else does — not someone else in the organisation, not an invitee who has
// not accepted, not an admin, not another agent — through any memory read.
// The organisation's and its linked project's memories stay its to read.
func TestASessionsMemoriesAreItsMembersAlone(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.joao, "read")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}, "role": "chat"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "private planning"})
	run := s.started(id)

	// Only session scope: an organisation or project scope is refused.
	for _, args := range []string{
		`{"scope":"organization","title":"Zephyr","content":"x"}`,
		`{"project":"BL","title":"Zephyr","content":"x"}`,
	} {
		if status, body := s.toolRaw(run, "remember", args); status != 422 {
			t.Errorf("remember %s: %d %s, want refused", args, status, body)
		}
	}
	status, out := s.tool(run, "remember", `{"title":"Confidential zephyr plan","content":"Private session decides to acquire Zephyr."}`)
	if status != 200 {
		t.Fatalf("remember: %d %v", status, out)
	}
	mid := out["id"].(string)
	var project, session string
	_ = s.owner.QueryRow(t.Context(), `SELECT coalesce(project_id, ''), coalesce(session_id, '') FROM memories WHERE id = $1`, mid).
		Scan(&project, &session)
	if project != "" || session != id {
		t.Errorf("stored in project %q, session %q; want the session's alone", project, session)
	}
	// Someone else's memories, which the session reads as before.
	orgMem, prjMem, webMem := "mem_org_"+s.org, "mem_prj_"+s.org, "mem_web_"+s.org
	mustExec(t, s.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, kind, author_kind)
		VALUES ($1, $4, NULL, 'Zephyr org fact', 'zephyr everywhere', 'fact', 'person'),
		       ($2, $4, $5, 'Zephyr billing fact', 'zephyr in billing', 'fact', 'person'),
		       ($3, $4, $6, 'Zephyr web fact', 'zephyr in web', 'fact', 'person')`, orgMem, prjMem, webMem, s.org, s.project, s.webProject)
	s.until("the memories to be indexed", func() bool {
		return s.count(`SELECT count(*) FROM search_documents WHERE source_id = ANY($1)`, []string{mid, orgMem, prjMem, webMem}) == 4
	})

	// Members read it, with its provenance.
	for _, who := range []string{s.marcio, s.joao} {
		status, out := s.as(who, "GET", "/internal/memory/memories/"+mid, nil)
		author, _ := out["author"].(map[string]any)
		if status != 200 || author["runId"] != run || author["role"] != "brainstorm" {
			t.Errorf("member %s reads %d %v", who, status, out)
		}
		if body := s.body(who, "/internal/memory/search?mode=words&q=zephyr"); !strings.Contains(body, mid) {
			t.Errorf("member %s does not find it: %s", who, body)
		}
		if body := s.body(who, "/internal/memory/memories?q=zephyr"); !strings.Contains(body, mid) {
			t.Errorf("member %s does not list it: %s", who, body)
		}
	}
	// Nobody else, through any read; nor its Run as anyone's provenance.
	for _, who := range []string{s.outsider, s.ana, s.admin} {
		if status, out := s.as(who, "GET", "/internal/memory/memories/"+mid, nil); status != 404 || strings.Contains(fmtJSON(out), "zephyr") {
			t.Errorf("%s reads the session's memory: %d %v", who, status, out)
		}
		for _, path := range []string{"/internal/memory/search?mode=words&q=zephyr", "/internal/memory/search?q=zephyr&project=" + s.project,
			"/internal/memory/memories?q=zephyr", "/internal/memory/memories?scope=organization", "/internal/memory/memories?project=" + s.project,
			"/internal/memory/memories?author=agent", "/internal/memory/index"} {
			if body := s.body(who, path); strings.Contains(body, mid) || strings.Contains(body, "acquire Zephyr") || strings.Contains(body, run) {
				t.Errorf("%s %s shows the session's memory: %s", who, path, body)
			}
		}
		for _, path := range []string{"/internal/memory/memories/" + mid, "/internal/memory/memories/" + mid + "/archive"} {
			method := map[bool]string{true: "PATCH", false: "POST"}[!strings.HasSuffix(path, "archive")]
			if status, _ := s.as(who, method, path, map[string]any{"title": "taken"}); status != 404 {
				t.Errorf("%s %s %s: %d, want 404", who, method, path, status)
			}
		}
	}
	// Another agent — a task's, in the same project — neither finds nor reads it.
	token, hash := agenttools.NewToken()
	task := s.taskIn(s.project, "Elsewhere", s.marcio)
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash)
		VALUES ('run_other_'||$1, $1, $2, $3, 1, 'running', 'implement', 'implementer', $4)`, s.org, s.project, task, hash)
	for tool, args := range map[string]string{"search_memory": `{"query":"zephyr"}`, "get_memory": `{"id":"` + mid + `"}`} {
		status, body := s.toolAs(token, tool, args)
		if tool == "get_memory" && status != 422 || tool == "search_memory" && strings.Contains(body, mid) ||
			strings.Contains(body, "acquire Zephyr") || strings.Contains(body, run) {
			t.Errorf("a task's agent %s: %d %s", tool, status, body)
		}
	}
	if _, body := s.toolAs(token, "search_memory", `{"query":"zephyr"}`); !strings.Contains(body, orgMem) || !strings.Contains(body, prjMem) {
		t.Errorf("a task's agent no longer finds the organisation's and its project's memories: %s", body)
	}
	// The session's agent finds its own, the organisation's and its linked project's; not an unlinked project's.
	_, body := s.toolRaw(run, "search_memory", `{"query":"zephyr"}`)
	for _, want := range []string{mid, orgMem, prjMem} {
		if !strings.Contains(body, want) {
			t.Errorf("the session's agent does not find %s: %s", want, body)
		}
	}
	if strings.Contains(body, webMem) {
		t.Errorf("the session's agent finds an unlinked project's memory: %s", body)
	}
	if status, body := s.toolRaw(run, "get_memory", `{"id":"`+mid+`"}`); status != 200 || !strings.Contains(body, "acquire Zephyr") {
		t.Errorf("the session's agent reads its memory: %d %s", status, body)
	}
	// Another session's agent does not.
	other := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Other",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+other+"/chat", map[string]any{"text": "hi"})
	otherRun := s.started(other)
	for tool, args := range map[string]string{"search_memory": `{"query":"zephyr"}`, "get_memory": `{"id":"` + mid + `"}`} {
		status, body := s.toolRaw(otherRun, tool, args)
		if tool == "get_memory" && status != 422 || tool == "search_memory" && strings.Contains(body, mid) ||
			strings.Contains(body, "acquire Zephyr") {
			t.Errorf("another session's agent %s: %d %s", tool, status, body)
		}
	}
	// Removed, a member reads it no more.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.joao+"/remove", nil)
	if status, _ := s.as(s.joao, "GET", "/internal/memory/memories/"+mid, nil); status != 404 {
		t.Errorf("a removed member still reads it: %d", status)
	}
}

// toolRaw calls one of the session agent's tools, its body as the agent reads it.
func (s *sessionWorld) toolRaw(runID, name, args string) (int, string) {
	s.t.Helper()
	v := s.luxRun(runID)
	if v == nil {
		s.t.Fatalf("no lux Run for %s", runID)
	}
	return s.callTool(s.syncer.Agent.ToolsURL, v.raw, name, args)
}

// toolAs calls a tool with a Run's bearer token.
func (s *sessionWorld) toolAs(token, name, args string) (int, string) {
	s.t.Helper()
	spec, _ := json.Marshal(map[string]any{"secrets": []map[string]string{{"name": "DUDE_TOOLS_AUTH", "value": "Bearer " + token}}})
	return s.callTool(s.syncer.Agent.ToolsURL, string(spec), name, args)
}

// body is a GET's body as a person, as text.
func (s *sessionWorld) body(person, path string) string {
	s.t.Helper()
	_, out := s.as(person, "GET", path, nil)
	return fmtJSON(out)
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
