package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/automation-requests.json from what the automation commands send")

const automationRequests = "testdata/automation-requests.json"

// automationCase is one automation command line and the last request it
// must send. Bodies are as automations.ts and @dude/domain's schemas take
// them; cli-bodies.test.ts parses each recorded one with that schema.
type automationCase struct {
	args   []string
	stdin  string
	method string
	path   string
	body   string
	// scope is the automation's, for a PUT of its settings (automationInputSchema(scope)).
	scope string
}

const prEvent = `{"action":"opened","pull_request":{"number":7}}`

// fixtureSettings is testdata/automation-get.json's AutomationInput fields,
// named here from @dude/domain's baseSchema, not from the command's list.
func fixtureSettings(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/automation-get.json")
	if err != nil {
		t.Fatal(err)
	}
	var got obj
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(pick(got, "name", "description", "kind", "trigger", "sessionMode", "expiryDays", "concurrency",
		"parallelMax", "repos", "repoFilter", "agent", "machineSizeId", "imageId", "setupScript", "command", "server",
		"webhook", "secrets", "notifyOnFailure"))
	return string(b)
}

func automationCases(t *testing.T) []automationCase {
	settings := fixtureSettings(t)
	return []automationCase{
		{[]string{"automation", "list"}, "", "GET", "/v1/automations", ``, ""},
		{[]string{"automation", "list", "--scope", "project", "--trigger", "github", "--kind", "agent:reviewer", "--enabled", "true", "--mine", "--name", "triage"}, "",
			"GET", "/v1/automations?enabled=true&kind=agent%3Areviewer&owner=mine&q=triage&scope=project&trigger=github", ``, ""},
		{[]string{"automation", "list", "ABS"}, "", "GET", "/v1/projects/prj_1/automations", ``, ""},
		{[]string{"automation", "show", "aut_1"}, "", "GET", "/v1/automations/aut_1", ``, ""},
		{[]string{"automation", "create", "--name", "Morning digest", "--kind", "agent", "--trigger", "schedule", "--tz", "Europe/Lisbon"}, "",
			"POST", "/v1/automations", `{"kind":"agent","name":"Morning digest","trigger":"schedule","tz":"Europe/Lisbon"}`, ""},
		{[]string{"automation", "create", "ABS", "--name", "Review", "--kind", "agent", "--trigger", "github", "--preset", "reviewer"}, "",
			"POST", "/v1/automations", `{"kind":"agent","name":"Review","preset":"reviewer","projectId":"prj_1","trigger":"github"}`, ""},
		{[]string{"automation", "create", "--name", "Release hook", "--kind", "webhook", "--trigger", "github"}, "",
			"POST", "/v1/automations", `{"kind":"webhook","name":"Release hook","trigger":"github"}`, ""},
		{[]string{"automation", "update", "aut_1", "testdata/automation-get.json"}, "", "PUT", "/v1/automations/aut_1", settings, "organization"},
		{[]string{"automation", "enable", "aut_1"}, "", "PUT", "/v1/automations/aut_1/enabled", `{"enabled":true}`, ""},
		{[]string{"automation", "disable", "aut_1"}, "", "PUT", "/v1/automations/aut_1/enabled", `{"enabled":false}`, ""},
		{[]string{"automation", "delete", "aut_1"}, "", "DELETE", "/v1/automations/aut_1", ``, ""},
		{[]string{"automation", "duplicate", "aut_1"}, "", "POST", "/v1/automations/aut_1/duplicate", ``, ""},
		{[]string{"automation", "owner", "aut_1", "per_2"}, "", "PUT", "/v1/automations/aut_1/owner", `{"personId":"per_2"}`, ""},
		{[]string{"automation", "secret", "aut_1"}, "test-signing-secret-0123\n", "PUT", "/v1/automations/aut_1/webhook-secret",
			`{"secret":"test-signing-secret-0123"}`, ""},
		{[]string{"automation", "run", "aut_1"}, "", "POST", "/v1/automations/aut_1/runs", `{}`, ""},
		{[]string{"automation", "run", "aut_gh", "--delivery", "dlv_1"}, "", "POST", "/v1/automations/aut_gh/runs", `{"deliveryId":"dlv_1"}`, ""},
		{[]string{"automation", "run", "aut_gh", "--body", "-", "--action", "opened", "--delivery-id", "test-1"}, prEvent,
			"POST", "/v1/automations/aut_gh/runs",
			`{"testEvent":{"action":"opened","body":{"action":"opened","pull_request":{"number":7}},"deliveryId":"test-1","event":"pull_request"}}`, ""},
		{[]string{"automation", "run", "aut_gh", "--body", "-", "--action", "opened", "--delivery-id", "test-3"}, `{"pull_request":{"number":7}}`,
			"POST", "/v1/automations/aut_gh/runs",
			`{"testEvent":{"action":"opened","body":{"action":"opened","pull_request":{"number":7}},"deliveryId":"test-3","event":"pull_request"}}`, ""},
		{[]string{"automation", "run", "aut_gh", "--body", "-", "--event", "issues", "--delivery-id", "test-2"}, `{"issue":{"number":3}}`,
			"POST", "/v1/automations/aut_gh/runs", `{"testEvent":{"action":null,"body":{"issue":{"number":3}},"deliveryId":"test-2","event":"issues"}}`, ""},
		{[]string{"automation", "runs", "aut_1", "--status", "failed", "--limit", "10"}, "", "GET", "/v1/automations/aut_1/runs?limit=10&status=failed", ``, ""},
		{[]string{"automation", "run-show", "aut_1", "arn_1"}, "", "GET", "/v1/automations/aut_1/runs/arn_1", ``, ""},
		{[]string{"automation", "deliveries", "aut_gh"}, "", "GET", "/v1/automations/aut_gh/deliveries", ``, ""},
		{[]string{"automation", "test", "aut_gh", "-"}, prEvent, "POST", "/v1/automations/aut_gh/test-event/evaluate",
			`{"body":{"action":"opened","pull_request":{"number":7}}}`, ""},
		{[]string{"automation", "start-fresh", "aut_1"}, "", "POST", "/v1/automations/aut_1/start-fresh", ``, ""},
		{[]string{"automation", "prompts", "aut_1"}, "", "GET", "/v1/automations/aut_1/prompt-history", ``, ""},
		{[]string{"automation", "prompt-restore", "aut_1", "1"}, "", "POST", "/v1/automations/aut_1/prompt-history/restore", `{"versionId":"apv_1"}`, ""},
		{[]string{"automation", "prompt-restore", "aut_1", "apv_2"}, "", "POST", "/v1/automations/aut_1/prompt-history/restore", `{"versionId":"apv_2"}`, ""},
		{[]string{"automation", "repos"}, "", "GET", "/v1/automations/repositories", ``, ""},
		{[]string{"automation", "next", "0 9 * * 1-5", "--tz", "Europe/Lisbon"}, "", "POST", "/v1/automations/schedule/next",
			`{"cron":"0 9 * * 1-5","tz":"Europe/Lisbon"}`, ""},
		{[]string{"automation", "subscriptions"}, "", "GET", "/v1/automations/subscriptions", ``, ""},
	}
}

// recordedRequest is one entry of testdata/automation-requests.json.
type recordedRequest struct {
	Args  []string          `json:"args"`
	Route string            `json:"route"`
	Query map[string]string `json:"query,omitempty"`
	Body  json.RawMessage   `json:"body,omitempty"`
	Scope string            `json:"scope,omitempty"`
}

func routeKey(t *testing.T, method, path string) string {
	routes, err := manifest()
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := dispatch(routes, method, path); ok {
		return r.key
	}
	t.Fatalf("%s %s is no route in routes.json", method, path)
	return ""
}

func TestAutomationCommandsReachTheirRoutes(t *testing.T) {
	var recorded []recordedRequest
	for _, tc := range automationCases(t) {
		srv, calls := fakeDude(t)
		if _, err := runCLI(t, srv, tc.stdin, tc.args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		c := last(*calls)
		body := checkRequest(t, c, routeCase{tc.args, tc.method, tc.path, tc.body})
		path, rawQuery, _ := strings.Cut(c.path, "?")
		rec := recordedRequest{Args: tc.args, Route: routeKey(t, c.method, path), Scope: tc.scope}
		if body != "" {
			rec.Body = json.RawMessage(body)
		}
		if rawQuery != "" {
			q, _ := url.ParseQuery(rawQuery)
			rec.Query = map[string]string{}
			for k := range q {
				rec.Query[k] = q.Get(k)
			}
		}
		recorded = append(recorded, rec)
	}
	got, err := json.MarshalIndent(recorded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(automationRequests, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(automationRequests)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: the automation commands send other requests now. Regenerate it with\n"+
			"  go -C orchestrator test ./cmd/dude-cli -run TestAutomationCommandsReachTheirRoutes -update\n"+
			"and run apps/control-plane/test/cli-bodies.test.ts, which parses each body with its schema", automationRequests)
	}
}

// The documented workflow: `dude automation show ID --settings`, edited, is
// automation update's input; so is the full output, read-only fields and all.
func TestAutomationSettingsRoundTrip(t *testing.T) {
	srv, calls := fakeDude(t)
	out, err := runCLI(t, srv, "", "automation", "show", "aut_1", "--settings")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(out, "Summarise yesterday", "Summarise last week", 1)
	if _, err := runCLI(t, srv, edited, "automation", "update", "aut_1", "-"); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(fixtureSettings(t), "Summarise yesterday", "Summarise last week", 1)
	if b, _ := json.Marshal(last(*calls).body); string(b) != want {
		t.Fatalf("PUT body\n%s\nwant\n%s", b, want)
	}
	full, err := runCLI(t, srv, "", "automation", "show", "aut_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, srv, full, "automation", "update", "aut_1", "-"); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(last(*calls).body); string(b) != fixtureSettings(t) {
		t.Fatalf("PUT body from the full output\n%s\nwant\n%s", b, fixtureSettings(t))
	}
	if _, err := runCLI(t, srv, full, "automation", "update", "aut_2", "-"); err == nil || !strings.Contains(err.Error(), "aut_1's, not aut_2's") {
		t.Fatalf("another automation's settings: %v", err)
	}
}

// cli-bodies.test.ts strips testdata/automation-readonly.json's fields from
// automation-get.json and parses the rest with the PUT's schema, so the list
// there must be the one automation update strips.
func TestAutomationReadOnlyIsShared(t *testing.T) {
	raw, err := os.ReadFile("testdata/automation-readonly.json")
	if err != nil {
		t.Fatal(err)
	}
	var shared []string
	if err := json.Unmarshal(raw, &shared); err != nil {
		t.Fatal(err)
	}
	if strings.Join(shared, ",") != strings.Join(automationReadOnly, ",") {
		t.Fatalf("testdata/automation-readonly.json is %v, automationReadOnly %v: make them the same", shared, automationReadOnly)
	}
}

func TestAutomationRunTestEventGetsADeliveryID(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, prEvent, "automation", "run", "aut_gh", "--body", "-"); err != nil {
		t.Fatal(err)
	}
	te, _ := last(*calls).body["testEvent"].(map[string]any)
	if id, _ := te["deliveryId"].(string); !strings.HasPrefix(id, "test-") || te["event"] != "pull_request" {
		t.Fatalf("testEvent %v", te)
	}
}

// The orchestrator filters a test event on the body's action, so --action
// must reach the body; a body naming another action is refused.
func TestAutomationRunActionIsWrittenIntoTheBody(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, `{"pull_request":{"number":7}}`, "automation", "run", "aut_gh", "--body", "-", "--action", "opened"); err != nil {
		t.Fatal(err)
	}
	te, _ := last(*calls).body["testEvent"].(map[string]any)
	body, _ := te["body"].(map[string]any)
	if te["action"] != "opened" || body["action"] != "opened" {
		t.Fatalf("testEvent %v", te)
	}
	n := len(*calls)
	_, err := runCLI(t, srv, `{"action":"closed"}`, "automation", "run", "aut_gh", "--body", "-", "--action", "opened")
	if err == nil || !strings.Contains(err.Error(), "opened") || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("a body with another action: %v", err)
	}
	if len(*calls) != n {
		t.Fatalf("made requests after the refusal: %+v", (*calls)[n:])
	}
}

// The signing secret is read from stdin only, and never printed: not in the
// output, not in an error, not even when the server's refusal quotes it.
func TestAutomationSecretStaysOffArgvAndOutput(t *testing.T) {
	const secret = "whsec_do-not-print-me-0123456789"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = append(seen, string(raw))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(raw), "refuse") {
			w.WriteHeader(422)
			w.Write(append([]byte(`{"error":"bad secret: `), append(bytes.Trim(raw, "{}"), []byte(`"}`)...)...))
			return
		}
		io.WriteString(w, `{"id":"aut_1","name":"Hook","webhookSecretHint":"6789","webhook":{"url":"https://x"}}`)
	}))
	t.Cleanup(srv.Close)

	out, err := runCLI(t, srv, secret+"\n", "automation", "secret", "aut_1")
	if err != nil || strings.Contains(out, secret) || !strings.Contains(out, `"webhookSecretHint": "6789"`) {
		t.Fatalf("output %q, %v", out, err)
	}
	if len(seen) != 1 || seen[0] != `{"secret":"`+secret+`"}` {
		t.Fatalf("sent %q", seen)
	}

	refused := secret + "-refuse"
	out, err = runCLI(t, srv, refused+"\n", "automation", "secret", "aut_1")
	var he *httpError
	if !errors.As(err, &he) || he.status != 422 || strings.Contains(err.Error(), refused) || strings.Contains(out, refused) {
		t.Fatalf("refusal: %q %v", out, err)
	}

	seen = nil
	if _, err := runCLI(t, srv, "", "automation", "secret", "aut_1", secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("a secret on the command line: %v", err)
	}
	short := "short-secret"
	if _, err := runCLI(t, srv, short+"\n", "automation", "secret", "aut_1"); err == nil || strings.Contains(err.Error(), short) {
		t.Errorf("a short secret: %v", err)
	}
	if _, err := runCLI(t, srv, "", "automation", "secret", "aut_1"); err == nil {
		t.Error("no secret on stdin succeeded")
	}
	if len(seen) != 0 {
		t.Fatalf("sent %q", seen)
	}
}

func TestAutomationChoicesAreCheckedBeforeAnyRequest(t *testing.T) {
	srv, calls := fakeDude(t)
	for _, args := range [][]string{
		{"automation", "list", "--trigger", "cron"},
		{"automation", "list", "--kind", "robot"},
		{"automation", "list", "--kind", "command:own"},
		{"automation", "list", "--kind", "agent:conductor"},
		{"automation", "list", "--enabled", "yes"},
		{"automation", "list", "--scope", "team"},
		{"automation", "list", "ABS", "--mine"},
		{"automation", "create", "--name", "x", "--kind", "agent"},
		{"automation", "create", "--name", "x", "--kind", "bot", "--trigger", "github"},
		{"automation", "create", "--name", "x", "--kind", "agent", "--trigger", "push"},
		{"automation", "create", "--name", "x", "--kind", "command", "--trigger", "github", "--preset", "reviewer"},
		{"automation", "create", "--name", "x", "--kind", "agent", "--trigger", "github", "--preset", "implementer"},
		{"automation", "runs", "aut_1", "--status", "done"},
		{"automation", "runs", "aut_1", "--limit", "500"},
		{"automation", "run", "aut_1", "--event", "push"},
		{"automation", "run", "aut_1", "--delivery", "d", "--body", "-"},
		{"automation", "update", "aut_1", "testdata/nope.json"},
		{"automation", "test", "aut_1", "-"},
	} {
		if _, err := runCLI(t, srv, "not json", args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("made requests: %+v", *calls)
	}
}
