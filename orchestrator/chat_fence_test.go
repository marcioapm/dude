package orchestrator_test

import (
	"testing"
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
