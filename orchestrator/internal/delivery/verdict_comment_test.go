package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	raw, _ := json.Marshal(st)
	return f.steps.testVerdict(f.ctx, workflow.StepContext{WorkflowRunID: "wf_c", OrganizationID: f.org,
		Step: "testVerdict", State: raw})
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
