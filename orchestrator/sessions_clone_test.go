package orchestrator_test

import (
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
// requires: the session's agent resumes, it does not fail.
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
}
