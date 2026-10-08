package orchestrator_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A session linking a project whose key and repository name lux would
// refuse as a spec name — an uppercase key, a name past 32 characters —
// starts: its spec names the checkout as lux takes it, at the same
// repos/<KEY>/<name> path; the agent is told that path; and a repository
// linked later, under the same kind of name, is added by the resume and
// held by the Run under the name the spec gave it.
func TestASessionLinkingAnyProjectGetsAValidSpec(t *testing.T) {
	s := newSessionWorld(t)
	const key, long = "BILL", "Billing-API.payments-ledger-service"
	mustExec(t, s.owner, `UPDATE projects SET key_prefix = $2 WHERE id = $1`, s.project, key)
	mustExec(t, s.owner, `UPDATE repositories SET name = $2 WHERE id = $1`, s.repoID, long)
	mustExec(t, s.owner, `UPDATE repositories SET name = 'Web' WHERE id = $1`, s.webRepo)
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{s.repoID}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	run := s.started(id)

	v := s.luxRun(run)
	if v == nil || v.spec.Git == nil || len(v.spec.Git.Repositories) != 1 {
		t.Fatalf("spec %+v", v)
	}
	got := v.spec.Git.Repositories[0]
	want := delivery.SessionRepo{Key: key, Name: long}.SpecName()
	if got.Name != want || !lux.NameRe.MatchString(got.Name) || !strings.HasPrefix(got.Name, "bill-billing-api-") {
		t.Errorf("spec name %q, want %q, valid for lux", got.Name, want)
	}
	if got.Path != "/workspace/repos/BILL/"+long {
		t.Errorf("checked out at %s", got.Path)
	}
	// The briefing (the scripted agent's spec carries its script instead).
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND strpos(prompt, $2) > 0`, run, "`/workspace/repos/BILL/"+long+"`"); n != 1 {
		t.Errorf("the agent is not told where its checkout is")
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_repositories = ARRAY[$2]`, run, want); n != 1 {
		t.Errorf("the Run does not hold %s", want)
	}
	// Held already: nothing for a resume to bring.
	for range 3 {
		s.pump()
	}
	if v := s.luxRun(run); v.resumed != 0 {
		t.Errorf("resumed %d times for a repository it holds", v.resumed)
	}

	// Linked later: added by a resume, under a name lux takes, and settled.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	web := delivery.SessionRepo{Key: "WC", Name: "Web"}.SpecName()
	s.until("the web checkout", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND $2 = ANY (lux_repositories) AND status = 'running'`, run, web) == 1
	})
	v = s.luxRun(run)
	i := slices.IndexFunc(v.spec.Git.Repositories, func(r lux.Repository) bool { return r.Name == web })
	if i < 0 || v.spec.Git.Repositories[i].Path != "/workspace/repos/WC/Web" || !lux.NameRe.MatchString(web) {
		t.Errorf("added %+v, want %s at /workspace/repos/WC/Web", v.spec.Git.Repositories, web)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'repository.clone_failed'`, run); n != 0 {
		t.Errorf("%d clones failed", n)
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1`, id); n != 2 {
		t.Errorf("%d repositories linked, want both", n)
	}
	if _, status := s.brainstorm(id); status == "failed" {
		t.Errorf("the session's agent failed")
	}
}

// A task's repository named in a way lux refuses as a spec name (upper
// case, a dot) is delivered as any: every phase's spec names it as lux
// takes it, the checkout, push and fast-forward match it back, and what
// the ledger and the pull request say name the repository as the task does.
func TestATaskRepositoryLuxWouldRefuseByNameIsDelivered(t *testing.T) {
	w := newWorld(t)
	const name = "Target.API"
	mustExec(t, w.owner, `UPDATE repositories SET name = $2 WHERE id = $1`, w.repoID, name)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Git == nil || len(spec.Git.Repositories) != 1 {
			t.Fatalf("%s: git %+v", spec.Labels["dude.phase"], spec.Git)
		}
		got := spec.Git.Repositories[0]
		if got.Name != lux.SpecName(name) || !lux.NameRe.MatchString(got.Name) || got.Path != "/workspace/repos/"+got.Name {
			t.Errorf("%s: repository %s at %s", spec.Labels["dude.phase"], got.Name, got.Path)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'repo' = $2`,
		wi, name); n == 0 {
		t.Errorf("no commit recorded on %s", name)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND repository_id = $2`, wi, w.repoID); n != 1 {
		t.Errorf("%d pull requests on the repository", n)
	}
}
