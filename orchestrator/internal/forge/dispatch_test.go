package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// A workflow's on: reads as the events it names, in each of YAML's forms;
// a file with no on: is not read as having none.
func TestWorkflowTriggersReadsEveryFormOfOn(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want []string
		ok   bool
	}{
		{"on: workflow_dispatch\n", []string{"workflow_dispatch"}, true},
		{"on: [push, workflow_dispatch]\n", []string{"push", "workflow_dispatch"}, true},
		{"on:\n  pull_request:\n    branches: [main]\n  workflow_dispatch:\n    inputs:\n      x: {}\n", []string{"pull_request", "workflow_dispatch"}, true},
		{"on:\n  schedule:\n    - cron: '0 0 * * *'\n", []string{"schedule"}, true},
		{"name: x\njobs: {}\n", nil, false},
		{"on: [unclosed\n", nil, false},
	} {
		got, ok := WorkflowTriggers([]byte(c.raw))
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("%q: %v %v, want %v %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

// A dispatch posts the ref and inputs to the workflow's dispatches and
// takes GitHub's 204; the dispatched runs are that workflow's, branch's and
// event's since a time, and a run of another event or branch GitHub
// answers anyway is left out.
func TestDispatchAndItsRunsAsGitHubAnswers(t *testing.T) {
	var posted map[string]any
	var listed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/repos/acme/api/actions/workflows/deploy.yml/dispatches":
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(204)
		case r.URL.Path == "/repos/acme/api/actions/workflows/deploy.yml/runs":
			listed = r.URL.RawQuery
			fmt.Fprint(w, `{"workflow_runs":[
				{"id":9,"event":"workflow_dispatch","head_branch":"dude/t","status":"queued","conclusion":null,"run_attempt":1,"created_at":"2026-11-12T10:00:05Z"},
				{"id":8,"event":"push","head_branch":"dude/t","status":"completed","conclusion":"success","run_attempt":1,"created_at":"2026-11-12T10:00:06Z"},
				{"id":7,"event":"workflow_dispatch","head_branch":"main","status":"queued","run_attempt":1,"created_at":"2026-11-12T10:00:07Z"},
				{"id":6,"event":"workflow_dispatch","head_branch":"dude/t","status":"queued","run_attempt":1,"created_at":"2026-11-12T09:00:00Z"}]}`)
		case r.URL.Path == "/repos/acme/api/actions/runs/9":
			fmt.Fprint(w, `{"id":9,"status":"completed","conclusion":"failure","run_attempt":2,"html_url":"https://x/9"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL})
	if err := gh.DispatchWorkflow(context.Background(), "acme/api", "deploy.yml", "dude/t", map[string]any{"env": "staging"}); err != nil {
		t.Fatal(err)
	}
	if posted["ref"] != "dude/t" || posted["inputs"].(map[string]any)["env"] != "staging" {
		t.Fatalf("posted %v", posted)
	}
	since := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	runs, err := gh.DispatchedRuns(context.Background(), "acme/api", "deploy.yml", "dude/t", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != 9 || runs[0].Status != "queued" {
		t.Fatalf("runs %+v", runs)
	}
	if listed != "branch=dude%2Ft&created=%3E%3D2026-11-12T10%3A00%3A00Z&event=workflow_dispatch&per_page=20" {
		t.Errorf("listed with %s", listed)
	}
	st, err := gh.RunStateOf(context.Background(), "acme/api", 9)
	if err != nil || st.Conclusion != "failure" || st.Attempt != 2 || st.URL != "https://x/9" {
		t.Fatalf("state %+v %v", st, err)
	}
}
