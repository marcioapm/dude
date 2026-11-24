package orchestrator_test

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// setupSecret is the setup manifest's secret, as phases.SetupSecret names it.
var setupSecret = phases.SetupSecret(nil)

// submittedOf is the lux Run of the dude Run, and its spec as submitted.
func (s *sessionWorld) submittedOf(runID string) (string, lux.Spec) {
	s.t.Helper()
	for _, r := range s.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] == runID {
			return r.ID, spec
		}
	}
	s.t.Fatalf("no lux Run of %s", runID)
	return "", lux.Spec{}
}

// A brainstorm reads, asks and proposes; it never builds or tests: its Run
// is submitted with no setup script, though its repository has one, and no
// manifest. A brainstorm Run submitted with one by an older dude (lux holds
// the manifest declared) still resumes, the manifest given again.
func TestABrainstormRunsNoSetupScriptAndAnOldOneStillResumes(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	mustExec(t, s.owner, `UPDATE repositories SET setup_script = 'npm ci' WHERE id = $1`, s.repoID)
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{s.repoID}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	run := s.started(id)
	luxID, spec := s.submittedOf(run)
	if spec.Git == nil || len(spec.Git.Repositories) == 0 {
		t.Fatalf("the brainstorm checked out no repository: %+v", spec.Git)
	}
	if spec.Init != nil && strings.Contains(spec.Init.Script, phases.SetupManifestPath) {
		t.Errorf("the brainstorm's init runs setup scripts: %q", spec.Init.Script)
	}
	if slices.ContainsFunc(spec.Secrets, func(sec lux.Secret) bool { return sec.Name == setupSecret.Name }) {
		t.Error("the brainstorm's spec carries the setup manifest")
	}

	// As if submitted by an older dude: lux holds the manifest declared.
	s.lux.DeclareSecret(luxID, setupSecret)
	s.parkSession(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	s.until("the agent resumed", func() bool {
		return len(s.resumedSecretNames(run)) == 1 && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, run) == 1
	})
	if got := s.resumedSecretNames(run)[0]; !slices.Contains(got, setupSecret.Name) {
		t.Errorf("the old brainstorm's resume carried %v, want the setup manifest", got)
	}
	// The manifest given again names the repository and its script, not a header alone.
	repo := base64.StdEncoding.EncodeToString([]byte(spec.Git.Repositories[0].Name))
	script := base64.StdEncoding.EncodeToString([]byte("npm ci"))
	if got := s.resumedManifest(luxID); !strings.Contains(got, repo+" ") || !strings.Contains(got, " "+script+"\n") {
		t.Errorf("the old brainstorm's resumed manifest %q names no %s with its script", got, spec.Git.Repositories[0].Name)
	}
	if now, status := s.brainstorm(id); now != run || status == "failed" {
		t.Errorf("the session's agent is %s %s, want %s running", now, status, run)
	}
}

// resumedManifest is the setup manifest the first resume of lux Run luxID
// carried, "" when it carried none.
func (s *sessionWorld) resumedManifest(luxID string) string {
	s.t.Helper()
	for _, r := range s.lux.Runs() {
		if r.ID != luxID || len(r.ResumeSecrets) == 0 {
			continue
		}
		for _, sec := range r.ResumeSecrets[0] {
			if sec.Name == setupSecret.Name {
				return sec.Value
			}
		}
	}
	return ""
}
