package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
)

// conductor makes the fixture task's conductor (no phase), and returns its
// token.
func (f *fixture) conductor(t *testing.T) string {
	t.Helper()
	token, hash := agenttools.NewToken()
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, mcp_token_hash)
		VALUES ('run_cond', $1, $2, $3, 1, 'running', 'conductor', $4)`, f.org, f.project, f.item, hash)
	return token
}

// postAs calls a tool over the JSON API and decodes its answer into out.
func (f *fixture) postAs(t *testing.T, token, tool, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("POST", f.url+"/tools/"+tool, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_ = json.NewDecoder(res.Body).Decode(out)
	return res.StatusCode
}

// dude findings and dude prs are the conductor's tools from its shell.
func TestTheConductorsToolsFromTheShell(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO review_findings (id, organization_id, task_id, category, severity, title, description)
		VALUES ('fnd_sh', $1, $2, 'correctness', 'high', 'A title', 'Its text')`, f.org, f.item)
	bin := cli(t)
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dude %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := dude("findings"); !strings.Contains(out, `"fnd_sh"`) || strings.Contains(out, "Its text") {
		t.Errorf("dude findings:\n%s", out)
	}
	if out := dude("findings", "fnd_sh"); !strings.Contains(out, "Its text") {
		t.Errorf("dude findings fnd_sh:\n%s", out)
	}
	if out := dude("prs"); !strings.Contains(out, `"pullRequests": []`) {
		t.Errorf("dude prs:\n%s", out)
	}
}

// The conductor's tools: the read ones every role has, ask_person,
// create_task, and its own findings and pull_requests.
func TestAConductorHasTheReadToolsAndItsOwn(t *testing.T) {
	f := setup(t)
	cs, err := f.connect(t, f.conductor(t))
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
	if got := strings.Join(names, ","); got != "ask_person,create_task,decide,dismiss_finding,emit_event,findings,get_memory,"+
		"list_epics,list_repositories,list_tasks,pull_requests,remember,request_repository,run_diff,search_memory,start_phase,update_task" {
		t.Errorf("a conductor sees %s", got)
	}
	// Nobody else has them.
	token := f.run(t, "run_impl", "implementer", "running")
	var out map[string]any
	for _, tool := range []string{"findings", "pull_requests", "start_phase", "decide", "dismiss_finding", "update_task"} {
		if status := f.postAs(t, token, tool, `{}`, &out); status != 404 {
			t.Errorf("an implementer's %s: %d", tool, status)
		}
	}
}

type findingsAnswer struct {
	Total    int `json:"total"`
	Findings []struct {
		ID, Severity, Category, Where, Status, Settled, RaisedBy, ResolvedBy string
		Title, Description, SuggestedFix, ResolutionNote                     string
	} `json:"findings"`
	Unknown []string `json:"unknown"`
}

// findings lists the task's findings without their text, open and most
// severe first, each with how it was settled; by id, it gives the text.
// Another task's finding is not there to read.
func TestFindingsListsHowEachWasSettledAndGivesTextById(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_rev', $1, $2, $3, 1, 'completed', 'review', 'reviewer'), ('run_fix', $1, $2, $3, 1, 'completed', 'fix', 'implementer')`,
		f.org, f.project, f.item)
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_other', $1, $2, 9, 'Other')`, f.org, f.project)
	for _, row := range []struct {
		id, task, sev, status, by string
		attempts                  int
	}{
		{"fnd_fixed", f.item, "blocking", "resolved", "run_fix", 1},
		{"fnd_low", f.item, "low", "open", "", 0},
		{"fnd_high", f.item, "high", "open", "", 2},
		{"fnd_ok", f.item, "medium", "accepted", "", 0},
		{"fnd_other", "wi_other", "blocking", "open", "", 0},
	} {
		mustExec(t, f.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, status, file, line,
				title, description, suggested_fix, resolved_by_run_id, resolution_note, fix_attempts)
			VALUES ($1, $2, $3, 'run_rev', 'correctness', $4::finding_severity, $5::finding_status, 'src/retry.ts', 41,
				'Title of '||$1, 'Why '||$1, 'Fix '||$1, NULLIF($6, ''), CASE WHEN $5 = 'resolved' THEN 'judged fixed' ELSE '' END, $7)`,
			row.id, f.org, row.task, row.sev, row.status, row.by, row.attempts)
	}

	var list findingsAnswer
	if status := f.postAs(t, token, "findings", `{}`, &list); status != 200 {
		t.Fatalf("findings: %d", status)
	}
	var lines []string
	for _, x := range list.Findings {
		lines = append(lines, fmt.Sprintf("%s %s %s %s %s", x.ID, x.Severity, x.Where, x.Status, x.Settled))
		if x.Title != "" || x.Description != "" || x.SuggestedFix != "" {
			t.Errorf("the list carries %s's text", x.ID)
		}
	}
	want := []string{
		"fnd_high high src/retry.ts:41 open open after 2 fix attempts",
		"fnd_low low src/retry.ts:41 open open",
		"fnd_fixed blocking src/retry.ts:41 resolved fixed by run_fix",
		"fnd_ok medium src/retry.ts:41 accepted accepted by a person",
	}
	if list.Total != 4 || strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings (total %d):\n%s\nwant\n%s", list.Total, strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	var one findingsAnswer
	f.postAs(t, token, "findings", `{"ids":["fnd_fixed","fnd_other","fnd_nope"]}`, &one)
	if len(one.Findings) != 1 || one.Findings[0].Description != "Why fnd_fixed" || one.Findings[0].SuggestedFix != "Fix fnd_fixed" ||
		one.Findings[0].ResolutionNote != "judged fixed" || one.Findings[0].RaisedBy != "run_rev" ||
		strings.Join(one.Unknown, ",") != "fnd_other,fnd_nope" {
		t.Errorf("by id: %+v", one)
	}
	var refused map[string]any
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("fnd_%d", i)
	}
	ids, _ := json.Marshal(map[string]any{"ids": many})
	if status := f.postAs(t, token, "findings", string(ids), &refused); status != 422 {
		t.Errorf("21 ids: %d %v", status, refused)
	}
}

// pull_requests gives each of the task's pull requests with its checks,
// reviews and feedback: author, kind, a short excerpt, and whether a fixer
// was sent it.
func TestPullRequestsGivesStateChecksReviewsAndFeedback(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_s', $1, $2, 'sdk', 'git://x/sdk.git')`,
		f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, state, checks, review, checks_json, reviews_json, unresolved_threads)
		VALUES ('pr_s', $1, $2, $3, 'repo_s', 88, 'https://x/pull/88', 'dude/x/attempt-1', 'main', '5d1e0aa', 'Retry', 'merged',
			'passing', 'approved', '[{"name":"ci","status":"completed","conclusion":"success"}]',
			'[{"login":"tiago","state":"APPROVED"}]', 0)`, f.org, f.project, f.item)
	long := strings.Repeat("browser tab will look hung past ~10s, cap it. ", 20)
	for i, c := range []struct{ author, kind, body, ignored string }{
		{"tiago", "line_comment", long, ""},
		{"bot", "comment", "Coverage: 87%", "not_permitted"},
		{"ana", "comment", "nice", ""},
	} {
		payload, _ := json.Marshal(map[string]any{"feedbackId": fmt.Sprint(i), "author": c.author, "kind": c.kind, "body": c.body,
			"path": "src/retry.ts", "number": 88, "repo": "sdk", "ignored": c.ignored})
		mustExec(t, f.owner, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
			VALUES ($1, $2, 'pull_request.commented', $3, 'integration', 'github', 'test', $4)`, fmt.Sprint("ev_", i), f.org, f.item, payload)
	}
	fixed, _ := json.Marshal([]map[string]any{{"source": "review", "author": "tiago", "body": long, "repo": "sdk", "path": "src/retry.ts"}})
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, pr_feedback)
		VALUES ('run_prfix', $1, $2, $3, 1, 'completed', 'fix', 'implementer', $4)`, f.org, f.project, f.item, fixed)

	var out struct {
		PullRequests []struct {
			Repo                        string
			Number                      int
			State, Head, Checks, Review string
			CheckDetails                []map[string]any
			Reviews                     []map[string]any
			FeedbackTotal               int
			Feedback                    []struct{ Author, Kind, Path, Excerpt, ActedOnBy, Ignored string }
		} `json:"pullRequests"`
	}
	if status := f.postAs(t, token, "pull_requests", `{}`, &out); status != 200 || len(out.PullRequests) != 1 {
		t.Fatalf("pull_requests: %d %+v", status, out)
	}
	p := out.PullRequests[0]
	if p.Repo != "sdk" || p.Number != 88 || p.State != "merged" || p.Head != "5d1e0aa" || p.Checks != "passing" || p.Review != "approved" ||
		len(p.CheckDetails) != 1 || p.CheckDetails[0]["conclusion"] != "success" || len(p.Reviews) != 1 || p.FeedbackTotal != 3 {
		t.Errorf("pull request: %+v", p)
	}
	if len(p.Feedback) != 3 {
		t.Fatalf("feedback: %+v", p.Feedback)
	}
	first := p.Feedback[0]
	if first.Author != "tiago" || first.Kind != "line_comment" || first.ActedOnBy != "run_prfix" || len([]rune(first.Excerpt)) != 280 ||
		!strings.HasPrefix(first.Excerpt, "browser tab will look hung") {
		t.Errorf("tiago's: %+v", first)
	}
	if p.Feedback[1].Ignored != "not_permitted" || p.Feedback[1].ActedOnBy != "" || p.Feedback[2].Excerpt != "nice" {
		t.Errorf("the others: %+v", p.Feedback[1:])
	}
}

// findings lists at most 200 of a task's findings, and says how many it has.
func TestFindingsListsAtMost200(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO review_findings (id, organization_id, task_id, category, severity, title, description, created_at)
		SELECT 'fnd_'||lpad(n::text, 3, '0'), $1, $2, 'correctness', 'low', 'T', 'D', now() + n * interval '1 millisecond'
		FROM generate_series(1, 201) n`, f.org, f.item)
	var list findingsAnswer
	if status := f.postAs(t, token, "findings", `{}`, &list); status != 200 {
		t.Fatalf("findings: %d", status)
	}
	if list.Total != 201 || len(list.Findings) != 200 {
		t.Fatalf("findings: total %d, returned %d; want 201 and 200", list.Total, len(list.Findings))
	}
	// All equally severe and open: oldest first, the newest left out.
	if list.Findings[0].ID != "fnd_001" || list.Findings[199].ID != "fnd_200" {
		t.Errorf("returned %s … %s, want fnd_001 … fnd_200", list.Findings[0].ID, list.Findings[199].ID)
	}
}

// pullRequestFixture is a conductor's task with one open pull request,
// sdk#88, and the conductor's token.
func pullRequestFixture(t *testing.T) (*fixture, string) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		('repo_s', $1, $2, 'sdk', 'git://x/sdk.git'), ('repo_w', $1, $2, 'web', 'git://x/web.git')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, state, checks, review)
		VALUES ('pr_s', $1, $2, $3, 'repo_s', 88, 'https://x/pull/88', 'dude/x/attempt-1', 'main', '5d1e0aa', 'Retry', 'open',
			'passing', 'pending'),
		       ('pr_w', $1, $2, $3, 'repo_w', 12, 'https://x/pull/12', 'dude/x/attempt-1', 'main', '9ab0c11', 'Retry', 'open',
			'passing', 'pending')`, f.org, f.project, f.item)
	return f, token
}

// comment records a comment on a pull request of the fixture's task.
func (f *fixture) comment(t *testing.T, id, repo string, number int, author, body, path string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"feedbackId": id, "author": author, "kind": "line_comment", "body": body,
		"path": path, "number": number, "repo": repo})
	mustExec(t, f.owner, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		VALUES ('ev_'||$1, $2, 'pull_request.commented', $3, 'integration', 'github', 'test', $4)`, id, f.org, f.item, payload)
}

type feedbackAnswer struct {
	PullRequests []struct {
		Repo          string
		FeedbackTotal int
		Feedback      []struct{ Author, Path, Excerpt, ActedOnBy string }
	} `json:"pullRequests"`
}

// pull_requests gives a pull request's latest 50 feedback items, oldest
// first, and says how many it has.
func TestPullRequestsGivesTheLatest50FeedbackItems(t *testing.T) {
	f, token := pullRequestFixture(t)
	for i := range 51 {
		f.comment(t, fmt.Sprintf("c%02d", i), "sdk", 88, "tiago", fmt.Sprintf("comment %02d", i), "a.go")
	}
	var out feedbackAnswer
	if status := f.postAs(t, token, "pull_requests", `{}`, &out); status != 200 || len(out.PullRequests) != 2 {
		t.Fatalf("pull_requests: %d %+v", status, out)
	}
	p := out.PullRequests[0]
	if p.Repo != "sdk" || p.FeedbackTotal != 51 || len(p.Feedback) != 50 {
		t.Fatalf("sdk#88: total %d, returned %d; want 51 and 50", p.FeedbackTotal, len(p.Feedback))
	}
	for i, fb := range p.Feedback {
		if want := fmt.Sprintf("comment %02d", i+1); fb.Excerpt != want {
			t.Fatalf("feedback %d is %q, want %q: the latest 50, oldest first", i, fb.Excerpt, want)
		}
	}
}

// The same reviewer's same words on two paths, and on another pull
// request: only the one a fixer was sent — its repository and path — reads
// as acted on.
func TestFeedbackIsActedOnOnlyWhereTheFixerWasSentIt(t *testing.T) {
	f, token := pullRequestFixture(t)
	f.comment(t, "c1", "sdk", 88, "tiago", "Handle the nil case", "a.go")
	f.comment(t, "c2", "sdk", 88, "tiago", "Handle the nil case", "b.go")
	f.comment(t, "c3", "web", 12, "tiago", "Handle the nil case", "a.go")
	sent, _ := json.Marshal([]map[string]any{{"source": "review", "author": "tiago", "body": "Handle the nil case",
		"repo": "sdk", "path": "a.go", "kind": "line_comment"}})
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, pr_feedback)
		VALUES ('run_prfix', $1, $2, $3, 1, 'completed', 'fix', 'implementer', $4)`, f.org, f.project, f.item, sent)

	var out feedbackAnswer
	if status := f.postAs(t, token, "pull_requests", `{}`, &out); status != 200 || len(out.PullRequests) != 2 {
		t.Fatalf("pull_requests: %d %+v", status, out)
	}
	acted := map[string]string{}
	for _, p := range out.PullRequests {
		for _, fb := range p.Feedback {
			acted[p.Repo+":"+fb.Path] = fb.ActedOnBy
		}
	}
	if acted["sdk:a.go"] != "run_prfix" || acted["sdk:b.go"] != "" || acted["web:a.go"] != "" {
		t.Errorf("acted on: %v; want only sdk:a.go, by run_prfix", acted)
	}
}
