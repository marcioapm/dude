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
	if run, err := gh.DispatchWorkflow(context.Background(), "acme/api", "deploy.yml", "dude/t", map[string]any{"env": "staging"}); err != nil || run.ID != 0 {
		t.Fatal(run, err)
	}
	if posted["ref"] != "dude/t" || posted["inputs"].(map[string]any)["env"] != "staging" || posted["return_run_details"] != true {
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

// Asked for run details, a current GitHub answers 200 with the run the
// dispatch created: its id is taken as it is.
func TestADispatchTakesTheRunGitHubAnswersWith(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"workflow_run_id":4242,"run_url":"https://api/x/runs/4242","html_url":"https://x/runs/4242"}`)
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL})
	run, err := gh.DispatchWorkflow(context.Background(), "acme/api", "ci.yml", "dude/t", nil)
	if err != nil || run.ID != 4242 || run.URL != "https://x/runs/4242" {
		t.Fatalf("%+v %v", run, err)
	}
}

// A workflow is the task's own CI only when it runs for that change: a
// push to its branch or a pull request into its base, as its filters say.
// A deploy on push to main, a release on tags and a schedule are not; a
// filter dude does not evaluate is "cannot tell", never yes.
func TestWorkflowRunsForChangeReadsTheFilters(t *testing.T) {
	for _, c := range []struct {
		raw, want string
	}{
		{"on: [push, workflow_dispatch]\n", ActionsExpected},
		{"on:\n  pull_request:\n  workflow_dispatch:\n", ActionsExpected},
		{"on:\n  push:\n    branches: [main]\n  workflow_dispatch:\n", ActionsNotExpected},
		{"on:\n  push:\n    tags: ['v*']\n  workflow_dispatch:\n", ActionsNotExpected},
		{"on:\n  schedule:\n    - cron: '0 3 * * *'\n  workflow_dispatch:\n", ActionsNotExpected},
		{"on: workflow_dispatch\n", ActionsNotExpected},
		{"on:\n  push:\n    branches: ['dude/**']\n", ActionsExpected},
		{"on:\n  push:\n    branches: ['dude/*']\n", ActionsNotExpected},
		{"on:\n  pull_request:\n    branches: [main]\n", ActionsExpected},
		{"on:\n  pull_request:\n    branches: [develop]\n", ActionsNotExpected},
		{"on:\n  pull_request:\n    paths: ['src/**']\n", ""},
		{"on:\n  push:\n    branches: ['dude/[ab]*']\n", ""},
		{"on:\n  push:\n    branches-ignore: [main]\n", ""},
		{"on:\n  push:\n    branches: [main]\n  pull_request:\n    paths: [x]\n", ""},
		{"on:\n  push:\n    branches: [main]\n  pull_request:\n", ActionsExpected},
		{"on: [unclosed\n", ""},
	} {
		if got := WorkflowRunsForChange([]byte(c.raw), "dude/t/task-1", "main"); got != c.want {
			t.Errorf("%q: %q, want %q", c.raw, got, c.want)
		}
	}
}
