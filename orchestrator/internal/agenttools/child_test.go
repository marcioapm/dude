package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func TestStartChildToolProfilesAndReplay(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_agent_parent", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_agent_parent'`)
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		VALUES ('wf_agent', $1, $2, 'task.delivery', 'agent', 'waiting', 'conductorDecision', '{"attempt":1}')`, f.org, f.item)
	for _, profile := range []string{"implementer", "fixer", "simplifier", "reviewer", "qa_browser", "investigator"} {
		body := fmt.Sprintf(`{"profile":%q,"key":%q}`, profile, profile)
		var first, again struct{ RunID string }
		if status := f.postAs(t, token, "start_agent", body, &first); status != 200 {
			t.Fatalf("%s: %d", profile, status)
		}
		if status := f.postAs(t, token, "start_agent", body, &again); status != 200 || first.RunID == "" || first.RunID != again.RunID {
			t.Fatalf("retry %s: %d %+v %+v", profile, status, first, again)
		}
		mustExec(t, f.owner, `UPDATE runs SET status='completed' WHERE id=$1`, first.RunID)
	}
	for _, profile := range []string{"unknown", "conductor", "brainstorm"} {
		if status, out := f.post(t, token, "start_agent", fmt.Sprintf(`{"profile":%q}`, profile)); status != 422 {
			t.Fatalf("%s: %d %v", profile, status, out)
		}
	}
	var count int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE source_kind='task_conductor'`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("children %d: %v", count, err)
	}
	if _, ok := f.getTools(t, token)["start_agent"]; !ok {
		t.Fatal("conductor lacks start_agent")
	}
	worker := f.run(t, "run_agent_worker", "implementer", "running")
	if _, ok := f.getTools(t, worker)["start_agent"]; ok {
		t.Fatal("worker lists start_agent")
	}
}

func TestStartChildToolCallIdentity(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_identity", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_identity'`)
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		VALUES ('wf_identity', $1, $2, 'task.delivery', 'identity', 'waiting', 'conductorDecision', '{"attempt":1}')`, f.org, f.item)
	call := func(id string) string {
		t.Helper()
		req, err := http.NewRequest("POST", f.url+"/tools/start_agent", strings.NewReader(`{"profile":"investigator"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Dude-Tool-Call-ID", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result struct{ RunID string }
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode != 200 || result.RunID == "" {
			t.Fatalf("call: %d %+v %v", resp.StatusCode, result, err)
		}
		return result.RunID
	}
	first := call("call-one")
	if again := call("call-one"); again != first {
		t.Fatalf("retry made %s instead of %s", again, first)
	}
	if other := call("call-two"); other == first {
		t.Fatal("distinct call IDs shared a child")
	}
}

func TestChildToolsRefuseWorkflowAndRecursion(t *testing.T) {
	var reads atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.WriteHeader(http.StatusOK)
		if strings.Contains(r.URL.Path, "annotations") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"output":{"title":"ci","summary":"failure","text":"exit code 1"}}`))
	}))
	t.Cleanup(github.Close)
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: t.Name(), APIBaseURL: github.URL})
	f := setupWith(t, &agenttools.Server{Forges: forgesOf{gh}})
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url)
		VALUES ('repo_child', $1, $2, 'example', 'https://github.com/acme/example')`, f.org, f.project)
	token := f.run(t, "run_report_child", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET source_kind='task_conductor',parent_run_id=id,child_profile='investigator',child_capabilities='{}' WHERE id='run_report_child'`)
	for _, name := range []string{"start_phase", "restart_run", "publish", "update_task", "ask_person", "create_task", "start_agent", "github_check_log"} {
		body := `{"profile":"investigator"}`
		if name == "github_check_log" {
			body = `{"repo":"acme/example","checkId":1}`
		}
		status, out := f.post(t, token, name, body)
		if (name == "start_agent" || name == "github_check_log") && !strings.Contains(fmt.Sprint(out["error"]), "cannot mutate workflow or start Runs") {
			t.Fatalf("child refusal boundary bypassed for %s: status %d, %v", name, status, out)
		}
		if status != 422 && status != 404 {
			t.Fatalf("child tool %s returned %d", name, status)
		}
	}
	if reads.Load() != 0 {
		t.Fatalf("child refusal reached external read: %d", reads.Load())
	}
	if status, _ := f.post(t, token, "list_tasks", `{}`); status != 200 {
		t.Fatalf("report read returned %d", status)
	}
	for _, name := range []string{"start_agent", "github_check_log"} {
		if _, ok := f.getTools(t, token)[name]; ok {
			t.Fatalf("child lists %s", name)
		}
	}
}
