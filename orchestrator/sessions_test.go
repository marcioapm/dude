package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// sessionWorld is a world whose organisation's Thinker requests the
// scripted agent (the brainstorm's tier), with a second project, WEB,
// holding the web repository, and people: Márcio, Ana, João, an admin and
// someone else.
type sessionWorld struct {
	*world
	webProject, webRepo                string
	marcio, ana, joao, admin, outsider string
}

func newSessionWorld(t *testing.T) *sessionWorld {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE model_tiers SET model = 'fake/scripted' WHERE organization_id = $1 AND name = 'Thinker'`, w.org)
	mustExec(t, w.owner, `UPDATE projects SET key_prefix = 'BL' WHERE id = $1`, w.project)
	s := &sessionWorld{world: w, webProject: "prj_web_" + w.org, webRepo: "repo_web_" + w.org}
	mustExec(t, w.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'web-console', $1, 'WC')`,
		s.webProject, w.org)
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, s.webRepo, w.org, s.webProject)
	for _, p := range []struct {
		field      *string
		name, role string
	}{
		{&s.marcio, "Márcio Martins", "member"}, {&s.ana, "Ana Nunes", "member"}, {&s.joao, "João Reis", "member"},
		{&s.admin, "Ada Admin", "admin"}, {&s.outsider, "Otto Other", "member"},
	} {
		*p.field = "per_" + strings.ToLower(strings.Fields(p.name)[0]) + "_" + w.org
		mustExec(t, w.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, $3, $4)`, *p.field, w.org, p.name, p.role)
	}
	return s
}

// as calls the orchestrator's internal API as a person, as the backend does
// for one signed in.
func (s *sessionWorld) as(person, method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.api+path, reader)
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", s.org)
	req.Header.Set("X-Dude-Credential-Kind", "person")
	req.Header.Set("X-Dude-Person", person)
	req.Header.Set("X-Dude-Actor", person)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (s *sessionWorld) ok(person, method, path string, body any) map[string]any {
	s.t.Helper()
	status, out := s.as(person, method, path, body)
	if status >= 300 {
		s.t.Fatalf("%s %s as %s: %d %v", method, path, person, status, out)
	}
	return out
}

// session makes a session owned by Márcio, linked to BL with its repository.
func (s *sessionWorld) session() string {
	s.t.Helper()
	out := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Usage-based billing",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{s.repoID}}}})
	return out["id"].(string)
}

// member invites and accepts.
func (s *sessionWorld) join(session, person, role string) {
	s.t.Helper()
	s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/people", map[string]any{"people": []string{person}, "role": role})
	s.ok(person, "POST", "/internal/sessions/"+session+"/accept", nil)
}

// brainstorm is the session's latest Run.
func (s *sessionWorld) brainstorm(session string) (id, status string) {
	_ = s.owner.QueryRow(context.Background(), `SELECT id, status::text FROM runs WHERE session_id = $1 ORDER BY created_at DESC LIMIT 1`,
		session).Scan(&id, &status)
	return id, status
}

// luxRun is the fake lux's Run for a dude Run.
func (s *sessionWorld) luxRun(runID string) *luxRunView {
	for _, r := range s.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] == runID {
			return &luxRunView{spec: spec, raw: string(r.Spec), inputs: slices.Clone(r.Inputs), resumes: slices.Clone(r.ResumeInputs),
				resumed: r.Resumed, toolCalls: slices.Clone(r.ToolCalls)}
		}
	}
	return nil
}

type luxRunView struct {
	spec      lux.Spec
	raw       string
	inputs    []string
	resumes   []string
	resumed   int
	toolCalls []string
}

// started is the session's agent once it has finished its first turn.
func (s *sessionWorld) started(session string) string {
	s.t.Helper()
	var id string
	s.until("the session's agent to answer", func() bool {
		id, _ = s.brainstorm(session)
		return id != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NOT NULL`, id) == 1
	})
	return id
}

// tool calls one of the session agent's tools with its Run's token.
func (s *sessionWorld) tool(runID, name, args string) (int, map[string]any) {
	s.t.Helper()
	v := s.luxRun(runID)
	if v == nil {
		s.t.Fatalf("no lux Run for %s", runID)
	}
	status, body := s.callTool(s.syncer.Agent.ToolsURL, v.raw, name, args)
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return status, out
}

// task makes a task in a project, owned by person, returning its id.
func (s *sessionWorld) taskIn(project, title, owner string) string {
	id := fmt.Sprintf("wi_%s_%d", strings.ToLower(strings.ReplaceAll(title, " ", "")), len(title))
	mustExec(s.t, s.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria)
		VALUES ($1, $2, $3, (SELECT next_task_number FROM projects WHERE id = $3), $4, 'A goal long enough to keep', '["works"]'::jsonb)`,
		id, s.org, project, title)
	mustExec(s.t, s.owner, `UPDATE projects SET next_task_number = next_task_number + 1 WHERE id = $1`, project)
	mustExec(s.t, s.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ($1, $2, $3, 0)`, id, owner, s.org)
	return id
}

func (s *sessionWorld) keyOf(task string) string {
	var key string
	_ = s.owner.QueryRow(context.Background(), `SELECT p.key_prefix || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, task).Scan(&key)
	return key
}

// proposal records a proposal on the session as its agent would.
func (s *sessionWorld) proposal(session string, items []delivery.ProposalItem) string {
	s.t.Helper()
	var id string
	if err := s.app.InOrg(context.Background(), s.org, func(tx pgx.Tx) error {
		var err error
		id, err = delivery.RecordProposal(context.Background(), tx, delivery.RunRef{Org: s.org, SessionID: session}, items)
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

// Nobody but an accepted member reaches a session: not someone else in
// the organisation, not an invitee who has not accepted, not an admin.
// Each route answers them the same 404, and their lists hold nothing of it
// — an invitee sees only the invitation.
func TestOnlyAcceptedMembersReachASession(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}, "role": "chat"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where would metering live?"})
	proposal := s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"}})

	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/internal/sessions/" + id, nil},
		{"POST", "/internal/sessions/" + id + "/title", map[string]any{"title": "Mine now"}},
		{"POST", "/internal/sessions/" + id + "/chat", map[string]any{"text": "hi"}},
		{"POST", "/internal/sessions/" + id + "/link", map[string]any{"projects": []any{}}},
		{"POST", "/internal/sessions/" + id + "/people", map[string]any{"people": []string{s.joao}}},
		{"POST", "/internal/sessions/" + id + "/people/" + s.ana + "/role", map[string]any{"role": "read"}},
		{"POST", "/internal/sessions/" + id + "/people/" + s.ana + "/remove", nil},
		{"POST", "/internal/sessions/" + id + "/owner", map[string]any{"person": s.joao}},
		{"POST", "/internal/sessions/" + id + "/file", map[string]any{"proposalId": proposal, "items": []int{0}}},
		{"POST", "/internal/sessions/" + id + "/archive", nil},
		{"POST", "/internal/sessions/" + id + "/unarchive", nil},
	}
	for _, who := range []struct{ name, id string }{{"someone else", s.outsider}, {"an invitee", s.ana}, {"an admin", s.admin}} {
		for _, r := range routes {
			if status, out := s.as(who.id, r.method, r.path, r.body); status != 404 {
				t.Errorf("%s: %s %s = %d %v, want 404", who.name, r.method, r.path, status, out)
			}
		}
		list := s.ok(who.id, "GET", "/internal/sessions", nil)
		if b, _ := json.Marshal(list["sessions"]); strings.Contains(string(b), id) {
			t.Errorf("%s lists the session: %s", who.name, b)
		}
	}
	// The invitee sees the invitation, and nothing said in it.
	list := s.ok(s.ana, "GET", "/internal/sessions", nil)
	b, _ := json.Marshal(list["invitations"])
	if !strings.Contains(string(b), id) || strings.Contains(string(b), "metering") {
		t.Errorf("the invitee's invitations: %s", b)
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE title = 'Meter runs'`); n != 0 {
		t.Errorf("a non-member filed")
	}
	// Accepted, they see it all, from the first message.
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/accept", nil)
	if status, _ := s.as(s.ana, "GET", "/internal/sessions/"+id, nil); status != 200 {
		t.Errorf("an accepted member: %d", status)
	}
}

// Only the owner invites, changes roles, removes and hands over.
func TestOnlyTheOwnerSharesAndHandsOver(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.join(id, s.joao, "read")
	for _, r := range []struct {
		path string
		body any
	}{
		{"/internal/sessions/" + id + "/people", map[string]any{"people": []string{s.outsider}}},
		{"/internal/sessions/" + id + "/people/" + s.joao + "/role", map[string]any{"role": "chat"}},
		{"/internal/sessions/" + id + "/people/" + s.joao + "/remove", nil},
		{"/internal/sessions/" + id + "/owner", map[string]any{"person": s.ana}},
		{"/internal/sessions/" + id + "/link", map[string]any{"projects": []any{}}},
	} {
		for _, who := range []string{s.ana, s.joao} {
			if status, out := s.as(who, "POST", r.path, r.body); status != 403 {
				t.Errorf("a member: %s = %d %v, want 403", r.path, status, out)
			}
		}
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1`, id); n != 3 {
		t.Errorf("%d people after members tried, want 3", n)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.joao+"/role", map[string]any{"role": "chat"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.joao+"/remove", nil)
	if status, _ := s.as(s.joao, "GET", "/internal/sessions/"+id, nil); status != 404 {
		t.Errorf("a removed member still reads it: %d", status)
	}
}

// A reader sees everything and writes nothing: not to the agent, not a
// File.
func TestAReaderCannotWriteOrFile(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.joao, "read")
	proposal := s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"}})
	if status, out := s.as(s.joao, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"}); status != 403 {
		t.Errorf("a reader wrote: %d %v", status, out)
	}
	if status, out := s.as(s.joao, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0}}); status != 403 {
		t.Errorf("a reader filed: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 0 {
		t.Errorf("a reader's message started the agent")
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE title = 'Meter runs'`); n != 0 {
		t.Errorf("a reader filed a task")
	}
	got := s.ok(s.joao, "GET", "/internal/sessions/"+id, nil)
	if b, _ := json.Marshal(got["proposals"]); !strings.Contains(string(b), "readers can't file") {
		t.Errorf("the card does not tell a reader why: %s", b)
	}
}

// File acts as the person who presses it: a new task is theirs; an edit
// only of a task they own that has not started — another's stays on the
// card for its owner, a started one is refused; a comment is theirs.
func TestFilingActsAsThePersonWhoPressesFile(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	mine := s.taskIn(s.project, "Daily rollup", s.marcio)
	hers := s.taskIn(s.project, "Checkout v2", s.ana)
	started := s.taskIn(s.project, "Started one", s.ana)
	mustExec(t, s.owner, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, step, task_id)
		VALUES ('wfr_started', $1, 'task.delivery', 'delivery:started', 'implement', $2)`, s.org, started)
	after := "Count each run id once, whatever the meter's key window"
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Dedupe runs", Goal: "The meter's 24h key is not enough for retries"},
		{Kind: "edit", Task: s.keyOf(mine), After: &delivery.TaskText{Goal: ptr(after)}},
		{Kind: "comment", Task: s.keyOf(hers), Text: "v2 should keep the method list in the plan step"},
		{Kind: "edit", Task: s.keyOf(started), After: &delivery.TaskText{Goal: ptr(after)}},
	})

	out := s.ok(s.ana, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1, 2, 3}})
	results, _ := out["results"].([]any)
	byItem := map[float64]map[string]any{}
	for _, r := range results {
		m := r.(map[string]any)
		byItem[m["item"].(float64)] = m
	}
	if byItem[0]["status"] != "filed" || byItem[2]["status"] != "filed" {
		t.Errorf("Ana's own task and comment: %v", results)
	}
	if byItem[1]["status"] != "refused" || !strings.Contains(fmt.Sprint(byItem[1]["why"]), "Márcio") {
		t.Errorf("Ana filed Márcio's edit: %v", byItem[1])
	}
	if byItem[3]["status"] != "refused" || !strings.Contains(fmt.Sprint(byItem[3]["why"]), "started") {
		t.Errorf("a started task's edit: %v", byItem[3])
	}
	var ownerOfNew string
	_ = s.owner.QueryRow(context.Background(), `SELECT tp.person_id FROM tasks t JOIN task_people tp ON tp.task_id = t.id AND tp.position = 0
		WHERE t.title = 'Dedupe runs'`).Scan(&ownerOfNew)
	if ownerOfNew != s.ana {
		t.Errorf("the new task is %s's, want Ana's", ownerOfNew)
	}
	var commentBy, commentText string
	_ = s.owner.QueryRow(context.Background(), `SELECT actor_id, payload->>'text' FROM events WHERE task_id = $1 AND event_type = 'task.comment'`,
		hers).Scan(&commentBy, &commentText)
	if commentBy != s.ana || !strings.Contains(commentText, "method list") {
		t.Errorf("the comment is %s's: %q", commentBy, commentText)
	}
	var goal string
	_ = s.owner.QueryRow(context.Background(), `SELECT goal FROM tasks WHERE id = $1`, mine).Scan(&goal)
	if goal == after {
		t.Errorf("Ana's File edited Márcio's task")
	}

	// The edit stays on the card for Márcio, who files it.
	card := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)
	if b, _ := json.Marshal(card["proposals"]); !strings.Contains(string(b), `"filedBy":"Ana Nunes"`) {
		t.Errorf("the card does not say Ana filed: %s", b)
	}
	out = s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{1, 3}})
	results, _ = out["results"].([]any)
	if results[0].(map[string]any)["status"] != "filed" || results[1].(map[string]any)["status"] != "refused" {
		t.Errorf("Márcio's File: %v", results)
	}
	_ = s.owner.QueryRow(context.Background(), `SELECT goal FROM tasks WHERE id = $1`, mine).Scan(&goal)
	if goal != after {
		t.Errorf("Márcio's edit: goal %q", goal)
	}
	var editedBy string
	_ = s.owner.QueryRow(context.Background(), `SELECT actor_id FROM events WHERE task_id = $1 AND event_type = 'task.updated'`, mine).Scan(&editedBy)
	if editedBy != s.marcio {
		t.Errorf("the edit is %s's, want Márcio's", editedBy)
	}
}

// The card says what blocks an item, beside its words: another member's
// task is "owner", naming them, so a client may leave it to them; a task
// that has started, or a project not linked, blocks everyone, its owner and
// a reader too, so it is "started" or "unlinked" whoever looks; what a
// member could file is "reader" to a reader.
func TestTheCardSaysWhatBlocksAnItem(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.join(id, s.joao, "read")
	marcios := s.taskIn(s.project, "Daily rollup", s.marcio)
	started := s.taskIn(s.project, "Started one", s.marcio)
	mustExec(t, s.owner, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, step, task_id)
		VALUES ('wfr_started', $1, 'task.delivery', 'delivery:started', 'implement', $2)`, s.org, started)
	after := "Count each run id once, whatever the meter's key window"
	s.proposal(id, []delivery.ProposalItem{
		{Kind: "edit", Task: s.keyOf(marcios), After: &delivery.TaskText{Goal: ptr(after)}},
		{Kind: "edit", Task: s.keyOf(started), After: &delivery.TaskText{Goal: ptr(after)}},
		{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"},
		{Kind: "task", Project: "ZZ", Title: "Elsewhere", Goal: "A project this session does not link"},
	})
	status := func(person string) []map[string]any {
		t.Helper()
		var got struct {
			Proposals []struct {
				Status []map[string]any `json:"status"`
			} `json:"proposals"`
		}
		b, _ := json.Marshal(s.ok(person, "GET", "/internal/sessions/"+id, nil))
		if err := json.Unmarshal(b, &got); err != nil || len(got.Proposals) != 1 {
			t.Fatalf("the card as %s: %v %s", person, err, b)
		}
		return got.Proposals[0].Status
	}

	ana := status(s.ana)
	if ana[0]["canFile"] != false || ana[0]["blockedBy"] != "owner" || !strings.Contains(fmt.Sprint(ana[0]["why"]), "Márcio") {
		t.Errorf("Márcio's task, as Ana: %v", ana[0])
	}
	if owner, _ := ana[0]["owner"].(map[string]any); owner["id"] != s.marcio || owner["name"] != "Márcio Martins" {
		t.Errorf("Márcio's task names its owner as %v", ana[0]["owner"])
	}
	if ana[1]["canFile"] != false || ana[1]["blockedBy"] != "started" || ana[1]["owner"] != nil {
		t.Errorf("a started task, as Ana: %v", ana[1])
	}

	marcio := status(s.marcio)
	if marcio[0]["canFile"] != true || marcio[0]["blockedBy"] != nil {
		t.Errorf("Márcio's own task, as Márcio: %v", marcio[0])
	}
	if marcio[1]["canFile"] != false || marcio[1]["blockedBy"] != "started" || !strings.Contains(fmt.Sprint(marcio[1]["why"]), "started") {
		t.Errorf("his started task, as Márcio: %v", marcio[1])
	}
	if marcio[2]["canFile"] != true || marcio[3]["canFile"] != false || marcio[3]["blockedBy"] != "unlinked" {
		t.Errorf("a linked and an unlinked project's task, as Márcio: %v %v", marcio[2], marcio[3])
	}

	joao := status(s.joao)
	if joao[2]["canFile"] != false || joao[2]["blockedBy"] != "reader" {
		t.Errorf("a task a member could file, as a reader: %v", joao[2])
	}
	if joao[1]["blockedBy"] != "started" || joao[3]["blockedBy"] != "unlinked" {
		t.Errorf("a started task and an unlinked project's, as a reader: %v %v", joao[1], joao[3])
	}
}

// Work filed from a session looks exactly like work a person typed: the
// task row, its people, its events — nothing names the session, its title,
// its Run or its agent.
func TestFiledWorkCarriesNoTraceOfTheSession(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	run := s.started(id)
	mine := s.taskIn(s.project, "Daily rollup", s.marcio)
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "epic", Project: "BL", Title: "Usage metering"},
		{Kind: "task", Project: "BL", Epic: "Usage metering", Title: "Meter runs", Goal: "Count experiment runs per org per day",
			AcceptanceCriteria: []string{"a run is counted once"}},
		{Kind: "edit", Task: s.keyOf(mine), After: &delivery.TaskText{AcceptanceCriteria: &[]string{"works", "dedupes"}}},
		{Kind: "comment", Task: s.keyOf(mine), Text: "note for later"},
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1, 2, 3}})

	var task, epic string
	var createdBy *string
	_ = s.owner.QueryRow(context.Background(), `SELECT id, epic_id, created_by_run_id FROM tasks WHERE title = 'Meter runs'`).
		Scan(&task, &epic, &createdBy)
	if task == "" || epic == "" || createdBy != nil {
		t.Fatalf("the filed task %q epic %q created_by_run_id %v", task, epic, createdBy)
	}
	var epicBy *string
	_ = s.owner.QueryRow(context.Background(), `SELECT created_by_run_id FROM epics WHERE id = $1`, epic).Scan(&epicBy)
	if epicBy != nil {
		t.Errorf("the epic names a Run")
	}
	traces := []string{id, run, "Usage-based billing", "ssn_", "brainstorm", "proposal", "prp_"}
	// The stored rows of every filed kind: the new task, its epic, the
	// edited task, both tasks' people, and anything else that refers to them.
	var rowText string
	if err := s.owner.QueryRow(context.Background(), `SELECT concat_ws(E'\n',
			(SELECT row_to_json(t)::text FROM tasks t WHERE t.id = $1),
			(SELECT row_to_json(t)::text FROM tasks t WHERE t.id = $3),
			(SELECT row_to_json(e)::text FROM epics e WHERE e.id = $2),
			(SELECT json_agg(tp)::text FROM task_people tp WHERE tp.task_id = ANY(ARRAY[$1, $3])),
			(SELECT json_agg(r)::text FROM task_repositories r WHERE r.task_id = ANY(ARRAY[$1, $3])),
			(SELECT json_agg(d)::text FROM search_documents d WHERE d.source_id = ANY(ARRAY[$1, $2, $3])))`,
		task, epic, mine).Scan(&rowText); err != nil {
		t.Fatal(err)
	}
	// The edit was applied, to the row checked.
	if !strings.Contains(rowText, "dedupes") {
		t.Fatalf("the edited task's row does not have the edit: %s", rowText)
	}
	rows, err := s.owner.Query(context.Background(), `SELECT row_to_json(e)::text FROM events e
		WHERE e.task_id = ANY($1) OR e.payload->>'epicId' = $2 OR e.payload->>'taskId' = ANY($1)`, []string{task, mine}, epic)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := pgx.CollectRows(rows, pgx.RowTo[string])
	kinds := map[string]bool{}
	for _, e := range events {
		var ev struct {
			Type string `json:"event_type"`
		}
		_ = json.Unmarshal([]byte(e), &ev)
		kinds[ev.Type] = true
	}
	for _, want := range []string{"task.created", "epic.created", "task.updated", "task.comment"} {
		if !kinds[want] {
			t.Errorf("no %s among the filed work's events: %v", want, kinds)
		}
	}
	// What the orchestrator's task routes answer about both tasks.
	var answers []string
	for _, tk := range []string{task, mine} {
		for _, path := range []string{"/internal/tasks/" + tk + "/recover", "/internal/tasks/" + tk + "/servers"} {
			status, out := s.as(s.marcio, "GET", path, nil)
			if status != 200 {
				t.Errorf("GET %s: %d %v", path, status, out)
			}
			b, _ := json.Marshal(out)
			answers = append(answers, string(b))
		}
	}
	for _, text := range append(append(events, rowText), answers...) {
		for _, trace := range traces {
			if strings.Contains(strings.ToLower(text), strings.ToLower(trace)) {
				t.Errorf("%q in %s", trace, text)
			}
		}
	}
	if n := s.count(`SELECT count(*) FROM events WHERE task_id = ANY($1) AND (session_id IS NOT NULL OR run_id IS NOT NULL)`,
		[]string{task, mine}); n != 0 {
		t.Errorf("%d events on the filed work name a session or Run", n)
	}
}

// The spec a session's agent is submitted with: Small, its organisation's
// Thinker, its image by the chain role → organisation default →
// DUDE_AGENT_IMAGE (it has no project), and every linked repository at
// repos/<key>/<name>, named <key>-<name> as lux takes it (SpecName),
// push:false, with no push branch.
// With nothing linked, no repositories at all.
func TestWhatASessionsAgentIsSubmittedWith(t *testing.T) {
	small := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 10 << 30}
	s := newSessionWorld(t)
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Two projects", "projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	run := s.started(id)
	v := s.luxRun(run)
	spec := v.spec
	if spec.Resources == nil || *spec.Resources != small {
		t.Errorf("resources %+v, want Small", spec.Resources)
	}
	var thinker string
	_ = s.owner.QueryRow(context.Background(), `SELECT model FROM model_tiers WHERE organization_id = $1 AND name = 'Thinker'`, s.org).Scan(&thinker)
	if spec.Labels["dude.model_tier"] != "Thinker" || spec.Labels["dude.model"] != thinker || spec.Labels["dude.role"] != "brainstorm" {
		t.Errorf("labels %v", spec.Labels)
	}
	if spec.Labels["dude.session"] != id || spec.Labels["dude.task"] != "" {
		t.Errorf("labels name session %q task %q", spec.Labels["dude.session"], spec.Labels["dude.task"])
	}
	if spec.Image.Ref != "default:img" {
		t.Errorf("image %s, want DUDE_AGENT_IMAGE", spec.Image.Ref)
	}
	if spec.Git == nil || spec.Git.Push != nil || len(spec.Git.Repositories) != 2 {
		t.Fatalf("git %+v, want two repositories and no push branch", spec.Git)
	}
	want := map[string]string{lux.SpecName("BL-target"): "/workspace/repos/BL/target", lux.SpecName("WC-web"): "/workspace/repos/WC/web"}
	for _, r := range spec.Git.Repositories {
		if want[r.Name] != r.Path || r.Push == nil || *r.Push {
			t.Errorf("repository %s at %s push %v", r.Name, r.Path, r.Push)
		}
	}
	if spec.Workload.BeforeStop != nil {
		t.Errorf("a session's agent reads a final diff")
	}
	if n := s.count(`SELECT cardinality(lux_pushes) FROM runs WHERE id = $1`, run); n != 0 {
		t.Errorf("%d repositories it may push", n)
	}

	// The role's own image, then the organisation's default.
	s.useLayer(imageLayer)
	for i, img := range []string{"img_role", "img_org"} {
		version := s.libraryImage(img, strings.ReplaceAll(img, "_", "-"), false)
		mustExec(t, s.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref) VALUES ($1, $2, $3, $4)`,
			s.org, version, imageLayer, fmt.Sprintf("registry.test/dude/%s@sha256:%064d", img, i+7))
	}
	mustExec(t, s.owner, `UPDATE organizations SET default_image_id = 'img_org',
		default_agent_models = jsonb_set(default_agent_models, '{brainstorm,image}', '"img_role"') WHERE id = $1`, s.org)
	for _, want := range []string{"img_role", "img_org"} {
		other := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Nothing linked"})["id"].(string)
		s.ok(s.marcio, "POST", "/internal/sessions/"+other+"/chat", map[string]any{"text": "hello"})
		r := s.started(other)
		v := s.luxRun(r)
		if !strings.Contains(v.spec.Image.Ref, want) {
			t.Errorf("image %s, want %s's", v.spec.Image.Ref, want)
		}
		if v.spec.Git != nil {
			t.Errorf("nothing linked, yet git %+v", v.spec.Git)
		}
		mustExec(t, s.owner, `UPDATE organizations SET default_agent_models = default_agent_models #- '{brainstorm,image}' WHERE id = $1`, s.org)
	}
}

// The agent is told who wrote each message: "Name: words", in its briefing
// and in every later message.
func TestEveryMessageReachesTheAgentAttributed(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "the meter dedupes per key"})
	run := s.started(id)
	var prompt string
	_ = s.owner.QueryRow(context.Background(), `SELECT prompt FROM runs WHERE id = $1`, run).Scan(&prompt)
	if !strings.Contains(prompt, "## The first message\n\nAna Nunes: the meter dedupes per key") {
		t.Errorf("the briefing's message is not attributed:\n%s", prompt)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "and retries come days later"})
	var text string
	_ = s.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run).Scan(&text)
	if text != "Márcio Martins: and retries come days later" {
		t.Errorf("directive text %q", text)
	}
	s.until("the agent to read it", func() bool {
		v := s.luxRun(run)
		return v != nil && slices.Contains(append(v.inputs, v.resumes...), "Márcio Martins: and retries come days later")
	})
	// Chat shows the words as written, by their writer.
	var shown, by string
	_ = s.owner.QueryRow(context.Background(), `SELECT payload->>'text', actor_id FROM events WHERE run_id = $1 AND event_type = 'chat.message'
		ORDER BY cursor DESC LIMIT 1`, run).Scan(&shown, &by)
	if shown != "and retries come days later" || by != s.marcio {
		t.Errorf("chat shows %q by %s", shown, by)
	}
}

// A session made with its first message starts its agent with that
// message in the same call: one Run, briefed with the message attributed
// to its writer, Chat showing it as written, and the links recorded.
func TestASessionMadeByItsFirstMessageStartsItsAgent(t *testing.T) {
	s := newSessionWorld(t)
	out := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "where does metering go?",
		"projects": []map[string]any{{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	id, _ := out["id"].(string)
	runID, _ := out["runId"].(string)
	if id == "" || runID == "" {
		t.Fatalf("create answered %v, want an id and a runId", out)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 1 {
		t.Fatalf("%d Runs, want 1", n)
	}
	if run := s.started(id); run != runID {
		t.Errorf("the agent that answered is %s, want %s", run, runID)
	}
	var prompt string
	_ = s.owner.QueryRow(context.Background(), `SELECT prompt FROM runs WHERE id = $1`, runID).Scan(&prompt)
	if !strings.Contains(prompt, "## The first message\n\nMárcio Martins: where does metering go?") {
		t.Errorf("the briefing does not carry the message:\n%s", prompt)
	}
	// Linked before the start, so the agent is briefed with what it reads.
	if !strings.Contains(prompt, "## Linked projects\n\n- WC (web-console)\n  - `web`") {
		t.Errorf("the briefing does not list the linked project:\n%s", prompt)
	}
	var shown, by string
	_ = s.owner.QueryRow(context.Background(), `SELECT payload->>'text', actor_id FROM events WHERE run_id = $1 AND event_type = 'chat.message'
		ORDER BY cursor LIMIT 1`, runID).Scan(&shown, &by)
	if shown != "where does metering go?" || by != s.marcio {
		t.Errorf("chat shows %q by %s", shown, by)
	}
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, id, s.webProject); n != 1 {
		t.Errorf("the project is not linked")
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1 AND repository_id = $2`, id, s.webRepo); n != 1 {
		t.Errorf("the repository is not linked")
	}
	if v := s.luxRun(runID); v == nil || v.spec.Git == nil || len(v.spec.Git.Repositories) != 1 {
		t.Errorf("the agent was not submitted with the linked repository: %+v", v)
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'owner'`, id, s.marcio); n != 1 {
		t.Errorf("the writer does not own it")
	}
}

// A first message the agent would not be given makes nothing: no session,
// no Run, and the same refusal as a message in Chat. Neither does one whose
// links are refused, since the start shares the create's transaction.
func TestARefusedFirstMessageMakesNoSession(t *testing.T) {
	s := newSessionWorld(t)
	for _, c := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"empty", map[string]any{"message": ""}, 400},
		{"blank", map[string]any{"message": "  \n "}, 400},
		{"too long", map[string]any{"message": strings.Repeat("a", delivery.ChatMessageMax+1)}, 400},
		{"an unknown project", map[string]any{"message": "hello", "projects": []map[string]any{{"projectId": "prj_nope", "repositoryIds": []string{}}}}, 404},
	} {
		if status, out := s.as(s.marcio, "POST", "/internal/sessions", c.body); status != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, status, out, c.want)
		}
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, s.org); n != 0 {
		t.Errorf("%d sessions left behind", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE organization_id = $1 AND role = 'brainstorm'`, s.org); n != 0 {
		t.Errorf("%d Runs left behind", n)
	}
}

// A start that fails after the session row is written, inside the agent's
// start, leaves nothing: the create and the start are one transaction.
func TestAFirstMessageWhoseAgentFailsToStartMakesNoSession(t *testing.T) {
	s := newSessionWorld(t)
	// Each test has its own database (dbtest), so this trigger refuses only
	// this world's brainstorm Runs.
	mustExec(t, s.owner, `CREATE FUNCTION fail_brainstorm_run() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'brainstorm runs refused by the test'; END $$ LANGUAGE plpgsql`)
	mustExec(t, s.owner, `CREATE TRIGGER fail_brainstorm_run BEFORE INSERT ON runs FOR EACH ROW
		WHEN (NEW.role = 'brainstorm') EXECUTE FUNCTION fail_brainstorm_run()`)
	t.Cleanup(func() {
		mustExec(t, s.owner, `DROP TRIGGER IF EXISTS fail_brainstorm_run ON runs`)
		mustExec(t, s.owner, `DROP FUNCTION IF EXISTS fail_brainstorm_run()`)
	})
	status, out := s.as(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello",
		"projects": []map[string]any{{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	if status < 500 {
		t.Errorf("create answered %d %v, want a server error", status, out)
	}
	for what, q := range map[string]string{
		"sessions":        `SELECT count(*) FROM sessions WHERE organization_id = $1`,
		"session_people":  `SELECT count(*) FROM session_people WHERE organization_id = $1`,
		"brainstorm Runs": `SELECT count(*) FROM runs WHERE organization_id = $1 AND role = 'brainstorm'`,
		"session.created": `SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'session.created'`,
	} {
		if n := s.count(q, s.org); n != 0 {
			t.Errorf("%d %s left behind", n, what)
		}
	}
}

// Without a message, create makes a session with its links and no agent
// until someone writes, answered with no runId.
func TestASessionMadeWithoutAMessageStartsNothing(t *testing.T) {
	s := newSessionWorld(t)
	out := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Usage-based billing",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{s.repoID}}}})
	id := out["id"].(string)
	if _, has := out["runId"]; has || out["title"] != "Usage-based billing" {
		t.Errorf("create answered %v", out)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 0 {
		t.Errorf("%d Runs, want none", n)
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1`, id); n != 1 {
		t.Errorf("links not recorded")
	}
}

// A question to one member waits for that member: another's message is
// held, never taken as the answer, and goes to the agent after it.
func TestAQuestionToOneMemberWaitsForThem(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "how long is the window?"})
	run := s.started(id)
	status, out := s.tool(run, "ask_person", `{"question":"Grow the 24h window for every kind?","choices":["Every kind","Only runs"],"to":"Ana"}`)
	if status != 200 {
		t.Fatalf("ask_person: %d %v", status, out)
	}
	qid := out["questionId"].(string)
	var to string
	_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(to_person, '') FROM questions WHERE id = $1`, qid).Scan(&to)
	if to != s.ana {
		t.Fatalf("the question is for %q, want Ana", to)
	}
	// In Ana's inbox, not Márcio's.
	if b, _ := json.Marshal(s.ok(s.ana, "GET", "/internal/sessions", nil)["questions"]); !strings.Contains(string(b), qid) {
		t.Errorf("Ana's inbox: %s", b)
	}
	if b, _ := json.Marshal(s.ok(s.marcio, "GET", "/internal/sessions", nil)["questions"]); strings.Contains(string(b), qid) {
		t.Errorf("Márcio's inbox has Ana's question: %s", b)
	}

	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "also cost estimates?"})
	var status2 string
	_ = s.owner.QueryRow(context.Background(), `SELECT status::text FROM questions WHERE id = $1`, qid).Scan(&status2)
	if status2 != "open" {
		t.Fatalf("Márcio's message settled Ana's question: %s", status2)
	}
	for range 5 {
		s.pump()
	}
	if v := s.luxRun(run); slices.ContainsFunc(append(v.inputs, v.resumes...), func(in string) bool { return strings.Contains(in, "cost estimates") }) {
		t.Fatalf("Márcio's message reached the agent before Ana answered: %v", v.inputs)
	}

	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "Only runs"})
	var answeredBy string
	_ = s.owner.QueryRow(context.Background(), `SELECT status::text, COALESCE(answered_by_person, '') FROM questions WHERE id = $1`, qid).
		Scan(&status2, &answeredBy)
	if status2 != "answered" || answeredBy != s.ana {
		t.Fatalf("question %s by %s", status2, answeredBy)
	}
	s.until("both to reach the agent", func() bool {
		v := s.luxRun(run)
		all := append(v.inputs, v.resumes...)
		answer := slices.IndexFunc(all, func(in string) bool { return strings.Contains(in, "Ana Nunes answered your question") })
		held := slices.IndexFunc(all, func(in string) bool { return strings.Contains(in, "Márcio Martins: also cost estimates?") })
		return answer >= 0 && held > answer
	})
}

// Handing over keeps the Run: the same agent goes on, told who owns it
// now; what was filed stays whose it was. To someone not in it, it waits
// for them to accept.
func TestHandingOverKeepsTheRun(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan it"})
	run := s.started(id)
	proposal := s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"}})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0}})

	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.ana, "keep": "read"})
	if now, _ := s.brainstorm(id); now != run {
		t.Errorf("a new Run %s after the handover, want %s", now, run)
	}
	var role string
	_ = s.owner.QueryRow(context.Background(), `SELECT role FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.marcio).Scan(&role)
	if role != "read" {
		t.Errorf("Márcio is %s, want read", role)
	}
	var told string
	_ = s.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run).Scan(&told)
	if !strings.HasPrefix(told, "Ana Nunes owns this session now. Márcio Martins stays in it and can read.") {
		t.Errorf("the agent was told %q", told)
	}
	var owner string
	_ = s.owner.QueryRow(context.Background(), `SELECT tp.person_id FROM tasks t JOIN task_people tp ON tp.task_id = t.id AND tp.position = 0
		WHERE t.title = 'Meter runs'`).Scan(&owner)
	if owner != s.marcio {
		t.Errorf("the filed task changed hands to %s", owner)
	}
	if status, _ := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.joao}}); status != 403 {
		t.Errorf("the old owner still shares: %d", status)
	}

	// To someone not in it: an invitation as owner, effective on accept.
	out := s.ok(s.ana, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.joao, "keep": "chat"})
	if out["pending"] != true {
		t.Errorf("handing to a non-member: %v", out)
	}
	_ = s.owner.QueryRow(context.Background(), `SELECT person_id FROM session_people WHERE session_id = $1 AND role = 'owner'`, id).Scan(&owner)
	if owner != s.ana {
		t.Errorf("owner before João accepts: %s", owner)
	}
	s.ok(s.joao, "POST", "/internal/sessions/"+id+"/accept", nil)
	_ = s.owner.QueryRow(context.Background(), `SELECT person_id FROM session_people WHERE session_id = $1 AND role = 'owner'`, id).Scan(&owner)
	if owner != s.joao {
		t.Errorf("owner after João accepts: %s", owner)
	}
	if now, _ := s.brainstorm(id); now != run {
		t.Errorf("a new Run after the second handover")
	}
}

// A session's agent's tools are its session's: one naming a project not
// linked is refused; none creates, edits, starts or steers anything.
func TestASessionsToolsAreScopedToItsLinkedProjects(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	other := s.taskIn(s.webProject, "Elsewhere", s.marcio)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look around"})
	run := s.started(id)

	for _, c := range []struct{ tool, args string }{
		{"list_tasks", `{"project":"WC"}`},
		{"list_epics", `{"project":"WC"}`},
		{"pull_requests", `{"project":"WC"}`},
		{"findings", `{"project":"WC"}`},
		{"list_repositories", `{"project":"WC"}`},
		{"search_memory", `{"project":"WC","query":"meter"}`},
		{"remember", `{"title":"t","content":"c","about":["` + s.keyOf(other) + `"]}`},
		{"propose", `{"items":[{"kind":"task","project":"WC","title":"x","goal":"a goal long enough to keep"}]}`},
		{"propose", `{"items":[{"kind":"comment","task":"` + s.keyOf(other) + `","text":"hi"}]}`},
	} {
		if status, out := s.tool(run, c.tool, c.args); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "not") {
			t.Errorf("%s %s: %d %v, want refused", c.tool, c.args, status, out)
		}
	}
	if status, out := s.tool(run, "list_tasks", `{}`); status != 200 {
		t.Errorf("list_tasks of the one linked project: %d %v", status, out)
	}
	for _, name := range []string{"create_task", "update_task", "start_phase", "steer", "decide", "publish", "request_repository", "run_diff", "emit_event"} {
		if status, _ := s.tool(run, name, `{}`); status != 404 {
			t.Errorf("a session's agent has %s: %d", name, status)
		}
	}
	// A proposal is checked as a person's task would be.
	if status, out := s.tool(run, "propose", `{"items":[{"kind":"task","project":"BL","title":"x","goal":"short"}]}`); status != 422 {
		t.Errorf("a short goal: %d %v", status, out)
	}
	if status, out := s.tool(run, "propose", `{"items":[{"kind":"task","title":"Meter runs","goal":"Count experiment runs per org per day"}]}`); status != 200 {
		t.Errorf("propose: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE title = 'Meter runs'`); n != 0 {
		t.Errorf("propose created a task")
	}
	// Unlinked, a project's tools refuse it at once.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []any{}})
	if status, _ := s.tool(run, "list_tasks", `{"project":"BL"}`); status != 422 {
		t.Errorf("an unlinked project is still read: %d", status)
	}
}

// A repository linked to a running session reaches its agent through the
// resume path: parked, resumed with git.repositories, told where it is.
func TestLinkingARepositoryAddsItByResume(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look at billing"})
	run := s.started(id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	s.until("the web checkout", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND $2 = ANY (lux_repositories) AND status = 'running'`, run, lux.SpecName("WC-web")) == 1
	})
	v := s.luxRun(run)
	if v.resumed == 0 {
		t.Fatalf("not resumed")
	}
	var added *lux.Repository
	for _, r := range v.spec.Git.Repositories {
		if r.Name == lux.SpecName("WC-web") {
			added = &r
		}
	}
	if added == nil || added.Path != "/workspace/repos/WC/web" || added.Push == nil || *added.Push {
		t.Errorf("added %+v", added)
	}
	if !slices.ContainsFunc(v.resumes, func(in string) bool {
		return strings.Contains(in, "WC/web is now checked out at /workspace/repos/WC/web")
	}) {
		t.Errorf("the agent was not told where: %v", v.resumes)
	}
	if now, _ := s.brainstorm(id); now != run {
		t.Errorf("linking started a new Run")
	}
}

// A session's Run is no task's: the workflow, the syncer's sweeps and the
// phase notifier leave it be while a delivery beside it reaches its pull
// request. The cost sweeper reads it from lux as any Run, and a task's
// cost in the same sweep is read too; the task's and its epic's metrics
// never count the session's spend.
func TestASessionRunBreaksNoLoop(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	// An earlier session Run that ended (failed) still has its cost read, and
	// is no task's to finish or notify.
	ended := "run_ended_" + s.org
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, ended_at, lux_run_id)
		VALUES ($1, $2, $3, 1, 'failed', 'agent', 'brainstorm', now() - interval '1 hour', 'lrun_ended')`, ended, s.org, id)
	// Ended in lux with its artifacts and cost due, as any Run's are.
	mustExec(t, s.owner, `UPDATE runs SET artifacts_due_at = now(), artifacts_next_at = now(), lux_cost_next_at = now() WHERE id = ANY($1)`,
		[]string{run, ended})
	epic := "epc_" + s.org
	mustExec(t, s.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Metering')`, epic, s.org, s.project)
	wi := s.task()
	mustExec(t, s.owner, `UPDATE tasks SET epic_id = $2 WHERE id = $1`, wi, epic)
	s.deliver(wi)
	s.until("a pull request beside the session", func() bool { return len(s.gh.Pulls()) == 1 })
	if n := s.count(`SELECT count(*) FROM workflow_runs WHERE task_id IS NULL`); n != 0 {
		t.Errorf("%d workflows with no task", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run); n != 0 {
		t.Errorf("the session's Run failed")
	}

	// The cost sweep: both session Runs and a task Run due together, each
	// priced by lux; one session Run's lux has nothing to say yet.
	var taskRun, taskLux, sessionLux string
	_ = s.owner.QueryRow(t0(), `SELECT id, lux_run_id FROM runs WHERE task_id = $1 AND lux_run_id IS NOT NULL ORDER BY created_at LIMIT 1`, wi).
		Scan(&taskRun, &taskLux)
	_ = s.owner.QueryRow(t0(), `SELECT lux_run_id FROM runs WHERE id = $1`, run).Scan(&sessionLux)
	price := func(luxID, ai string) {
		s.lux.SetCost(luxID, lux.RunCost{Status: lux.CostFinal, Final: true,
			ByFamily: []lux.FamilyCost{{Family: lux.FamilyAI, Currency: "USD", Amount: lux.Decimal(ai)}}})
	}
	price(sessionLux, "7.5")
	price(taskLux, "0.25")
	mustExec(t, s.owner, `UPDATE runs SET lux_cost_next_at = now() - interval '1 second' WHERE id = ANY($1)`, []string{run, ended, taskRun})
	costs := &phases.Costs{DB: s.app, Lux: s.syncer.Lux, Log: quiet}
	if _, err := costs.Sweep(t0()); err != nil {
		t.Fatalf("the cost sweep failed with session Runs due: %v", err)
	}
	usd := func(runID string) float64 {
		var v *float64
		_ = s.owner.QueryRow(t0(), `SELECT lux_ai_usd::float8 FROM runs WHERE id = $1`, runID).Scan(&v)
		if v == nil {
			return -1
		}
		return *v
	}
	if got := usd(run); got != 7.5 {
		t.Errorf("the session Run's cost: %v, want 7.5", got)
	}
	if got := usd(taskRun); got != 0.25 {
		t.Errorf("the task Run's cost beside it: %v, want 0.25", got)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.cost.reported' AND task_id IS NULL AND session_id = $2`,
		run, id); n != 1 {
		t.Errorf("the session Run's cost event: %d, want 1 on the session, no task", n)
	}
	// The ended one, unknown to lux, is put off, not left due at once.
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_cost_next_at > now()`, ended); n != 1 {
		t.Errorf("the ended session Run is still due at once")
	}

	// Metrics: the task's and its epic's count the task's Runs, not the session's spend.
	var taskCost, epicCost, own float64
	_ = s.owner.QueryRow(t0(), `SELECT cost_usd FROM task_metrics($1)`, wi).Scan(&taskCost)
	_ = s.owner.QueryRow(t0(), `SELECT cost_usd FROM epic_metrics($1)`, epic).Scan(&epicCost)
	_ = s.owner.QueryRow(t0(), `SELECT COALESCE(sum(run_model_usd(r)), 0)::float8 FROM runs r WHERE r.task_id = $1 AND r.kind = 'agent'`, wi).Scan(&own)
	if taskCost >= 7.5 || epicCost >= 7.5 {
		t.Errorf("the session's 7.5 USD is in the task's (%v) or epic's (%v) metrics", taskCost, epicCost)
	}
	if math.Abs(taskCost-own) > 1e-9 || math.Abs(epicCost-own) > 1e-9 {
		t.Errorf("task metrics %v, epic %v, want both the task's Runs' own %v", taskCost, epicCost, own)
	}
	if n := s.count(`SELECT count(*) FROM workflow_runs WHERE task_id IS NULL`); n != 0 {
		t.Errorf("%d workflows with no task after the sweeps", n)
	}
}
