package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// signInProxy answers every request as a sign-in proxy would, and records
// the Authorization header of each request it, or the sign-in host, sees.
func signInProxy(t *testing.T, crossHost bool) (*httptest.Server, *[]string) {
	t.Helper()
	var auths []string
	signIn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, "sign-in:"+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<!DOCTYPE html><title>Sign in</title>")
	}))
	t.Cleanup(signIn.Close)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, "api:"+r.Header.Get("Authorization"))
		if r.URL.Path == "/login" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<!DOCTYPE html><title>Sign in</title>")
			return
		}
		target := srv.URL + "/login?redirect=" + r.URL.Path
		if crossHost {
			// Another host name, not only another port: Go keeps the
			// key across ports on one host.
			target = strings.Replace(signIn.URL, "127.0.0.1", "localhost", 1) + "/cdn-cgi/access/login"
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &auths
}

func TestRedirectIsAnErrorAndSendsNothingOn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		crossHost bool
		args      []string
	}{
		{"same host", false, []string{"project", "list"}},
		{"another host", true, []string{"project", "list"}},
		{"api, same host", false, []string{"api", "GET", "/v1/me"}},
		{"api, another host", true, []string{"api", "GET", "/v1/me"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, auths := signInProxy(t, tc.crossHost)
			out, err := runCLI(t, srv, "", tc.args...)
			if err == nil {
				t.Fatalf("succeeded: %s", out)
			}
			for _, part := range []string{"redirected (302) to ", "/login", "behind a sign-in", "dude whoami"} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("missing %q: %v", part, err)
				}
			}
			if len(*auths) != 1 || (*auths)[0] != "api:Bearer k3y" {
				t.Fatalf("requests: %q, want only the first, with the key", *auths)
			}
			if strings.Contains(out, "DOCTYPE") {
				t.Fatalf("printed the page: %s", out)
			}
		})
	}
}

func TestRedirectWithoutLocationNamesNoTarget(t *testing.T) {
	for _, status := range []int{http.StatusNotModified, http.StatusFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		t.Cleanup(srv.Close)
		_, err := runCLI(t, srv, "", "api", "GET", "/v1/me")
		want := fmt.Sprintf("GET /v1/me: the server redirected (%d): it is behind a sign-in", status)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("%d: err = %v, want %q…", status, err, want)
		}
	}
}

func TestHTMLAnswerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!DOCTYPE html><title>Sign in</title>")
	}))
	t.Cleanup(srv.Close)
	for _, args := range [][]string{{"project", "list"}, {"api", "GET", "/v1/me"}, {"api", "GET", "v1/me"}} {
		out, err := runCLI(t, srv, "", args...)
		if err == nil || !strings.Contains(err.Error(), "HTML page") || !strings.Contains(err.Error(), "behind a sign-in") {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}
	// Outside /v1/, dude api prints whatever the server answers.
	out, err := runCLI(t, srv, "", "api", "GET", "/")
	var got map[string]any
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got["body"] != "<!DOCTYPE html><title>Sign in</title>" {
		t.Fatalf("GET /: %q %v", out, err)
	}
}

func TestJSONAnswerIsUnchanged(t *testing.T) {
	srv, calls := fakeDude(t)
	out, err := runCLI(t, srv, "", "api", "GET", "/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["organization"] == nil || len(*calls) != 1 {
		t.Fatalf("%s %v %+v", out, err, *calls)
	}
}
