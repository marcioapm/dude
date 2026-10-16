package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// verdictFixture is a task that took over alice's pull request acme/repo
// #number, with a finished test Run failing its one criterion.
type verdictFixture struct {
	t      *testing.T
	ctx    context.Context
	org    string
	owner  *pgx.Conn
	pool   *pgxpool.Pool
	steps  *steps
	gh     *fakegithub.Server
	number int
}

type forgeOf struct{ gh *forge.GitHub }

func (f forgeOf) For(context.Context, string) (*forge.GitHub, error) { return f.gh, nil }

func newVerdictFixture(t *testing.T) *verdictFixture {
	app, owner := dbtest.Open(t)
	f := &verdictFixture{t: t, ctx: context.Background(), org: dbtest.Org(t, owner), owner: owner}
	f.gh = fakegithub.New("", "acme/repo")
	f.number = f.gh.OpenAsPerson("alice", "feature", "main", "Greet")
	srv := httptest.NewServer(f.gh.Handler())
	t.Cleanup(srv.Close)
	reply := "```yaml\n---\ncriterion: 1\nstatus: fail\nobserved: No greeting.\n```\n"
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_c', $1, 'P', 'p', 'P')`, nil},
		{`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
			VALUES ('wi_c', $1, 'prj_c', 1, 'Greet', 'greet', '["it greets"]', 'running')`, nil},
		{`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ('repo_c', $1, 'prj_c', 'repo', 'https://github.com/acme/repo.git', 'main')`, nil},
		{`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, body, state, checks, adopted)
			VALUES ('pr_c', $1, 'prj_c', 'wi_c', 'repo_c', $2, '', 'feature', 'main', '', 'Greet', '', 'open', 'unknown', true)`,
			[]any{f.number}},
		{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at)
			VALUES ('run_c', $1, 'prj_c', 'wi_c', 1, 'completed', 'test', 'qa_browser', 'agent', now())`, nil},
		{`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_c', $1, 'agent.message', 'run_c', 'wi_c', 'agent', 'run_c', 'test', jsonb_build_object('text', $2::text))`,
			[]any{reply}},
	} {
		if _, err := owner.Exec(f.ctx, s.sql, append([]any{f.org}, s.args...)...); err != nil {
			t.Fatal(err)
		}
	}
	// A pool of the test's own, so the connections it holds are this
	// step's alone.
	pool, err := pgxpool.NewWithConfig(f.ctx, app.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.pool = pool
	client := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})
	f.steps = &steps{s: &Store{DB: &db.DB{Pool: pool}}, forges: forgeOf{client}}
	return f
}

// holdPost makes GitHub hang every comment dude posts or edits until
// letGo; entered closes when the first one hangs.
func (f *verdictFixture) holdPost() (entered chan struct{}, letGo func()) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once, in sync.Once
	letGo = func() { once.Do(func() { close(release) }) }
	f.t.Cleanup(letGo) // before the server closes: Close waits for this handler
	f.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/comments") || r.Method == "PATCH" {
				in.Do(func() { close(entered) })
				<-release
			}
			return 0
		}
	})
	return entered, letGo
}

// testVerdict runs the gate's step on the failed test.
func (f *verdictFixture) testVerdict() (workflow.Result, error) {
	st := &State{TaskID: "wi_c", ProjectID: "prj_c", Attempt: 1, TestRunID: "run_c", Policy: DefaultPolicy(),
		PullRequestIDs: []string{"pr_c"}, Heads: map[string]string{"repo": "abc1234"},
		Source: &Source{Kind: SourcePR, RepositoryID: "repo_c", Repository: "repo", Number: f.number,
			HeadBranch: "feature", BaseBranch: "main"}}
	return f.verdictStep(st)
}

func (f *verdictFixture) verdictStep(st *State) (workflow.Result, error) {
	raw, _ := json.Marshal(st)
	return f.steps.testVerdict(f.ctx, workflow.StepContext{WorkflowRunID: "wf_c", OrganizationID: f.org,
		Step: "testVerdict", State: raw})
}

func (f *verdictFixture) openedState() *State {
	f.t.Helper()
	if _, err := f.owner.Exec(f.ctx, `UPDATE pull_requests SET adopted = false WHERE id = 'pr_c'`); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.owner.Exec(f.ctx, `UPDATE runs SET base_refs = '{"repo":"abc1234"}' WHERE id = 'run_c'`); err != nil {
		f.t.Fatal(err)
	}
	return &State{TaskID: "wi_c", ProjectID: "prj_c", Attempt: 1, TestRunID: "run_c", Policy: DefaultPolicy(),
		PullRequestIDs: []string{"pr_c"}, Heads: map[string]string{"repo": "abc1234"}}
}

func TestOpenedPRVerdictRefreshPreservesHumanContent(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	before, after := "A person's introduction.\n\n", "\n\n## Notes\nA person's closing paragraph.\n"
	pr := f.gh.Pull(f.number)
	f.gh.Set(func(s *fakegithub.Server) {
		pr.Title = "A person's title"
		pr.Body = before + markedVerification("## Verification\nOld verdict.") + after
	})
	res, err := f.verdictStep(st)
	if err != nil || res.Next != "fix" {
		t.Fatalf("first verdict: next %q, %v", res.Next, err)
	}
	first := markedVerification(verificationSection(&prVerification{Heads: st.Heads,
		Criteria: []prCriterion{{Text: "it greets", Status: VerdictFail}}}, st.Heads, ""))
	if got := f.gh.Pull(f.number).Body; got != before+first+after {
		t.Fatalf("first verdict body:\n%s\nwant:\n%s", got, before+first+after)
	}
	_, err = f.owner.Exec(f.ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at, base_refs)
		VALUES ('run_c2', $1, 'prj_c', 'wi_c', 1, 'completed', 'test', 'qa_browser', 'agent', now(), '{"repo":"def5678"}')`, f.org)
	if err != nil {
		t.Fatal(err)
	}
	reply := "```yaml\n---\ncriterion: 1\nstatus: not-applicable\nobserved: No greeting required.\n```\n"
	_, err = f.owner.Exec(f.ctx, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
		VALUES ('ev_c2', $1, 'agent.message', 'run_c2', 'wi_c', 'agent', 'run_c2', 'test', jsonb_build_object('text', $2::text))`, f.org, reply)
	if err != nil {
		t.Fatal(err)
	}
	before = "Human edit after the first verdict.\n\n"
	f.gh.Set(func(s *fakegithub.Server) { pr.Body = before + first + after })
	st.TestRunID, st.Heads["repo"] = "run_c2", "def5678"
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	second := markedVerification(verificationSection(&prVerification{Heads: st.Heads,
		Criteria: []prCriterion{{Text: "it greets", Status: VerdictNotApplicable}}}, st.Heads, ""))
	pr = f.gh.Pull(f.number)
	if pr.Body != before+second+after || pr.Title != "A person's title" || len(pr.Comments) != 0 {
		t.Fatalf("re-test PR: %+v; want preserved human content and updated verdict:\n%s", pr, before+second+after)
	}
}

func TestOpenedPRVerdictAppendsMissingSection(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	pr := f.gh.Pull(f.number)
	f.gh.Set(func(s *fakegithub.Server) { pr.Body = "Human text.\n" })
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	section := markedVerification(verificationSection(&prVerification{Heads: st.Heads,
		Criteria: []prCriterion{{Text: "it greets", Status: VerdictFail}}}, st.Heads, ""))
	if got := f.gh.Pull(f.number).Body; got != "Human text.\n\n"+section {
		t.Fatalf("appended body %q", got)
	}
}

// A body from before dude marked its section, with a person's fenced
// example too: two verdicts leave both as they were and exactly one
// marked section, current.
func TestOpenedPRVerdictKeepsALegacyAndAFencedSectionAndMarksItsOwnOnce(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	human := "## Verification\nChecked by hand on staging.\n\n```md\n" + verificationStart + "\n## Verification\n" +
		verificationEnd + "\n```\n"
	pr := f.gh.Pull(f.number)
	f.gh.Set(func(s *fakegithub.Server) { pr.Body = human })
	for range 2 {
		if _, err := f.verdictStep(st); err != nil {
			t.Fatal(err)
		}
	}
	section := markedVerification(verificationSection(&prVerification{Heads: st.Heads,
		Criteria: []prCriterion{{Text: "it greets", Status: VerdictFail}}}, st.Heads, ""))
	if got := f.gh.Pull(f.number).Body; got != human+"\n"+section {
		t.Fatalf("body after two verdicts:\n%q\nwant:\n%q", got, human+"\n"+section)
	}
}

// A fenced example indented under a list item, holding dude's markers, is
// the person's text: dude only ever writes its markers at column 0.
func TestOpenedPRVerdictLeavesAMarkedExampleInAListAlone(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	example := "- Example:\n\n    ```md\n    " + verificationStart + "\n    Human example, not a verdict.\n    " +
		verificationEnd + "\n    ```\n\n"
	pr := f.gh.Pull(f.number)
	f.gh.Set(func(s *fakegithub.Server) {
		pr.Body = example + verificationStart + "\n## Verification\nOld.\n" + verificationEnd + "\n"
	})
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	if got, want := f.gh.Pull(f.number).Body, example+f.currentSection(st)+"\n"; got != want {
		t.Fatalf("body:\n%q\nwant:\n%q", got, want)
	}
}

// manyOpened makes the task's pull requests six, one in each of six
// repositories, every one with a person's body; their fakes, by repo name.
// options configure the GitHub client the step uses for them.
func (f *verdictFixture) manyOpened(options ...func(*forge.GitHub)) (*State, map[string]*fakegithub.Server) {
	f.t.Helper()
	st := f.openedState()
	if _, err := f.owner.Exec(f.ctx, `DELETE FROM pull_requests WHERE id = 'pr_c'`); err != nil {
		f.t.Fatal(err)
	}
	st.PullRequestIDs, st.Heads = nil, map[string]string{}
	fakes := map[string]*fakegithub.Server{}
	mux := http.NewServeMux()
	for i := range 6 {
		name := fmt.Sprintf("r%d", i)
		g := fakegithub.New("", "acme/"+name)
		n := g.OpenAsPerson("alice", "feature", "main", "Greet")
		p := g.Pull(n)
		g.Set(func(s *fakegithub.Server) { p.Body = "Body of " + name + ".\n" })
		fakes[name] = g
		mux.Handle("/repos/acme/"+name+"/", g.Handler())
		for _, s := range []string{
			`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
				VALUES ('repo_' || $2, $1, 'prj_c', $2, 'https://github.com/acme/' || $2 || '.git', 'main')`,
			`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
				base_branch, head_sha, title, body, state, checks)
				VALUES ('pr_' || $2, $1, 'prj_c', 'wi_c', 'repo_' || $2, 1, '', 'feature', 'main', '', 'Greet', '', 'open', 'unknown')`,
		} {
			if _, err := f.owner.Exec(f.ctx, s, f.org, name); err != nil {
				f.t.Fatal(err)
			}
		}
		st.PullRequestIDs = append(st.PullRequestIDs, "pr_"+name)
		st.Heads[name] = "abc1234"
	}
	srv := httptest.NewServer(mux)
	f.t.Cleanup(srv.Close)
	f.steps.forges = forgeOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL}, options...)}
	return st, fakes
}

func (f *verdictFixture) refreshEvents() []map[string]any {
	f.t.Helper()
	rows, err := f.owner.Query(f.ctx, `SELECT payload FROM events WHERE task_id = 'wi_c'
		AND event_type = 'pull_request.verification_updated' ORDER BY cursor`)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// GitHub answers no request until four are waiting on it at once: a
// refresh that takes the pull requests one at a time never gets an answer
// within its budget, one that takes them four at a time refreshes all six,
// and never asks more than four at once.
func TestOpenedPRVerdictRefreshesSixPullRequestsFourAtATimeInOneBudget(t *testing.T) {
	f := newVerdictFixture(t)
	f.steps.s.CommentTimeout = 2 * time.Second
	st, fakes := f.manyOpened()
	var mu sync.Mutex
	inFlight, peak := 0, 0
	open := make(chan struct{})
	var opened sync.Once
	for _, g := range fakes {
		g.Set(func(s *fakegithub.Server) {
			s.Intercept = func(r *http.Request) int {
				mu.Lock()
				inFlight++
				peak = max(peak, inFlight)
				if inFlight >= 4 {
					opened.Do(func() { close(open) })
				}
				mu.Unlock()
				defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
				select {
				case <-open:
					return 0
				case <-r.Context().Done():
					return http.StatusGatewayTimeout
				}
			}
		})
	}
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	events := f.refreshEvents()
	if len(events) != 6 {
		t.Fatalf("%d refresh events, want one for each of the 6 pull requests: %v", len(events), events)
	}
	for _, e := range events {
		if e["error"] != nil {
			t.Errorf("pull request %v not refreshed: %v", e["repo"], e["error"])
		}
	}
	// The fixture's test Run tested repo alone, so beside these six it is stale.
	section := markedVerification(verificationSection(&prVerification{Heads: map[string]string{"repo": "abc1234"},
		Criteria: []prCriterion{{Text: "it greets", Status: VerdictFail}}}, st.Heads, ""))
	for name, g := range fakes {
		want := "Body of " + name + ".\n\n" + section
		if body := g.Pull(1).Body; body != want {
			t.Errorf("%s body %q, want %q", name, body, want)
		}
	}
	if peak > 4 {
		t.Errorf("%d requests to GitHub at once, want at most 4", peak)
	}
}

// GitHub never answering: the six pull requests share one budget, each
// records its own failure, and the step goes on. Every request dude sends
// carries the same deadline, the one budget's.
func TestOpenedPRVerdictGitHubNeverAnsweringCostsOneBudget(t *testing.T) {
	f := newVerdictFixture(t)
	f.steps.s.CommentTimeout = time.Second
	deadlines := &deadlineTransport{}
	st, fakes := f.manyOpened(forge.WithTransport(deadlines))
	for _, g := range fakes {
		g.Set(func(s *fakegithub.Server) {
			s.Intercept = func(r *http.Request) int {
				<-r.Context().Done()
				return http.StatusGatewayTimeout
			}
		})
	}
	start := time.Now()
	res, err := f.verdictStep(st)
	// A guard against a refresh that never ends, not a timing check.
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("the refresh took %v with GitHub hanging", took)
	}
	if err != nil || res.Next != "fix" {
		t.Fatalf("step: next %q, %v", res.Next, err)
	}
	seen := deadlines.seen()
	if len(seen) != 6 {
		t.Fatalf("%d requests reached GitHub, want the first of each of the 6 pull requests: %v", len(seen), seen)
	}
	for _, d := range seen {
		if d.IsZero() || !d.Equal(seen[0]) {
			t.Fatalf("request deadlines %v, want all six the one budget's", seen)
		}
	}
	events := f.refreshEvents()
	repos := map[any]bool{}
	for _, e := range events {
		if e["error"] == nil {
			t.Errorf("pull request %v recorded as refreshed", e["repo"])
		}
		repos[e["repo"]] = true
	}
	if len(events) != 6 || len(repos) != 6 {
		t.Fatalf("refresh events %v, want one failure for each of the 6 pull requests", events)
	}
}

// deadlineTransport records the deadline each request's context carries,
// zero for none, and sends it on.
type deadlineTransport struct {
	mu        sync.Mutex
	deadlines []time.Time
}

func (d *deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	at, _ := r.Context().Deadline()
	d.mu.Lock()
	d.deadlines = append(d.deadlines, at)
	d.mu.Unlock()
	return http.DefaultTransport.RoundTrip(r)
}

func (d *deadlineTransport) seen() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.deadlines)
}

// personEdits has a person edit the pull request's body before GitHub
// answers dude's request i to it (1 is the first), when edit(i) is not "":
// the body they leave. patches counts the PATCHes dude sent.
func (f *verdictFixture) personEdits(edit func(i int) string) (patches func() int) {
	var mu sync.Mutex
	requests, patched := 0, 0
	pr := f.gh.Pull(f.number)
	paths := []string{fmt.Sprintf("/pulls/%d", f.number), fmt.Sprintf("/issues/%d", f.number)}
	f.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if !strings.HasSuffix(r.URL.Path, paths[0]) && !strings.HasSuffix(r.URL.Path, paths[1]) {
				return 0
			}
			mu.Lock()
			defer mu.Unlock()
			requests++
			if r.Method == "PATCH" {
				patched++
			}
			if body := edit(requests); body != "" {
				f.gh.Set(func(*fakegithub.Server) { pr.Body = body })
			}
			return 0
		}
	})
	return func() int { mu.Lock(); defer mu.Unlock(); return patched }
}

func (f *verdictFixture) currentSection(st *State) string {
	return markedVerification(verificationSection(&prVerification{Heads: st.Heads,
		Criteria: []prCriterion{{Text: "it greets", Status: VerdictFail}}}, st.Heads, ""))
}

// A person edits the body after dude read it and before dude writes it:
// their edit stays, and dude's section is current in it.
func TestOpenedPRVerdictKeepsAPersonsEditMadeWhileItUpdates(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	pr := f.gh.Pull(f.number)
	f.gh.Set(func(s *fakegithub.Server) { pr.Body = "Before.\n" })
	edited := "Edited while dude was updating.\n"
	f.personEdits(func(i int) string {
		if i == 2 {
			return edited
		}
		return ""
	})
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	if got, want := f.gh.Pull(f.number).Body, edited+"\n"+f.currentSection(st); got != want {
		t.Fatalf("body %q\nwant %q", got, want)
	}
	for _, e := range f.refreshEvents() {
		if e["error"] != nil {
			t.Fatalf("refresh recorded %v", e["error"])
		}
	}
}

// A body that changes on every look: dude gives up for this verdict,
// writes nothing, and says so; the person's last edit stands.
func TestOpenedPRVerdictGivesUpOnABodyThatKeepsChanging(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	var mu sync.Mutex
	lastEdit := ""
	patches := f.personEdits(func(i int) string {
		mu.Lock()
		defer mu.Unlock()
		lastEdit = fmt.Sprintf("Edit %d.\n", i)
		return lastEdit
	})
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	want := lastEdit
	mu.Unlock()
	if body := f.gh.Pull(f.number).Body; patches() != 0 || body != want {
		t.Fatalf("%d PATCHes, body %q: want the person's last edit %q, untouched", patches(), body, want)
	}
	events := f.refreshEvents()
	if len(events) != 1 || events[0]["error"] != "body changed during update" {
		t.Fatalf("refresh events %v, want the give-up recorded", events)
	}
}

// A body already current is not written again.
func TestOpenedPRVerdictWritesNothingWhenTheSectionIsCurrent(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	pr := f.gh.Pull(f.number)
	f.gh.Set(func(s *fakegithub.Server) { pr.Body = "Intro.\n\n" + f.currentSection(st) })
	patches := f.personEdits(func(int) string { return "" })
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	if patches() != 0 {
		t.Fatalf("%d PATCHes of a body already current", patches())
	}
	if events := f.refreshEvents(); len(events) != 1 || events[0]["error"] != nil {
		t.Fatalf("refresh events %v", events)
	}
}

func TestOpenedPRVerdictHoldsNoRowOrConnection(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	entered, letGo := f.holdPost()
	done := make(chan error, 1)
	go func() { _, err := f.verdictStep(st); done <- err }()
	wait(t, entered)
	tx, err := f.owner.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, lockErr := tx.Exec(f.ctx, `SELECT id FROM pull_requests WHERE id = 'pr_c' FOR UPDATE NOWAIT`)
	_ = tx.Rollback(f.ctx)
	if lockErr != nil {
		t.Errorf("PR row locked during GitHub: %v", lockErr)
	}
	if n := f.pool.Stat().AcquiredConns(); n != 0 {
		t.Errorf("%d connections held during GitHub", n)
	}
	letGo()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("verdict step did not return")
	}
}

func TestOpenedPRVerdictGitHubFailureProceedsAndRetries(t *testing.T) {
	f := newVerdictFixture(t)
	st := f.openedState()
	f.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" {
				return http.StatusServiceUnavailable
			}
			return 0
		}
	})
	res, err := f.verdictStep(st)
	if err != nil || res.Next != "fix" {
		t.Fatalf("GitHub failure stopped delivery: next %q, %v", res.Next, err)
	}
	var failures int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM events WHERE task_id = 'wi_c'
		AND event_type = 'pull_request.verification_updated' AND payload ? 'error'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("%d recorded GitHub failures, want 1", failures)
	}
	f.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
	if _, err := f.verdictStep(st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.gh.Pull(f.number).Body, "## Verification\n") || len(f.gh.Pull(f.number).Comments) != 0 {
		t.Fatalf("retry PR: %+v", f.gh.Pull(f.number))
	}
}

func (f *verdictFixture) verdictEvents() []map[string]any {
	f.t.Helper()
	rows, err := f.owner.Query(f.ctx, `SELECT payload FROM events WHERE task_id = 'wi_c'
		AND event_type = $1 ORDER BY cursor`, EvPullRequestVerdictCommented)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// While GitHub takes its time over the verdict comment, the step holds no
// database connection and no lock on the pull request's row: another
// session (a PR sync's write, a fix's record) takes the row at once.
func TestAVerdictCommentGitHubIsSlowOverHoldsNoRowOrConnection(t *testing.T) {
	f := newVerdictFixture(t)
	entered, letGo := f.holdPost()
	type outcome struct {
		res workflow.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() { res, err := f.testVerdict(); done <- outcome{res, err} }()
	wait(t, entered)

	tx, err := f.owner.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, lockErr := tx.Exec(f.ctx, `SELECT id FROM pull_requests WHERE id = 'pr_c' FOR UPDATE NOWAIT`)
	_ = tx.Rollback(f.ctx)
	if lockErr != nil {
		t.Errorf("locking the pull request's row while GitHub answers the verdict comment: %v", lockErr)
	}
	if n := f.pool.Stat().AcquiredConns(); n != 0 {
		t.Errorf("%d database connections held while GitHub answers the verdict comment, want none", n)
	}

	letGo()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the step never returned once GitHub answered")
	}
	if got.err != nil || got.res.Next != "fix" {
		t.Fatalf("step: next %q, %v; want the failed test sent to the fixer", got.res.Next, got.err)
	}
	comments := f.gh.Pull(f.number).Comments
	events := f.verdictEvents()
	if len(comments) != 1 || len(events) != 1 || events[0]["commentId"] != float64(comments[0].ID) {
		t.Fatalf("comments %+v, events %v: want the verdict posted and recorded", comments, events)
	}
}

// GitHub hanging past the comment's time limit: the step returns well
// before forge's own per-request limit, the delivery goes on, and the
// ledger says the comment failed.
func TestAVerdictCommentGitHubNeverAnswersIsGivenUpOn(t *testing.T) {
	f := newVerdictFixture(t)
	f.steps.s.CommentTimeout = time.Second
	f.holdPost()
	start := time.Now()
	res, err := f.testVerdict()
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("the step took %v with GitHub hanging, want about the 1s limit", took)
	}
	if err != nil || res.Next != "fix" {
		t.Fatalf("step: next %q, %v; want the failed test sent to the fixer", res.Next, err)
	}
	events := f.verdictEvents()
	if len(events) != 1 || events[0]["error"] == nil || events[0]["commentId"] != nil {
		t.Fatalf("verdict events %v, want the failure recorded", events)
	}
	if len(f.gh.Pull(f.number).Comments) != 0 {
		t.Fatalf("comments %+v, want none", f.gh.Pull(f.number).Comments)
	}
}
