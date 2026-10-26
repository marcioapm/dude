package agenttools_test

import (
	"strings"
	"testing"
)

// fencedSession is a session started from Slack, linked to TEXT (the
// fixture's project) and SEC, with a live agent; SEC is hidden from Slack.
// It returns the agent's token.
func (f *fixture) fencedSession(t *testing.T, id string) string {
	t.Helper()
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_sec_'||$1, $1, 'Secret', 'sec', 'SEC')
		ON CONFLICT DO NOTHING`, f.org)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_sec_'||$1, $1, 'prj_sec_'||$1, 'vault', 'https://github.com/acme/vault.git', 'main') ON CONFLICT DO NOTHING`, f.org)
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_sec_'||$1, $1, 'prj_sec_'||$1, 1, 'Rotate keys')
		ON CONFLICT DO NOTHING`, f.org)
	mustExec(t, f.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
		VALUES ('mem_sec_'||$1, $1, 'prj_sec_'||$1, 'Vault webhook secret', 'the vault webhook', 'system', 'test') ON CONFLICT DO NOTHING`, f.org)
	token := f.session(t, id, "", "")
	mustExec(t, f.owner, `UPDATE sessions SET origin = '{"provider":"slack","conversationId":"C1"}' WHERE id = $1`, id)
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ($1, $2, $3), ($1, 'prj_sec_'||$3, $3)`,
		id, f.project, f.org)
	mustExec(t, f.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', 'prj_sec_'||$1)
		ON CONFLICT DO NOTHING`, f.org)
	return token
}

func refusedSaying(t *testing.T, status int, out map[string]any, want string) {
	t.Helper()
	if status != 422 || !strings.Contains(strings.ToLower(out["error"].(string)), strings.ToLower(want)) {
		t.Fatalf("%d %v, want a refusal saying %q", status, out, want)
	}
}

// A project hidden from Slack is out of reach of a session started there,
// even linked by hand: its tasks and epics, its repositories, its memory,
// a proposal naming it.
func TestASessionFromChatCannotReachAHiddenProject(t *testing.T) {
	f := setup(t)
	token := f.fencedSession(t, "ssn_fenced")

	// Tasks and epics.
	status, out := f.post(t, token, "list_tasks", `{"project":"SEC"}`)
	refusedSaying(t, status, out, "SEC is hidden from what is started from slack")
	status, out = f.post(t, token, "list_epics", `{"project":"SEC"}`)
	refusedSaying(t, status, out, "hidden")
	// With SEC out of the linked set, TEXT is the only project: no name needed.
	if status, out := f.post(t, token, "list_tasks", `{}`); status != 200 || !strings.Contains(out["tasks"].([]any)[0].(map[string]any)["key"].(string), "TEXT") {
		t.Fatalf("list_tasks of the one project left: %d %v", status, out)
	}
	// Repositories.
	status, out = f.post(t, token, "list_repositories", `{"project":"SEC"}`)
	refusedSaying(t, status, out, "hidden")
	// Memory: neither searched nor read by id.
	status, out = f.post(t, token, "get_memory", `{"id":"mem_sec_`+f.org+`"}`)
	if status != 422 {
		t.Fatalf("get_memory of a hidden project's memory: %d %v", status, out)
	}
	status, out = f.post(t, token, "search_memory", `{"project":"SEC","query":"vault"}`)
	refusedSaying(t, status, out, "hidden")
	// A proposal: a task in it, or linking it.
	status, out = f.post(t, token, "propose", `{"items":[{"kind":"link_project","project":"SEC","reason":"the keys"}]}`)
	refusedSaying(t, status, out, "hidden")
	status, out = f.post(t, token, "propose", `{"items":[{"kind":"task","project":"SEC","title":"Rotate","goal":"Rotate the vault keys every month."}]}`)
	refusedSaying(t, status, out, "hidden")
	status, out = f.post(t, token, "relate", `{"from":"SEC-1","to":"TEXT-1","kind":"related"}`)
	if status != 422 {
		t.Fatalf("relate across into a hidden project: %d %v", status, out)
	}
	// Settings of the project.
	status, out = f.post(t, token, "settings", `{"project":"SEC"}`)
	refusedSaying(t, status, out, "hidden")
}

// The same session, not started from chat, reads the same project.
func TestASessionNotFromChatIsNotFenced(t *testing.T) {
	f := setup(t)
	token := f.fencedSession(t, "ssn_web")
	mustExec(t, f.owner, `UPDATE sessions SET origin = NULL WHERE id = 'ssn_web'`)
	if status, out := f.post(t, token, "list_tasks", `{"project":"SEC"}`); status != 200 {
		t.Fatalf("list_tasks: %d %v", status, out)
	}
	if status, out := f.post(t, token, "list_repositories", `{"project":"SEC"}`); status != 200 {
		t.Fatalf("list_repositories: %d %v", status, out)
	}
	if status, out := f.post(t, token, "get_memory", `{"id":"mem_sec_`+f.org+`"}`); status != 200 {
		t.Fatalf("get_memory: %d %v", status, out)
	}
}

// A task filed from Slack whose project is hidden since: its agent's own
// project tools are refused.
func TestATaskFromChatWhoseProjectIsHiddenIsRefusedItsProject(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_fenced_impl", "implementer", "running")
	mustExec(t, f.owner, `UPDATE tasks SET origin = '{"provider":"slack"}' WHERE id = $1`, f.item)
	if status, out := f.post(t, token, "list_tasks", `{}`); status != 200 {
		t.Fatalf("before hiding: %d %v", status, out)
	}
	mustExec(t, f.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, f.org, f.project)
	for _, tool := range []string{"list_tasks", "list_repositories", "search_memory"} {
		args := `{}`
		if tool == "search_memory" {
			args = `{"query":"x"}`
		}
		status, out := f.post(t, token, tool, args)
		refusedSaying(t, status, out, "this task's project is hidden from what is started from slack")
	}
	// A task not filed from chat is not fenced.
	mustExec(t, f.owner, `UPDATE tasks SET origin = NULL WHERE id = $1`, f.item)
	if status, out := f.post(t, token, "list_tasks", `{}`); status != 200 {
		t.Fatalf("a task not from chat: %d %v", status, out)
	}
}
