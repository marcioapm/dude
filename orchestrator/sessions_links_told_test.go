package orchestrator_test

import (
	"strings"
	"testing"
	"time"
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

// In a session started from a chat thread, a message typed in dude says
// so, as a thread's says it came from the thread; in one started in dude
// there is nothing to tell apart: "Name: words".
func TestAMessageTypedInDudeSaysSoInASessionFromChat(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "I already linked it"})
	run := s.started(id)
	if p := s.one(`SELECT prompt FROM runs WHERE id = $1`, run); !strings.Contains(p, "## The first message\n\nMárcio Martins (in dude): I already linked it") {
		t.Errorf("the briefing's message:\n%s", p)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look again"})
	if d := s.one(`SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run); d != "Márcio Martins (in dude): look again" {
		t.Errorf("directive %q", d)
	}

	plain := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+plain+"/chat", map[string]any{"text": "hello"})
	other := s.started(plain)
	s.ok(s.marcio, "POST", "/internal/sessions/"+plain+"/chat", map[string]any{"text": "again"})
	if d := s.one(`SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, other); d != "Márcio Martins: again" {
		t.Errorf("directive with no chat origin %q", d)
	}
}

// The syncer settling a failed clone locks the session's Run, then deletes
// its session_repositories row. An owner unticking that repository at the
// same moment locks the Run before touching the row, so the two queue one
// behind the other instead of deadlocking (40P01).
func TestAnUnlinkWaitsForTheSyncerSettlingAFailedClone(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look at billing"})
	run := s.started(id)

	syncer := s.openTx()
	if _, err := syncer.Exec(t0(), `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, run); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		status int
		body   map[string]any
	}
	unlinked := make(chan answer, 1)
	go func() {
		status, body := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
			{"projectId": s.project, "repositoryIds": []string{}}}})
		unlinked <- answer{status, body}
	}()
	for deadline := time.Now().Add(10 * time.Second); s.count(`SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> $1
		  AND query LIKE '%FROM runs r WHERE r.session_id = $1%FOR NO KEY UPDATE%'`, syncer.pid) == 0; {
		select {
		case a := <-unlinked:
			t.Fatalf("the unlink did not wait on the syncer's Run: %d %v", a.status, a.body)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the unlink never waited on a lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The unlink waits; the syncer goes on to its delete, which must not
	// wait on the unlink.
	if _, err := syncer.Exec(t0(), `DELETE FROM session_repositories WHERE session_id = $1 AND repository_id = $2`, id, s.repoID); err != nil {
		t.Fatalf("the syncer's delete: %v", err)
	}
	if err := syncer.Commit(t0()); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-unlinked:
		if a.status != 200 {
			t.Fatalf("the unlink: %d %v", a.status, a.body)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the unlink never finished")
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1`, id); n != 0 {
		t.Errorf("%d repositories still linked", n)
	}
}

// An answer typed in dude, as a message or through the form, says so in a
// session started from chat, as any other message does; in one started in
// dude it is named as before.
func TestAnAnswerTypedInDudeSaysSoInASessionFromChat(t *testing.T) {
	for _, way := range []string{"message", "form", "message to a fresh agent"} {
		t.Run(way, func(t *testing.T) {
			s := newSessionWorld(t)
			id, run, qid := s.asked(`{"question":"Which layout?"}`, false)
			mustExec(t, s.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
			want := "Márcio Martins (in dude) answered your question \"Which layout?\":\n\nGrid"
			switch way {
			case "form":
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/questions/"+qid+"/answer", map[string]any{"text": "Grid"})
			case "message":
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "Grid"})
			default:
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "Grid"})
				next := s.freshAgent(id, run)
				if p := s.prompt(next); !strings.Contains(p, want) {
					t.Errorf("the fresh agent's briefing lacks %q:\n%s", want, p)
				}
				return
			}
			if d := s.one(`SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run); d != want {
				t.Errorf("directive %q, want %q", d, want)
			}
		})
	}
	s := newSessionWorld(t)
	_, run, qid := s.asked(`{"question":"Which layout?"}`, false)
	id := s.one(`SELECT session_id FROM runs WHERE id = $1`, run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/questions/"+qid+"/answer", map[string]any{"text": "Grid"})
	if d := s.one(`SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run); d != "Márcio Martins answered your question \"Which layout?\":\n\nGrid" {
		t.Errorf("directive with no chat origin %q", d)
	}
}
