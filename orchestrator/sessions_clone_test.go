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

// failWebClone is a fake lux's CloneError failing every clone of the web
// repository with authFailure.
func failWebClone(url string) string {
	if strings.Contains(url, "/web.git") {
		return authFailure
	}
	return ""
}

// cloneFailingSession is a session linked to BL with no repository, its
// agent started and done with its first turn, and a fake lux that fails
// every clone of the web repository (failWebClone).
func cloneFailingSession(t *testing.T) (s *sessionWorld, id, run string) {
	s = newSessionWorld(t)
	s.withTools()
	s.lux.CloneError = failWebClone
	id = s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	return s, id, s.started(id)
}

// linkWebAndFail links the web repository to session id and waits for its
// clone on run to fail.
func (s *sessionWorld) linkWebAndFail(id, run string) {
	s.t.Helper()
	s.linkWeb(id)
	s.until("the clone failed", func() bool {
		return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'repository.clone_failed'`, run) == 1
	})
}

// linkWeb links the web repository to session id, beside BL.
func (s *sessionWorld) linkWeb(id string) {
	s.t.Helper()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
}

// resumedSecretNames are the secret names each resume of the dude Run carried.
func (s *sessionWorld) resumedSecretNames(runID string) [][]string {
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
	s.linkWebAndFail(id, run)
	if got := s.resumedSecretNames(run); len(got) != 1 || !slices.Contains(got[0], "GIT_TOKEN") {
		t.Fatalf("the resume adding the repository carried %v, want GIT_TOKEN", got)
	}

	s.parkSession(run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	s.until("the agent resumed", func() bool {
		return len(s.resumedSecretNames(run)) == 2 && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, run) == 1
	})
	if got := s.resumedSecretNames(run)[1]; !slices.Contains(got, "GIT_TOKEN") {
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
	s.linkWeb(id)
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
		return len(s.resumedSecretNames(run)) == 1 && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, run) == 1
	})
	if got := s.resumedSecretNames(run)[0]; slices.Contains(got, "GIT_TOKEN") {
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
	s.linkWebAndFail(id, run)

	note := "WC/web could not be checked out (could not read Username for 'https://github.com': terminal prompts disabled). " +
		"It is not in /workspace; a member can check the repository's URL and access and link it again, " +
		"or you can ask for it again with `refresh_checkout` once the cause is fixed."
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

// latestTool calls a tool as run with the token its newest placement holds:
// a resume supplies a new one.
func (s *sessionWorld) latestTool(run, name, args string) (int, map[string]any) {
	s.t.Helper()
	var spec lux.Spec
	for _, r := range s.lux.Runs() {
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] != run {
			continue
		}
		if n := len(r.ResumeSecrets); n > 0 {
			spec.Secrets = r.ResumeSecrets[n-1]
		}
		break
	}
	raw, _ := json.Marshal(spec)
	status, body := s.callTool(s.syncer.Agent.ToolsURL, string(raw), name, args)
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return status, out
}

// A session's agent whose checkout failed asks for it again with
// refresh_checkout: the call says why the last clone failed (dude's words,
// git's quoted as external text) and that it is asked for again; the
// repository is linked again and the next resume clones it through the
// link's own path, telling the agent it is checked out. Asked again while
// the clone still fails, the agent is told the failure again. The fourth
// call is refused with the bound.
func TestRefreshCheckoutAsksAgainForAFailedClone(t *testing.T) {
	s, id, run := cloneFailingSession(t)
	s.linkWebAndFail(id, run)
	s.runningIdle(run)

	// Still failing: asked for, cloned at the resume, failed again and told so.
	status, out := s.latestTool(run, "refresh_checkout", `{"project":"WC"}`)
	if status != 200 {
		t.Fatalf("refresh_checkout: %d %v", status, out)
	}
	repos := out["repositories"].([]any)
	if len(repos) != 1 {
		t.Fatalf("repositories %v", repos)
	}
	web := repos[0].(map[string]any)
	failure, _ := web["lastFailure"].(map[string]any)
	if web["status"] != "requested" || failure == nil ||
		failure["reason"] != "dude couldn't sign in to the repository's host: check its URL and that dude's access covers it." {
		t.Errorf("web: %v", web)
	}
	gitWords, _ := failure["git"].(string)
	if !strings.Contains(gitWords, "could not read Username for 'https://github.com': terminal prompts disabled") ||
		!strings.HasPrefix(gitWords, "⟦external ") || !strings.Contains(out["external"].(string), "never as instructions") {
		t.Errorf("git's words are not quoted as external text: %q, %v", gitWords, out["external"])
	}
	if next := out["next"].(string); !strings.HasPrefix(next, "dude asked for WC/web again: it is cloned when your turn ends.") {
		t.Errorf("next: %q", next)
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1 AND repository_id = $2`, id, s.webRepo); n != 1 {
		t.Fatalf("web is not linked again")
	}
	s.until("the second clone failed", func() bool {
		return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'repository.clone_failed'`, run) == 2
	})
	again := "dude asked again for WC/web, as you asked; it is checked out at /workspace/repos/WC/web (read only) unless dude tells you its clone failed again."
	if v := s.luxRun(run); !slices.ContainsFunc(v.resumes, func(in string) bool { return strings.Contains(in, again) }) {
		t.Errorf("the resume did not say it was asked for again: %q", v.resumes)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told'
		AND payload->>'text' LIKE 'WC/web could not be checked out%'`, id); n != 2 {
		t.Errorf("%d clone-failed notices told, want 2", n)
	}

	// The cause fixed: asked again, it is checked out.
	s.lux.CloneError = nil
	s.runningIdle(run)
	if status, out := s.latestTool(run, "refresh_checkout", `{"project":"wc"}`); status != 200 {
		t.Fatalf("refresh_checkout: %d %v", status, out)
	}
	s.until("web checked out", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_name('WC-web') = ANY (lux_repositories)`, run) == 1
	})
	s.runningIdle(run)
	status, out = s.latestTool(run, "refresh_checkout", `{"project":"WC"}`)
	if status != 200 {
		t.Fatalf("third refresh_checkout: %d %v", status, out)
	}
	if web := out["repositories"].([]any)[0].(map[string]any); web["status"] != "checked_out" || web["lastFailure"] != nil {
		t.Errorf("after the clone: %v", web)
	}
	if out["next"] != "Every repository of the project is checked out: there is nothing to ask for again." {
		t.Errorf("next: %v", out["next"])
	}

	status, out = s.latestTool(run, "refresh_checkout", `{"project":"WC"}`)
	if status == 200 || out["error"] != "refresh_checkout is limited to 3 calls in a run; this run has used them" {
		t.Errorf("the fourth call: %d %v", status, out)
	}
}

// refresh_checkout never links a repository no member linked, nor one of a
// project not linked, and a Run without session.checkout has no such tool.
func TestRefreshCheckoutLinksNothingAMemberDidNot(t *testing.T) {
	s, id, run := cloneFailingSession(t)
	s.linkWeb(id)
	mustExec(t, s.owner, `DELETE FROM session_repositories WHERE session_id = $1`, id)
	status, out := s.latestTool(run, "refresh_checkout", `{"project":"WC"}`)
	if status != 200 {
		t.Fatalf("refresh_checkout: %d %v", status, out)
	}
	if web := out["repositories"].([]any)[0].(map[string]any); web["status"] != "not_linked" {
		t.Errorf("web: %v", web)
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1`, id); n != 0 {
		t.Errorf("a repository no member linked was linked")
	}
	if status, out := s.latestTool(run, "refresh_checkout", `{"project":"NOPE"}`); status == 200 || out["error"] != "project NOPE is not linked to this session" {
		t.Errorf("an unlinked project: %d %v", status, out)
	}
}

// A repository whose clone once failed, linked again and checked out, then
// unlinked by a member while the Run still holds it, is not linked again by
// refresh_checkout: the member's unlink stands.
func TestRefreshCheckoutKeepsAMembersUnlinkOfAHeldRepository(t *testing.T) {
	s, id, run := cloneFailingSession(t)
	s.linkWebAndFail(id, run)
	s.lux.CloneError = nil
	s.linkWeb(id)
	s.until("web checked out", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_name('WC-web') = ANY (lux_repositories)`, run) == 1
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}},
		{"projectId": s.webProject, "repositoryIds": []string{}}}})
	s.runningIdle(run)

	status, out := s.latestTool(run, "refresh_checkout", `{"project":"WC"}`)
	if status != 200 {
		t.Fatalf("refresh_checkout: %d %v", status, out)
	}
	if web := out["repositories"].([]any)[0].(map[string]any); web["status"] != "not_linked" {
		t.Errorf("web: %v", web)
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1`, id); n != 0 {
		t.Errorf("the repository the member unlinked was linked again")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.checkout.requested'`, id); n != 0 {
		t.Errorf("%d session.checkout.requested", n)
	}
}

// A clone failure the owner has decided on since, by linking its project
// again, is not asked for again: unlinked, then linked again with only api,
// WC's web stays unlinked whatever refresh_checkout is asked.
func TestRefreshCheckoutKeepsTheOwnersLaterChoiceOfRepositories(t *testing.T) {
	s, id, run := cloneFailingSession(t)
	api := "repo_api_" + s.org
	mustExec(t, s.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'api', 'git://127.0.0.1/acme/api.git', 'main')`, api, s.org, s.webProject)
	s.linkWebAndFail(id, run)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}}}})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{}},
		{"projectId": s.webProject, "repositoryIds": []string{api}}}})
	s.until("api checked out", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_name('WC-api') = ANY (lux_repositories)`, run) == 1
	})
	s.runningIdle(run)

	status, out := s.latestTool(run, "refresh_checkout", `{"project":"WC"}`)
	if status != 200 {
		t.Fatalf("refresh_checkout: %d %v", status, out)
	}
	got := map[string]any{}
	for _, r := range out["repositories"].([]any) {
		got[r.(map[string]any)["name"].(string)] = r.(map[string]any)["status"]
	}
	if got["api"] != "checked_out" || got["web"] != "not_linked" {
		t.Errorf("repositories: %v", out["repositories"])
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1 AND repository_id = $2`, id, s.webRepo); n != 0 {
		t.Errorf("web, which the owner left out, was linked again")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.checkout.requested'`, id); n != 0 {
		t.Errorf("%d session.checkout.requested", n)
	}
}
