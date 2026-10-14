package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
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
	// Not the host's CPU count: the re-request slots are sized from it.
	f.usePool(16)
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

// usePool gives the store a fresh app-role pool of maxConns connections.
func (f *rerequestFixture) usePool(maxConns int32) {
	f.t.Helper()
	cfg := f.s.DB.Pool.Config()
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(pool.Close)
	f.s = &Store{DB: &db.DB{Pool: pool}}
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

// hold makes repo's GitHub hang the nth review request (1-based) until
// letGo; entered closes once it hangs. Other requests are answered.
func (f *rerequestFixture) hold(repo string, nth int32) (entered chan struct{}, letGo func()) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	letGo = func() { once.Do(func() { close(release) }) }
	f.t.Cleanup(letGo) // a failed test must not leave the request held
	var posts atomic.Int32
	f.gh[repo].Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/requested_reviewers") && posts.Add(1) == nth {
				close(entered)
				<-release
			}
			return 0
		}
	})
	return entered, letGo
}

func wait(t *testing.T, c chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(30 * time.Second):
		t.Fatal("no caller reached GitHub")
	}
}

// ageClaims makes every claim an hour old: no age lets a caller overtake a
// live holder.
func (f *rerequestFixture) ageClaims() {
	f.exec(`UPDATE pull_request_rerequests SET claimed_at = now() - interval '1 hour' WHERE organization_id = $1`)
}

// A holder whose request is slow is not overtaken, however old its claim:
// a caller meanwhile asks nobody and comes back. The holder's request
// lands, jerv approves the head, and the caller come back asks nobody
// again: jerv is not put back on requested_reviewers after approving.
func TestASlowRerequestHolderIsNotOvertaken(t *testing.T) {
	f := newRerequestFixture(t, "target")
	head := f.fix("target")
	entered, letGo := f.hold("target", 1)
	heads := map[string]string{"target": head}
	first := make(chan error, 1)
	go func() { first <- f.rerequest(heads) }()
	wait(t, entered)
	f.ageClaims()
	if err := f.rerequest(heads); !errors.Is(err, ErrRerequestBusy) {
		t.Fatalf("a caller during the holder's request: %v, want ErrRerequestBusy", err)
	}
	// The fake records a request once it answers: the held one is not yet.
	if got := f.asks("target"); len(got) != 0 {
		t.Fatalf("review requests %v while the holder's hangs, want none", got)
	}
	letGo()
	if err := <-first; err != nil {
		t.Fatalf("the holder: %v", err)
	}
	f.gh["target"].Review(1, "jerv", "APPROVED")
	if err := f.rerequest(heads); err != nil {
		t.Fatalf("the caller come back: %v", err)
	}
	if n := len(f.asks("target")); n != 1 {
		t.Errorf("%d review requests, want 1", n)
	}
	if p := f.gh["target"].Pull(1); len(p.Requested) != 0 {
		t.Errorf("requested %v after jerv approved the head", p.Requested)
	}
	if e := f.eventFor("target", head); !slices.Equal(names(e["reviewersRequested"]), []string{"jerv"}) {
		t.Errorf("recorded %v", e)
	}
}

// After GitHub refuses the batch (ana is no collaborator: 422), the
// holder asks each alone; a caller arriving while it is between those
// requests asks nobody. 403-free, so the count is every POST sent.
func TestARerequestHolderBetweenItsOneByOneRequestsIsNotOvertaken(t *testing.T) {
	f := newRerequestFixture(t, "target")
	f.gh["target"].Review(1, "ana", "COMMENTED")
	f.gh["target"].Set(func(s *fakegithub.Server) { s.Permissions["ana"] = "none" })
	head := f.fix("target")
	entered, letGo := f.hold("target", 2) // the first one-by-one request
	heads := map[string]string{"target": head}
	first := make(chan error, 1)
	go func() { first <- f.rerequest(heads) }()
	wait(t, entered)
	f.ageClaims()
	if err := f.rerequest(heads); !errors.Is(err, ErrRerequestBusy) {
		t.Fatalf("a caller between the holder's requests: %v, want ErrRerequestBusy", err)
	}
	letGo()
	if err := <-first; err != nil {
		t.Fatalf("the holder: %v", err)
	}
	if err := f.rerequest(heads); err != nil {
		t.Fatalf("the caller come back: %v", err)
	}
	if n := len(f.asks("target")); n != 3 {
		t.Errorf("review requests %v, want the batch and one each", f.asks("target"))
	}
	e := f.eventFor("target", head)
	if !slices.Equal(names(e["reviewersRequested"]), []string{"jerv"}) || !slices.Equal(names(e["reviewersRefused"]), []string{"ana"}) {
		t.Errorf("recorded %v", e)
	}
}

// killHolder ends the database session that holds a re-request's lock, as
// the holder's process dying or losing Postgres does, and waits until
// Postgres has (and with it, freed the lock).
func (f *rerequestFixture) killHolder() {
	f.t.Helper()
	var n int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE pg_terminate_backend(pid, 10000)) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND pid <> pg_backend_pid()
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	if n != 1 {
		f.t.Fatalf("%d lock-holding sessions ended, want 1", n)
	}
}

// A holder whose session ended mid-way (here, while GitHub refuses its
// batch) loses the head at once: the next caller takes it over without
// waiting, and the old holder, its 422 back, sends none of its one-by-one
// requests and records nothing.
func TestARerequestHolderWhoseSessionEndedStopsAndIsTakenOverAtOnce(t *testing.T) {
	f := newRerequestFixture(t, "target")
	f.gh["target"].Review(1, "ana", "COMMENTED")
	f.gh["target"].Set(func(s *fakegithub.Server) { s.Permissions["ana"] = "none" })
	head := f.fix("target")
	entered, letGo := f.hold("target", 1) // the holder's batch
	heads := map[string]string{"target": head}
	first := make(chan error, 1)
	go func() { first <- f.rerequest(heads) }()
	wait(t, entered)
	f.killHolder()
	if err := f.rerequest(heads); err != nil {
		t.Fatalf("the taker: %v", err)
	}
	if n := len(f.asks("target")); n != 3 {
		t.Fatalf("review requests %v, want the taker's batch and its two", f.asks("target"))
	}
	letGo()
	if err := <-first; !errors.Is(err, ErrRerequestBusy) {
		t.Fatalf("the holder: %v, want ErrRerequestBusy", err)
	}
	// The holder's batch, answered now, and nothing after it.
	if n := len(f.asks("target")); n != 4 {
		t.Errorf("review requests %v: the holder went on asking after its session ended", f.asks("target"))
	}
	e := f.eventFor("target", head)
	if !slices.Equal(names(e["reviewersRequested"]), []string{"jerv"}) || !slices.Equal(names(e["reviewersRefused"]), []string{"ana"}) {
		t.Errorf("recorded %v", e)
	}
}

// A holder that ended at each point leaves a row a later caller finishes.
// Its logins GitHub now holds are recorded as requested without asking;
// one GitHub still does not hold (not sent, or refused) is asked once more.
func TestAnUnfinishedRerequestIsFinishedByTheNextCaller(t *testing.T) {
	for _, tc := range []struct {
		name      string
		asking    string // the row's asking, as SQL
		requested bool   // GitHub holds jerv's request
		refuse    int    // GitHub's status for review requests
		asks      int
		recorded  string
	}{
		{name: "before writing whom it asks", asking: "NULL", asks: 1, recorded: "reviewersRequested"},
		{name: "after writing whom, before asking", asking: "'{jerv}'", asks: 1, recorded: "reviewersRequested"},
		{name: "after GitHub refused", asking: "'{jerv}'", refuse: 403, asks: 1, recorded: "reviewersRefused"},
		{name: "after GitHub took it", asking: "'{jerv}'", requested: true, asks: 0, recorded: "reviewersRequested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRerequestFixture(t, "target")
			head := f.fix("target")
			if tc.requested {
				f.gh["target"].RequestReview(1, "jerv")
			}
			f.gh["target"].Set(func(s *fakegithub.Server) { s.ReviewRequestStatus = tc.refuse })
			// Just claimed: an unfinished row needs no age to be taken over.
			f.exec(`INSERT INTO pull_request_rerequests (organization_id, task_id, repo, number, head, token, asking)
				VALUES ($1, 'wi_q', 'target', 1, $2, 'rrc_dead', `+tc.asking+`)`, head)
			for range 2 {
				if err := f.rerequest(map[string]string{"target": head}); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.asks("target"); len(got) != tc.asks {
				t.Errorf("review requests %v, want %d", got, tc.asks)
			}
			if e := f.eventFor("target", head); !slices.Equal(names(e[tc.recorded]), []string{"jerv"}) {
				t.Errorf("recorded %v, want jerv in %s", e, tc.recorded)
			}
		})
	}
}

// Not knowing who reviewed is recorded once, as an error, and asks
// nobody; the step goes on, and a replay for that head does nothing more.
func TestARerequestThatCannotReadReviewsRecordsWhy(t *testing.T) {
	for _, path := range []string{"/pulls/1", "/pulls/1/reviews"} {
		t.Run(path, func(t *testing.T) {
			f := newRerequestFixture(t, "target")
			head := f.fix("target")
			f.gh["target"].Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if r.Method == "GET" && strings.HasSuffix(r.URL.Path, path) {
						return http.StatusInternalServerError
					}
					return 0
				}
			})
			for range 2 {
				if err := f.rerequest(map[string]string{"target": head}); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.asks("target"); len(got) != 0 {
				t.Errorf("asked %v", got)
			}
			want := map[string]any{"number": float64(1), "repo": "target", "head": head, "error": "github 500: Internal Server Error"}
			if e := f.eventFor("target", head); !reflect.DeepEqual(e, want) {
				t.Errorf("recorded %#v, want only the read's error %#v", e, want)
			}
		})
	}
}

// A closed or merged pull request is not asked about.
func TestAClosedPullRequestIsNotAskedAbout(t *testing.T) {
	for _, state := range []string{"closed", "merged"} {
		t.Run(state, func(t *testing.T) {
			f := newRerequestFixture(t, "target")
			head := f.fix("target")
			f.exec(`UPDATE pull_requests SET state = $2 WHERE organization_id = $1`, state)
			if err := f.rerequest(map[string]string{"target": head}); err != nil {
				t.Fatal(err)
			}
			if got := f.asks("target"); len(got) != 0 {
				t.Errorf("asked %v", got)
			}
			if e := f.events(); len(e) != 0 {
				t.Errorf("recorded %v", e)
			}
		})
	}
}

// One pull request held by another caller does not hold up the next: a
// caller asks b's reviewers while a's holder hangs, and is told to come
// back; nobody is asked twice.
func TestABusyPullRequestDoesNotHoldUpTheNext(t *testing.T) {
	f := newRerequestFixture(t, "a", "b")
	heads := map[string]string{"a": f.fix("a"), "b": f.fix("b")}
	entered, letGo := f.hold("a", 1)
	first := make(chan error, 1)
	go func() { first <- f.rerequest(heads) }()
	wait(t, entered)
	if err := f.rerequest(heads); !errors.Is(err, ErrRerequestBusy) {
		t.Fatalf("the second caller: %v, want ErrRerequestBusy", err)
	}
	if n := len(f.asks("b")); n != 1 {
		t.Fatalf("b asked %d times while a was held, want 1", n)
	}
	letGo()
	if err := <-first; err != nil {
		t.Fatalf("the holder: %v", err)
	}
	if err := f.rerequest(heads); err != nil {
		t.Fatal(err)
	}
	if len(f.asks("a")) != 1 || len(f.asks("b")) != 1 {
		t.Errorf("a asked %v, b asked %v; want each once", f.asks("a"), f.asks("b"))
	}
	f.eventFor("a", heads["a"])
	f.eventFor("b", heads["b"])
}

// Re-requests hold a pool connection while GitHub answers; with the
// smallest pools the orchestrator starts with (2 to 4 connections), one
// holder is admitted, so a slow GitHub cannot take every connection.
// Callers past the bound, and the held head's own caller, are busy at once
// without reaching GitHub, and unrelated work still gets a connection
// within a short deadline.
func TestHeldRerequestsLeaveThePoolToOtherWork(t *testing.T) {
	for _, maxConns := range []int32{2, 3, 4} {
		t.Run(fmt.Sprint(maxConns), func(t *testing.T) { heldRerequestsLeaveThePool(t, maxConns) })
	}
}

func heldRerequestsLeaveThePool(t *testing.T, maxConns int32) {
	repos := []string{"a", "b", "c", "d"}
	f := newRerequestFixture(t, repos...)
	f.usePool(maxConns)
	holders := rerequestSlotCount(maxConns)
	if holders >= int(maxConns) {
		t.Fatalf("%d slots for a pool of %d leave no connection free", holders, maxConns)
	}
	heads := map[string]string{}
	entered := map[string]chan struct{}{}
	var letGo []func()
	for _, r := range repos {
		heads[r] = f.fix(r)
		e, g := f.hold(r, 1)
		entered[r], letGo = e, append(letGo, g)
	}
	held := make(chan error, holders)
	for _, r := range repos[:holders] {
		go func() { held <- f.rerequest(map[string]string{r: heads[r]}) }()
		wait(t, entered[r])
	}
	// Without the bound each of these would take a connection and hang in
	// its own held POST; with it each returns at once.
	for _, r := range append(slices.Clone(repos[holders:]), repos[0]) {
		got := make(chan error, 1)
		go func() { got <- f.rerequest(map[string]string{r: heads[r]}) }()
		select {
		case err := <-got:
			if !errors.Is(err, ErrRerequestBusy) {
				t.Fatalf("%s while the slots are held: %v, want ErrRerequestBusy", r, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waited for the held re-requests instead of being busy", r)
		}
	}
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	var prs int
	err := f.s.DB.InOrg(ctx, f.org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM pull_requests`).Scan(&prs)
	})
	if err != nil || prs != len(repos) {
		t.Fatalf("unrelated work while re-requests are held: %d pull requests, %v", prs, err)
	}
	for _, r := range repos[holders:] {
		select {
		case <-entered[r]:
			t.Fatalf("%s reached GitHub past the bound", r)
		default:
		}
	}
	for _, g := range letGo {
		g()
	}
	for range holders {
		if err := <-held; err != nil {
			t.Fatalf("a holder: %v", err)
		}
	}
	for _, r := range repos {
		if err := f.rerequest(map[string]string{r: heads[r]}); err != nil {
			t.Fatalf("%s come back: %v", r, err)
		}
		if n := len(f.asks(r)); n != 1 {
			t.Errorf("%s asked %d times, want 1", r, n)
		}
		f.eventFor(r, heads[r])
	}
}

// A pool too small to spare a quarter still admits one re-request, so
// delivery is never refused forever; larger pools keep three quarters free.
// A pool of 1 is refused at orchestrator startup.
func TestRerequestSlotsLeaveMostOfThePool(t *testing.T) {
	for maxConns, want := range map[int32]int{2: 1, 3: 1, 4: 1, 8: 2, 16: 4, 32: 8, 100: 8} {
		if got := rerequestSlotCount(maxConns); got != want {
			t.Errorf("pool of %d: %d slots, want %d", maxConns, got, want)
		}
	}
}

// advisoryLocks is how many advisory locks the test database's sessions hold.
func (f *rerequestFixture) advisoryLocks() int {
	f.t.Helper()
	var n int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// However a holder ends after taking the lock, its slot and lock are
// freed: a pool of four admits one holder, and the next re-request of the
// same head, on whichever session, runs. A connection whose lock could not
// be released normally (its session ended, or it no longer held the lock)
// is closed, not pooled.
func TestARerequestHolderFreesItsSlotAndLockHoweverItEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		end    func(f *rerequestFixture, c *rerequestClaim, cancel func(), unlock func())
		closed bool
	}{
		{name: "context canceled", end: func(_ *rerequestFixture, _ *rerequestClaim, cancel, unlock func()) {
			cancel()
			unlock()
		}},
		{name: "panic", end: func(_ *rerequestFixture, _ *rerequestClaim, _, unlock func()) {
			defer func() { _ = recover() }()
			defer unlock()
			panic("in the holder")
		}},
		{name: "session ended", closed: true, end: func(f *rerequestFixture, _ *rerequestClaim, _, unlock func()) {
			f.killHolder()
			unlock()
		}},
		{name: "lock gone from its session", closed: true, end: func(f *rerequestFixture, c *rerequestClaim, _, unlock func()) {
			if _, err := c.conn.Exec(f.ctx, `SELECT pg_advisory_unlock_all()`); err != nil {
				f.t.Fatal(err)
			}
			unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRerequestFixture(t, "target")
			f.usePool(4)
			head := f.fix("target")
			c := &rerequestClaim{s: f.s, org: f.org, taskID: f.st.TaskID, repo: "target", number: 1, head: head}
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			unlock, err := c.lock(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.rerequest(map[string]string{"target": head}); !errors.Is(err, ErrRerequestBusy) {
				t.Fatalf("while held: %v, want ErrRerequestBusy", err)
			}
			total := f.s.DB.Pool.Stat().TotalConns()
			tc.end(f, c, cancel, unlock)
			stat := f.s.DB.Pool.Stat()
			if n := f.advisoryLocks(); n != 0 || stat.AcquiredConns() != 0 {
				t.Fatalf("after the holder ended: %d advisory locks, %d connections out", n, stat.AcquiredConns())
			}
			if tc.closed && stat.TotalConns() != total-1 {
				t.Errorf("pool has %d connections, had %d: the holder's went back to it", stat.TotalConns(), total)
			}
			if err := f.rerequest(map[string]string{"target": head}); err != nil {
				t.Fatalf("the next caller: %v", err)
			}
			if n := len(f.asks("target")); n != 1 {
				t.Errorf("%d review requests, want 1", n)
			}
			f.eventFor("target", head)
		})
	}
}
