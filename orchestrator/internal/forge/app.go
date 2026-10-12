package forge

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub App authentication. The credential's secret is the App's PEM
// private key; dude signs a short JWT with it, finds the installation that
// covers a repository's owner, and exchanges the JWT for that
// installation's token. One App installed on several accounts (a person's
// and an organization's) is one credential: installations are per owner.
//
// The state lives in appSessions, keyed by the credential, because
// Resolver.For builds a new *GitHub on every call: each would otherwise mint
// a token of its own, and GitHub limits how many an App may create.

// tokenRefreshMargin: an installation token is replaced once fewer than this
// many minutes of its hour remain, so a push or a clone that starts with it
// never meets its expiry halfway.
const tokenRefreshMargin = 10 * time.Minute

// jwtLifetime is how long a minted JWT is good for; GitHub refuses one that
// lives past ten minutes.
const jwtLifetime = 9 * time.Minute

type appSession struct {
	key    *rsa.PrivateKey
	keyErr error
	appID  string
	// The clock installation tokens' expiry is judged by; a JWT is always
	// stamped with the real time, which GitHub checks.
	now func() time.Time

	mu            sync.Mutex
	owners        map[string]*ownerEntry
	installations map[string]*tokenEntry
	identity      *identityEntry
}

// ownerEntry and tokenEntry each have a lock of their own, held across the
// GitHub request that fills them: callers wanting the same one wait for
// that request rather than each making it.
type ownerEntry struct {
	mu           sync.Mutex
	installation string
}

type tokenEntry struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

type identityEntry struct {
	mu        sync.Mutex
	slug      string
	botUserID int64
}

var appSessions sync.Map // sessionKey → *appSession

func sessionKey(c Credential) string {
	sum := sha256.Sum256([]byte(c.APIBaseURL + "\x00" + c.AppID + "\x00" + c.Secret))
	return hex.EncodeToString(sum[:])
}

func (g *GitHub) app() *appSession {
	key := sessionKey(g.cred)
	if s, ok := appSessions.Load(key); ok {
		return s.(*appSession)
	}
	s := &appSession{appID: strings.TrimSpace(g.cred.AppID), now: g.now,
		owners: map[string]*ownerEntry{}, installations: map[string]*tokenEntry{}}
	s.key, s.keyErr = parsePrivateKey(g.cred.Secret)
	actual, _ := appSessions.LoadOrStore(key, s)
	return actual.(*appSession)
}

// parsePrivateKey reads the PEM GitHub hands out (PKCS#1, "RSA PRIVATE
// KEY") or the same key converted to PKCS#8.
func parsePrivateKey(secret string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(secret)))
	if block == nil {
		return nil, &Error{Status: http.StatusUnauthorized, Message: "the GitHub App's private key is not PEM: paste the whole .pem file GitHub generated"}
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if k, ok := parsed.(*rsa.PrivateKey); err == nil && ok {
		return k, nil
	}
	return nil, &Error{Status: http.StatusUnauthorized, Message: "the GitHub App's private key is not an RSA private key"}
}

// jwt is a fresh RS256 JWT for the App: issued a minute in the past, for
// clocks that run ahead of GitHub's.
func (s *appSession) jwt() (string, error) {
	if s.keyErr != nil {
		return "", s.keyErr
	}
	if s.appID == "" {
		return "", &Error{Status: http.StatusUnauthorized, Message: "the GitHub App credential has no App ID"}
	}
	now := time.Now()
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(jwtLifetime).Unix(), "iss": s.appID})
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// appRequest is a request authenticated as the App itself (the JWT), not
// as an installation: the only way to reach /app/... endpoints.
func (g *GitHub) appRequest(ctx context.Context, s *appSession, method, path string, out any) error {
	jwt, err := s.jwt()
	if err != nil {
		return err
	}
	return g.send(ctx, method, g.cred.APIBaseURL+path, "Bearer "+jwt, nil, out)
}

// appToken is an installation token for scope: "owner/repo", a bare owner
// (an organization's members), or "" for a request about no account (a
// user's profile), which any installation's token may make.
func (g *GitHub) appToken(ctx context.Context, scope string) (string, error) {
	s := g.app()
	installation, err := g.installationFor(ctx, s, scope)
	if err != nil {
		return "", err
	}
	return g.installationToken(ctx, s, installation)
}

// installationFor resolves which installation of the App covers scope's
// owner, once per owner: the App may be installed on several accounts, each
// its own installation. A repository is asked about by name (GET
// /repos/{owner}/{repo}/installation); a bare owner as a user or
// organization (GET /users/{owner}/installation, which answers for both).
// The credential's installation id, when set, is the default for a request
// about no account and the installation used when GitHub finds none for the
// owner.
func (g *GitHub) installationFor(ctx context.Context, s *appSession, scope string) (string, error) {
	owner, repo, _ := strings.Cut(scope, "/")
	if owner == "" {
		return g.anyInstallation(ctx, s)
	}
	owner = strings.ToLower(owner)
	s.mu.Lock()
	e, ok := s.owners[owner]
	if !ok {
		e = &ownerEntry{}
		s.owners[owner] = e
	}
	s.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.installation != "" {
		return e.installation, nil
	}
	path := "/users/" + url.PathEscape(owner) + "/installation"
	if repo != "" {
		path = "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/installation"
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	err := g.appRequest(ctx, s, "GET", path, &inst)
	if ge, ok := err.(*Error); ok && ge.NotFound() {
		if id := strings.TrimSpace(g.cred.InstallationID); id != "" {
			e.installation = id
			return id, nil
		}
		return "", &Error{Status: http.StatusNotFound,
			Message: fmt.Sprintf("the GitHub App is not installed on %s: install it there, with access to the repository", scope)}
	}
	if err != nil {
		return "", err
	}
	e.installation = strconv.FormatInt(inst.ID, 10)
	return e.installation, nil
}

func (g *GitHub) anyInstallation(ctx context.Context, s *appSession) (string, error) {
	if id := strings.TrimSpace(g.cred.InstallationID); id != "" {
		return id, nil
	}
	s.mu.Lock()
	for _, e := range s.owners {
		if e.installation != "" {
			s.mu.Unlock()
			return e.installation, nil
		}
	}
	s.mu.Unlock()
	var list []struct {
		ID int64 `json:"id"`
	}
	if err := g.appRequest(ctx, s, "GET", "/app/installations?per_page=1", &list); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", &Error{Status: http.StatusNotFound, Message: "the GitHub App is not installed anywhere"}
	}
	return strconv.FormatInt(list[0].ID, 10), nil
}

// installationToken is a token for installation, reused until fewer than
// tokenRefreshMargin of its life remain. The entry's lock is held across
// the exchange, so a token about to expire is replaced once however many
// callers want it at that moment.
func (g *GitHub) installationToken(ctx context.Context, s *appSession, installation string) (string, error) {
	s.mu.Lock()
	e, ok := s.installations[installation]
	if !ok {
		e = &tokenEntry{}
		s.installations[installation] = e
	}
	s.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.token != "" && e.expires.Sub(s.now()) > tokenRefreshMargin {
		return e.token, nil
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err := g.appRequest(ctx, s, "POST", "/app/installations/"+url.PathEscape(installation)+"/access_tokens", &out)
	if ge, ok := err.(*Error); ok && ge.NotFound() {
		// The installation was removed: the owner's must be found again.
		s.forgetInstallation(installation)
		return "", &Error{Status: http.StatusNotFound, Message: "the GitHub App's installation " + installation + " no longer exists"}
	}
	if err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", &Error{Status: http.StatusBadGateway, Message: "GitHub answered the installation token exchange without a token"}
	}
	e.token, e.expires = out.Token, out.ExpiresAt
	return e.token, nil
}

func (s *appSession) forgetInstallation(installation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for owner, e := range s.owners {
		if e.installation == installation {
			delete(s.owners, owner)
		}
	}
}

// BotIdentity is the App's bot account, as commits name it: "<slug>[bot]"
// and its noreply address, which GitHub links to the bot by its user id.
type BotIdentity struct {
	Name, Email string
}

// IsApp says whether the credential is a GitHub App's.
func (g *GitHub) IsApp() bool { return g.cred.Auth == "github_app" }

// Bot is the App's commit identity: its slug from GET /app (as the App),
// its bot user's id from GET /users/<slug>[bot]. Read once per credential.
// A PAT has none: the zero value and no error.
func (g *GitHub) Bot(ctx context.Context) (BotIdentity, error) {
	if !g.IsApp() {
		return BotIdentity{}, nil
	}
	s := g.app()
	s.mu.Lock()
	if s.identity == nil {
		s.identity = &identityEntry{}
	}
	e := s.identity
	s.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.slug == "" {
		var app struct {
			Slug string `json:"slug"`
		}
		if err := g.appRequest(ctx, s, "GET", "/app", &app); err != nil {
			return BotIdentity{}, err
		}
		if app.Slug == "" {
			return BotIdentity{}, &Error{Status: http.StatusBadGateway, Message: "GitHub answered GET /app without the App's slug"}
		}
		e.slug = app.Slug
	}
	if e.botUserID == 0 {
		var user struct {
			ID int64 `json:"id"`
		}
		if err := g.doURL(ctx, "", "GET", g.cred.APIBaseURL+"/users/"+url.PathEscape(e.slug+"[bot]"), nil, &user); err != nil {
			return BotIdentity{}, err
		}
		e.botUserID = user.ID
	}
	login := e.slug + "[bot]"
	return BotIdentity{Name: login, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", e.botUserID, login)}, nil
}

// send makes one GitHub request with the given Authorization and decodes
// the answer, turning a refusal into *Error and no answer into Unreachable.
func (g *GitHub) send(ctx context.Context, method, target, authorization string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.http.Do(req)
	if err != nil {
		return &Unreachable{err}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return &Unreachable{err}
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = fmt.Sprintf("%s %s failed", method, target)
		}
		refusal := &Error{Status: res.StatusCode, Message: e.Message}
		markRateLimit(refusal, res.Header, "rate limit: ")
		return refusal
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
