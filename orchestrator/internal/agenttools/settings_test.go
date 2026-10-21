package agenttools_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/api"
)

// settingsOf is what the settings tool answers token for args.
func (f *fixture) settingsOf(t *testing.T, token, args string) map[string]any {
	t.Helper()
	var out map[string]any
	if status := f.postAs(t, token, "settings", args, &out); status != 200 {
		t.Fatalf("settings %s: %d %v", args, status, out)
	}
	return out
}

// settingsRoute is what the control plane's settings page reads for path
// (testdata/settings-route.ts), as a member of the fixture's organisation,
// over the fixture's database and an orchestrator API on it.
func (f *fixture) settingsRoute(t *testing.T, path string) map[string]any {
	t.Helper()
	orch := httptest.NewServer((&api.Server{DB: f.app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Kick: func() {}}).Handler())
	defer orch.Close()
	_, file, _, _ := runtime.Caller(0)
	cmd := exec.Command("bun", "run", "orchestrator/testdata/settings-route.ts", f.org, "psn_member", path)
	cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..", "..")
	c := f.owner.Config()
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("DATABASE_URL=postgres://dude_app:dude_app@%s:%d/%s", c.Host, c.Port, c.Database),
		"DUDE_ORCHESTRATOR_URL="+orch.URL, "DUDE_ORCHESTRATOR_TOKEN=svc")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("settings route %s: %v\n%s", path, err, stderr.Bytes())
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("settings route %s: %v: %s", path, err, out)
	}
	return body
}

// orchestratorGitHubSettings is what GET /v1/forge/settings passes through:
// the orchestrator's /internal/github-settings for the organisation.
func (f *fixture) orchestratorGitHubSettings(t *testing.T) map[string]any {
	t.Helper()
	orch := httptest.NewServer((&api.Server{DB: f.app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Kick: func() {}}).Handler())
	defer orch.Close()
	req, _ := http.NewRequest("GET", orch.URL+"/internal/github-settings", nil)
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", f.org)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode != 200 {
		t.Fatalf("github settings: %d %v", res.StatusCode, err)
	}
	return out
}

// The organisation sets maxReviewIterations (X) and maxPrFixIterations,
// which the project overrides (Y); nothing sets maxAttemptsPerFinding (Z).
// The tool says organization, project and dude with the values the policy
// resolves to, and for every key the settings page shows, the same value
// and the same project-or-not as the page; the page has no dude source, so
// its organization covers both. GitHub's settings are what GET
// /v1/forge/settings shows, the stored key the organisation's.
func TestSettingsLayersEachValueAsTheSettingsPageDoes(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `UPDATE organizations SET delivery_policy = '{"maxReviewIterations":7,"maxPrFixIterations":4}' WHERE id = $1`, f.org)
	mustExec(t, f.owner, `UPDATE projects SET delivery_policy = '{"maxPrFixIterations":6}' WHERE id = $1`, f.project)
	mustExec(t, f.owner, `INSERT INTO forge_credentials (id, organization_id, auth, secret, api_base_url, settings)
		VALUES ('fc_s', $1, 'pat', 'x', 'http://127.0.0.1:1', '{"fixRoundsPerPr":8}')`, f.org)

	out := f.settingsOf(t, token, `{"section":"delivery"}`)
	scope, _ := field(out, "settings", 0).(map[string]any)
	if field(scope, "project", "key") != "TEXT" {
		t.Errorf("the conductor's scope: %v", scope["project"])
	}
	for key, want := range map[string]struct {
		value  float64
		source string
	}{"maxReviewIterations": {7, "organization"}, "maxPrFixIterations": {6, "project"}, "maxAttemptsPerFinding": {2, "dude"}} {
		if got := field(scope, "delivery", key); field(got, "value") != want.value || field(got, "source") != want.source {
			t.Errorf("%s: %v, want %v from %s", key, got, want.value, want.source)
		}
	}
	if out["github"] != nil {
		t.Errorf("section delivery answers github: %v", out["github"])
	}

	page := f.settingsRoute(t, "/v1/projects/"+f.project+"/settings")
	pageDelivery, _ := page["delivery"].(map[string]any)
	if len(pageDelivery) == 0 {
		t.Fatalf("the settings page: %v", page)
	}
	for key, shown := range pageDelivery {
		got := field(scope, "delivery", key)
		gotJSON, _ := json.Marshal(field(got, "value"))
		wantJSON, _ := json.Marshal(field(shown, "value"))
		pageSource, toolSource := field(shown, "source"), field(got, "source")
		if !bytes.Equal(gotJSON, wantJSON) || (pageSource == "project") != (toolSource == "project") {
			t.Errorf("%s: tool %s from %v, page %s from %v", key, gotJSON, toolSource, wantJSON, pageSource)
		}
	}

	gh := field(f.settingsOf(t, token, `{"section":"github"}`), "github").(map[string]any)
	shown := f.orchestratorGitHubSettings(t)
	if len(gh) != len(shown) {
		t.Errorf("github keys: tool %d, page %d", len(gh), len(shown))
	}
	for key, value := range shown {
		gotJSON, _ := json.Marshal(field(gh, key, "value"))
		wantJSON, _ := json.Marshal(value)
		want := "dude"
		if key == "fixRoundsPerPr" {
			want = "organization"
		}
		if !bytes.Equal(gotJSON, wantJSON) && key != "reviewLogins" || field(gh, key, "source") != want {
			t.Errorf("github %s: tool %s from %v, page %s", key, gotJSON, field(gh, key, "source"), wantJSON)
		}
	}
}
