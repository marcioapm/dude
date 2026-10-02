package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

func TestPushPreflightUsesEnterpriseOriginAndGitAuthentication(t *testing.T) {
	fake := fakegithub.New("", "acme/repo")
	fake.ReceiveToken = "same-token"
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	for _, suffix := range []string{"", "/api/v3/"} {
		g := NewGitHub(Credential{Secret: "same-token", APIBaseURL: srv.URL + suffix})
		for _, repository := range []string{srv.URL + "/acme/repo.git", "ssh://git@" + strings.TrimPrefix(srv.URL, "http://") + "/acme/repo.git"} {
			if err := g.CheckPushAccess(context.Background(), repository); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(fake.ReceiveRequests) != 4 {
		t.Fatalf("requests: %v", fake.ReceiveRequests)
	}
}

func TestPushPreflightTestGatewayRequiresExactOptIn(t *testing.T) {
	fake := fakegithub.New("", "acme/repo")
	fake.ReceiveToken = "secret"
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	local, _ := url.Parse(srv.URL)
	for _, tc := range []struct {
		name, configured, apiScheme, apiHost, gitHost, token string
		allowed                                              bool
	}{
		{"trusted", "gateway.test", "http", "gateway.test", "gateway.test", "secret", true},
		{"unset", "", "http", "gateway.test", "gateway.test", "secret", false},
		{"wrong opt-in", "other.test", "http", "gateway.test", "gateway.test", "secret", false},
		{"wrong git host", "gateway.test", "http", "gateway.test", "other.test", "secret", false},
		{"wrong API host", "gateway.test", "http", "other.test", "gateway.test", "secret", false},
		{"HTTPS API", "gateway.test", "https", "gateway.test", "gateway.test", "secret", false},
		{"wrong token", "gateway.test", "http", "gateway.test", "gateway.test", "wrong", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			g := NewGitHub(Credential{Secret: tc.token, APIBaseURL: tc.apiScheme + "://" + tc.apiHost + ":43210"}, WithTestGitHost(tc.configured))
			g.http.Transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				calls++
				return (&net.Dialer{}).DialContext(ctx, network, local.Host)
			}}
			defer g.http.CloseIdleConnections()
			err := g.CheckPushAccess(context.Background(), "git://"+tc.gitHost+":43211/acme/repo.git")
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
			wantCalls := 0
			if tc.allowed || tc.name == "wrong token" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("HTTP connections=%d, want %d", calls, wantCalls)
			}
		})
	}
	if len(fake.ReceiveRequests) != 2 {
		t.Fatalf("discovery requests: %v", fake.ReceiveRequests)
	}
}

func TestPushPreflightNormalizesTrailingSlash(t *testing.T) {
	for _, path := range []string{"/owner/repo/", "/owner/repo.git/"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/owner/repo.git/info/refs" || r.URL.RawQuery != "service=git-receive-pack" {
					t.Errorf("request URL = %s, want /owner/repo.git/info/refs?service=git-receive-pack", r.URL)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
			}))
			defer srv.Close()
			g := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL})
			if err := g.CheckPushAccess(context.Background(), srv.URL+path); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("sent %d requests, want 1", calls)
			}
		})
	}
}

func TestPushPreflightNeverContactsAnUntrustedOrigin(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer srv.Close()
	for _, base := range []string{"", "https://ghe.example/api/v3", srv.URL + "/unexpected", "ftp://ghe.example", "https://user:password@ghe.example"} {
		g := NewGitHub(Credential{Secret: "secret", APIBaseURL: base})
		if err := g.CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git"); err == nil {
			t.Fatalf("accepted untrusted origin with base %q", base)
		}
	}
	for _, path := range []string{"/acme/repo.git?x=1", "/acme/repo.git#fragment", "/acme/../repo.git", "/acme/%2fother.git",
		"/acme/repo//", "/acme/repo.git//", "/acme/repo/extra/", "//acme/repo/", "/acme/./", "/acme/../"} {
		if err := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL}).CheckPushAccess(context.Background(), srv.URL+path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if calls != 0 {
		t.Fatalf("sent %d untrusted requests", calls)
	}
}

func TestPushPreflightDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
			defer destination.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, status) }))
			defer srv.Close()
			err := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL}).CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git")
			if err == nil || Transient(err) || calls != 0 {
				t.Fatalf("redirect followed: calls %d, error %v", calls, err)
			}
		})
	}
}

func TestPushPreflightRejectsAnHTMLGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
	}))
	defer srv.Close()
	err := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL}).CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git")
	if err == nil || !Transient(err) || !strings.Contains(err.Error(), "no Git advertisement") {
		t.Fatalf("gateway response: %v", err)
	}
}

func TestPushPreflightTimeoutIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	g := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL})
	g.http.Timeout = 20 * time.Millisecond
	err := g.CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git")
	if !Transient(err) || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestPushPreflightSuccessBodyConnectionReuse(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size        int
		connections int32
	}{
		{"advertisement 120KiB", 120 << 10, 1},
		{"exact 1MiB cap", 1 << 20, 1},
		{"over cap 2MiB", 2 << 20, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body strings.Builder
			packet := func(line string) { fmt.Fprintf(&body, "%04x%s", len(line)+4, line) }
			packet("# service=git-receive-pack\n")
			body.WriteString("0000")
			packet(strings.Repeat("a", 40) + " refs/heads/main\x00report-status delete-refs\n")
			for i := 0; body.Len() < tc.size-4; i++ {
				remaining := tc.size - 4 - body.Len()
				n := min(1024, remaining)
				if remaining > n && remaining-n < 128 {
					n -= 128
				}
				prefix := strings.Repeat("a", 40) + fmt.Sprintf(" refs/heads/branch-%d-", i)
				packet(prefix + strings.Repeat("x", n-4-len(prefix)-1) + "\n")
			}
			body.WriteString("0000")
			if body.Len() != tc.size {
				t.Fatalf("fixture length = %d, want %d", body.Len(), tc.size)
			}

			var connections atomic.Int32
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 1 {
					t.Errorf("protocol = %s, want HTTP/1.1", r.Proto)
				}
				w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
				w.Header().Set("Content-Length", fmt.Sprint(body.Len()))
				_, _ = io.WriteString(w, body.String())
			}))
			srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			srv.StartTLS()
			defer srv.Close()
			g := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL})
			g.http = srv.Client()
			g.http.Timeout = requestTimeout
			defer g.http.CloseIdleConnections()
			for i := 0; i < 3; i++ {
				if err := g.CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git"); err != nil {
					t.Fatalf("probe %d: %v", i, err)
				}
			}
			if got := connections.Load(); got != tc.connections {
				t.Fatalf("connections = %d, want %d for 3 probes", got, tc.connections)
			}
		})
	}
}

func TestPushPreflightSuccessBodyFailuresAreTransient(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
				w.Header().Set("Content-Length", "1024")
				_, _ = io.WriteString(w, "001e# service=git-receive-pack\n")
				w.(http.Flusher).Flush()
				if timeout {
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			g := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL})
			g.http = srv.Client()
			g.http.Timeout = requestTimeout
			if timeout {
				g.http.Timeout = 100 * time.Millisecond
			}
			defer g.http.CloseIdleConnections()
			err := g.CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git")
			var unreachable *Unreachable
			if !errors.As(err, &unreachable) || !Transient(err) {
				t.Fatalf("body failure = %v, want transient Unreachable", err)
			}
			if timeout {
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Fatalf("body timeout = %v, want timeout error", err)
				}
			} else if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated body = %v, want unexpected EOF", err)
			}
		})
	}
}

func TestPushPreflightClassifiesHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		headers   http.Header
		message   string
		transient bool
	}{
		{name: "JSON primary limit", status: 403, body: `{"message":"API rate limit exceeded for user ID 1."}`, message: "API rate limit exceeded for user ID 1.", transient: true},
		{name: "plaintext primary limit", status: 403, body: "API rate limit exceeded for user ID 1.", message: "API rate limit exceeded for user ID 1.", transient: true},
		{name: "JSON secondary limit", status: 403, body: `{"message":"You have exceeded a secondary rate limit."}`, message: "You have exceeded a secondary rate limit.", transient: true},
		{name: "plaintext secondary limit", status: 403, body: "You have exceeded a secondary rate limit.", message: "You have exceeded a secondary rate limit.", transient: true},
		{name: "Retry-After only", status: 403, body: "Forbidden", headers: http.Header{"Retry-After": {"60"}}, message: "Forbidden", transient: true},
		{name: "remaining zero only", status: 403, body: "Forbidden", headers: http.Header{"X-Ratelimit-Remaining": {"0"}}, message: "Forbidden", transient: true},
		{name: "429 retained", status: 429, body: "Too many requests", message: "Too many requests", transient: true},
		{name: "503 retained", status: 503, body: `{"message":"Service unavailable"}`, message: "Service unavailable", transient: true},
		{name: "401 authentication denial", status: 401, body: `{"message":"Bad credentials"}`},
		{name: "403 permission denial", status: 403, body: `{"message":"Resource not accessible by personal access token"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := fakegithub.New("", "acme/repo")
			fake.ReceiveStatus, fake.ReceiveBody, fake.ReceiveHeaders = tc.status, tc.body, tc.headers
			srv := httptest.NewServer(fake.Handler())
			defer srv.Close()

			err := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL}).CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git")
			var refusal *Error
			if !errors.As(err, &refusal) {
				t.Fatalf("error = %v, want a forge Error", err)
			}
			if refusal.Status != tc.status {
				t.Errorf("status = %d, want %d", refusal.Status, tc.status)
			}
			if got := Transient(err); got != tc.transient {
				t.Errorf("Transient(%v) = %v, want %v", err, got, tc.transient)
			}
			if tc.transient {
				if !strings.Contains(refusal.Message, tc.message) {
					t.Errorf("message = %q, want retained diagnostic %q", refusal.Message, tc.message)
				}
				if strings.Contains(refusal.Message, "Contents:") {
					t.Errorf("transient failure received Contents guidance: %v", err)
				}
			} else if !strings.Contains(refusal.Message, "Contents: Read and write") {
				t.Errorf("denial lacks Contents guidance: %v", err)
			}
		})
	}
}

func TestPushPreflightBoundsHTTPErrorBody(t *testing.T) {
	const limit = 64 << 10
	prefix := strings.Repeat("x", limit)
	fake := fakegithub.New("", "acme/repo")
	fake.ReceiveStatus = http.StatusServiceUnavailable
	fake.ReceiveBody = prefix + strings.Repeat("y", 1<<20) + "end-of-response"
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()

	err := NewGitHub(Credential{Secret: "secret", APIBaseURL: srv.URL}).CheckPushAccess(context.Background(), srv.URL+"/acme/repo.git")
	var refusal *Error
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a forge Error", err)
	}
	if refusal.Status != http.StatusServiceUnavailable || !Transient(err) {
		t.Errorf("status = %d, transient = %v, want 503 and transient", refusal.Status, Transient(err))
	}
	if len(refusal.Message) != limit {
		t.Errorf("error body length = %d, want %d", len(refusal.Message), limit)
	}
	if refusal.Message != prefix {
		t.Error("error body did not retain exactly the bounded response prefix")
	}
}

// Every false positive here costs a fix Run; every false negative is a
// person's request silently ignored. The tests pin both directions.

var open = Status{PullRequestRef: PullRequestRef{Number: 1, State: StateOpen, HeadSHA: "abc"}, Checks: ChecksPassing, Review: "pending"}
var prior = Status{PullRequestRef: PullRequestRef{State: StateOpen}, Checks: ChecksPassing}

func comment(body string) Feedback {
	return Feedback{ID: body, Author: "alice", Body: body, Kind: KindComment, CreatedAt: "2026-09-22T10:00:00Z"}
}

func TestAPersonAskingForAChangeIsActionable(t *testing.T) {
	if s := Classify(prior, open, []Feedback{comment("Please rename `foo` to `bar`.")}, nil); s == nil || s.Kind != "actionable" {
		t.Fatalf("signal = %+v", s)
	}
}

func TestAChangesRequestedReviewIsARequestHoweverBrief(t *testing.T) {
	f := comment("ok")
	f.Kind = KindChangesRequested
	if s := Classify(prior, open, []Feedback{f}, nil); s == nil || s.Kind != "actionable" {
		t.Fatalf("signal = %+v", s)
	}
}

func TestChecksTurningRedWakeAFixer(t *testing.T) {
	failing := open
	failing.Checks = ChecksFailing
	s := Classify(prior, failing, nil, nil)
	if s == nil || s.Feedback[0].Source != "checks" {
		t.Fatalf("signal = %+v", s)
	}
}

func TestChecksThatWereAlreadyRedDoNot(t *testing.T) {
	failing := open
	failing.Checks = ChecksFailing
	if s := Classify(Status{PullRequestRef: PullRequestRef{State: StateOpen}, Checks: ChecksFailing}, failing, nil, nil); s != nil {
		t.Fatalf("signal = %+v, want nil", s)
	}
}

func TestALineCommentKeepsItsFile(t *testing.T) {
	f := comment("This leaks the token")
	f.Kind, f.Path = KindLineComment, "src/push.go"
	if s := Classify(prior, open, []Feedback{f}, nil); s == nil || s.Feedback[0].Path != "src/push.go" {
		t.Fatalf("signal = %+v", s)
	}
}

// An approval or checks going green wake no fixer: they may make the work
// item ready to merge, and that is all the workflow is told.
func TestApprovalsAndGreenChecksWakeNoFixer(t *testing.T) {
	approved := open
	approved.Review = ReviewApproved
	if s := Classify(prior, approved, nil, nil); s == nil || s.Kind != "readiness" || len(s.Feedback) > 0 {
		t.Errorf("approval: %+v", s)
	}
	// Green, but not approved: nothing is ready, nothing to say.
	if s := Classify(Status{PullRequestRef: PullRequestRef{State: StateOpen}, Checks: ChecksFailing}, open, nil, nil); s != nil {
		t.Errorf("green: %+v", s)
	}
	// Approved already, checks going green: now it is ready.
	approved.Checks = ChecksPassing
	if s := Classify(Status{PullRequestRef: PullRequestRef{State: StateOpen}, Checks: ChecksPending, Review: ReviewApproved}, approved, nil, nil); s == nil || s.Kind != "readiness" {
		t.Errorf("approved then green: %+v", s)
	}
}

func TestCourtesyIsNotARequest(t *testing.T) {
	// "LGTM so far, thanks!" once slipped through an anchored pattern and woke
	// a fixer for nothing, in the end-to-end test.
	for _, body := range []string{"LGTM", "lgtm!", "Thanks", "Looks good to me.", "👍", "Ship it",
		"LGTM so far, thanks!", "Great work, thank you 🎉", "looks good for now", "❤️"} {
		if IsActionableComment(comment(body), nil) {
			t.Errorf("%q was treated as a request", body)
		}
	}
}

func TestARequestAmongCourtesiesIsStillARequest(t *testing.T) {
	for _, body := range []string{"LGTM, but please rename foo", "Looks good. Why is this async?",
		"thanks — can you add a test", "Great work, fix the typo in the README", "Thanks! Could you also add a test?"} {
		if !IsActionableComment(comment(body), nil) {
			t.Errorf("%q was filtered out", body)
		}
	}
}

func TestBotsAndTheFactoryItselfAreNotFeedback(t *testing.T) {
	for _, author := range []string{"dependabot[bot]", "codecov-bot", "github-actions"} {
		f := comment("Coverage dropped 2%")
		f.Author = author
		if IsActionableComment(f, nil) {
			t.Errorf("bot %s was treated as a reviewer", author)
		}
	}
	f := comment("Addressed in abc123")
	f.Author = "dude-factory"
	if IsActionableComment(f, []string{"dude-factory"}) {
		t.Error("the factory's own comment woke a fixer")
	}
}

func TestMergeAndCloseEndTheLoopOnce(t *testing.T) {
	for _, state := range []string{StateMerged, StateClosed} {
		cur := open
		cur.State = state
		s := Classify(prior, cur, []Feedback{comment("Please change this")}, nil)
		if s == nil || s.Kind != "terminal" || s.State != state {
			t.Errorf("%s: %+v", state, s)
		}
		if s := Classify(Status{PullRequestRef: PullRequestRef{State: state}}, cur, nil, nil); s != nil {
			t.Errorf("%s seen twice reported again: %+v", state, s)
		}
	}
}

func TestSlugFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/api.git":      "acme/api",
		"git@github.com:acme/api.git":          "acme/api",
		"git://127.0.0.1:9418/acme/target.git": "acme/target",
		"/tmp/remote.git":                      "",
	} {
		if got := SlugFromURL(in); got != want {
			t.Errorf("SlugFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A blip must be retried; a refusal must not be retried into the same wall.
func TestTransientErrors(t *testing.T) {
	for err, want := range map[error]bool{
		&Error{Status: 502, Message: "Bad Gateway"}:                                     true,
		&Error{Status: 429, Message: "slow down"}:                                       true,
		&Error{Status: 403, Message: "API rate limit exceeded"}:                         true,
		&Error{Status: 403, Message: "You have triggered an abuse detection mechanism"}: true,
		fmt.Errorf("compare: %w", &Unreachable{errors.New("connection refused")}):       true,
		&Error{Status: 422, Message: "Update is not a fast forward"}:                    false,
		&Error{Status: 403, Message: "Resource not accessible by integration"}:          false,
		errors.New("lux reported no push result"):                                       false,
	} {
		if got := Transient(err); got != want {
			t.Errorf("Transient(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestAlreadyExistsIsOnlyTheDuplicatePR(t *testing.T) {
	if !(&Error{Status: 422, Message: "A pull request already exists for acme:branch."}).AlreadyExists() {
		t.Error("a duplicate PR was not recognised")
	}
	if (&Error{Status: 422, Message: "No commits between main and branch"}).AlreadyExists() {
		t.Error("another validation error was taken for a duplicate PR")
	}
}

func TestCheckRunsCountAndTheWorstWins(t *testing.T) {
	for _, c := range []struct{ status, conclusion, want string }{
		{"queued", "", ChecksPending}, {"in_progress", "", ChecksPending},
		{"completed", "success", ChecksPassing}, {"completed", "skipped", ChecksPassing},
		{"completed", "neutral", ChecksPassing}, {"completed", "failure", ChecksFailing},
		{"completed", "cancelled", ChecksPending}, {"completed", "action_required", ChecksPending},
		{"completed", "stale", ChecksPending}, {"completed", "timed_out", ChecksFailing},
	} {
		if got := checkRunState(c.status, c.conclusion); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.status, c.conclusion, got, c.want)
		}
	}
	if worseChecks(ChecksUnknown, ChecksPassing) != ChecksPassing ||
		worseChecks(ChecksPassing, ChecksPending) != ChecksPending ||
		worseChecks(ChecksFailing, ChecksPending) != ChecksFailing {
		t.Error("worseChecks ranks wrong")
	}
}

// checksServer answers a pull request whose head has the given combined
// status, and whose check-runs listing is answered by runs(page).
func checksServer(t *testing.T, status string, runs func(w http.ResponseWriter, page string)) *GitHub {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/api/pulls/1":
			fmt.Fprint(w, `{"number":1,"state":"open","head":{"sha":"abc"}}`)
		case "/repos/acme/api/commits/abc/status":
			fmt.Fprint(w, status)
		case "/repos/acme/api/commits/abc/check-runs":
			runs(w, r.URL.Query().Get("page"))
		case "/repos/acme/api/pulls/1/reviews":
			fmt.Fprint(w, `[{"user":{"login":"alice"},"state":"APPROVED"}]`)
		case "/graphql":
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`)
		case "/repos/acme/api/compare/...abc":
			fmt.Fprint(w, `{"behind_by":0}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return NewGitHub(Credential{Auth: "pat", Secret: "ghp_secret_token", APIBaseURL: srv.URL})
}

func forbidden(w http.ResponseWriter) {
	w.WriteHeader(403)
	fmt.Fprint(w, `{"message":"Resource not accessible by personal access token"}`)
}

const codeRabbitPassed = `{"state":"success","total_count":1,"statuses":[{"context":"CodeRabbit","state":"success"}]}`

// A token that may not read check runs still reads the pull request, but never as
// passing: there may be CI it cannot see. The list says why, beside every
// check it could read, and the entry saying so is no check that failed.
func TestCheckRunsItCannotReadArePendingAndSayWhy(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		runs         func(w http.ResponseWriter, page string)
		checks       string
		actual       []string // the real checks read, by name
		failed       []string
	}{
		{name: "no statuses", status: `{"state":"pending","total_count":0}`,
			runs: func(w http.ResponseWriter, _ string) { forbidden(w) }, checks: ChecksPending},
		{name: "a successful subset", status: codeRabbitPassed,
			runs: func(w http.ResponseWriter, _ string) { forbidden(w) }, checks: ChecksPending, actual: []string{"CodeRabbit"}},
		{name: "a real failure", status: `{"state":"failure","total_count":1,"statuses":[{"context":"lint","state":"failure"}]}`,
			runs: func(w http.ResponseWriter, _ string) { forbidden(w) }, checks: ChecksFailing, actual: []string{"lint"}, failed: []string{"lint"}},
		{name: "refused on a later page", status: codeRabbitPassed, runs: func(w http.ResponseWriter, page string) {
			if page == "2" {
				forbidden(w)
				return
			}
			fmt.Fprint(w, `{"total_count":2,"check_runs":[{"id":5,"name":"backend","status":"completed","conclusion":"success"}]}`)
		}, checks: ChecksPending, actual: []string{"CodeRabbit", "backend"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := checksServer(t, tc.status, tc.runs).PullRequest(context.Background(), "acme/api", 1)
			if err != nil {
				t.Fatal(err)
			}
			if st.Checks != tc.checks {
				t.Errorf("checks = %s, want %s", st.Checks, tc.checks)
			}
			if d := CheckDiagnostic(st.CheckList); d != CheckRunsForbidden {
				t.Errorf("diagnostic = %q in %+v", d, st.CheckList)
			}
			var actual, failed []string
			for _, c := range st.CheckList {
				if c.Diagnostic != "" {
					if c.Status != CheckUnavailable || c.Conclusion != "" || c.RunID != 0 || c.App != "" || c.URL != "" || c.Failed() {
						t.Errorf("the diagnostic reads as a check: %+v", c)
					}
					continue
				}
				actual = append(actual, c.Name)
				if c.Failed() {
					failed = append(failed, c.Name)
				}
			}
			if fmt.Sprint(actual) != fmt.Sprint(tc.actual) || fmt.Sprint(failed) != fmt.Sprint(tc.failed) {
				t.Errorf("checks read %v failed %v, want %v failed %v", actual, failed, tc.actual, tc.failed)
			}
			if Ready(st) {
				t.Error("ready with check runs unreadable")
			}
			want := []string{"GitHub refused the check-runs read; a fine-grained token cannot read check runs, so use a classic token with the repo scope (or a GitHub App once supported), and check SSO authorization, organization token approval and the token's repository access"}
			if tc.checks == ChecksFailing {
				want = append([]string{"checks are failing"}, want...)
			}
			if got := Blockers(st); !slices.Equal(got, want) {
				t.Errorf("blockers = %q, want %q", got, want)
			}
		})
	}
}

// Access restored: the next read has no diagnostic, and its checks decide.
func TestCheckRunsReadableAgainClearTheDiagnostic(t *testing.T) {
	var denied atomic.Bool
	denied.Store(true)
	g := checksServer(t, codeRabbitPassed, func(w http.ResponseWriter, _ string) {
		if denied.Load() {
			forbidden(w)
			return
		}
		fmt.Fprint(w, `{"total_count":1,"check_runs":[{"id":5,"name":"backend","status":"completed","conclusion":"success"}]}`)
	})
	if st, err := g.PullRequest(context.Background(), "acme/api", 1); err != nil || CheckDiagnostic(st.CheckList) == "" {
		t.Fatalf("denied: %+v %v", st, err)
	}
	denied.Store(false)
	st, err := g.PullRequest(context.Background(), "acme/api", 1)
	if err != nil || st.Checks != ChecksPassing || CheckDiagnostic(st.CheckList) != "" || len(st.CheckList) != 2 {
		t.Fatalf("restored: %+v %v", st, err)
	}
	if !Ready(st) {
		t.Errorf("not ready once readable and green: %v", Blockers(st))
	}
}

// A 403 GitHub marks as a rate limit only by its headers is a rate limit:
// the sync fails, to be tried again, and no diagnostic is recorded.
func TestAHeaderOnlyRateLimitOnCheckRunsFailsTheSync(t *testing.T) {
	for name, header := range map[string][2]string{
		"Retry-After":    {"Retry-After", "60"},
		"none remaining": {"X-RateLimit-Remaining", "0"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := checksServer(t, codeRabbitPassed, func(w http.ResponseWriter, _ string) {
				w.Header().Set(header[0], header[1])
				w.WriteHeader(403)
				fmt.Fprint(w, `{"message":"You have been blocked"}`)
			}).PullRequest(context.Background(), "acme/api", 1)
			var e *Error
			if !errors.As(err, &e) || !Transient(err) || !strings.Contains(e.Message, "You have been blocked") ||
				strings.Contains(err.Error(), "ghp_secret_token") {
				t.Fatalf("err = %v, want a transient error keeping GitHub's message", err)
			}
		})
	}
}

// GitHub's older secondary limit is a headerless 403 saying "abuse
// detection": a limit to wait out, not a refusal to diagnose.
func TestAnAbuseDetectionLimitOnCheckRunsFailsTheSync(t *testing.T) {
	_, err := checksServer(t, codeRabbitPassed, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"message":"You have triggered an abuse detection mechanism"}`)
	}).PullRequest(context.Background(), "acme/api", 1)
	var e *Error
	if !errors.As(err, &e) || e.Status != 403 || e.Message != "You have triggered an abuse detection mechanism" || !Transient(err) {
		t.Fatalf("err = %v, want a transient 403 keeping GitHub's message", err)
	}
}

// GitHub Enterprise without Actions has no check-runs endpoint: the
// statuses are the whole reading, with nothing said to be unreadable.
func TestNoCheckRunsEndpointIsNoCheckRuns(t *testing.T) {
	st, err := checksServer(t, codeRabbitPassed, func(w http.ResponseWriter, _ string) { w.WriteHeader(404) }).
		PullRequest(context.Background(), "acme/api", 1)
	if err != nil || st.Checks != ChecksPassing || CheckDiagnostic(st.CheckList) != "" || len(st.CheckList) != 1 {
		t.Fatalf("status %+v, err %v", st, err)
	}
}

// Losing sight of check runs is not a failure an agent could fix, nor a
// change in readiness; a real failure beside it still is, and names only
// the real check.
func TestUnreadableCheckRunsWakeNobody(t *testing.T) {
	diag := Check{Name: "GitHub check runs", Status: CheckUnavailable, Diagnostic: CheckRunsForbidden}
	was := Status{PullRequestRef: PullRequestRef{State: StateOpen}, Checks: ChecksPending, Review: ReviewApproved}
	now := was
	now.CheckList = []Check{{Name: "CodeRabbit", Status: "completed", Conclusion: "success"}, diag}
	if s := Classify(was, now, nil, nil); s != nil {
		t.Errorf("denied read signalled %+v", s)
	}
	now.Checks = ChecksFailing
	now.CheckList = append(now.CheckList, Check{Name: "lint", Status: "completed", Conclusion: "failure"})
	s := Classify(was, now, nil, nil)
	if s == nil || s.Kind != "actionable" || len(s.Feedback) != 1 || len(s.Feedback[0].Checks) != 1 || s.Feedback[0].Checks[0].Name != "lint" {
		t.Errorf("real failure beside a denied read: %+v", s)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer srv.Close()
	n, err := NewGitHub(Credential{Secret: "x", APIBaseURL: srv.URL}).RerunFailed(context.Background(), "acme/api", []Check{diag})
	if n != 0 || err != nil || requests.Load() != 0 {
		t.Errorf("re-ran %d (%v) with %d requests for a diagnostic", n, err, requests.Load())
	}
}

// A matrix build has more check runs than a page holds: a failure on the
// second page is a failure.
func TestEveryPageOfCheckRunsCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/api/pulls/1":
			fmt.Fprint(w, `{"number":1,"state":"open","head":{"sha":"abc"}}`)
		case "/repos/acme/api/commits/abc/status":
			fmt.Fprint(w, `{"state":"pending","total_count":0}`)
		case "/repos/acme/api/commits/abc/check-runs":
			conclusion := "success"
			if r.URL.Query().Get("page") == "2" {
				conclusion = "failure"
			}
			fmt.Fprintf(w, `{"total_count":2,"check_runs":[{"status":"completed","conclusion":%q}]}`, conclusion)
		case "/repos/acme/api/pulls/1/reviews":
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	st, err := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}).PullRequest(context.Background(), "acme/api", 1)
	if err != nil || st.Checks != ChecksFailing {
		t.Fatalf("status %+v, err %v", st, err)
	}
}

// A rate limit is a 403 too, and not a refusal: it fails the sync, to be
// tried again, rather than reading as no check runs.
func TestARateLimitOnCheckRunsFailsTheSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/api/pulls/1":
			fmt.Fprint(w, `{"number":1,"state":"open","head":{"sha":"abc"}}`)
		case "/repos/acme/api/commits/abc/status":
			fmt.Fprint(w, `{"state":"pending","total_count":0}`)
		case "/repos/acme/api/commits/abc/check-runs":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"API rate limit exceeded for user ID 1."}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	_, err := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}).PullRequest(context.Background(), "acme/api", 1)
	if err == nil || !Transient(err) {
		t.Fatalf("err = %v, want a transient error", err)
	}
}
