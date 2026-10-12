package orchestrator_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// authFailure is git's error for a private repository it cloned without
// credentials, as lux reported it for a URL GitHub redirected.
const authFailure = "git clone: exit status 128: error: unable to read askpass response from '/bin/false'\n" +
	"fatal: could not read Username for 'https://github.com': terminal prompts disabled"

// cloneFailingSession is a session linked to BL with no repository, its
// agent started and done with its first turn, and a fake lux that fails
// every clone of the web repository with git's authentication error.
func cloneFailingSession(t *testing.T) (s *sessionWorld, id, run string) {
	s = newSessionWorld(t)
	s.withTools()
	s.lux.CloneError = func(url string) string {
		if strings.Contains(url, "/web.git") {
			return authFailure
		}
		return ""
	}
	id = s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	return s, id, s.started(id)
}

// resumeSecrets are the secret names each resume of the dude Run carried.
func (s *sessionWorld) resumeSecrets(runID string) [][]string {
	for _, r := range s.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] != runID {
			continue
		}
		var out [][]string
		for _, secrets := range slices.Clone(r.ResumeSecrets) {
			var names []string
			for _, sec := range secrets {
				if sec.Value != "" {
					names = append(names, sec.Name)
				}
			}
			out = append(out, names)
		}
		return out
	}
	return nil
}

// parkSession parks the session's agent as its quiet turn's end would.
func (s *sessionWorld) parkSession(run string) {
	s.t.Helper()
	s.until("the session's turn done", func() bool {
		return s.count(`SELECT count(*) FROM runs r WHERE id = $1 AND status = 'running' AND turn_done_at IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.failed_at IS NULL)`, run) == 1
	})
	mustExec(s.t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours' WHERE id = $1`, run)
	s.until("the session parked", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'session'
			AND lux_state = 'stopped'`, run) == 1
	})
}

// A repository linked to a session whose clone fails is dropped by lux,
// which keeps its credential (GIT_TOKEN) as a secret of the Run. The next
// resume's spec names no repository, yet it supplies GIT_TOKEN, as lux
// requires: the session's agent resumes, it does not fail. Linked again,
// with the credential still declared, the repository is checked out.
func TestAResumeAfterAFailedCloneSuppliesTheCredentialLuxKept(t *testing.T) {
	s, id, run := cloneFailingSession(t)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	s.until("the clone failed", func() bool {
		return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'repository.clone_failed'`, run) == 1
	})
	if got := s.resumeSecrets(run); len(got) != 1 || !slices.Contains(got[0], "GIT_TOKEN") {
		t.Fatalf("the resume adding the repository carried %v, want GIT_TOKEN", got)
	}

	s.parkSession(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	s.until("the agent resumed", func() bool {
		return len(s.resumeSecrets(run)) == 2 && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, run) == 1
	})
	if got := s.resumeSecrets(run)[1]; !slices.Contains(got, "GIT_TOKEN") {
		t.Errorf("the resume after the failed clone carried %v, want GIT_TOKEN", got)
	}
	s.until("its turn answered", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND turn_done_at IS NOT NULL`, run) == 1
	})
	if now, status := s.brainstorm(id); now != run || status == "failed" {
		t.Errorf("the session's agent is %s %s, want %s running", now, status, run)
	}

	// The member links it again once the clone can succeed: lux accepts the
	// credential it kept declared, and web is checked out.
	s.lux.CloneError = nil
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	s.until("web checked out", func() bool {
		return s.count(`SELECT count(*) FROM events c JOIN events k ON k.run_id = c.run_id AND k.event_type = 'git.checkout'
				AND k.payload->>'repo' = c.payload->>'repo'
			WHERE c.run_id = $1 AND c.event_type = 'git.clone' AND c.payload->>'status' = 'cloned'`, run) == 1
	})
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1 AND repository_id = $2`, id, s.webRepo); n != 1 {
		t.Errorf("web is not linked after the re-link")
	}
}

// A resume of a Run lux declares no GIT_TOKEN for, with no repository,
// sends none: the forge's token is not handed to lux for nothing.
func TestAResumeWithNoRepositorySendsNoGitToken(t *testing.T) {
	s, id, run := cloneFailingSession(t)
	s.parkSession(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	s.until("the agent resumed", func() bool {
		return len(s.resumeSecrets(run)) == 1 && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, run) == 1
	})
	if got := s.resumeSecrets(run)[0]; slices.Contains(got, "GIT_TOKEN") {
		t.Errorf("the resume carried %v, want no GIT_TOKEN", got)
	}
}

// A linked repository whose clone fails is said: its agent gets dude's
// line naming it and git's reason (a directive, delivered as any), the
// members a line in Chat in plain words, and the session no longer links
// the repository. Its project stays linked, showing nothing checked out,
// and list_repositories gives the repository no path. A github.com
// repository's sign-in failure names GitHub.
func TestAFailedCloneIsToldToTheAgentAndTheMembers(t *testing.T) {
	s, id, run := cloneFailingSession(t)
	mustExec(t, s.owner, `UPDATE repositories SET url = 'https://github.com/acme/web.git' WHERE id = $1`, s.webRepo)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	s.until("the clone failed", func() bool {
		return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'repository.clone_failed'`, run) == 1
	})

	note := "WC/web could not be checked out (could not read Username for 'https://github.com': terminal prompts disabled). " +
		"It is not in /workspace; a member can check the repository's URL and access and link it again."
	var directive string
	if err := s.owner.QueryRow(context.Background(), `SELECT id FROM directives WHERE run_id = $1 AND text = $2`, run, note).Scan(&directive); err != nil {
		t.Fatalf("no directive telling the agent: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told'
		AND payload->>'text' = $2 AND payload->>'directiveId' = $3`, id, note, directive); n != 1 {
		t.Errorf("%d session.told for it", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'repository.clone_failed'
		AND payload->>'key' = 'WC' AND payload->>'name' = 'web' AND payload->>'reason' = $2`, run,
		"dude couldn't sign in to GitHub for this repository: check its URL and that dude's GitHub access covers it."); n != 1 {
		t.Errorf("the members' line does not name WC/web with the reason in plain words")
	}
	s.until("the agent read it", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, directive) == 1
	})
	if v := s.luxRun(run); !slices.Contains(v.inputs, note) {
		t.Errorf("the agent's inputs: %v", v.inputs)
	}

	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1`, id); n != 0 {
		t.Errorf("%d repositories still linked", n)
	}
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, id, s.webProject); n != 1 {
		t.Errorf("the project's link went with the repository's")
	}
	status, out := s.tool(run, "list_repositories", `{"project":"WC"}`)
	if status != 200 {
		t.Fatalf("list_repositories: %d %v", status, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "/workspace/repos/WC/web") {
		t.Errorf("list_repositories still says web is checked out: %s", raw)
	}
	if _, status := s.brainstorm(id); status == "failed" {
		t.Errorf("the session's agent failed")
	}
}
