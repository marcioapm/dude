package agenttools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func TestAutomationToolsScopeAndLifecycle(t *testing.T) {
	posts := 0
	lastBody := ""
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repos/acme/web/issues/7/comments" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		posts++
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "html_url": "https://example.com/comment/42"})
	}))
	defer ghServer.Close()
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "automation-test", APIBaseURL: ghServer.URL})
	f := setupWith(t, &agenttools.Server{Forges: forgesOf{gh}})
	token, hash := agenttools.NewToken()
	person := "person_" + f.org
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1,$2,'Ana','admin')`, person, f.org)
	mustExec(t, f.owner, `INSERT INTO automations (id, organization_id, project_id, name, kind, owner_id, trigger) VALUES ('aut_tools',$1,$2,'Tools','agent',$3,'{"kind":"schedule","cron":"0 * * * *","tz":"UTC"}')`, f.org, f.project, person)
	scope := agenttools.AutomationScope{ProjectID: f.project, OwnerID: person, Repos: []string{"acme/web"}, EventRepo: "acme/web", EventNumber: 7}
	mustExec(t, f.owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source, mcp_token_hash, tools_scope) VALUES ('arn_tools',$1,'aut_tools','running','schedule',$2,$3)`, f.org, hash, scope)
	req, _ := http.NewRequest("GET", f.url+"/tools", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	err = json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("list: %d %v", resp.StatusCode, err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"create_task", "github_check_log", "github_comment", "github_issue", "github_pull_request", "github_search"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools: %v", names)
	}
	if status, out := f.post(t, token, "create_task", `{"title":"Review automation findings","goal":"Check the findings from the scheduled automation."}`); status != 200 {
		t.Fatalf("create: %d %v", status, out)
	}
	var by, owner, actingFor string
	err = f.owner.QueryRow(context.Background(), `SELECT t.created_by_automation_run_id, tp.person_id, e.payload->>'actingFor' FROM tasks t JOIN task_people tp ON tp.task_id=t.id JOIN events e ON e.task_id=t.id AND e.event_type='task.created' WHERE t.project_id=$1 AND t.number=2`, f.project).Scan(&by, &owner, &actingFor)
	if err != nil || by != "arn_tools" || owner != person || actingFor != person {
		t.Fatalf("attribution: %s %s %s %v", by, owner, actingFor, err)
	}
	if status, _ := f.post(t, token, "github_comment", `{"repo":"acme/outside","number":7,"body":"No access"}`); status != 422 {
		t.Fatalf("outside repo: %d", status)
	}
	if posts != 0 {
		t.Fatal("out-of-scope comment reached GitHub")
	}
	if status, out := f.post(t, token, "github_comment", `{"body":"Automation findings"}`); status != 200 {
		t.Fatalf("comment: %d %v", status, out)
	}
	if posts != 1 {
		t.Fatalf("posts: %d", posts)
	}
	// A project secret the automation gives its runs, pasted into a tool
	// call, reaches neither GitHub nor the ledger.
	mustExec(t, f.owner, `INSERT INTO project_secrets (organization_id, project_id, name, value, hint) VALUES ($1, $2, 'DEPLOY_KEY', 'acme-deploy-s3cr3t-value', 'alue')`, f.org, f.project)
	mustExec(t, f.owner, `UPDATE automations SET secrets = '[{"source":"project","name":"DEPLOY_KEY"}]' WHERE id = 'aut_tools'`)
	if status, out := f.post(t, token, "github_comment", `{"body":"Using acme-deploy-s3cr3t-value to deploy"}`); status != 200 {
		t.Fatalf("comment with a secret: %d %v", status, out)
	}
	if strings.Contains(lastBody, "acme-deploy-s3cr3t-value") || !strings.Contains(lastBody, "to deploy") {
		t.Errorf("GitHub got %q", lastBody)
	}
	var leaked int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE organization_id = $1 AND payload::text LIKE '%acme-deploy-s3cr3t-value%'`, f.org).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("ledger events carrying the secret: %d %v", leaked, err)
	}
	// An organisation secret it picks is redacted the same way.
	mustExec(t, f.owner, `INSERT INTO organization_secrets (organization_id, name, value, hint) VALUES ($1, 'ORG_DEPLOY', 'acme-org-s3cr3t-value', 'alue')`, f.org)
	mustExec(t, f.owner, `UPDATE automations SET secrets = '[{"source":"organization","name":"ORG_DEPLOY"}]' WHERE id = 'aut_tools'`)
	if status, out := f.post(t, token, "github_comment", `{"body":"Using acme-org-s3cr3t-value to deploy"}`); status != 200 {
		t.Fatalf("comment with an organisation secret: %d %v", status, out)
	}
	if strings.Contains(lastBody, "acme-org-s3cr3t-value") || !strings.Contains(lastBody, "[REDACTED:ORG_DEPLOY]") {
		t.Errorf("GitHub got %q", lastBody)
	}
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE organization_id = $1 AND payload::text LIKE '%acme-org-s3cr3t-value%'`, f.org).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("ledger events carrying the organisation secret: %d %v", leaked, err)
	}
	if status, _ := f.post(t, token, "publish", `{}`); status != 404 {
		t.Fatalf("publish: %d", status)
	}
	mustExec(t, f.owner, `UPDATE automation_runs SET tools_scope=jsonb_set(tools_scope,'{projectId}','""') WHERE id='arn_tools'`)
	if status, _ := f.post(t, token, "create_task", `{"title":"No project","goal":"An organisation schedule has no destination project."}`); status != 422 {
		t.Fatalf("no project: %d", status)
	}
	for _, status := range []string{"succeeded", "failed", "cancelled", "skipped"} {
		mustExec(t, f.owner, `UPDATE automation_runs SET status=$1 WHERE id='arn_tools'`, status)
		if code, _ := f.post(t, token, "github_comment", `{"body":"Ended"}`); code != 401 {
			t.Fatalf("ended %s: %d", status, code)
		}
	}
}
