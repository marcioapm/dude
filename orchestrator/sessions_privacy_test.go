package orchestrator_test

import (
	"strings"
	"testing"
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

// toolRaw calls one of the session agent's tools, its body as the agent reads it.
func (s *sessionWorld) toolRaw(runID, name, args string) (int, string) {
	s.t.Helper()
	v := s.luxRun(runID)
	if v == nil {
		s.t.Fatalf("no lux Run for %s", runID)
	}
	return s.callTool(s.syncer.Agent.ToolsURL, v.raw, name, args)
}
