package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// dude prs --feedback, repeated, asks pull_requests for those items in full;
// without it, for the listing alone.
func TestDudePrsPassesFeedbackIDs(t *testing.T) {
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tools/pull_requests" {
			t.Errorf("request %s", r.URL.Path)
		}
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		asked = append(asked, args)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pullRequests":[]}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	if err := run([]string{"prs", "--feedback", "issue-comment-7", "--feedback", "line-comment-9"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"prs"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 {
		t.Fatalf("calls: %v", asked)
	}
	ids, _ := asked[0]["feedback"].([]any)
	if !slices.Equal(ids, []any{"issue-comment-7", "line-comment-9"}) {
		t.Errorf("with --feedback: %v", asked[0])
	}
	if _, ok := asked[1]["feedback"]; ok || len(asked[1]) != 0 {
		t.Errorf("without --feedback: %v", asked[1])
	}
	if err := run([]string{"prs", "issue-comment-7"}, &out); err == nil {
		t.Error("an id without --feedback is taken")
	}
}

// dude prs --refresh calls refresh_pull_requests with no arguments; with
// --feedback as well it is a usage error and calls nothing, since the
// refresh takes no ids.
func TestDudePrsRefresh(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, args)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pullRequests":[]}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	if err := run([]string{"prs", "--refresh"}, &out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"/tools/refresh_pull_requests"}) || len(bodies[0]) != 0 {
		t.Fatalf("--refresh: %v %v", paths, bodies)
	}
	err := run([]string{"prs", "--refresh", "--feedback", "issue-comment-7"}, &out)
	if err == nil || !strings.Contains(err.Error(), "usage: dude prs") {
		t.Errorf("--refresh with --feedback: %v", err)
	}
	if len(paths) != 1 {
		t.Errorf("--refresh with --feedback called %v", paths[1:])
	}
}
