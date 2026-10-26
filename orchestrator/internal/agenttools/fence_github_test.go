package agenttools_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A GitHub read of a hidden project's repository is refused before GitHub
// is asked, for a chat-born session (the project linked before it was
// hidden) and for a task from chat whose own project is another; the task's
// own project's repository, hidden too, is read.
func TestAGitHubReadOfAHiddenProjectsRepositoryIsRefusedUnasked(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = w.Write([]byte(`{"number":7,"title":"t","state":"open","body":""}`))
	}))
	t.Cleanup(srv.Close)
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_fence_" + strconv.FormatInt(time.Now().UnixNano(), 10), APIBaseURL: srv.URL})
	f := setupWith(t, &agenttools.Server{Forges: forgesOf{gh}})
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_txt_'||$1, $1, $2, 'text', 'https://github.com/acme/text.git', 'main')`, f.org, f.project)
	session := f.fencedSession(t, "ssn_gh_fenced")
	status, out := f.post(t, session, "github_issue", `{"repo":"acme/vault","number":7}`)
	if status != 422 || asked.Load() != 0 {
		t.Fatalf("a session's read of the hidden project's repository: %d %v, GitHub asked %d times", status, out, asked.Load())
	}

	mustExec(t, f.owner, `UPDATE tasks SET origin = '{"provider":"slack"}' WHERE id = $1`, f.item)
	mustExec(t, f.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, f.org, f.project)
	conductor := f.conductor(t)
	if status, out := f.post(t, conductor, "github_issue", `{"repo":"acme/vault","number":7}`); status != 422 || asked.Load() != 0 {
		t.Fatalf("a task's read of another hidden project's repository: %d %v, GitHub asked %d times", status, out, asked.Load())
	}
	if status, out := f.post(t, conductor, "github_issue", `{"repo":"acme/text","number":7}`); status != 200 || asked.Load() == 0 {
		t.Fatalf("a task's read of its own hidden project's repository: %d %v", status, out)
	}
}
