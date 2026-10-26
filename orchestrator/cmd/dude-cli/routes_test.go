package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// localCommands talk to no dude server: they read or change the credentials
// file, (api) call whatever route they are given, or (upgrade) call GitHub.
var localCommands = map[string]bool{"logout": true, "orgs": true, "use": true, "api": true, "upgrade": true}

// lookupRoutes are what project, taskID and runID call to resolve a PROJECT,
// TASK or RUN argument: any command may send them on the way to its own route.
var lookupRoutes = map[string]bool{"GET /v1/projects": true, "GET /v1/tasks/:id": true}

// manifestRoute is one routes.json entry: the CLI commands that call it, or none (apiOnly).
type manifestRoute struct {
	method   string
	segments []string
	key      string
	commands []string
}

var manifest = sync.OnceValues(func() ([]manifestRoute, error) {
	raw, err := os.ReadFile("routes.json")
	if err != nil {
		return nil, err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("routes.json: %w", err)
	}
	var out []manifestRoute
	for key, v := range entries {
		method, pattern, ok := strings.Cut(key, " ")
		if !ok {
			return nil, fmt.Errorf("routes.json: %q is not METHOD PATTERN", key)
		}
		r := manifestRoute{method: method, segments: strings.Split(strings.Trim(pattern, "/"), "/"), key: key}
		var one string
		var many []string
		var apiOnly struct{ APIOnly *string }
		switch {
		case json.Unmarshal(v, &one) == nil:
			r.commands = []string{one}
		case json.Unmarshal(v, &many) == nil:
			r.commands = many
		case json.Unmarshal(v, &apiOnly) == nil && apiOnly.APIOnly != nil:
		default:
			return nil, fmt.Errorf("routes.json: %q is a command, a list of them, or {\"apiOnly\": …}", key)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
})

func (r manifestRoute) matches(method, path string) bool {
	if method != r.method {
		return false
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) != len(r.segments) {
		return false
	}
	for i, s := range r.segments {
		if !strings.HasPrefix(s, ":") && s != segs[i] {
			return false
		}
	}
	return true
}

// dispatch is the one route the server sends method and path to: of the
// matching routes, the one whose leftmost differing segment is literal, as the
// routers register them (cli-routes.test.ts fails if one shadows another).
func dispatch(routes []manifestRoute, method, path string) (manifestRoute, bool) {
	var best manifestRoute
	found := false
	for _, r := range routes {
		if r.matches(method, path) && (!found || moreSpecific(r, best)) {
			best, found = r, true
		}
	}
	return best, found
}

// moreSpecific is whether a has a literal at the first segment where a and b differ.
func moreSpecific(a, b manifestRoute) bool {
	for i := range a.segments {
		if i >= len(b.segments) {
			return false
		}
		la, lb := !strings.HasPrefix(a.segments[i], ":"), !strings.HasPrefix(b.segments[i], ":")
		if la != lb {
			return la
		}
	}
	return false
}

// recorder sends each request on and keeps a copy: method, path and body.
type recorder struct {
	next http.RoundTripper
	sent *[]call
}

func (rec recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	c := call{method: req.Method, path: req.URL.RequestURI()}
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.body)
		}
	}
	*rec.sent = append(*rec.sent, c)
	return rec.next.RoundTrip(req)
}

var (
	exercisedMu sync.Mutex
	// exercised is every "COMMAND\tMETHOD PATTERN" a test saw a command send.
	exercised = map[string]bool{}
)

// unmappedRequest is why command may not send c, or "" when routes.json maps
// c's route to it. It records the pair for TestMain's coverage check.
func unmappedRequest(t *testing.T, command string, c call) string {
	if localCommands[command] {
		return ""
	}
	routes, err := manifest()
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := strings.Cut(c.path, "?")
	r, ok := dispatch(routes, c.method, path)
	if !ok {
		return fmt.Sprintf("dude %s sent %s %s, which is no route in routes.json", command, c.method, path)
	}
	for _, name := range r.commands {
		if name == command {
			exercisedMu.Lock()
			exercised[command+"\t"+r.key] = true
			exercisedMu.Unlock()
			return ""
		}
	}
	if lookupRoutes[r.key] {
		return ""
	}
	return fmt.Sprintf("dude %s sent %s %s, but routes.json does not map %s to %s", command, c.method, path, r.key, command)
}

// TestMain checks, after a full run, that every command routes.json maps to
// a route was seen sending it.
func TestMain(m *testing.M) {
	code := m.Run()
	if f := flag.Lookup("test.run"); code == 0 && (f == nil || f.Value.String() == "") {
		routes, err := manifest()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var unseen []string
		for _, r := range routes {
			for _, name := range r.commands {
				if !exercised[name+"\t"+r.key] {
					unseen = append(unseen, fmt.Sprintf("  dude %s → %s", name, r.key))
				}
			}
		}
		if len(unseen) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: routes.json maps these commands to routes no test saw them send; add a row to routeCases (routes_test.go):\n%s\n",
				strings.Join(unseen, "\n"))
			code = 1
		}
	}
	os.Exit(code)
}

// The server sends /v1/automations/subscriptions to its literal route, not to
// /v1/automations/:id, so a command mapped only to the :id route may not send it.
func TestALiteralRouteBeatsItsParamSibling(t *testing.T) {
	if why := unmappedRequest(t, "automation", call{method: "GET", path: "/v1/automations/subscriptions"}); !strings.Contains(why, "does not map GET /v1/automations/subscriptions to automation") {
		t.Fatalf("automation sending GET /v1/automations/subscriptions: %q", why)
	}
	if why := unmappedRequest(t, "automation", call{method: "GET", path: "/v1/automations/aut_1"}); why != "" {
		t.Fatal(why)
	}
}

func TestManifestNamesOnlyCommandsThatExist(t *testing.T) {
	routes, err := manifest()
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, r := range routes {
		for _, name := range r.commands {
			named[name] = true
			if _, ok := commands[name]; !ok {
				t.Errorf("routes.json maps %s to dude %s, which is no command; fix the name or make the route apiOnly", r.key, name)
			}
		}
	}
	for name := range commands {
		if !named[name] && !localCommands[name] {
			t.Errorf("dude %s is in no routes.json entry: map the routes it calls to it, or add it to localCommands if it calls none", name)
		}
	}
}

// routeCase is one command line and the last request it must send. Bodies
// are as the route's zod schema takes them, not as the CLI happens to send them.
type routeCase struct {
	args   []string
	method string
	path   string
	body   string
}

func checkRequest(t *testing.T, c call, want routeCase) string {
	t.Helper()
	body := ""
	if c.body != nil {
		b, _ := json.Marshal(c.body)
		body = string(b)
	}
	if c.method != want.method || c.path != want.path || body != want.body {
		t.Errorf("%v: %s %s %s, want %s %s %s", want.args, c.method, c.path, body, want.method, want.path, want.body)
	}
	return body
}

// routeCases covers what the older route tables do not: every mapping in
// routes.json is seen at least once across all of them.
var routeCases = []routeCase{
	{[]string{"login", "http://placeholder"}, "GET", "/v1/me", ``},
	{[]string{"whoami"}, "GET", "/v1/me", ``},
	{[]string{"people"}, "GET", "/v1/people", ``},
	{[]string{"projects"}, "GET", "/v1/projects", ``},
	{[]string{"repos", "ABS"}, "GET", "/v1/projects/prj_1", ``},
	{[]string{"epics", "ABS"}, "GET", "/v1/projects/prj_1/epics", ``},
	{[]string{"overview", "ABS"}, "GET", "/v1/projects/prj_1/overview", ``},
	{[]string{"list", "ABS", "--status", "review"}, "GET", "/v1/tasks?projectId=prj_1&status=review", ``},
	{[]string{"show", "wi_12"}, "GET", "/v1/tasks/wi_12", ``},
	{[]string{"raw", "wi_12"}, "GET", "/v1/tasks/wi_12", ``},
	{[]string{"relations", "wi_12"}, "GET", "/v1/tasks/wi_12/relations", ``},
	{[]string{"metrics", "wi_12"}, "GET", "/v1/tasks/wi_12/metrics", ``},
	{[]string{"prs", "wi_12"}, "GET", "/v1/pull-requests?taskId=wi_12", ``},
	{[]string{"run", "run_9"}, "GET", "/v1/runs/run_9", ``},
	{[]string{"diff", "run_9"}, "GET", "/v1/runs/run_9/diff", ``},
	{[]string{"directives", "run_9"}, "GET", "/v1/runs/run_9/directives", ``},
	{[]string{"session", "ses_1"}, "GET", "/v1/sessions/ses_1", ``},
	{[]string{"questions", "wi_12"}, "GET", "/v1/questions?taskId=wi_12", ``},
	{[]string{"artifacts", "wi_12"}, "GET", "/v1/artifacts?taskId=wi_12", ``},
	{[]string{"servers", "wi_12"}, "GET", "/v1/tasks/wi_12/servers", ``},
	{[]string{"repo-requests"}, "GET", "/v1/repository-requests", ``},
	{[]string{"epic-delete", "ep_1"}, "DELETE", "/v1/epics/ep_1", ``},
	{[]string{"epic-metrics", "ep_1"}, "GET", "/v1/epics/ep_1/metrics", ``},
	{[]string{"peers", "ep_1"}, "GET", "/v1/epics/ep_1/peers", ``},
	{[]string{"threads", "ep_1"}, "GET", "/v1/epics/ep_1/threads", ``},
	{[]string{"notes", "ep_1"}, "GET", "/v1/epics/ep_1/notes", ``},
	{[]string{"note-retire", "ep_1", "2"}, "POST", "/v1/epics/ep_1/notes/2/retire", ``},
	{[]string{"auto-start", "wi_12", "--how", "talk"}, "PUT", "/v1/tasks/wi_12/auto-start", `{"how":"talk"}`},
	{[]string{"no-auto-start", "wi_12"}, "DELETE", "/v1/tasks/wi_12/auto-start", ``},
	{[]string{"relate", "wi_12", "--related", "ABS-3", "--why", "same bug"}, "POST", "/v1/tasks/wi_12/relations", `{"kind":"related","task":"ABS-3","why":"same bug"}`},
	{[]string{"done", "wi_12"}, "POST", "/v1/tasks/wi_12/done", `{}`},
	{[]string{"assign", "wi_12", "per_1"}, "PUT", "/v1/tasks/wi_12/people", `{"people":["per_1"]}`},
	{[]string{"retry", "wi_12"}, "POST", "/v1/tasks/wi_12/retry", ``},
	{[]string{"leave", "run_9"}, "POST", "/v1/runs/run_9/leave", ``},
	{[]string{"answer", "q_1", "yes"}, "POST", "/v1/questions/q_1/answer", `{"text":"yes"}`},
	{[]string{"memory", "mem_1"}, "GET", "/v1/memory/memories/mem_1", ``},
	{[]string{"memory-archive", "mem_1"}, "POST", "/v1/memory/memories/mem_1/archive", ``},
	{[]string{"memory-restore", "mem_1"}, "POST", "/v1/memory/memories/mem_1/restore", ``},
	{[]string{"update", "wi_12", "--repo", "web"}, "PATCH", "/v1/tasks/wi_12", `{"repositories":[{"access":"write","id":"rep_web"}]}`},
	{[]string{"create", "ABS", "--title", "New", "--goal", "A goal long enough to save.", "--repo", "web", "--related", "ABS-12"}, "POST", "/v1/tasks/wi_13/relations", `{"kind":"related","task":"ABS-12"}`},
}

func TestRouteCases(t *testing.T) {
	for _, tc := range routeCases {
		srv, calls := fakeDude(t)
		args, stdin := tc.args, "from stdin"
		if args[0] == "login" {
			args, stdin = []string{"login", srv.URL}, "k3y\n"
		}
		if _, err := runCLI(t, srv, stdin, args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		checkRequest(t, last(*calls), tc)
	}
}
