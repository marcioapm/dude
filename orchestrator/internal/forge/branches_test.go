package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBranchSHAEncodesTheExactBranch(t *testing.T) {
	for _, branch := range []string{"feature/login", "feature/#123", "feature/100%done", "feature/%23"} {
		t.Run(branch, func(t *testing.T) {
			var seen string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = strings.TrimPrefix(r.URL.Path, "/repos/acme/target/git/ref/heads/")
				fmt.Fprint(w, `{"object":{"sha":"exact-commit"}}`)
			}))
			defer srv.Close()
			gh := NewGitHub(Credential{Auth: "pat", Secret: "example", APIBaseURL: srv.URL})
			sha, err := gh.BranchSHA(context.Background(), "acme/target", branch)
			if err != nil || sha != "exact-commit" || seen != branch {
				t.Fatalf("sha=%q err=%v decoded branch=%q; want %q", sha, err, seen, branch)
			}
		})
	}
}

func TestBranchGoneRequiresAVisibleRepository(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		refStatus, repoStatus int
		gone, wantErr         bool
	}{
		{"deleted branch", 404, 200, true, false},
		{"repository hidden", 404, 404, false, true},
		{"repository unavailable", 404, 502, false, true},
		{"ref unavailable", 502, 200, false, true},
		{"branch exists", 200, 404, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status, body := tc.repoStatus, `{"id":1}`
				if strings.Contains(r.URL.Path, "/git/ref/") {
					status, body = tc.refStatus, `{"object":{"sha":"present"}}`
				}
				if status != 200 {
					body = `{"message":"Not Found"}`
				}
				w.WriteHeader(status)
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			gh := NewGitHub(Credential{Auth: "pat", Secret: "example", APIBaseURL: srv.URL})
			gone, err := gh.BranchGone(context.Background(), "acme/target", "feature/c")
			if gone != tc.gone || (err != nil) != tc.wantErr {
				t.Fatalf("gone=%v err=%v; want gone=%v error=%v", gone, err, tc.gone, tc.wantErr)
			}
		})
	}
}
