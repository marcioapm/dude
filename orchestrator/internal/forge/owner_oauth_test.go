package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

type oauthTransport struct {
	target string
	base   http.RoundTripper
}

func (t oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	target, _ := http.NewRequest("GET", t.target, nil)
	u.Scheme, u.Host = target.URL.Scheme, target.URL.Host
	copy.URL = &u
	return t.base.RoundTrip(copy)
}

func TestOwnerRefreshRotatesOnceAcrossConcurrentCallers(t *testing.T) {
	database, owner := dbtest.Open(t)
	ctx := context.Background()
	_, err := owner.Exec(ctx, `INSERT INTO organizations (id,name,slug) VALUES ('org_oauth','OAuth','oauth');
   INSERT INTO people (id,organization_id,name,email) VALUES ('per_oauth','org_oauth','Owner','owner@example.com');
   INSERT INTO forge_credentials (id,organization_id,forge,auth,secret,app_id,oauth_client_id,oauth_client_secret)
   VALUES ('fc_oauth','org_oauth','github','github_app','pem','1','client','secret');
   INSERT INTO github_user_authorizations (organization_id,person_id,credential_id,credential_version,github_user_id,
     github_login,access_token,refresh_token,expires_at,refresh_expires_at)
   VALUES ('org_oauth','per_oauth','fc_oauth',1,123,'owner','old-access','old-refresh',now()-interval '1 second',now()+interval '1 day')`)
	if err != nil {
		t.Fatal(err)
	}
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "old-refresh" || in["client_id"] != "client" || in["client_secret"] != "secret" {
			t.Errorf("invalid exchange: %#v", in)
		}
		exchanges.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 28800, "refresh_token_expires_in": 15552000})
	}))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = oauthTransport{server.URL, previous}
	defer func() { http.DefaultTransport = previous }()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := OwnerToken(ctx, database, "org_oauth", "per_oauth")
			if err != nil || token != "new-access" {
				t.Errorf("token=%q err=%v", token, err)
			}
		}()
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges=%d", exchanges.Load())
	}
	var access, refresh string
	err = owner.QueryRow(ctx, `SELECT access_token,refresh_token FROM github_user_authorizations WHERE person_id='per_oauth'`).Scan(&access, &refresh)
	if err != nil || access != "new-access" || refresh != "new-refresh" {
		t.Fatalf("stored %q %q: %v", access, refresh, err)
	}
	if _, err := OwnerToken(ctx, database, "org_oauth", "someone_else"); err == nil {
		t.Fatal("another person used the authorization")
	}
	if _, err := OwnerToken(ctx, database, "another_org", "per_oauth"); err == nil {
		t.Fatal("another organization used the authorization")
	}
	_, err = owner.Exec(ctx, `UPDATE forge_credentials SET app_id='2' WHERE id='fc_oauth'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OwnerToken(ctx, database, "org_oauth", "per_oauth"); err == nil {
		t.Fatal("replacement retained authorization")
	}
}

func TestOwnerRefreshFailureKindsArePersisted(t *testing.T) {
	database, owner := dbtest.Open(t)
	ctx := context.Background()
	_, err := owner.Exec(ctx, `INSERT INTO organizations (id,name,slug) VALUES ('org_fail','Fail','fail');
		INSERT INTO people (id,organization_id,name,email) VALUES ('per_fail','org_fail','Owner','fail@example.com');
		INSERT INTO forge_credentials (id,organization_id,forge,auth,secret,app_id,oauth_client_id,oauth_client_secret)
		VALUES ('fc_fail','org_fail','github','github_app','pem','1','client','secret');
		INSERT INTO github_user_authorizations (organization_id,person_id,credential_id,credential_version,github_user_id,
			github_login,access_token,refresh_token,expires_at,refresh_expires_at)
		VALUES ('org_fail','per_fail','fc_fail',1,1,'owner','still-valid','refresh',now()+interval '30 seconds',now()+interval '1 day')`)
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusBadGateway
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":"bad_refresh_token"}`))
	}))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = oauthTransport{server.URL, previous}
	defer func() { http.DefaultTransport = previous }()
	token, err := OwnerToken(ctx, database, "org_fail", "per_fail")
	if err != nil || token != "still-valid" {
		t.Fatalf("transient refresh token=%q err=%v", token, err)
	}
	var required bool
	_ = owner.QueryRow(ctx, `SELECT reconnect_required FROM github_user_authorizations`).Scan(&required)
	if required {
		t.Fatal("transient failure required reconnect")
	}
	status = 200
	if _, err := owner.Exec(ctx, `UPDATE github_user_authorizations SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := OwnerToken(ctx, database, "org_fail", "per_fail"); err == nil || err.Error() == "" || strings.Contains(err.Error(), "bad_refresh_token") {
		t.Fatalf("invalid grant error %v", err)
	}
	_ = owner.QueryRow(ctx, `SELECT reconnect_required FROM github_user_authorizations`).Scan(&required)
	if !required {
		t.Fatal("invalid grant did not require reconnect")
	}
}

func TestOwnerCreationDoesNotChangeInstallationClient(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			w.Write([]byte(`{"number":1,"html_url":"https://github.com/o/r/pull/1","state":"open"}`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()
	gh := NewGitHub(Credential{Auth: "pat", Secret: "installation", APIBaseURL: server.URL})
	if _, err := gh.OpenPullRequestAs(context.Background(), OpenPullRequest{Slug: "o/r"}, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := gh.FindPullRequest(context.Background(), "o/r", "branch", "main"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "POST Bearer owner" || seen[1] != "GET Bearer installation" {
		t.Fatalf("requests: %v", seen)
	}
	if _, err := gh.OpenPullRequestAs(context.Background(), OpenPullRequest{Slug: "o/r"}, ""); err == nil {
		t.Fatal("empty token accepted")
	}
}
