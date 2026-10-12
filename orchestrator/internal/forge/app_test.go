package forge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// appFixture is a fake GitHub serving one App installed on two accounts
// (acme, an organisation, and marcio, a person), and a client of it.
type appFixture struct {
	app   *fakegithub.App
	url   string
	pem   string
	clock *fakeClock
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newAppFixture(t *testing.T) *appFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Now()}
	app := fakegithub.NewApp(9001, "dude-test", &key.PublicKey)
	app.Set(func(a *fakegithub.App) {
		a.Installations = map[string]int64{"acme": 11, "marcio": 22}
		a.Now = clock.now
	})
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return &appFixture{app: app, url: srv.URL, pem: string(block), clock: clock}
}

func (f *appFixture) client(c Credential) *GitHub {
	c.Auth, c.APIBaseURL = "github_app", f.url
	if c.Secret == "" {
		c.Secret = f.pem
	}
	if c.AppID == "" {
		c.AppID = "9001"
	}
	return NewGitHub(c, WithClock(f.clock.now))
}

func TestAnAppTokenIsTheInstallationOfTheRepositorysOwner(t *testing.T) {
	f := newAppFixture(t)
	g := f.client(Credential{})
	ctx := context.Background()

	org, err := g.Token(ctx, "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	personal, err := g.Token(ctx, "marcio/dotfiles")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.app.Installation(org); got != 11 {
		t.Errorf("acme/api's token is installation %d's, want 11", got)
	}
	if got := f.app.Installation(personal); got != 22 {
		t.Errorf("marcio/dotfiles's token is installation %d's, want 22", got)
	}
	// Another repository of the same owner: the owner's installation is
	// known, and its token is still good.
	again, err := g.Token(ctx, "ACME/web")
	if err != nil || again != org {
		t.Errorf("acme/web: %q, %v; want acme's token %q again", again, err, org)
	}
	f.app.Set(func(a *fakegithub.App) {
		if a.Lookups["/repos/acme/web/installation"] != 0 || a.Lookups["/repos/acme/api/installation"] != 1 {
			t.Errorf("installation lookups %v: want one for acme, by its first repository", a.Lookups)
		}
	})
	if n := f.app.ExchangesFor(11); n != 1 {
		t.Errorf("%d exchanges for acme's installation, want 1", n)
	}
}

func TestAPATTokenIsThePAT(t *testing.T) {
	g := NewGitHub(Credential{Auth: "pat", Secret: "ghp_x"})
	if tok, err := g.Token(context.Background(), "acme/api"); err != nil || tok != "ghp_x" {
		t.Errorf("PAT: %q, %v", tok, err)
	}
}

func TestAnAppTokenIsRefreshedOnlyNearItsExpiry(t *testing.T) {
	f := newAppFixture(t)
	g := f.client(Credential{})
	ctx := context.Background()
	first, err := g.Token(ctx, "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	// 49 minutes into its hour: 11 left, more than the margin.
	f.clock.advance(49 * time.Minute)
	if again, _ := g.Token(ctx, "acme/api"); again != first {
		t.Errorf("with 11 minutes left the token was replaced")
	}
	// A new client for the same credential (Resolver.For makes one per
	// call) shares the cache.
	if other, _ := f.client(Credential{}).Token(ctx, "acme/api"); other != first {
		t.Errorf("another client of the same credential minted its own token")
	}
	f.clock.advance(2 * time.Minute)
	fresh, err := g.Token(ctx, "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == first {
		t.Errorf("with 9 minutes left the token was not replaced")
	}
	if n := f.app.ExchangesFor(11); n != 2 {
		t.Errorf("%d exchanges, want 2", n)
	}
}

func TestConcurrentCallersShareOneExchange(t *testing.T) {
	f := newAppFixture(t)
	f.app.Set(func(a *fakegithub.App) { a.ExchangeDelay = 100 * time.Millisecond })
	ctx := context.Background()
	tokens := make([]string, 20)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Go(func() {
			// Each its own client, as each sweep's Resolver.For gives.
			tok, err := f.client(Credential{}).Token(ctx, "acme/repo"+strconv.Itoa(i%3))
			if err != nil {
				t.Error(err)
			}
			tokens[i] = tok
		})
	}
	wg.Wait()
	for _, tok := range tokens {
		if tok != tokens[0] {
			t.Fatalf("callers got different tokens: %v", tokens)
		}
	}
	if n := f.app.ExchangesFor(11); n != 1 {
		t.Errorf("%d exchanges for 20 concurrent callers, want 1", n)
	}
	f.app.Set(func(a *fakegithub.App) {
		lookups := 0
		for _, n := range a.Lookups {
			lookups += n
		}
		if lookups != 1 {
			t.Errorf("%d installation lookups for one owner, want 1: %v", lookups, a.Lookups)
		}
	})
}

func TestTheInstallationIDIsTheDefaultWhereGitHubNamesNone(t *testing.T) {
	f := newAppFixture(t)
	f.app.Set(func(a *fakegithub.App) { a.Installations["elsewhere"] = 33 })
	g := f.client(Credential{InstallationID: "33"})
	ctx := context.Background()
	// No account: the credential's installation.
	tok, err := g.Token(ctx, "")
	if err != nil || f.app.Installation(tok) != 33 {
		t.Errorf("no scope: %q (installation %d), %v; want installation 33", tok, f.app.Installation(tok), err)
	}
	// An owner GitHub knows: its own, not the default.
	if tok, _ := g.Token(ctx, "marcio/x"); f.app.Installation(tok) != 22 {
		t.Errorf("marcio/x got installation %d, want 22", f.app.Installation(tok))
	}
	// An owner GitHub has no installation for: the default.
	if tok, err := g.Token(ctx, "stranger/x"); err != nil || f.app.Installation(tok) != 33 {
		t.Errorf("stranger/x: installation %d, %v; want the default, 33", f.app.Installation(tok), err)
	}
	// The default is not that owner's for good: once the App is installed
	// there, its token is that installation's.
	f.app.Set(func(a *fakegithub.App) { a.Installations["stranger"] = 44 })
	if tok, err := g.Token(ctx, "stranger/y"); err != nil || f.app.Installation(tok) != 44 {
		t.Errorf("stranger/y after the install: installation %d, %v; want 44", f.app.Installation(tok), err)
	}
	// The same key with no default shares nothing a default decided.
	f.app.Set(func(a *fakegithub.App) { delete(a.Installations, "stranger") })
	if _, err := f.client(Credential{}).Token(ctx, "stranger/z"); !Refused(err) {
		t.Errorf("no default, owner without the App: %v, want a refusal", err)
	}
}

// Calls about no account read the owners' installations while calls for a
// repository fill them in: run under -race.
func TestScopedAndUnscopedCallsRunTogether(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, err := f.client(Credential{}).Token(ctx, []string{"acme", "marcio"}[i%2]+"/r"+strconv.Itoa(i)); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := f.client(Credential{}).Token(ctx, ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// A refresh GitHub fails to answer leaves the token in hand, which still
// has minutes to live.
func TestAFailedRefreshKeepsATokenStillValid(t *testing.T) {
	f := newAppFixture(t)
	g := f.client(Credential{})
	ctx := context.Background()
	first, err := g.Token(ctx, "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	f.clock.advance(51 * time.Minute)
	f.app.Set(func(a *fakegithub.App) { a.Down = 502 })
	tok, err := g.Token(ctx, "acme/api")
	if err != nil || tok != first {
		t.Errorf("with 9 minutes left and GitHub failing: %q, %v; want the old token", tok, err)
	}
	if n := f.app.AttemptsFor(11); n != 2 {
		t.Errorf("%d exchanges asked for, want 2: the refresh was tried", n)
	}
	f.app.Set(func(a *fakegithub.App) { a.Down = 0 })
	if again, _ := g.Token(ctx, "acme/api"); again == first || f.app.ExchangesFor(11) != 2 {
		t.Errorf("GitHub back: %q (exchanges %d); want a new token from a second exchange", again, f.app.ExchangesFor(11))
	}
	f.clock.advance(time.Hour)
	f.app.Set(func(a *fakegithub.App) { a.Down = 502 })
	if _, err := g.Token(ctx, "acme/api"); !Transient(err) {
		t.Errorf("an expired token and GitHub failing: %v, want the transient error", err)
	}
}

// An owner who reinstalls the App has a new installation: the first call
// after it fails as worth retrying, and the retry finds the new one.
func TestAReinstalledAppIsFoundOnTheRetry(t *testing.T) {
	f := newAppFixture(t)
	g := f.client(Credential{})
	ctx := context.Background()
	if _, err := g.Token(ctx, "acme/api"); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(55 * time.Minute)
	f.app.Set(func(a *fakegithub.App) { a.Installations["acme"] = 81 })
	if _, err := g.Token(ctx, "acme/api"); !Transient(err) {
		t.Fatalf("the removed installation's exchange: %v, want transient", err)
	}
	if tok, err := g.Token(ctx, "acme/api"); err != nil || f.app.Installation(tok) != 81 {
		t.Errorf("the retry: installation %d, %v; want 81", f.app.Installation(tok), err)
	}
	// Uninstalled for good: the lookup's refusal.
	f.clock.advance(55 * time.Minute)
	f.app.Set(func(a *fakegithub.App) { delete(a.Installations, "acme") })
	_, _ = g.Token(ctx, "acme/api")
	if _, err := g.Token(ctx, "acme/api"); !Refused(err) {
		t.Errorf("after the uninstall: %v, want a refusal", err)
	}
}

func TestAppFailuresAreClassified(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	cases := []struct {
		name      string
		g         *GitHub
		scope     string
		transient bool
	}{
		{"a key that is not PEM", f.client(Credential{Secret: "ghp_not_a_key"}), "acme/api", false},
		{"another App's key", f.client(Credential{AppID: "1"}), "acme/api", false},
		{"not installed on the owner", f.client(Credential{}), "stranger/x", false},
	}
	for _, tc := range cases {
		_, err := tc.g.Token(ctx, tc.scope)
		if err == nil || Transient(err) != tc.transient || Refused(err) == tc.transient {
			t.Errorf("%s: %v (transient %v, refused %v), want transient %v", tc.name, err, Transient(err), Refused(err), tc.transient)
		}
	}

	f.app.Set(func(a *fakegithub.App) { a.Down = 502 })
	if _, err := f.client(Credential{}).Token(ctx, "acme/api"); !Transient(err) {
		t.Errorf("GitHub answering 502: %v, want transient", err)
	}
	gone := NewGitHub(Credential{Auth: "github_app", Secret: f.pem, AppID: "9001", APIBaseURL: "http://127.0.0.1:1"})
	if _, err := gone.Token(ctx, "acme/api"); !Transient(err) {
		t.Errorf("GitHub unreachable: %v, want transient", err)
	}
}

func TestTheBotIdentityIsTheAppsBotUser(t *testing.T) {
	f := newAppFixture(t)
	g := f.client(Credential{})
	bot, err := g.Bot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := BotIdentity{Name: "dude-test[bot]", Email: "4242+dude-test[bot]@users.noreply.github.com"}
	if bot != want {
		t.Errorf("bot %+v, want %+v", bot, want)
	}
	if pat, err := NewGitHub(Credential{Auth: "pat", Secret: "x"}).Bot(context.Background()); err != nil || pat != (BotIdentity{}) {
		t.Errorf("a PAT's bot: %+v, %v; want none", pat, err)
	}
}

// An API call as the App goes with the installation token of the
// repository it names.
func TestAnAppsAPICallsUseTheirRepositorysInstallation(t *testing.T) {
	f := newAppFixture(t)
	var seen []string
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.Handle("/", f.app.Handler())
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.PathValue("owner")+"="+strconv.FormatInt(f.app.Installation(r.Header.Get("Authorization")[len("Bearer "):]), 10))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := NewGitHub(Credential{Auth: "github_app", Secret: f.pem, AppID: "9001", APIBaseURL: srv.URL}, WithClock(f.clock.now))
	for _, slug := range []string{"acme/api", "marcio/notes"} {
		if _, err := g.BranchSHA(context.Background(), slug, "main"); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] != "acme=11" || seen[1] != "marcio=22" {
		t.Errorf("requests went with %v, want acme=11 then marcio=22", seen)
	}
}
