package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitDiffBasesReadsAuthenticatedRecordedCommits(t *testing.T) {
	want := "target " + strings.Repeat("a", 40) + " " + strings.Repeat("b", 40) + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/git/diff-bases" || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "wrong request", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	t.Setenv("DUDE_TOOLS_TOKEN", "test-token")
	old := requestTimeout
	t.Cleanup(func() { requestTimeout = old })
	var out strings.Builder
	if err := run([]string{"git", "diff-bases"}, &out); err != nil || out.String() != want {
		t.Fatalf("recorded bases %q: %v", out.String(), err)
	}
}
