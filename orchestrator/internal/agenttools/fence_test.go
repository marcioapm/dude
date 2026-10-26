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

// relate writes a person's relation from one task to another (a related
// edge in its stored order).
func (f *fixture) relate(t *testing.T, from, to, kind string) {
	t.Helper()
	if kind == "related" && to < from {
		from, to = to, from
	}
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_rel_'||$1, $1, 'Rel Person') ON CONFLICT DO NOTHING`, f.org)
	mustExec(t, f.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $2, $3, $4::task_relation_kind, 'per_rel_'||$1)`, f.org, from, to, kind)
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

// A task filed from Slack whose project is hidden since keeps working on
// its own project (hiding applies to new work), and still reaches no other
// hidden project.
func TestATaskFromChatWhoseProjectIsHiddenKeepsItsOwnProject(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_fenced_impl", "implementer", "running")
	mustExec(t, f.owner, `UPDATE tasks SET origin = '{"provider":"slack"}' WHERE id = $1`, f.item)
	mustExec(t, f.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
		VALUES ('mem_own_'||$1, $1, $2, 'Truncation rules', 'words longer than forty runes', 'system', 'test')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, f.org, f.project)
	for _, c := range [][2]string{{"list_tasks", `{}`}, {"list_epics", `{}`}, {"list_repositories", `{}`},
		{"get_memory", `{"id":"mem_own_` + f.org + `"}`}} {
		if status, out := f.post(t, token, c[0], c[1]); status != 200 {
			t.Errorf("%s on its own hidden project: %d %v", c[0], status, out)
		}
	}
	conductor := f.conductor(t)
	if status, out := f.post(t, conductor, "settings", `{"section":"delivery"}`); status != 200 {
		t.Errorf("the conductor's settings of its own hidden project: %d %v", status, out)
	}
	status, body := f.raw(t, token, "search_memory", `{"query":"truncation"}`)
	if status != 200 || !strings.Contains(body, "mem_own_") {
		t.Errorf("search_memory of its own project: %d %s", status, body)
	}
	// Another hidden project stays out of reach, its tasks' keys included.
	f.fencedSession(t, "ssn_other")
	f.relate(t, "wi_sec_"+f.org, f.item, "blocks")
	status, body = f.raw(t, token, "list_tasks", `{}`)
	if status != 200 || strings.Contains(body, "SEC-1") {
		t.Errorf("list_tasks shows a hidden project's blocker: %d %s", status, body)
	}
	status, out := f.post(t, conductor, "settings", `{"project":"SEC"}`)
	refusedSaying(t, status, out, "SEC is hidden from what is started from slack")
	if status, out := f.post(t, token, "get_memory", `{"id":"mem_sec_`+f.org+`"}`); status != 422 {
		t.Errorf("get_memory of another hidden project's memory: %d %v", status, out)
	}
	// Not from chat at all: the same.
	mustExec(t, f.owner, `UPDATE tasks SET origin = NULL WHERE id = $1`, f.item)
	if status, out := f.post(t, token, "list_tasks", `{}`); status != 200 {
		t.Fatalf("a task not from chat: %d %v", status, out)
	}
}

// fencedChild is an investigator the fenced session's agent started, with
// its own token.
func (f *fixture) fencedChild(t *testing.T, session string) string {
	t.Helper()
	parent := f.fencedSession(t, session)
	var started struct{ RunID string }
	if status := f.postAs(t, parent, "start_agent", `{"profile":"investigator","key":"look"}`, &started); status != 200 || started.RunID == "" {
		t.Fatalf("start_agent: %d %+v", status, started)
	}
	return f.tokenForRun(t, started.RunID)
}

// An investigator a chat-born session's agent starts reads as its session
// does: a hidden project's memory, task and project document are not found,
// by search or by id, and its tasks' keys do not show through a relation.
func TestASessionsInvestigatorCannotReachAHiddenProject(t *testing.T) {
	f := setup(t)
	child := f.fencedChild(t, "ssn_inv_fenced")
	mustExec(t, f.owner, `UPDATE projects SET description = 'Secret vault keys rotation' WHERE id = 'prj_sec_'||$1`, f.org)
	mustExec(t, f.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
		VALUES ('mem_txt_'||$1, $1, $2, 'Vault client in TEXT', 'the vault client wrapper', 'system', 'test')`, f.org, f.project)
	for _, q := range []string{"vault", "rotate keys", "secret"} {
		status, body := f.raw(t, child, "search_memory", `{"query":"`+q+`"}`)
		if status != 200 {
			t.Fatalf("search_memory %q: %d %s", q, status, body)
		}
		if strings.Contains(body, "mem_sec_") || strings.Contains(body, "SEC-1") || strings.Contains(body, "prj_sec_") {
			t.Errorf("search_memory %q reaches the hidden project: %s", q, body)
		}
	}
	// The session's linked, unhidden project is still searched.
	if status, body := f.raw(t, child, "search_memory", `{"query":"vault"}`); status != 200 || !strings.Contains(body, "mem_txt_") {
		t.Errorf("search_memory of the linked project: %d %s", status, body)
	}
	if status, out := f.post(t, child, "get_memory", `{"id":"mem_sec_`+f.org+`"}`); status != 422 {
		t.Errorf("get_memory of a hidden project's memory: %d %v", status, out)
	}
	if status, out := f.post(t, child, "get_memory", `{"id":"mem_txt_`+f.org+`"}`); status != 200 {
		t.Errorf("get_memory of the linked project's memory: %d %v", status, out)
	}
	// Its project-less task tools find no project at all.
	for _, tool := range []string{"list_tasks", "list_epics", "list_repositories"} {
		if _, body := f.raw(t, child, tool, `{}`); strings.Contains(body, "SEC") || strings.Contains(body, "vault") {
			t.Errorf("%s reaches the hidden project: %s", tool, body)
		}
	}
}

// settings with no project named lists a session's linked projects, none
// hidden from its chat, whatever the section.
func TestSettingsOfAChatBornSessionLeaveOutAHiddenLinkedProject(t *testing.T) {
	f := setup(t)
	token := f.fencedSession(t, "ssn_settings_fenced")
	for _, args := range []string{`{}`, `{"section":"delivery"}`} {
		status, body := f.raw(t, token, "settings", args)
		if status != 200 || strings.Contains(body, `"SEC"`) || strings.Contains(body, "Secret") || !strings.Contains(body, `"TEXT"`) {
			t.Errorf("settings %s: %d %s", args, status, body)
		}
	}
}

// A relation whose other side is in a hidden project shows neither its key
// nor its status in list_tasks, for the session's agent and its own tasks.
func TestRelationsIntoAHiddenProjectAreLeftOut(t *testing.T) {
	f := setup(t)
	token := f.fencedSession(t, "ssn_rel_fenced")
	f.relate(t, "wi_sec_"+f.org, f.item, "blocks")
	f.relate(t, f.item, "wi_sec_"+f.org, "related")
	status, body := f.raw(t, token, "list_tasks", `{"project":"TEXT"}`)
	if status != 200 || strings.Contains(body, "SEC-1") {
		t.Errorf("list_tasks shows the hidden side: %d %s", status, body)
	}
	// Not from chat: the relations show.
	mustExec(t, f.owner, `UPDATE sessions SET origin = NULL WHERE id = 'ssn_rel_fenced'`)
	if status, body := f.raw(t, token, "list_tasks", `{"project":"TEXT"}`); status != 200 || !strings.Contains(body, "SEC-1") {
		t.Errorf("list_tasks of a session not from chat: %d %s", status, body)
	}
}
