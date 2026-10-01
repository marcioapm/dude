package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A Run with a token, in a project with one task already.
type fixture struct {
	owner         *pgx.Conn
	url           string
	org           string
	project, item string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	f := &fixture{owner: owner, org: org, project: "prj_" + org, item: "wi_" + org}
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, next_task_number)
		VALUES ($1, $2, 'P', $1, 'TEXT', 2)`, f.project, org)
	mustExec(t, owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Text utilities')`,
		"epc_"+org, org, f.project)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ($1, $2, $3, 1, 'Truncate long words', 'running')`, f.item, org, f.project)
	srv := httptest.NewServer((&agenttools.Server{DB: app}).Handler())
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// run makes a phase Run with a role and status, and returns its token.
func (f *fixture) run(t *testing.T, id, role, status string) string {
	t.Helper()
	token, hash := agenttools.NewToken()
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash)
		VALUES ($1, $2, $3, $4, 1, $5::run_status, 'implement', $6::agent_role, $7)`, id, f.org, f.project, f.item, status, role, hash)
	return token
}

func (f *fixture) connect(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: f.url, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearer{token}},
	}, nil)
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestAnImplementerRecordsWorkItFoundAndSeesIt(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_impl", "implementer", "running")
	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_task", Arguments: map[string]any{
		"title": "Hyphenated words split wrongly", "goal": "Found while truncating: `re-enter` becomes `re-`.",
		"acceptanceCriteria": []string{"hyphenated words stay whole"}, "epic": "text utilities",
	}})
	if err != nil || res.IsError {
		t.Fatalf("create_task: %v %+v", err, res)
	}
	var created struct{ Key string }
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &created)
	if created.Key != "TEXT-2" {
		t.Errorf("key = %q, want TEXT-2", created.Key)
	}

	// Marked as the agent's, in the epic, not started.
	var status, byRun, epic string
	if err := f.owner.QueryRow(context.Background(), `SELECT w.status::text, w.created_by_run_id, e.title
		FROM tasks w JOIN epics e ON e.id = w.epic_id WHERE w.number = 2 AND w.project_id = $1`, f.project).
		Scan(&status, &byRun, &epic); err != nil {
		t.Fatal(err)
	}
	if status != "received" || byRun != "run_impl" || epic != "Text utilities" {
		t.Errorf("created: status %s by %s in %s", status, byRun, epic)
	}
	// The call is in the ledger, on the calling Run.
	var calls int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE run_id = 'run_impl'
		AND event_type = $1 AND payload->>'tool' = 'create_task'`, agenttools.EventType).Scan(&calls)
	if calls != 1 {
		t.Errorf("%d ledger events for the call", calls)
	}

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_tasks: %v %+v", err, res)
	}
	raw, _ = json.Marshal(res.StructuredContent)
	for _, want := range []string{`"key":"TEXT-1"`, `"yours":true`, `"key":"TEXT-2"`, `"createdBy":"TEXT-1"`, `"Text utilities"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("list_tasks lacks %s: %s", want, raw)
		}
	}

	// A list is structured content as an object, which MCP requires of it:
	// strict clients (OpenCode's) refuse an array.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_repositories: %v %+v", err, res)
	}
	if raw, _ := json.Marshal(res.StructuredContent); !strings.HasPrefix(string(raw), `{"items":[`) {
		t.Errorf("list_repositories' structured content: %s", raw)
	}

	// A bad request is the agent's to fix, said plainly.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_task",
		Arguments: map[string]any{"title": "x", "goal": "y", "epic": "no such epic"}})
	if err != nil || !res.IsError {
		t.Fatalf("an unknown epic was accepted: %v %+v", err, res)
	}
}

// Work an agent finds is owned by whoever drives the task it found it in:
// the person who hears of it and decides whether it is worth doing.
func TestWorkAnAgentFindsInheritsItsKeylessPersonOwner(t *testing.T) {
	f := setup(t)
	// A removed member first, then an owner whose id sorts after the
	// remaining member's: only the first active person by position passes on.
	owner, member, removed := "per_z_owner_"+f.org, "per_a_member_"+f.org, "per_0_removed_"+f.org
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name, removed_at) VALUES ($1, $2, 'Gone', now())`, removed, f.org)
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name) VALUES ($1, $3, 'Zoe'), ($2, $3, 'Abe')`, owner, member, f.org)
	mustExec(t, f.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		VALUES ($1, $2, $5, 0), ($1, $3, $5, 3), ($1, $4, $5, 8)`, f.item, removed, owner, member, f.org)
	token := f.run(t, "run_found", "implementer", "running")
	if status, out := f.post(t, token, "create_task", `{"title":"Found on the way","goal":"why"}`); status != 200 {
		t.Fatalf("create: %d %v", status, out)
	}
	rows, err := f.owner.Query(context.Background(), `SELECT tp.person_id, tp.position FROM tasks t JOIN task_people tp ON tp.task_id = t.id
		WHERE t.project_id = $1 AND t.number = 2 ORDER BY tp.position, tp.person_id`, f.project)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var person string
		var position int
		if err := rows.Scan(&person, &position); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s@%d", person, position))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{owner + "@0"}; !slices.Equal(got, want) {
		t.Errorf("child's people %v, want %v", got, want)
	}
	var keys int
	var legacyOwner *string
	if err := f.owner.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM api_keys WHERE person_id = $2), owner_key_id
		FROM tasks WHERE project_id = $1 AND number = 2`, f.project, owner).Scan(&keys, &legacyOwner); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || legacyOwner != nil {
		t.Fatalf("keyless inheritance minted or required a key: keys=%d legacyOwner=%v", keys, legacyOwner)
	}
}

func TestAReviewerCannotCreateWork(t *testing.T) {
	f := setup(t)
	cs, err := f.connect(t, f.run(t, "run_rev", "reviewer", "running"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	// Seeing work, recording events and what it learned, not making work or
	// stopping for a person.
	if strings.Join(names, ",") != "emit_event,get_memory,list_epics,list_repositories,list_tasks,remember,request_repository,run_diff,search_memory" {
		t.Errorf("a reviewer sees %v", names)
	}
}

func TestOnlyALiveRunsTokenGetsIn(t *testing.T) {
	f := setup(t)
	ended := f.run(t, "run_done", "implementer", "completed")
	for name, token := range map[string]string{"no token": "", "a stranger's": "dude_run_nope", "an ended Run's": ended} {
		if _, err := f.connect(t, token); err == nil {
			t.Errorf("%s token was let in", name)
		}
	}
	req, _ := http.NewRequest("POST", f.url, strings.NewReader(`{}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", res.StatusCode)
	}
}

func mustExec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestListWorkFindsByText(t *testing.T) {
	f := setup(t)
	cs, err := f.connect(t, f.run(t, "run_x", "implementer", "running"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for text, want := range map[string]bool{"TRUNCATE": true, "hyphen": false} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{"text": text}})
		if err != nil || res.IsError {
			t.Fatalf("list_tasks %q: %v %+v", text, err, res)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		if strings.Contains(string(raw), "TEXT-1") != want {
			t.Errorf("list_tasks %q: %s", text, raw)
		}
	}
}

// post calls a tool over the JSON API, as the dude CLI does.
func (f *fixture) post(t *testing.T, token, tool, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.url+"/tools/"+tool, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestTheCLIsJSONAPICallsTheSameTools(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_cli", "implementer", "running")
	if status, out := f.post(t, token, "create_task", `{"title":"From the CLI","goal":"why"}`); status != 200 || out["key"] != "TEXT-2" {
		t.Errorf("create: %d %v", status, out)
	}
	if status, out := f.post(t, token, "create_task", `{"title":"x","colour":"red"}`); status != 422 {
		t.Errorf("an unknown argument: %d %v", status, out)
	}
	if status, _ := f.post(t, f.run(t, "run_rev2", "reviewer", "running"), "create_task", `{"title":"x"}`); status != 404 {
		t.Errorf("a reviewer created work: %d", status)
	}
	var calls int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE run_id = 'run_cli' AND event_type = $1`,
		agenttools.EventType).Scan(&calls)
	if calls != 1 {
		t.Errorf("%d ledger events, want the one successful call", calls)
	}
}

func TestATaskTakesA64KGoalAnd16KOfCriteriaInAllAndNoMore(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_lim", "implementer", "running")
	body := func(goal int, criteria ...int) string {
		cs := make([]string, len(criteria))
		for i, n := range criteria {
			cs[i] = strings.Repeat("c", n)
		}
		b, _ := json.Marshal(map[string]any{"title": "Limits", "goal": strings.Repeat("g", goal), "acceptanceCriteria": cs})
		return string(b)
	}
	// One criterion far past the old 2,000 each, and the whole allowance split unevenly.
	for _, ok := range []string{body(agenttools.GoalMax, 3000), body(10, agenttools.CriteriaMax-5000, 5000)} {
		if status, out := f.post(t, token, "create_task", ok); status != 200 {
			t.Errorf("within the limits: %d %v", status, out)
		}
	}
	status, out := f.post(t, token, "create_task", body(agenttools.GoalMax+1))
	if status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "a goal of 65536") {
		t.Errorf("a goal over 64K: %d %v", status, out)
	}
	status, out = f.post(t, token, "create_task", body(10, agenttools.CriteriaMax-5000, 5001))
	if status != 422 || out["error"] != "acceptance criteria too long: at most 16384 characters in all" {
		t.Errorf("criteria over 16K in all: %d %v", status, out)
	}
	var made int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM tasks WHERE project_id = $1 AND title = 'Limits'`, f.project).Scan(&made)
	if made != 2 {
		t.Errorf("%d tasks made, want the 2 within the limits", made)
	}
}

func TestTheLimitsCountUTF16UnitsAsTheAPIDoes(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_u16", "implementer", "running")
	body := func(goal string, criteria ...string) string {
		b, _ := json.Marshal(map[string]any{"title": "Units", "goal": goal, "acceptanceCriteria": criteria})
		return string(b)
	}
	count := func() int {
		var n int
		_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM tasks WHERE project_id = $1 AND title = 'Units'`, f.project).Scan(&n)
		return n
	}
	// 😀 is two UTF-16 units (four bytes, one rune); é is one unit (two bytes).
	emojiGoal := strings.Repeat("😀", 32768)
	emojiCriterion := strings.Repeat("😀", 8192)
	accepted := []string{
		body(emojiGoal),
		body("g", emojiCriterion),
		body(strings.Repeat("é", 65536), strings.Repeat("é", 16384)),
	}
	for i, ok := range accepted {
		if status, out := f.post(t, token, "create_task", ok); status != 200 {
			t.Errorf("accepted case %d at the limits: %d %v", i, status, out)
		}
	}
	if n := count(); n != 3 {
		t.Fatalf("%d tasks made, want the 3 at the limits", n)
	}
	refused := []string{
		body(emojiGoal + "g"),
		body(strings.Repeat("😀", 32767) + "gg" + "g"),
		body("g", emojiCriterion, "c"),
		body("g", strings.Repeat("😀", 8191)+"cc", "c"),
		body(strings.Repeat("é", 65537)),
		body("g", strings.Repeat("é", 16385)),
	}
	for i, over := range refused {
		if status, out := f.post(t, token, "create_task", over); status != 422 {
			t.Errorf("refused case %d over the limits: %d %v", i, status, out)
		}
	}
	if n := count(); n != 3 {
		t.Errorf("%d tasks after the refusals, want still 3", n)
	}
}

func TestA64KGoalThatJSONEscapesSixfoldIsStillATask(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_esc", "implementer", "running")
	goal := strings.Repeat("<", 65536)
	criteria := []string{strings.Repeat(">", 16384)}
	b, _ := json.Marshal(map[string]any{"title": "Escaped", "goal": goal, "acceptanceCriteria": criteria})
	if len(b) < 480_000 {
		t.Fatalf("the body is %d bytes; json.Marshal was expected to escape it sixfold", len(b))
	}
	status, out := f.post(t, token, "create_task", string(b))
	if status != 200 {
		t.Fatalf("a %d-byte request within the limits: %d %v", len(b), status, out)
	}
	var gotGoal string
	var gotCriteria []string
	_ = f.owner.QueryRow(context.Background(), `SELECT goal, acceptance_criteria FROM tasks WHERE project_id = $1 AND title = 'Escaped'`,
		f.project).Scan(&gotGoal, &gotCriteria)
	if gotGoal != goal || len(gotCriteria) != 1 || gotCriteria[0] != criteria[0] {
		t.Errorf("saved a goal of %d and criteria %d, want them as sent", len(gotGoal), len(gotCriteria))
	}
}

func TestAnAgentAsksAPersonThroughATool(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_ask", "implementer", "running")
	status, out := f.post(t, token, "ask_person", `{"question":"Keep hyphenated words whole?","choices":["yes","no"]}`)
	if status != 200 || out["questionId"] == nil {
		t.Fatalf("ask: %d %v", status, out)
	}
	var prompt, wiStatus string
	_ = f.owner.QueryRow(context.Background(), `SELECT q.prompt, w.status::text FROM questions q JOIN tasks w ON w.id = q.task_id
		WHERE q.run_id = 'run_ask' AND q.status = 'open'`).Scan(&prompt, &wiStatus)
	if prompt != "Keep hyphenated words whole?" || wiStatus != "awaiting_input" {
		t.Errorf("question %q, task %s", prompt, wiStatus)
	}
	// One at a time.
	if status, _ := f.post(t, token, "ask_person", `{"question":"And another?"}`); status != 422 {
		t.Errorf("a second open question: %d", status)
	}
}

func TestACustomEventIsRecordedInItsOwnNamespace(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_ev", "reviewer", "running")
	if status, out := f.post(t, token, "emit_event", `{"type":"progress","data":{"done":3,"of":10}}`); status != 200 {
		t.Fatalf("emit: %d %v", status, out)
	}
	var done float64
	_ = f.owner.QueryRow(context.Background(), `SELECT (payload->'data'->>'done')::float FROM events
		WHERE run_id = 'run_ev' AND event_type = 'agent.custom.progress'`).Scan(&done)
	if done != 3 {
		t.Errorf("progress event not recorded: done=%v", done)
	}
	for _, bad := range []string{`{"type":"Run.Completed"}`, `{"type":"../x"}`, `{"type":"progress","data":"` + strings.Repeat("x", 17<<10) + `"}`} {
		if status, _ := f.post(t, token, "emit_event", bad); status != 422 {
			t.Errorf("%.40s accepted: %d", bad, status)
		}
	}
}

func TestARepositoryRequestStaysInTheProjectAndOnlyImplementersAskToWrite(t *testing.T) {
	f := setup(t)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'https://github.com/acme/web.git', 'main')`, "repo_web_"+f.org, f.org, f.project)
	// Another project of the same organization, with its own repository.
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Other', $1, 'OTH')`,
		"prj_other_"+f.org, f.org)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'infra', 'https://github.com/acme/infra.git', 'main')`, "repo_infra_"+f.org, f.org, "prj_other_"+f.org)

	impl := f.run(t, "run_i", "implementer", "running")
	if status, out := f.post(t, impl, "request_repository", `{"repository":"acme/infra","reason":"x"}`); status != 422 ||
		!strings.Contains(out["error"].(string), "no repository") {
		t.Errorf("another project's repository: %d %v", status, out)
	}
	rev := f.run(t, "run_r", "reviewer", "running")
	if status, _ := f.post(t, rev, "request_repository", `{"repository":"web","write":true,"reason":"x"}`); status != 422 {
		t.Errorf("a reviewer asked to write: %d", status)
	}
	if status, _ := f.post(t, rev, "request_repository", `{"repository":"web","reason":"to read the client"}`); status != 200 {
		t.Errorf("a reviewer could not ask to read: %d", status)
	}
	if status, _ := f.post(t, impl, "request_repository", `{"repository":"web","write":true,"reason":"to change the client"}`); status != 200 {
		t.Errorf("an implementer could not ask to write: %d", status)
	}
}

func TestARetriedStartCarriesTheSameToken(t *testing.T) {
	key := []byte("k")
	a, _ := agenttools.RunToken(key, "run_1", 0)
	b, _ := agenttools.RunToken(key, "run_1", 0)
	c, _ := agenttools.RunToken(key, "run_1", 1)
	d, _ := agenttools.RunToken(key, "run_2", 0)
	if a != b || a == c || a == d {
		t.Errorf("tokens: same start %v, next start differs %v, other run differs %v", a == b, a != c, a != d)
	}
}

func TestARunawayAgentIsSlowedDown(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_loop", "implementer", "running")
	var last int
	for i := 0; i < 25; i++ {
		last, _ = f.post(t, token, "create_task", fmt.Sprintf(`{"title":"spam %d"}`, i))
	}
	if last != 422 {
		t.Errorf("the 25th task was accepted: %d", last)
	}
	var made int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM tasks WHERE created_by_run_id = 'run_loop'`).Scan(&made)
	if made != 20 {
		t.Errorf("%d tasks made, want the limit of 20", made)
	}
}

// A model fills in arguments as the schema says: data must read as JSON
// data, not bytes.
func TestEmitEventsSchemaAsksForAnObject(t *testing.T) {
	f := setup(t)
	cs, err := f.connect(t, f.run(t, "run_schema", "implementer", "running"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Name != "emit_event" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		if strings.Contains(string(raw), `"integer"`) {
			t.Errorf("emit_event's data reads as bytes to a model: %s", raw)
		}
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "emit_event",
		Arguments: map[string]any{"type": "milestone", "data": map[string]any{"done": "implemented"}}})
	if err != nil || res.IsError {
		t.Fatalf("emit: %v %+v", err, res)
	}
	// And as the JSON text of an object, which models also send.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "emit_event",
		Arguments: map[string]any{"type": "milestone", "data": `{"done": "implemented"}`}})
	if err != nil || res.IsError {
		t.Fatalf("emit as text: %v %+v", err, res)
	}
	var both int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events
		WHERE run_id = 'run_schema' AND event_type = 'agent.custom.milestone' AND payload->'data'->>'done' = 'implemented'`).Scan(&both)
	if both != 2 {
		t.Errorf("%d of 2 milestones have their data as an object", both)
	}
}

// The CLI in an image from before "work item" became "task" calls the old
// names; its calls under them count against the same budget.
func TestAnOldCLIsToolNamesStillWork(t *testing.T) {
	f := setup(t)
	old := f.run(t, "run_old", "implementer", "running")
	// The CLI in an image from before the rename, by the old name; its
	// calls under that name count against the same budget.
	if status, out := f.post(t, old, "create_work_item", `{"title":"From an old CLI","goal":"why"}`); status != 200 {
		t.Errorf("create_work_item, the old name: %d %v", status, out)
	}
	mustExec(t, f.owner, `INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
		SELECT 'evt_old_' || g, $1, $2, 'run_old', 'agent', 'run_old', 'runner', '{"tool":"create_work_item"}'
		FROM generate_series(1, 30) g`, f.org, agenttools.EventType)
	if status, _ := f.post(t, old, "create_task", `{"title":"over budget","goal":"why"}`); status != 422 {
		t.Errorf("calls under the old name did not count: %d", status)
	}
}
