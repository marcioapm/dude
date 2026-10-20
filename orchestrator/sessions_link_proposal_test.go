package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// proposeLink has the session's agent propose linking project, through
// the propose tool, and answers the card.
func (s *sessionWorld) proposeLink(run, project, reason string) (int, map[string]any) {
	s.t.Helper()
	return s.latestTool(run, "propose", `{"items":[{"kind":"link_project","project":"`+project+`","reason":"`+reason+`"}]}`)
}

// The session's agent proposes linking WC, which the session does not link:
// the item is kept with its reason; a chat member cannot confirm it, the
// owner can, and doing so links WC with its repository through the link
// API's own write (setLinks), recorded as session.linked with the whole
// set. The linked project reaches the running agent at its next resume,
// checked out, and its next briefing lists it.
func TestTheOwnerConfirmsALinkTheAgentProposed(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does the console call billing?"})
	run := s.started(id)
	if p := s.prompt(run); !strings.Contains(p, "## The organisation's other projects") || !strings.Contains(p, "- WC (web-console)") {
		t.Errorf("the briefing does not list WC as one to propose:\n%s", p)
	}

	status, out := s.proposeLink(run, "wc", "the console's calls to billing live in web")
	if status != 200 {
		t.Fatalf("propose: %d %v", status, out)
	}
	proposal := out["proposalId"].(string)
	card := s.ok(s.ana, "GET", "/internal/sessions/"+id, nil)["proposals"].([]any)[0].(map[string]any)
	item := card["items"].([]any)[0].(map[string]any)
	if item["kind"] != "link_project" || item["project"] != "WC" || item["reason"] != "the console's calls to billing live in web" {
		t.Errorf("item %v", item)
	}
	if st := card["status"].([]any)[0].(map[string]any); st["canFile"] != false || st["blockedBy"] != "session_owner" {
		t.Errorf("Ana's status %v, want only the owner", st)
	}
	results := s.ok(s.ana, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0}})["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "refused" || r["why"] != "only the session's owner can link a project" {
		t.Errorf("Ana's confirm: %v", r)
	}
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, id, s.webProject); n != 0 {
		t.Fatal("a chat member linked a project")
	}

	s.runningIdle(run)
	results = s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0}})["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "filed" || r["key"] != "WC" || r["kind"] != "link_project" {
		t.Errorf("Márcio's confirm: %v", r)
	}
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, id, s.webProject); n != 1 {
		t.Errorf("WC is not linked")
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1 AND repository_id IN ($2, $3)`, id, s.webRepo, s.repoID); n != 2 {
		t.Errorf("%d repositories linked, want BL's and WC's", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.linked' AND actor_id = $2
		AND jsonb_array_length(payload->'projects') = 2`, id, s.marcio); n != 1 {
		t.Errorf("no session.linked by Márcio with the whole set")
	}
	detail := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)
	var keys []string
	for _, p := range detail["session"].(map[string]any)["projects"].([]any) {
		keys = append(keys, p.(map[string]any)["key"].(string))
	}
	if !slices.Equal(keys, []string{"BL", "WC"}) {
		t.Errorf("the session's projects %v", keys)
	}
	if st := detail["proposals"].([]any)[0].(map[string]any)["status"].([]any)[0].(map[string]any); st["filed"] != true || st["key"] != "WC" {
		t.Errorf("the card's status %v", st)
	}
	s.until("WC/web checked out on the agent", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_name('WC-web') = ANY (lux_repositories)`, run) == 1
	})
	if v := s.luxRun(run); !slices.ContainsFunc(v.resumes, func(in string) bool { return strings.Contains(in, "A member linked WC/web") }) {
		t.Errorf("the agent was not told WC/web is checked out: %q", v.resumes)
	}

	// Proposed again once linked: refused, saying so.
	s.runningIdle(run)
	if status, out := s.proposeLink(run, "WC", "again"); status == 200 || out["error"] != "item 1: project WC is linked to this session already" {
		t.Errorf("proposing a linked project: %d %v", status, out)
	}
}

// link_project names only a project of the session's organisation: another
// organisation's key, or none at all, is refused naming the ones it can
// propose; a reason is required.
func TestALinkProposalIsScopedToTheOrganisation(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	other := s.otherOrg()
	mustExec(t, s.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_theirs', $1, 'theirs', 'theirs', 'THR')`, other)

	for project, want := range map[string]string{
		"THR":  "item 1: no project THR in this organisation; the projects you can propose to link are WC",
		"NOPE": "item 1: no project NOPE in this organisation; the projects you can propose to link are WC",
	} {
		if status, out := s.proposeLink(run, project, "why not"); status == 200 || out["error"] != want {
			t.Errorf("%s: %d %v, want %q", project, status, out, want)
		}
	}
	if status, out := s.latestTool(run, "propose", `{"items":[{"kind":"link_project","project":"WC"}]}`); status == 200 ||
		out["error"] != "item 1: a link_project gives its reason, 1 to 2000 characters, for the member who confirms it" {
		t.Errorf("no reason: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM session_proposals WHERE session_id = $1`, id); n != 0 {
		t.Errorf("a refused proposal was kept")
	}

	// A card naming a project that is gone by the time the owner confirms
	// it links nothing.
	status, out := s.proposeLink(run, "WC", "the console")
	if status != 200 {
		t.Fatalf("propose: %d %v", status, out)
	}
	mustExec(t, s.owner, `DELETE FROM repositories WHERE project_id = $1`, s.webProject)
	mustExec(t, s.owner, `DELETE FROM projects WHERE id = $1`, s.webProject)
	results := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": out["proposalId"], "items": []int{0}})["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "refused" || r["why"] != "no project WC in this organisation" {
		t.Errorf("confirming a removed project: %v", r)
	}
	var linked int
	_ = s.owner.QueryRow(context.Background(), `SELECT count(*) FROM session_projects WHERE session_id = $1`, id).Scan(&linked)
	if linked != 1 {
		t.Errorf("%d projects linked, want BL alone", linked)
	}
}

// A proposal naming one project to link twice is refused: filing both
// would fail the whole card. A reason is at most 2,000 characters.
func TestALinkProposalNamesEachProjectOnce(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	status, out := s.latestTool(run, "propose", `{"items":[{"kind":"link_project","project":"WC","reason":"web"},`+
		`{"kind":"link_project","project":"web-console","reason":"again"}]}`)
	if status == 200 || out["error"] != "item 2: project WC is item 1's already" {
		t.Errorf("a project proposed twice: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM session_proposals WHERE session_id = $1`, id); n != 0 {
		t.Errorf("a refused proposal was kept")
	}
	long := strings.Repeat("r", 2001)
	if status, out := s.proposeLink(run, "WC", long); status == 200 ||
		out["error"] != "item 1: a link_project gives its reason, 1 to 2000 characters, for the member who confirms it" {
		t.Errorf("a reason of 2,001 characters: %d %v", status, out)
	}
	if status, out := s.proposeLink(run, "WC", long[:2000]); status != 200 {
		t.Errorf("a reason of 2,000 characters: %d %v", status, out)
	}
}

// A project a member links between the agent's proposal and the owner's
// confirm is blocked for everyone, as linked already, and links nothing more.
func TestALinkConfirmedAfterAMemberLinkedItIsBlocked(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	status, out := s.proposeLink(run, "WC", "the console")
	if status != 200 {
		t.Fatalf("propose: %d %v", status, out)
	}
	s.linkWeb(id)
	card := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["proposals"].([]any)[0].(map[string]any)
	if st := card["status"].([]any)[0].(map[string]any); st["canFile"] != false || st["blockedBy"] != "linked" {
		t.Errorf("the link's status %v", st)
	}
	results := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": out["proposalId"], "items": []int{0}})["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "refused" || r["why"] != "project WC is linked already" {
		t.Errorf("the confirm: %v", r)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.linked'`, id); n != 1 {
		t.Errorf("%d session.linked, want the member's alone", n)
	}
}

// The briefing lists at most 50 of the organisation's unlinked projects,
// then how many more there are.
func TestTheBriefingListsFiftyUnlinkedProjects(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	// WC and 51 more: 52 unlinked.
	for i := range 51 {
		p := fmt.Sprintf("prj_more%02d_%s", i, s.org)
		mustExec(t, s.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, $3, $1, $3)`,
			p, s.org, fmt.Sprintf("M%02d", i))
	}
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	p := s.prompt(s.started(id))
	const heading = "## The organisation's other projects\nNot linked to this session; the owner links one when the conversation needs it.\n\n"
	start := strings.Index(p, heading)
	if start < 0 {
		t.Fatalf("no list of unlinked projects:\n%s", p)
	}
	section, _, _ := strings.Cut(p[start+len(heading):], "\n\n")
	lines := strings.Split(section, "\n")
	if len(lines) != 51 || lines[50] != "- and 2 more" || lines[0] != "- M00 (M00)" || lines[49] != "- M49 (M49)" {
		t.Errorf("%d lines, first %q, 50th %q, last %q", len(lines), lines[0], lines[min(49, len(lines)-1)], lines[len(lines)-1])
	}
}

// A session linking the most projects it may (50) files a card's other
// items; only its link_project is refused, saying why.
func TestALinkBeyondTheSessionsLimitRefusesOnlyThatItem(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	links := []map[string]any{{"projectId": s.project, "repositoryIds": []string{}}}
	for i := range 49 {
		p := fmt.Sprintf("prj_more%02d_%s", i, s.org)
		mustExec(t, s.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, $1, $1, $3)`,
			p, s.org, fmt.Sprintf("M%02d", i))
		links = append(links, map[string]any{"projectId": p, "repositoryIds": []string{}})
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": links})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	status, out := s.latestTool(run, "propose", `{"items":[`+
		`{"kind":"task","project":"BL","title":"Meter runs","goal":"Count experiment runs per org per day"},`+
		`{"kind":"link_project","project":"WC","reason":"the console"}]}`)
	if status != 200 {
		t.Fatalf("propose: %d %v", status, out)
	}
	card := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["proposals"].([]any)[0].(map[string]any)
	if st := card["status"].([]any)[1].(map[string]any); st["canFile"] != false || st["blockedBy"] != "limit" {
		t.Errorf("the link's status %v", st)
	}
	results := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": out["proposalId"], "items": []int{0, 1}})["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "filed" {
		t.Errorf("the task: %v", r)
	}
	if r := results[1].(map[string]any); r["status"] != "refused" || r["why"] != "the session links 50 projects already, the most it can" {
		t.Errorf("the link: %v", r)
	}
	if n := s.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, id, s.webProject); n != 0 {
		t.Errorf("WC was linked beyond the limit")
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE project_id = $1 AND title = 'Meter runs'`, s.project); n != 1 {
		t.Errorf("the task was not filed")
	}
}
