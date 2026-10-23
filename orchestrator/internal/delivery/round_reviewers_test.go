package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// laterRound is a task whose whole change touched routes, a .tsx file and
// a migration, whose first review round ended at aaa; its heads are now
// bbb, and GitHub compares aaa...bbb as files.
type laterRound struct {
	t        *testing.T
	ctx      context.Context
	org      string
	owner    *pgx.Conn
	exec     func(q string, args ...any)
	steps    *steps
	files    []map[string]any
	status   int
	compares int
	st       *State
}

// The whole task's paths, which pick every conditional reviewer.
var wholeTask = []string{"repo/src/api/routes/work.ts", "repo/web/src/App.tsx", "repo/migrations/031_x.sql", "repo/src/auth/login.ts"}

func newLaterRound(t *testing.T, files ...string) *laterRound {
	app, owner := dbtest.Open(t)
	r := &laterRound{t: t, ctx: context.Background(), org: dbtest.Org(t, owner), owner: owner, status: 200}
	r.exec = func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(r.ctx, q, append([]any{r.org}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range files {
		r.files = append(r.files, map[string]any{"filename": name, "status": "modified", "additions": 3, "deletions": 1})
	}
	r.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_rr', $1, 'P', 'p', 'P')`)
	r.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_rr', $1, 'prj_rr', 1, 'T', 'g', 'running')`)
	r.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES ('repo_rr', $1, 'prj_rr', 'repo', 'https://github.com/acme/repo.git', 'main')`)
	r.exec(`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_rr', 'repo_rr', $1)`)
	r.st = &State{TaskID: "wi_rr", ProjectID: "prj_rr", Attempt: 1, Iteration: 1, Decider: "policy", Policy: DefaultPolicy(),
		Heads: map[string]string{"repo": "bbb"}, ChangedPaths: wholeTask}
	raw, _ := json.Marshal(r.st)
	r.exec(`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id)
		VALUES ('wfr_rr', $1, 'task.delivery', 'rr', 'running', 'review', $2, 'wi_rr')`, raw)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.Contains(req.URL.Path, "/compare/aaa...bbb") {
			t.Errorf("unexpected request %s", req.URL.Path)
		}
		r.compares++
		w.WriteHeader(r.status)
		_ = json.NewEncoder(w).Encode(map[string]any{"files": r.files})
	}))
	t.Cleanup(srv.Close)
	pool, err := pgxpool.NewWithConfig(r.ctx, app.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	r.steps = &steps{s: &Store{DB: &db.DB{Pool: pool}}, forges: forgeOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "t", APIBaseURL: srv.URL})}}
	return r
}

// firstRound records a completed review round of the attempt at aaa.
func (r *laterRound) firstRound() *laterRound {
	r.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category, base_refs, ended_at)
		VALUES ('run_rr1', $1, 'prj_rr', 'wi_rr', 1, 'completed', 'review', 'reviewer', 'agent', 'correctness', '{"repo":"aaa"}', now())`)
	return r
}

// attempted adds an open finding of category a fix attempted.
func (r *laterRound) attempted(category string) {
	r.exec(`INSERT INTO review_findings (id, organization_id, task_id, run_id, severity, category, title, description, status, fix_attempts)
		VALUES ('fnd_'||$2::text, $1, 'wi_rr', 'run_rr1', 'high', $2, 'T', 'd', 'open', 1)`, category)
}

func (r *laterRound) sc(step string) workflow.StepContext {
	raw, _ := json.Marshal(r.st)
	return workflow.StepContext{WorkflowRunID: "wfr_rr", OrganizationID: r.org, Step: step, State: raw}
}

// review runs the review step and returns the round's reviewers and why
// each runs, as its run.created records them, and the step's result.
func (r *laterRound) review() ([]string, map[string][]string, workflow.Result) {
	r.t.Helper()
	res, err := r.steps.review(r.ctx, r.sc("review"))
	if err != nil {
		r.t.Fatal(err)
	}
	var payload struct {
		Reviewers []string            `json:"reviewers"`
		Reasons   map[string][]string `json:"reasons"`
	}
	var raw []byte
	if err := r.owner.QueryRow(r.ctx, `SELECT payload FROM events WHERE task_id = 'wi_rr' AND event_type = 'run.created'
		AND run_id IS NULL ORDER BY occurred_at DESC, id DESC LIMIT 1`).Scan(&raw); err != nil {
		r.t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		r.t.Fatal(err)
	}
	var made []string
	rows, err := r.owner.Query(r.ctx, `SELECT category FROM runs WHERE task_id = 'wi_rr' AND id <> 'run_rr1' ORDER BY category`)
	if err != nil {
		r.t.Fatal(err)
	}
	made, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		r.t.Fatal(err)
	}
	if want := slices.Sorted(slices.Values(payload.Reviewers)); !slices.Equal(made, want) {
		r.t.Fatalf("runs made %v, run.created says %v", made, payload.Reviewers)
	}
	return payload.Reviewers, payload.Reasons, res
}

var firstRoundOfWholeTask = []string{"correctness", "tests", "security", "database", "api", "frontend", "performance"}

func TestALaterRoundOfACIChangeRunsCorrectnessAlone(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	got, why, _ := r.review()
	if !slices.Equal(got, []string{"correctness"}) {
		t.Fatalf("reviewers %v, want correctness alone", got)
	}
	if !slices.Equal(why["correctness"], []string{ReasonAlways}) {
		t.Errorf("why %v", why)
	}
	if r.compares != 1 {
		t.Errorf("%d compares for one repository's round, want 1 shared with the resumption", r.compares)
	}
}

func TestALaterRoundRunsTheReviewerOfAFindingItJudges(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	r.attempted("api")
	got, why, _ := r.review()
	if !slices.Equal(got, []string{"correctness", "api"}) {
		t.Fatalf("reviewers %v, want correctness and api", got)
	}
	if !slices.Equal(why["api"], []string{ReasonFinding}) {
		t.Errorf("why %v", why)
	}
}

// The round's own change is one .tsx file: product, so the required
// reviewers, and frontend by its path; not the reviewers the whole task's
// routes, migrations and auth picked.
func TestALaterRoundsReviewersFollowItsOwnChangeNotTheWholeTasks(t *testing.T) {
	r := newLaterRound(t, "web/src/Button.tsx").firstRound()
	got, why, _ := r.review()
	if !slices.Equal(got, []string{"correctness", "tests", "frontend", "performance"}) {
		t.Fatalf("reviewers %v", got)
	}
	if !slices.Equal(why["frontend"], []string{ReasonPaths}) || !slices.Equal(why["tests"], []string{ReasonRequired}) ||
		!slices.Equal(why["correctness"], []string{ReasonAlways, ReasonRequired}) {
		t.Errorf("why %v", why)
	}
}

func TestALargeLaterRoundRunsTheFirstRoundsReviewers(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	r.files[0]["additions"] = MaxResumeDeltaLines
	got, why, _ := r.review()
	if !slices.Equal(got, firstRoundOfWholeTask) {
		t.Fatalf("reviewers %v, want the first round's", got)
	}
	if !slices.Equal(why["database"], []string{ReasonLargeRound}) {
		t.Errorf("why %v", why)
	}
}

func TestAnUnmeasurableLaterRoundRunsTheFirstRoundsReviewers(t *testing.T) {
	for _, how := range []string{"refused", "truncated", "no forge"} {
		t.Run(how, func(t *testing.T) {
			r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
			switch how {
			case "refused":
				r.status = 404
			case "truncated":
				for range 300 {
					r.files = append(r.files, map[string]any{"filename": "docs/a.md", "additions": 0, "deletions": 0})
				}
			case "no forge":
				r.steps.forges = nil
			}
			got, why, _ := r.review()
			if !slices.Equal(got, firstRoundOfWholeTask) {
				t.Fatalf("reviewers %v, want the first round's", got)
			}
			if !slices.Equal(why["api"], []string{ReasonFirstRound}) {
				t.Errorf("why %v", why)
			}
		})
	}
}

func TestTheFirstRoundOfAnAttemptRunsTheWholeChangesReviewers(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml")
	got, _, _ := r.review()
	if !slices.Equal(got, firstRoundOfWholeTask) {
		t.Fatalf("reviewers %v", got)
	}
	if r.compares != 0 {
		t.Errorf("%d compares for a first round", r.compares)
	}
}

// A project that runs no reviewer always: a CI-only later round has no
// reviewer, and the loop goes on as from a clear round.
func TestALaterRoundWithNoReviewerGoesOnClear(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	p, err := ResolvePolicy(nil, json.RawMessage(`{"alwaysReviewers":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	r.st.Policy = p
	got, _, res := r.review()
	if len(got) != 0 || res.Next != "awaitReview" || len(res.AwaitSignals) != 0 {
		t.Fatalf("reviewers %v, result %+v; want none, straight to awaitReview", got, res)
	}
	r.st = res.State.(*State)
	after, err := r.steps.awaitReview(r.ctx, r.sc("awaitReview"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Next != "simplify" || after.State.(*State).Iteration != 2 {
		t.Fatalf("after an empty round: %+v", after)
	}
}

// policy layers raw over the defaults, as a project's settings do.
func (r *laterRound) policy(raw string) {
	r.t.Helper()
	p, err := ResolvePolicy(nil, json.RawMessage(raw))
	if err != nil {
		r.t.Fatal(err)
	}
	r.st.Policy = p
}

// completeRound marks every Run the test's review steps made completed at
// the heads they started from.
func (r *laterRound) completeRound() {
	r.exec(`UPDATE runs SET status = 'completed', ended_at = now() WHERE organization_id = $1 AND task_id = 'wi_rr' AND id <> 'run_rr1'`)
}

// A pull request's CI-only fix on its candidate, in a project that runs no
// reviewer always: promotion waits on a completed review at the candidate's
// head, so the round runs the required reviewers rather than none.
func TestAnEmptyRoundOnACandidateRunsTheRequiredReviewers(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	r.policy(`{"alwaysReviewers":[]}`)
	r.st.Candidate = &Candidate{Round: 1, Branch: CandidateBranch("wi_rr", 1), From: map[string]string{"repo": "aaa"}, Base: map[string]string{"repo": "aaa"}}
	res, err := r.steps.promoteCandidate(r.ctx, r.sc("promoteCandidate"))
	if err != nil || res.Next != "review" {
		t.Fatalf("promoteCandidate on an unreviewed head went to %q: %v", res.Next, err)
	}
	got, why, res := r.review()
	if !slices.Equal(got, []string{"correctness", "tests", "performance"}) || len(res.AwaitSignals) == 0 {
		t.Fatalf("reviewers %v, result %+v; want the required ones, awaited", got, res)
	}
	if !slices.Equal(why["tests"], []string{ReasonRequired}) {
		t.Errorf("why %v", why)
	}
	r.completeRound()
	if unreviewed, err := r.steps.s.unreviewedHeads(r.ctx, r.org, r.st, r.st.Candidate); err != nil || len(unreviewed) != 0 {
		t.Fatalf("after the round, promotion still finds %v unreviewed: %v", unreviewed, err)
	}
}

// With no required reviewer either, the gate-bound round falls back to the
// first round's reviewers.
func TestAnEmptyGateBoundRoundWithNoRequiredReviewerRunsTheFirstRounds(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	r.policy(`{"alwaysReviewers":[],"requiredReviewers":[]}`)
	r.st.Candidate = &Candidate{Round: 1, Branch: CandidateBranch("wi_rr", 1), From: map[string]string{"repo": "aaa"}, Base: map[string]string{"repo": "aaa"}}
	got, why, _ := r.review()
	if !slices.Equal(got, []string{"security", "database", "api", "frontend"}) || !slices.Equal(why["api"], []string{ReasonFirstRound}) {
		t.Fatalf("reviewers %v %v", got, why)
	}
}

// A CI-only head the conductor published, before any pull request: the
// pull request step sends it to review, and that round must complete a
// review at the head for the step to let it through.
func TestAnEmptyRoundOnAnUntestedConductorHeadRunsTheRequiredReviewers(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	r.policy(`{"alwaysReviewers":[]}`)
	r.exec(`INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, status, heads)
		VALUES ('pub_rr', $1, 'wi_rr', 'run_rr1', 'req_rr', 'published', '{"repo":{"sha":"bbb"}}')`)
	res, err := r.steps.openPullRequest(r.ctx, r.sc("openPullRequest"))
	if err != nil || res.Next != "review" {
		t.Fatalf("openPullRequest on an untested conductor head went to %q: %v", res.Next, err)
	}
	got, why, res := r.review()
	if !slices.Equal(got, []string{"correctness", "tests", "performance"}) || len(res.AwaitSignals) == 0 {
		t.Fatalf("reviewers %v, result %+v; want the required ones, awaited", got, res)
	}
	if !slices.Equal(why["correctness"], []string{ReasonRequired}) {
		t.Errorf("why %v", why)
	}
	r.completeRound()
	if _, diverted, err := r.steps.reviewUntested(r.ctx, r.sc("openPullRequest"), r.st); err != nil || diverted {
		t.Fatalf("after the round, the pull request step still diverts: %v", err)
	}
}

func TestDirectedAndRetriedRoundsKeepTheirCategories(t *testing.T) {
	r := newLaterRound(t, ".github/workflows/x.yml").firstRound()
	r.st.RetryCategories = []string{"security"}
	got, why, _ := r.review()
	if !slices.Equal(got, []string{"security"}) || !slices.Equal(why["security"], []string{ReasonRetry}) {
		t.Fatalf("retry: %v %v", got, why)
	}

	r = newLaterRound(t, ".github/workflows/x.yml").firstRound()
	r.st.Directed = &Directed{Phase: PhaseReview, Categories: []string{"database"}}
	got, why, _ = r.review()
	if !slices.Equal(got, []string{"database"}) || !slices.Equal(why["database"], []string{ReasonDirected}) {
		t.Fatalf("directed: %v %v", got, why)
	}
}

func TestAlwaysReviewersAndNonProductPathsAreValidated(t *testing.T) {
	if _, err := ResolvePolicy(json.RawMessage(`{"alwaysReviewers":["vibes"]}`)); err == nil {
		t.Error("an unknown always-run reviewer was accepted")
	}
	if _, err := ResolvePolicy(json.RawMessage(`{"nonProductPaths":[""]}`)); err == nil {
		t.Error("an empty non-product glob was accepted")
	}
	p, err := ResolvePolicy(json.RawMessage(`{"alwaysReviewers":["security"]}`), json.RawMessage(`{"nonProductPaths":["infra/**"]}`))
	if err != nil || !slices.Equal(p.AlwaysReviewers, []string{"security"}) || !slices.Equal(p.NonProductPaths, []string{"infra/**"}) {
		t.Fatalf("layers: %+v %v", p, err)
	}
	if d := DefaultPolicy(); !slices.Equal(d.AlwaysReviewers, []string{"correctness"}) ||
		!slices.Equal(d.NonProductPaths, []string{".github/**", "docs/**", "**/*.md"}) {
		t.Errorf("defaults %v %v", d.AlwaysReviewers, d.NonProductPaths)
	}
}
