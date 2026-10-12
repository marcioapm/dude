package fakegithub

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// App stands in for GitHub's App endpoints: it checks each JWT against the
// App's public key and id as GitHub does (RS256, iss, exp no more than ten
// minutes after iat, not expired), resolves a repository or account to its
// installation, and issues installation tokens with an expiry.
type App struct {
	ID   int64
	Slug string
	Key  *rsa.PublicKey
	// BotUserID is the id GET /users/<slug>[bot] answers.
	BotUserID int64

	mu sync.Mutex
	// Installations by account login (lower case); a repository belongs to
	// its owner's.
	Installations map[string]int64
	// Users GET /users/{login} knows, by login (lower case): their id.
	Users map[string]int64
	// TokenLifetime is how long an issued token lives (GitHub: an hour).
	TokenLifetime time.Duration
	// Now is the fake's clock for issued tokens' expiry.
	Now func() time.Time
	// Exchanges counts token exchanges per installation; Lookups,
	// installation lookups per path.
	Exchanges map[int64]int
	Lookups   map[string]int
	// ExchangeDelay holds each exchange, so concurrent callers overlap.
	ExchangeDelay time.Duration
	// Down answers every App request with this status (0: up).
	Down   int
	issued map[string]int64 // token → installation
	serial int
}

func NewApp(id int64, slug string, key *rsa.PublicKey) *App {
	return &App{ID: id, Slug: slug, Key: key, BotUserID: 4242, Installations: map[string]int64{}, Users: map[string]int64{},
		TokenLifetime: time.Hour, Now: time.Now, Exchanges: map[int64]int{}, Lookups: map[string]int{}, issued: map[string]int64{}}
}

// Set changes the fake's state under its lock.
func (a *App) Set(f func(a *App)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a)
}

// Installation is the installation a token was issued for, 0 for none.
func (a *App) Installation(token string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.issued[token]
}

// ExchangesFor is how many tokens were issued for installation.
func (a *App) ExchangesFor(installation int64) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Exchanges[installation]
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app", a.withJWT(func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"id": a.ID, "slug": a.Slug})
	}))
	mux.HandleFunc("GET /app/installations", a.withJWT(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		out := []any{}
		for _, id := range a.Installations {
			out = append(out, map[string]any{"id": id})
		}
		a.mu.Unlock()
		write(w, 200, out)
	}))
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", a.withJWT(a.installation))
	mux.HandleFunc("GET /users/{owner}/installation", a.withJWT(a.installation))
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", a.withJWT(a.exchange))
	mux.HandleFunc("GET /users/{login}", func(w http.ResponseWriter, r *http.Request) {
		if !a.tokenOK(r) {
			fail(w, 401, "Bad credentials")
			return
		}
		login := r.PathValue("login")
		a.mu.Lock()
		id, ok := a.Users[strings.ToLower(login)]
		a.mu.Unlock()
		if login == a.Slug+"[bot]" {
			id, ok = a.BotUserID, true
		}
		if !ok {
			fail(w, 404, "Not Found")
			return
		}
		write(w, 200, map[string]any{"login": login, "id": id})
	})
	return mux
}

func (a *App) installation(w http.ResponseWriter, r *http.Request) {
	owner := strings.ToLower(r.PathValue("owner"))
	a.mu.Lock()
	a.Lookups[r.URL.Path]++
	id, ok := a.Installations[owner]
	a.mu.Unlock()
	if !ok {
		fail(w, 404, "Not Found")
		return
	}
	write(w, 200, map[string]any{"id": id, "account": map[string]string{"login": owner}})
}

func (a *App) exchange(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a.mu.Lock()
	delay := a.ExchangeDelay
	known := false
	for _, inst := range a.Installations {
		known = known || inst == id
	}
	a.mu.Unlock()
	if !known {
		fail(w, 404, "Not Found")
		return
	}
	time.Sleep(delay)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.serial++
	a.Exchanges[id]++
	token := fmt.Sprintf("ghs_fake_%d_%d", id, a.serial)
	a.issued[token] = id
	write(w, 201, map[string]any{"token": token, "expires_at": a.Now().Add(a.TokenLifetime).UTC().Format(time.RFC3339)})
}

// tokenOK: an installation token the fake issued.
func (a *App) tokenOK(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && a.Installation(token) != 0
}

// withJWT refuses a request whose bearer is not a valid JWT of this App, as
// GitHub does (401), and answers Down when set.
func (a *App) withJWT(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		down := a.Down
		a.mu.Unlock()
		if down != 0 {
			fail(w, down, http.StatusText(down))
			return
		}
		jwt, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if msg := a.checkJWT(jwt); msg != "" {
			fail(w, 401, msg)
			return
		}
		next(w, r)
	}
}

func (a *App) checkJWT(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "A JSON web token could not be decoded"
	}
	enc := base64.RawURLEncoding
	var header struct {
		Alg string `json:"alg"`
	}
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	h, err1 := enc.DecodeString(parts[0])
	c, err2 := enc.DecodeString(parts[1])
	sig, err3 := enc.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || json.Unmarshal(h, &header) != nil || json.Unmarshal(c, &claims) != nil {
		return "A JSON web token could not be decoded"
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if header.Alg != "RS256" || rsa.VerifyPKCS1v15(a.Key, crypto.SHA256, digest[:], sig) != nil {
		return "A JSON web token could not be decoded"
	}
	now := time.Now().Unix()
	switch {
	case claims.Iss != strconv.FormatInt(a.ID, 10):
		return "'Issuer' claim ('iss') must be an Integer"
	case claims.Exp-claims.Iat > 600:
		return "'Expiration time' claim ('exp') is too far in the future"
	case claims.Exp < now:
		return "'Expiration time' claim ('exp') must be a numeric value representing the future time at which the assertion expires"
	case claims.Iat > now+60:
		return "'Issued at' claim ('iat') must be an Integer representing the time that the assertion was issued"
	}
	return ""
}
