package orchestrator_test

import (
	"strings"
	"testing"
)

// linkNotes are the texts dude queued on the Run telling it of a link.
func (s *sessionWorld) linkNotes(run string) []string {
	s.t.Helper()
	var out []string
	rows, err := s.owner.Query(t0(), `SELECT text FROM directives WHERE run_id = $1 AND text LIKE 'A member %linked%' ORDER BY created_at, id`, run)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		_ = rows.Scan(&text)
		out = append(out, text)
	}
	return out
}

// A project linked with no repository to a session whose agent is live is
// told to it as its next input, without parking it: its tools read the
// project, and a member ticks repositories to check one out.
func TestAProjectLinkedWithNoRepositoryIsToldToTheLiveAgent(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look at billing"})
	run := s.started(id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{}}}})
	notes := s.linkNotes(run)
	want := "A member linked WC (web-console) with no repository: your tools now read its tasks, pull requests and findings"
	if len(notes) != 1 || !strings.HasPrefix(notes[0], want) || !strings.Contains(notes[0], "ticks its repositories") {
		t.Fatalf("notes %q", notes)
	}
	if strings.Contains(notes[0], "BL") {
		t.Errorf("a project linked before is told again: %q", notes[0])
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND dude_pause IS NULL`, run); n != 1 {
		t.Errorf("the agent was parked for a link with no repository")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'session.told' AND payload->>'text' LIKE 'A member linked WC%'`, run); n != 1 {
		t.Errorf("%d session.told", n)
	}
	s.until("the agent to read it", func() bool {
		v := s.luxRun(run)
		for _, in := range append(v.inputs, v.resumes...) {
			if strings.HasPrefix(in, want) {
				return true
			}
		}
		return false
	})
	if v := s.luxRun(run); v.resumed != 0 {
		t.Errorf("resumed %d times", v.resumed)
	}
	// The same set again changes nothing and tells nothing.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{}}}})
	if notes := s.linkNotes(run); len(notes) != 1 {
		t.Errorf("an unchanged set was told: %q", notes)
	}
}

// Unlinking is told: a repository whose project stays, then the project.
func TestAnUnlinkIsToldToTheLiveAgent(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look at billing"})
	run := s.started(id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}}}})
	notes := s.linkNotes(run)
	if len(notes) != 1 || notes[0] != "A member unlinked the repository BL/target; BL stays linked. "+
		"A checkout of it you hold stays until this session's next fresh agent; do not rely on it." {
		t.Fatalf("notes %q", notes)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []any{}})
	notes = s.linkNotes(run)
	if len(notes) != 2 || notes[1] != "A member unlinked BL (P): your tools now refuse it. "+
		"A checkout of it you hold stays until this session's next fresh agent; do not rely on it." {
		t.Fatalf("notes %q", notes)
	}
}

// A project linked with its repository gets its project line, but its
// checkout is told once, by the resume that clones it.
func TestAProjectLinkedWithARepositoryIsNotAnnouncedTwice(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look at billing"})
	run := s.started(id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	notes := s.linkNotes(run)
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "A member linked WC (web-console) with repositories:") ||
		strings.Contains(notes[0], "WC/web") || strings.Contains(notes[0], "/workspace/") {
		t.Fatalf("notes %q", notes)
	}
	s.untilWebCheckedOut(run)
	where := "A member linked WC/web; it is checked out at /workspace/repos/WC/web"
	told := 0
	v := s.luxRun(run)
	for _, in := range v.resumes {
		told += strings.Count(in, where)
	}
	told += s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, $2) > 0`, run, where)
	if told != 1 {
		t.Errorf("the checkout was told %d times: %q %q", told, v.inputs, v.resumes)
	}
}

// A project hidden from the chat a session came from is never named, even
// unlinked; the rest of the change is told.
func TestALinkNoteNeverNamesAProjectHiddenFromTheChat(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.linkWeb(id)
	mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
	mustExec(t, s.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, s.org, s.webProject)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look at billing"})
	run := s.started(id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []any{}})
	notes := s.linkNotes(run)
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "A member unlinked BL (P)") {
		t.Fatalf("notes %q", notes)
	}
	for _, n := range notes {
		if strings.Contains(n, "WC") || strings.Contains(n, "web-console") {
			t.Errorf("the hidden project is named: %q", n)
		}
	}
}

// With no live agent nothing is queued: the next one's briefing lists
// what is linked.
func TestALinkWithNoLiveAgentQueuesNothing(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{}}}})
	if n := s.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id WHERE r.session_id = $1`, id); n != 0 {
		t.Errorf("%d directives with no agent", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 0 {
		t.Errorf("a link started an agent")
	}
}
