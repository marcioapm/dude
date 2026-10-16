package agenttools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// githubAnswering is a Caller whose GitHub is h.
func githubAnswering(t *testing.T, h http.HandlerFunc) Caller {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return Caller{env: env{prepared: forge.NewGitHub(forge.Credential{Secret: t.Name(), APIBaseURL: srv.URL})}}
}

// A repository GitHub answers with a permanent redirect has moved: the
// agent is told so in dude's words, and nothing of the redirect's body.
func TestAMovedRepositoryIsRefusedSayingSo(t *testing.T) {
	const canary = "private-redirect-body-canary"
	moved := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://api.github.test/repositories/42")
		w.WriteHeader(http.StatusMovedPermanently)
		_, _ = w.Write([]byte(`{"message":"Moved Permanently ` + canary + `","url":"https://api.github.test/repositories/42"}`))
	}
	for _, read := range []githubRead{
		{Tool: "github_search", Slug: "acme/old", Kind: "issue"},
		{Tool: "github_issue", Slug: "acme/old", Number: 7},
		{Tool: "github_pull_request", Slug: "acme/old", Number: 7},
	} {
		t.Run(read.Tool, func(t *testing.T) {
			out, err := readOnGitHub(context.Background(), githubAnswering(t, moved), read)
			want := "repository acme/old has moved on GitHub; update the project's repository URL"
			if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error()+string(out), canary) {
				t.Fatalf("err=%v out=%s, want %q without the body", err, out, want)
			}
		})
	}
}
