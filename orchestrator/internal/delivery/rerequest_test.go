package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// rerequestFixture is a task with an open pull request #1 in each of the
// named repositories (acme/<name> on one fake GitHub), each reviewed by
// jerv on its first commit.
type rerequestFixture struct {
	t      *testing.T
	ctx    context.Context
	org    string
	owner  *pgx.Conn
	s      *Store
	st     *State
	client *forge.GitHub
	gh     map[string]*fakegithub.Server
}

func newRerequestFixture(t *testing.T, repos ...string) *rerequestFixture {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	f := &rerequestFixture{t: t, ctx: context.Background(), org: org, owner: owner, s: &Store{DB: app},
		st: &State{TaskID: "wi_q", ProjectID: "prj_q"}, gh: map[string]*fakegithub.Server{}}
	f.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_q', $1, 'P', 'p', 'P')`)
	f.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
		VALUES ('wi_q', $1, 'prj_q', 1, 'Greet', 'greet', 'review')`)
	mux := http.NewServeMux()
	for _, name := range repos {
		bare := filepath.Join(t.TempDir(), name+".git")
		seed := filepath.Join(t.TempDir(), name)
		for _, args := range [][]string{
			{"init", "-q", "--bare", "-b", "main", bare},
			{"init", "-q", "-b", "feature", seed},
			{"-C", seed, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "initial"},
			{"-C", seed, "push", "-q", bare, "feature"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		g := fakegithub.New(bare, "acme/"+name)
		f.gh[name] = g
		mux.Handle("/repos/acme/"+name+"/", g.Handler())
		f.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ($2, $1, 'prj_q', $3, $4, 'main')`, "repo_"+name, name, "https://github.com/acme/"+name+".git")
		f.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, body, state, checks)
			VALUES ($2, $1, 'prj_q', 'wi_q', $3, 1, $4, 'feature', 'main', '', 'Greet', '', 'open', 'unknown')`,
			"pr_"+name, "repo_"+name, "https://github.com/acme/"+name+"/pull/1")
		f.st.PullRequestIDs = append(f.st.PullRequestIDs, "pr_"+name)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.client = forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})
	for _, name := range repos {
		if _, err := f.client.OpenPullRequest(f.ctx, forge.OpenPullRequest{Slug: "acme/" + name, Title: "Greet", Head: "feature", Base: "main"}); err != nil {
			t.Fatal(err)
		}
		f.gh[name].Review(1, "jerv", "COMMENTED")
	}
	return f
}

func (f *rerequestFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.owner.Exec(f.ctx, sql, append([]any{f.org}, args...)...); err != nil {
		f.t.Fatal(err)
	}
}

// fix pushes dude's fix onto repo's pull request head; its commit, which
// the repository's name in its message keeps apart from the others'.
func (f *rerequestFixture) fix(repo string) string {
	return f.gh[repo].CommitOnTop("feature", "dude's fix to "+repo)
}

func (f *rerequestFixture) rerequest(heads map[string]string) error {
	return f.s.RerequestReviews(f.ctx, f.org, f.st, f.client, heads)
}

// asks is the review requests repo's GitHub received.
func (f *rerequestFixture) asks(repo string) []fakegithub.ReviewRequest {
	var out []fakegithub.ReviewRequest
	f.gh[repo].Set(func(s *fakegithub.Server) { out = slices.Clone(s.ReviewRequests) })
	return out
}

// events is the task's re-request events' payloads, oldest first.
func (f *rerequestFixture) events() []map[string]any {
	f.t.Helper()
	rows, err := f.owner.Query(f.ctx, `SELECT payload FROM events WHERE task_id = 'wi_q' AND event_type = $1 ORDER BY cursor`,
		EvPullRequestReviewRerequested)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			f.t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// eventFor is the one event for repo's head; fails unless there is exactly one.
func (f *rerequestFixture) eventFor(repo, head string) map[string]any {
	f.t.Helper()
	var found []map[string]any
	for _, e := range f.events() {
		if e["repo"] == repo && e["head"] == head {
			found = append(found, e)
		}
	}
	if len(found) != 1 || found[0]["number"] != float64(1) {
		f.t.Fatalf("events for %s@%s: %v", repo, head, found)
	}
	return found[0]
}

func names(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

// A step replayed after its re-request (a crash before the transition
// committed) asks nobody again for the same head, even one GitHub refused
// (jerv is still not requested then); a later head is asked about afresh.
func TestAReplayedRerequestAsksOncePerHead(t *testing.T) {
	f := newRerequestFixture(t, "target")
	head := f.fix("target")
	f.gh["target"].Set(func(s *fakegithub.Server) { s.ReviewRequestStatus = 403 })
	for range 2 {
		if err := f.rerequest(map[string]string{"target": head}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.asks("target")); n != 1 {
		t.Fatalf("%d review requests for one head, want 1", n)
	}

	f.gh["target"].Set(func(s *fakegithub.Server) { s.ReviewRequestStatus = 0 })
	next := f.gh["target"].CommitOnTop("feature", "dude's next fix")
	if err := f.rerequest(map[string]string{"target": next}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.asks("target")); n != 2 {
		t.Errorf("%d review requests after a second head, want 2", n)
	}
	if e := f.events(); len(e) != 2 {
		t.Errorf("%d re-request events, want one per head", len(e))
	}
}

// A person's commit on top of dude's fix makes the head theirs: nobody is
// asked about it and nothing is recorded, now or on a replay.
func TestAHeadSomeonePushedOnTopOfIsNotAskedAbout(t *testing.T) {
	f := newRerequestFixture(t, "target")
	head := f.fix("target")
	f.gh["target"].CommitOnTop("feature", "a person's commit on top")
	for range 2 {
		if err := f.rerequest(map[string]string{"target": head}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.asks("target"); len(got) != 0 {
		t.Errorf("asked %v", got)
	}
	if e := f.events(); len(e) != 0 {
		t.Errorf("recorded %v", e)
	}
}

// One pull request already done for its head does not end the others':
// two repositories, each with pull request #1. The first call has only
// a's head; the second has both, and asks b's reviewers, not a's again.
func TestARerequestDoneForOnePullRequestStillAsksTheNext(t *testing.T) {
	f := newRerequestFixture(t, "a", "b")
	a := f.fix("a")
	if err := f.rerequest(map[string]string{"a": a}); err != nil {
		t.Fatal(err)
	}
	b := f.fix("b")
	if a == b {
		t.Fatal("the two fixes share a commit")
	}
	if err := f.rerequest(map[string]string{"a": a, "b": b}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.asks("a")); n != 1 {
		t.Errorf("a asked %d times, want 1", n)
	}
	if got := f.asks("b"); len(got) != 1 || !slices.Equal(got[0].Logins, []string{"jerv"}) {
		t.Errorf("b asked %v, want jerv once", got)
	}
	if e := f.events(); len(e) != 2 {
		t.Errorf("events %v, want one per repository", e)
	}
	f.eventFor("a", a)
	if e := f.eventFor("b", b); !slices.Equal(names(e["reviewersRequested"]), []string{"jerv"}) {
		t.Errorf("b recorded %v", e)
	}
}

// Recording b's outcome fails after a's was recorded and after b's
// request reached GitHub. The step fails; replayed, it neither asks a
// again nor repeats b's request (GitHub holds it now), and records b's.
func TestARerequestWhoseRecordFailedIsFinishedOnReplay(t *testing.T) {
	f := newRerequestFixture(t, "a", "b")
	heads := map[string]string{"a": f.fix("a"), "b": f.fix("b")}
	for _, sql := range []string{`CREATE FUNCTION refuse_b() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.event_type = 'pull_request.review_rerequested' AND NEW.payload->>'repo' = 'b' THEN
				RAISE EXCEPTION 'refused for the test';
			END IF;
			RETURN NEW;
		END $$`,
		`CREATE TRIGGER refuse_b BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION refuse_b()`,
	} {
		if _, err := f.owner.Exec(f.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.rerequest(heads); err == nil || !strings.Contains(err.Error(), "refused for the test") {
		t.Fatalf("err %v, want the event write's failure", err)
	}
	if len(f.asks("a")) != 1 || len(f.asks("b")) != 1 || len(f.events()) != 1 {
		t.Fatalf("after the failure: a asked %v, b asked %v, events %v", f.asks("a"), f.asks("b"), f.events())
	}

	if _, err := f.owner.Exec(f.ctx, `DROP TRIGGER refuse_b ON events`); err != nil {
		t.Fatal(err)
	}
	if err := f.rerequest(heads); err != nil {
		t.Fatal(err)
	}
	if len(f.asks("a")) != 1 || len(f.asks("b")) != 1 {
		t.Errorf("replayed: a asked %v, b asked %v; want each once", f.asks("a"), f.asks("b"))
	}
	f.eventFor("a", heads["a"])
	if e := f.eventFor("b", heads["b"]); !slices.Equal(names(e["reviewersRequested"]), []string{"jerv"}) {
		t.Errorf("b recorded %v", e)
	}
}

// Two callers at once for the same head: the one that claims it asks
// GitHub; the other asks nothing and is told to come back, and so is a
// third that arrives while the holder's request is in flight. GitHub
// refuses (403), so jerv is never in requested_reviewers to hide a second
// request.
func TestConcurrentRerequestsAskOnce(t *testing.T) {
	f := newRerequestFixture(t, "target")
	head := f.fix("target")
	inPOST, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	letGo := func() { released.Do(func() { close(release) }) }
	t.Cleanup(letGo) // a failed test must not leave the request held
	var posts atomic.Int32
	f.gh["target"].Set(func(s *fakegithub.Server) {
		s.ReviewRequestStatus = 403
		s.Intercept = func(r *http.Request) int {
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/requested_reviewers") && posts.Add(1) == 1 {
				close(inPOST)
				<-release
			}
			return 0
		}
	})
	heads := map[string]string{"target": head}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; results <- f.rerequest(heads) }()
	}
	close(start)
	// The claim holder is held in its POST: the first result is the other's.
	select {
	case <-inPOST:
	case <-time.After(30 * time.Second):
		t.Fatal("no caller reached GitHub")
	}
	if err := <-results; !errors.Is(err, ErrRerequestBusy) {
		t.Fatalf("the second caller: %v, want ErrRerequestBusy", err)
	}
	if err := f.rerequest(heads); !errors.Is(err, ErrRerequestBusy) {
		t.Fatalf("a caller during the holder's request: %v, want ErrRerequestBusy", err)
	}
	letGo()
	if err := <-results; err != nil {
		t.Fatalf("the claim holder: %v", err)
	}
	if err := f.rerequest(heads); err != nil {
		t.Fatalf("the second caller come back: %v", err)
	}
	if n := len(f.asks("target")); n != 1 {
		t.Errorf("%d review requests, want 1", n)
	}
	if e := f.eventFor("target", head); !slices.Equal(names(e["reviewersRefused"]), []string{"jerv"}) {
		t.Errorf("recorded %v", e)
	}
}

// A holder that outlives its claim records nothing: its claim lapses
// while its request hangs, a second caller takes it over and records the
// outcome, and the first finds it taken when its request returns.
func TestALapsedRerequestClaimIsTheTakersToRecord(t *testing.T) {
	f := newRerequestFixture(t, "target")
	head := f.fix("target")
	inPOST, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	letGo := func() { released.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	var held atomic.Bool
	f.gh["target"].Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "POST" && held.CompareAndSwap(false, true) {
				// Only the first holder's request hangs.
				close(inPOST)
				<-release
				return http.StatusBadGateway
			}
			return 0
		}
	})
	heads := map[string]string{"target": head}
	first := make(chan error, 1)
	go func() { first <- f.rerequest(heads) }()
	<-inPOST
	f.exec(`UPDATE pull_request_rerequests SET claimed_at = now() - interval '1 hour' WHERE organization_id = $1`)
	if err := f.rerequest(heads); err != nil {
		t.Fatalf("the taker: %v", err)
	}
	letGo()
	if err := <-first; !errors.Is(err, ErrRerequestBusy) {
		t.Fatalf("the lapsed holder: %v, want ErrRerequestBusy", err)
	}
	if e := f.eventFor("target", head); !slices.Equal(names(e["reviewersRequested"]), []string{"jerv"}) {
		t.Errorf("recorded %v, want the taker's request of jerv", e)
	}
	if p := f.gh["target"].Pull(1); !slices.Equal(p.Requested, []string{"jerv"}) {
		t.Errorf("requested %v", p.Requested)
	}
}

// A holder that crashed after its POST and before recording left only who
// it was asking. The next caller, once the claim lapses, records GitHub's
// answer to that request without asking again.
func TestACrashedRerequestIsRecordedNotRepeated(t *testing.T) {
	f := newRerequestFixture(t, "target")
	head := f.fix("target")
	f.gh["target"].RequestReview(1, "jerv") // the crashed holder's request, which GitHub took
	f.exec(`INSERT INTO pull_request_rerequests (organization_id, task_id, repo, number, head, token, claimed_at, asking)
		VALUES ($1, 'wi_q', 'target', 1, $2, 'rrc_dead', now() - interval '1 hour', '{jerv}')`, head)
	if err := f.rerequest(map[string]string{"target": head}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.asks("target")); n != 0 {
		t.Errorf("%d review requests, want none", n)
	}
	if e := f.eventFor("target", head); !slices.Equal(names(e["reviewersRequested"]), []string{"jerv"}) {
		t.Errorf("recorded %v", e)
	}
}
