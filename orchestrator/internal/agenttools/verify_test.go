package agenttools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// verify starts a report-only child with publishing off on the commit
// named; an implementer is not granted it, and a verifier itself is
// refused verify, start_agent and publish as every report child is.
func TestVerifyStartsAChildThatCannotPublish(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_verify_parent", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_verify_parent'`)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_verify', $1, $2, 'api', 'https://github.com/acme/api', 'main')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ($1, 'repo_verify', $2, 'write')`, f.item, f.org)
	head := "0123456789abcdef0123456789abcdef01234567"
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		VALUES ('wf_verify', $1, $2, 'task.delivery', 'verify', 'waiting', 'review', jsonb_build_object('attempt', 1, 'heads', jsonb_build_object('api', $3::text)))`,
		f.org, f.item, head)

	var out struct{ RunID, Repo, Commit string }
	if status := f.postAs(t, token, "verify", `{"commands":["go test ./..."]}`, &out); status != 200 || out.Commit != head || out.Repo != "api" {
		t.Fatalf("verify: %d %+v", status, out)
	}
	var phase, prompt string
	var publish bool
	var bases map[string]string
	if err := f.owner.QueryRow(context.Background(), `SELECT phase::text, (child_capabilities->>'publishCode')::bool, base_refs, child_prompt
		FROM runs WHERE id = $1 AND source_kind = 'task_conductor' AND parent_run_id = 'run_verify_parent'`, out.RunID).
		Scan(&phase, &publish, &bases, &prompt); err != nil {
		t.Fatal(err)
	}
	if phase != "investigate" || publish || bases["api"] != head {
		t.Fatalf("the verifier: %s publish %v bases %v", phase, publish, bases)
	}

	implementer := f.run(t, "run_verify_impl", "implementer", "running")
	if status, _ := f.post(t, implementer, "verify", `{}`); status != 404 {
		t.Fatalf("an implementer's verify: %d", status)
	}
	// A verifier whose role does hold verify, start_agent and publish (a
	// conductor's): it is refused them because it is a child, not for want
	// of a grant. (One live conductor a task: the parent's Run has ended.)
	mustExec(t, f.owner, `UPDATE runs SET status='completed' WHERE id='run_verify_parent'`)
	child := f.run(t, "run_verify_child", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL,source_kind='task_conductor',parent_run_id='run_verify_parent',child_profile='investigator',
		child_capabilities='{"publishCode":false,"verify":{"repo":"api","commit":"`+head+`"}}' WHERE id='run_verify_child'`)
	for _, name := range []string{"verify", "start_agent", "publish"} {
		if status, out := f.post(t, child, name, `{}`); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "cannot mutate workflow or start Runs") {
			t.Errorf("a verifier's %s: %d %v", name, status, out)
		}
	}
}
