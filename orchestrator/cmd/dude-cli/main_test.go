package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// call is one request the fake server saw.
type call struct {
	method, path string
	body         map[string]any
	auth         string
}

// fakeDude answers the routes the CLI resolves names through, and records
// every request.
func fakeDude(t *testing.T) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c := call{method: r.Method, path: r.URL.RequestURI(), auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.body)
		}
		calls = append(calls, c)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case c.auth == "Bearer bad-key":
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
		case r.URL.Path == "/v1/me":
			io.WriteString(w, `{"organization":{"id":"org_1","slug":"acme","name":"Acme"},"person":{"id":"per_1","name":"Ana","role":"admin","lastSeenAt":"x"}}`)
		case r.URL.Path == "/v1/projects" && r.Method == "GET":
			io.WriteString(w, `{"projects":[{"id":"prj_1","key":"ABS","slug":"abs","name":"ABsmartly"}]}`)
		case r.URL.Path == "/v1/projects/prj_1":
			io.WriteString(w, `{"id":"prj_1","repositories":[{"id":"rep_web","name":"web","url":"u","defaultBranch":"main"},{"id":"rep_api","name":"api","url":"v","defaultBranch":"main"}]}`)
		case r.URL.Path == "/v1/tasks" && r.Method == "GET":
			io.WriteString(w, `{"tasks":[{"id":"wi_12","key":"ABS-12","title":"T","status":"running"}]}`)
		case r.URL.Path == "/v1/tasks" && r.Method == "POST":
			io.WriteString(w, `{"id":"wi_13","key":"ABS-13","title":"New","status":"received","projectId":"prj_1"}`)
		case r.URL.Path == "/v1/tasks/wi_12" && r.Method == "GET":
			io.WriteString(w, `{"id":"wi_12","key":"ABS-12","title":"T","status":"running","projectId":"prj_1",
				"owner":{"id":"usr_1","name":"Ana"},"bulky":"x",
				"runs":[{"id":"run_new","status":"running","extra":1},{"id":"run_old"}]}`)
		case r.URL.Path == "/v1/nope":
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"no route for GET /v1/nope"}`)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func runCLI(t *testing.T, srv *httptest.Server, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	c := &cli{
		creds: func(string) (string, string, error) { return srv.URL, "k3y", nil },
		http:  srv.Client(),
		stdin: strings.NewReader(stdin),
		out:   &out,
	}
	err := c.run(args)
	return out.String(), err
}

func last(calls []call) call { return calls[len(calls)-1] }

func TestShowResolvesKeyAndBriefs(t *testing.T) {
	srv, calls := fakeDude(t)
	out, err := runCLI(t, srv, "", "show", "abs-12")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err, out)
	}
	if got["owner"] != "Ana" || got["bulky"] != nil || got["key"] != "ABS-12" {
		t.Fatalf("brief: %v", got)
	}
	runs := got["runs"].([]any)
	if r := runs[0].(map[string]any); r["extra"] != nil || r["id"] != "run_new" {
		t.Fatalf("runs: %v", runs)
	}
	if a := (*calls)[0].auth; a != "Bearer k3y" {
		t.Fatalf("auth header %q", a)
	}
}

func TestCreateWithReposRelationsAndStart(t *testing.T) {
	srv, calls := fakeDude(t)
	_, err := runCLI(t, srv, "the goal, long enough to pass",
		"create", "ABS", "--title", "New", "--goal", "-", "--criterion", "a", "--criterion", "b",
		"--repo", "api:read", "--blocked-by", "ABS-12", "--start")
	if err != nil {
		t.Fatal(err)
	}
	var create, rel, start *call
	for i := range *calls {
		c := &(*calls)[i]
		switch {
		case c.method == "POST" && c.path == "/v1/tasks":
			create = c
		case c.path == "/v1/tasks/wi_13/relations":
			rel = c
		case c.path == "/v1/tasks/wi_13/deliver":
			start = c
		}
	}
	if create == nil || rel == nil || start == nil {
		t.Fatalf("calls: %+v", *calls)
	}
	if create.body["goal"] != "the goal, long enough to pass" || create.body["projectId"] != "prj_1" {
		t.Fatalf("create body: %v", create.body)
	}
	repos := create.body["repositories"].([]any)
	if r := repos[0].(map[string]any); r["id"] != "rep_api" || r["access"] != "read" {
		t.Fatalf("repos: %v", repos)
	}
	if rel.body["kind"] != "blocked_by" || rel.body["task"] != "ABS-12" {
		t.Fatalf("relation: %v", rel.body)
	}
}

func TestCreateRefusesUnknownRepo(t *testing.T) {
	srv, _ := fakeDude(t)
	_, err := runCLI(t, srv, "", "create", "ABS", "--title", "x", "--goal", "a goal of sixteen+", "--repo", "nope")
	if err == nil || !strings.Contains(err.Error(), "it has: api, web") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRefIsTasksNewestRun(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "steer", "ABS-12", "look at X", "--interrupt"); err != nil {
		t.Fatal(err)
	}
	c := last(*calls)
	if c.path != "/v1/runs/run_new/steer" || c.body["text"] != "look at X" || c.body["interrupt"] != true {
		t.Fatalf("steer: %+v", c)
	}
}

func TestUpdateEpicNoneSendsNull(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "update", "wi_12", "--epic", "none"); err != nil {
		t.Fatal(err)
	}
	c := last(*calls)
	if v, ok := c.body["epicId"]; c.method != "PATCH" || !ok || v != nil {
		t.Fatalf("update: %+v", c)
	}
}

func TestAnswerWithChoices(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "answer", "q_1", "because", "--choice", "0", "--choice", "2"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(last(*calls).body)
	if want := `{"answers":[{"choices":[0,2],"text":"because"}],"text":"because"}`; string(b) != want {
		t.Fatalf("body %s, want %s", b, want)
	}
}

func TestRelateNeedsExactlyOne(t *testing.T) {
	srv, _ := fakeDude(t)
	if _, err := runCLI(t, srv, "", "relate", "wi_12"); err == nil {
		t.Fatal("relate with no kind succeeded")
	}
}

func TestAPIPassesErrorsThrough(t *testing.T) {
	srv, _ := fakeDude(t)
	_, err := runCLI(t, srv, "", "api", "get", "v1/nope")
	var he *httpError
	if !errors.As(err, &he) || he.status != 404 || !strings.Contains(he.body, "no route") {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIBodyFromStdin(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, `{"a":1}`, "api", "POST", "/v1/x", "-"); err != nil {
		t.Fatal(err)
	}
	if c := last(*calls); c.method != "POST" || c.body["a"] != float64(1) {
		t.Fatalf("api: %+v", c)
	}
}

func TestArgumentCountIsChecked(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "show"); err == nil {
		t.Fatal("show without TASK succeeded")
	}
	if _, err := runCLI(t, srv, "", "frobnicate"); err == nil {
		t.Fatal("unknown command succeeded")
	}
	if len(*calls) != 0 {
		t.Fatalf("made requests: %+v", *calls)
	}
}
