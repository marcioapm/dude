package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/automations"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Run now and the test-event check through the real handlers and runner:
// each refusal with its status and error, and the accepted run.
func TestAutomationRunNowAndEvaluateRefusals(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1 || '_p', $1, 'web', 'web', 'WEB')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ($1 || '_r', $1, $1 || '_p', 'web', 'https://github.com/acme/web.git')`,
		`INSERT INTO people (id, organization_id, name, role) VALUES ($1 || '_ana', $1, 'Ana', 'admin')`,
		`INSERT INTO automations (id, organization_id, project_id, name, kind, enabled, trigger, command) VALUES
			('aut_api_ev', $1, $1 || '_p', 'Triage', 'command', true,
			 '{"kind":"github","event":"pull_request","actions":["opened"],"filters":{"author":["!dependabot[bot]"]}}', 'true'),
			('aut_api_sched', $1, $1 || '_p', 'Nightly', 'command', true, '{"kind":"schedule","cron":"0 3 * * *","tz":"UTC"}', 'true')`,
		`INSERT INTO webhook_deliveries (id, organization_id, event, payload, automations_at) VALUES
			('d-api', $1, 'pull_request', '{"action":"opened","repository":{"full_name":"acme/web"},"sender":{"login":"ana"},"pull_request":{"number":3}}', now())`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := &automations.Runner{DB: app, Log: log}
	s := &Server{DB: app, Token: "svc", Log: log, Automations: runner}
	call := func(s *Server, path, body string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer svc")
		r.Header.Set("X-Dude-Organization", org)
		r.Header.Set("X-Dude-Person", org+"_ana")
		r.Header.Set("X-Dude-Actor", org+"_ana")
		r.Header.Set("X-Dude-Credential-Kind", "person")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	errOf := func(out map[string]any) string {
		e, _ := out["error"].(map[string]any)
		code, _ := e["code"].(string)
		msg, _ := e["message"].(string)
		return code + ": " + msg
	}
	pr := func(sender string) string {
		return `{"action":"opened","repository":{"full_name":"acme/web"},"sender":{"login":"` + sender + `"},"pull_request":{"number":7}}`
	}
	run := "/internal/automations/aut_api_ev/runs"
	evaluate := "/internal/automations/aut_api_ev/test-event/evaluate"
	for _, c := range []struct {
		name, path, body string
		status           int
		err              string
	}{
		{"filtered test event", run, `{"testEvent":{"event":"pull_request","deliveryId":"test-1","body":` + pr("dependabot[bot]") + `}}`,
			422, "filtered: it would be skipped because of author: it is by dependabot[bot], who is excluded"},
		{"array body", run, `{"testEvent":{"event":"pull_request","body":[1]}}`, 400, "bad_request: a test event's body is a JSON object"},
		{"null body", run, `{"testEvent":{"event":"pull_request","body":null}}`, 400, "bad_request: a test event's body is a JSON object"},
		{"string body", run, `{"testEvent":{"event":"pull_request","body":"x"}}`, 400, "bad_request: a test event's body is a JSON object"},
		{"invalid JSON", run, `{"testEvent":`, 400, "bad_request: invalid JSON body: unexpected EOF"},
		{"no event", run, `{}`, 400, "bad_request: bad request: an event automation runs on a past delivery or a test event"},
		{"unknown delivery", run, `{"deliveryId":"d-none"}`, 404, "not_found: not found: no stored delivery d-none"},
		{"unknown automation", "/internal/automations/aut_none/runs", `{}`, 404, "not_found: no automation aut_none"},
		{"schedule given an event", "/internal/automations/aut_api_sched/runs", `{"deliveryId":"d-api"}`, 400,
			"bad_request: bad request: a scheduled automation runs on no event"},
		{"evaluate a schedule", "/internal/automations/aut_api_sched/test-event/evaluate", `{"body":{}}`, 400,
			"bad_request: a scheduled automation takes no event"},
		{"evaluate a non-object", evaluate, `{"body":[1]}`, 400, "bad_request: a test event's body is a JSON object"},
		{"evaluate an unknown automation", "/internal/automations/aut_none/test-event/evaluate", `{"body":{}}`, 404, "not_found: no automation aut_none"},
	} {
		status, out := call(s, c.path, c.body)
		if status != c.status || !strings.HasPrefix(errOf(out), c.err) {
			t.Errorf("%s: %d %q, want %d %q", c.name, status, errOf(out), c.status, c.err)
		}
	}
	if status, out := call(s, run, `{"testEvent":{"event":"pull_request","body":`+pr("dependabot[bot]")+`}}`); out["verdict"] == nil || status != 422 {
		t.Errorf("a filtered event's verdict: %d %v", status, out)
	}
	if status, out := call(s, evaluate, `{"body":`+pr("dependabot[bot]")+`}`); status != 200 || out["matches"] != false || out["filter"] != "author" {
		t.Errorf("evaluate filtered: %d %v", status, out)
	}
	if status, out := call(s, evaluate, `{"body":`+pr("ana")+`}`); status != 200 || out["matches"] != true {
		t.Errorf("evaluate matching: %d %v", status, out)
	}
	if status, out := call(s, run, `{"deliveryId":"d-api"}`); status != 202 || !strings.HasPrefix(out["runId"].(string), "arn_") {
		t.Errorf("a past delivery: %d %v", status, out)
	}
	off := &Server{DB: app, Token: "svc", Log: log}
	for _, path := range []string{run, evaluate} {
		if status, out := call(off, path, `{"body":{}}`); status != 503 || errOf(out) != "unavailable: automations are not running here" {
			t.Errorf("%s with no runner: %d %v", path, status, out)
		}
	}

	// A schedule's next three times, as the scheduler computes them, in its zone.
	status, out := call(s, "/internal/automations/schedule/next", `{"cron":"0 3 * * *","tz":"Europe/Lisbon"}`)
	times, _ := out["times"].([]any)
	if status != 200 || len(times) != 3 {
		t.Fatalf("next: %d %v", status, out)
	}
	lisbon, _ := time.LoadLocation("Europe/Lisbon")
	var prev time.Time
	for _, raw := range times {
		at, err := time.Parse(time.RFC3339, raw.(string))
		if err != nil || at.In(lisbon).Hour() != 3 || at.In(lisbon).Minute() != 0 || !at.After(prev) {
			t.Errorf("next time %v %v", raw, err)
		}
		prev = at
	}
	if status, out := call(s, "/internal/automations/schedule/next", `{"cron":"0 3 * *","tz":"UTC"}`); status != 400 ||
		errOf(out) != "bad_request: a schedule is five fields: minute hour day-of-month month day-of-week" {
		t.Errorf("a bad cron: %d %v", status, out)
	}
}
