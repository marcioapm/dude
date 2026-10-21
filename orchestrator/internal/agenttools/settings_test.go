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
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
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
	return f.settingsRouteWith(t, path, nil, nil)
}

// settingsRouteWith is settingsRoute with the orchestrator API given an
// embedder and an indexer, for the memory index page.
func (f *fixture) settingsRouteWith(t *testing.T, path string, e embeddings.Embedder, indexer interface {
	Health() memory.Health
	Resume()
}) map[string]any {
	t.Helper()
	orch := httptest.NewServer((&api.Server{DB: f.app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Kick: func() {}, Embedder: e, Indexer: indexer}).Handler())
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
	recordRoute(path, body)
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
	mustExec(t, f.owner, `UPDATE organizations SET delivery_policy = '{"maxReviewIterations":7,"maxPrFixIterations":4,"conductorWarmMinutes":7}' WHERE id = $1`, f.org)
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
	if status, got := f.post(t, token, "settings", `{"section":"bogus"}`); status != 422 ||
		!strings.Contains(fmt.Sprint(got["error"]), "delivery, roles, prompts, github, network, capabilities, machines") {
		t.Errorf("section bogus: %d %v", status, got)
	}
	roles := f.settingsOf(t, token, `{"section":"roles"}`)
	if s := field(roles, "settings", 0).(map[string]any); s["delivery"] != nil || s["prompts"] != nil || roles["capabilities"] != nil ||
		s["roles"] == nil {
		t.Errorf("section roles: %v", s)
	}
	// dude's park and idle defaults may be the deployment's; a set one is not.
	for key, noted := range map[string]bool{"parkAfterMinutes": true, "idleNudgeMinutes": true, "conductorWarmMinutes": false,
		"maxAttemptsPerFinding": false} {
		if note, _ := field(scope, "delivery", key, "note").(string); (note != "") != noted {
			t.Errorf("%s: note %q", key, note)
		}
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

	// A project's own reviewer rules are its, their globs people's text.
	mustExec(t, f.owner, `UPDATE projects SET delivery_policy = '{"conditionalReviewers":[{"category":"database","whenPathsMatch":["db/**"]}]}'
		WHERE id = $1`, f.project)
	rules := field(f.settingsOf(t, token, `{"section":"delivery"}`), "settings", 0, "delivery", "conditionalReviewers")
	if glob, _ := field(rules, "value", 0, "whenPathsMatch", 0).(string); field(rules, "source") != "project" ||
		!strings.HasPrefix(glob, "⟦external ") || !strings.Contains(glob, "⟧db/**⟦/external ") {
		t.Errorf("a project's conditionalReviewers: %v", rules)
	}
}

// roleSettingsWorld is an organisation whose implementer runs on tier Big
// at 90 minutes on size L in image web-img, with notes; the project moves
// the reviewer to tier Small and gives it notes, and the fixer has nothing
// of its own. It has a GitHub credential, a webhook secret, an OAuth
// client secret, a project secret, and saved prompts.
func roleSettingsWorld(t *testing.T) (*fixture, string) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO model_tiers (id, organization_id, name, model, headers, options) VALUES
		('mtr_big', $1, 'Big', 'big-model', '{"X-Api-Key":"TIER-HEADER-SECRET"}', '{"apiKey":"TIER-OPTIONS-SECRET"}'),
		('mtr_small', $1, 'Small', 'small-model', NULL, NULL)`, f.org)
	mustExec(t, f.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES
		('msz_l', $1, 'L', 8, 16384, 50)`, f.org)
	mustExec(t, f.owner, `INSERT INTO images (id, organization_id, name) VALUES ('img_web', $1, 'web-img')`, f.org)
	mustExec(t, f.owner, `UPDATE organizations SET default_agent_models = $2::jsonb, agent_egress = '{api.example.com}' WHERE id = $1`, f.org,
		`{"implementer":{"tier":"mtr_big","timeLimitMinutes":90,"machineSize":"msz_l","image":"img_web","context":"ORG-NOTES-SECRET-TEXT"}}`)
	mustExec(t, f.owner, `UPDATE projects SET agent_models = $2::jsonb, agent_egress = '{docs.example.com}', agent_egress_mode = 'only'
		WHERE id = $1`, f.project, `{"reviewer":{"tier":"mtr_small","context":"PROJECT-NOTES-SECRET-TEXT"}}`)
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name) VALUES ('psn_ana', $1, 'Ana')`, f.org)
	mustExec(t, f.owner, `INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, created_by, created_at) VALUES
		('pv_1', $1, NULL, 'implementer', NULL, 'ORG-PROMPT-BODY-TEXT', 'psn_ana', '2026-01-02T03:04:05Z'),
		('pv_2', $1, $2, 'reviewer', 'replace', 'PROJECT-PROMPT-BODY-TEXT', 'psn_ana', '2026-02-03T04:05:06Z'),
		('pv_3', $1, $2, 'implementer', 'add', '  ', 'psn_ana', '2026-02-03T04:05:06Z')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO forge_credentials (id, organization_id, auth, secret, api_base_url, webhook_secret, oauth_client_id,
			oauth_client_secret) VALUES ('fc_r', $1, 'pat', 'GH-CREDENTIAL-SECRET', 'http://127.0.0.1:1', 'WEBHOOK-SECRET-VALUE', 'client',
			'OAUTH-CLIENT-SECRET-VALUE')`, f.org)
	mustExec(t, f.owner, `INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients)
		VALUES ($1, $2, 'DB_PASSWORD', 'PROJECT-SECRET-VALUE', 'HINT-SECRET', '{implementer,previews}')`, f.project, f.org)
	return f, token
}

// roleIn is the tool's row for role in its first scope's roles.
func roleIn(out map[string]any, role string) map[string]any {
	roles, _ := field(out, "settings", 0, "roles").([]any)
	for _, r := range roles {
		if m, _ := r.(map[string]any); m["role"] == role {
			return m
		}
	}
	return nil
}

// Each role's tier (with the model it resolves to), harness, time limit,
// machine size and image, with the layer each is set in; the fixer's
// taken from the implementer says so. What the settings page shows of
// each role (/v1/projects/:id/settings) agrees on the ids and on
// project-or-not. Notes say only that they are set.
func TestSettingsRolesAreResolvedAsTheSettingsPageResolvesThem(t *testing.T) {
	f, token := roleSettingsWorld(t)
	out := f.settingsOf(t, token, `{"section":"roles"}`)
	impl, rev, fixer := roleIn(out, "implementer"), roleIn(out, "reviewer"), roleIn(out, "fixer")
	if field(impl, "tier", "source") != "organization" || field(impl, "tier", "value", "id") != "mtr_big" ||
		!strings.Contains(field(impl, "tier", "value", "model").(string), "big-model") ||
		field(impl, "timeLimitMinutes", "value") != 90.0 || field(impl, "timeLimitMinutes", "source") != "organization" ||
		field(impl, "machineSize", "value", "id") != "msz_l" || field(impl, "image", "value", "id") != "img_web" ||
		field(impl, "harness", "value") != "opencode" || field(impl, "harness", "source") != "dude" || impl["projectNotes"] != false {
		t.Errorf("implementer: %v", impl)
	}
	if field(rev, "tier", "source") != "project" || field(rev, "tier", "value", "id") != "mtr_small" || rev["projectNotes"] != true ||
		field(rev, "timeLimitMinutes", "value") != 120.0 || field(rev, "timeLimitMinutes", "source") != "dude" {
		t.Errorf("reviewer: %v", rev)
	}
	if field(fixer, "tier", "value", "id") != "mtr_big" || field(fixer, "tier", "followsImplementer") != true ||
		field(fixer, "machineSize", "followsImplementer") != true || field(fixer, "image", "followsImplementer") != true ||
		field(fixer, "tier", "source") != "organization" || field(fixer, "machineSize", "source") != "organization" {
		t.Errorf("fixer: %v", fixer)
	}
	if field(impl, "label") != "Implementer" || field(roleIn(out, "qa_browser"), "label") != "Tester" {
		t.Errorf("labels: %v %v", impl["label"], roleIn(out, "qa_browser")["label"])
	}
	// The project's secrets come with its roles.
	if field(out, "settings", 0, "secrets", 0, "name") != "DB_PASSWORD" {
		t.Errorf("section roles' secrets: %v", field(out, "settings", 0, "secrets"))
	}

	page := f.settingsRoute(t, "/v1/projects/"+f.project+"/settings")
	sameRolesAsThePage(t, out, page)
}

// sameRolesAsThePage checks the tool's roles in out against the settings
// page's: the same ids and values, and project-or-not alike.
func sameRolesAsThePage(t *testing.T, out, page map[string]any) {
	t.Helper()
	for role, shown := range page["roles"].(map[string]any) {
		got := roleIn(out, role)
		if got == nil {
			t.Errorf("%s: on the page, not in the tool", role)
			continue
		}
		for key, id := range map[string][]any{"tier": {"tier", "value", "id"}, "machineSize": {"machineSize", "value", "id"},
			"image": {"image", "value", "id"}, "harness": {"harness", "value"}, "timeLimitMinutes": {"timeLimitMinutes", "value"}} {
			pageValue, toolValue := field(shown, key, "value"), field(got, id...)
			if key == "machineSize" && pageValue == nil {
				// The page's null is the organisation's default size.
				if field(got, "machineSize", "source") != "dude" {
					t.Errorf("%s machineSize: page default, tool %v", role, field(got, "machineSize"))
				}
				continue
			}
			if key == "timeLimitMinutes" && pageValue == nil {
				pageValue = 120.0
			}
			if pageValue != toolValue {
				t.Errorf("%s %s: page %v, tool %v", role, key, pageValue, toolValue)
			}
			pageProject := field(shown, key, "source") == "project" && field(shown, key, "followsImplementer") != true
			if pageProject != (field(got, key, "source") == "project") && field(got, key, "followsImplementer") != true {
				t.Errorf("%s %s: page from %v, tool from %v", role, key, field(shown, key, "source"), field(got, key, "source"))
			}
		}
	}
}

// A brainstorm's row is what its Runs are built with: the organisation's
// Brainstorm setting with the session's own choice on top, never a linked
// project's roles.brainstorm, image or prompt.
func TestABrainstormsRowIsItsSessionsOverTheOrganisations(t *testing.T) {
	f, _ := roleSettingsWorld(t)
	token := f.session(t, "ssn_tier", "", "")
	mustExec(t, f.owner, `UPDATE sessions SET tier = 'mtr_big' WHERE id = 'ssn_tier'`)
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_tier', $1, $2)`, f.project, f.org)
	mustExec(t, f.owner, `UPDATE projects SET agent_models = agent_models || $2::jsonb WHERE id = $1`, f.project,
		`{"brainstorm":{"tier":"mtr_small","harness":"codex","machineSize":"msz_l","image":"img_web","timeLimitMinutes":30}}`)
	mustExec(t, f.owner, `INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, created_by, created_at)
		VALUES ('pv_b', $1, $2, 'brainstorm', 'replace', 'x', 'psn_ana', '2026-02-03T04:05:06Z')`, f.org, f.project)

	out := f.settingsOf(t, token, `{}`)
	if field(out, "settings", 0, "project", "key") != "TEXT" {
		t.Fatalf("the brainstorm's scope: %v", field(out, "settings", 0, "project"))
	}
	b := roleIn(out, "brainstorm")
	if field(b, "tier", "value", "id") != "mtr_big" || field(b, "tier", "source") != "session" ||
		field(b, "harness", "value") != "opencode" || field(b, "harness", "source") != "dude" ||
		field(b, "timeLimitMinutes", "source") != "dude" || field(b, "machineSize", "source") != "dude" ||
		field(b, "image", "source") != "dude" || b["projectNotes"] != nil {
		t.Errorf("brainstorm: %v", b)
	}
	if m := field(f.settingsOf(t, token, `{"section":"machines"}`), "settings", 0, "machines", "brainstorm", "source"); m != "dude" {
		t.Errorf("brainstorm's machine: %v", m)
	}
	for _, p := range field(out, "settings", 0, "prompts").([]any) {
		if m := p.(map[string]any); m["role"] == "brainstorm" && (m["project"] != nil || m["projectSaved"] != nil) {
			t.Errorf("brainstorm's prompt: %v", m)
		}
	}
	// The other roles still read the project.
	if field(roleIn(out, "reviewer"), "tier", "source") != "project" {
		t.Errorf("reviewer: %v", roleIn(out, "reviewer"))
	}
	// The size the brainstorm's agent raised its session to.
	mustExec(t, f.owner, `UPDATE sessions SET agent_machine = '{"sizeId":"msz_l","moveId":"mmv_1","askedBy":"run_b","reason":"r",
		"at":"2026-03-04T05:06:07Z"}' WHERE id = 'ssn_tier'`)
	if m := field(roleIn(f.settingsOf(t, token, `{}`), "brainstorm"), "machineSize"); field(m, "value", "id") != "msz_l" ||
		field(m, "source") != "session" {
		t.Errorf("a raised brainstorm's machine: %v", m)
	}
}

// A brainstorm's investigator row is the investigator it starts
// (sessionChildSpec): the organisation's agent models and the session's
// raised size, never a linked project's. A conductor's is its project's.
func TestASessionsInvestigatorRowIsTheOneItsBrainstormStarts(t *testing.T) {
	f, conductor := roleSettingsWorld(t)
	token := f.session(t, "ssn_inv", "", "")
	mustExec(t, f.owner, `UPDATE sessions SET tier = 'mtr_big' WHERE id = 'ssn_inv'`)
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_inv', $1, $2)`, f.project, f.org)
	mustExec(t, f.owner, `UPDATE projects SET agent_models = agent_models || $2::jsonb WHERE id = $1`, f.project,
		`{"investigator":{"tier":"mtr_small","harness":"codex","machineSize":"msz_l","image":"img_web","timeLimitMinutes":30}}`)

	out := f.settingsOf(t, token, `{"section":"prompts"}`)
	for _, p := range field(out, "settings", 0, "prompts").([]any) {
		if m := p.(map[string]any); m["role"] == "investigator" {
			if m["organization"] != "per-run" || m["note"] != "per-run child prompt, not saved org/project prompts" || m["organizationSaved"] != nil || m["project"] != nil || m["projectSaved"] != nil {
				t.Errorf("session investigator prompt: %v", m)
			}
		}
	}
	inv := roleIn(f.settingsOf(t, token, `{}`), "investigator")
	if field(inv, "timeLimitMinutes", "value") != nil || field(inv, "timeLimitMinutes", "note") != "not enforced for session investigators" {
		t.Errorf("session investigator no-progress limit: %v", inv["timeLimitMinutes"])
	}
	for _, key := range []string{"tier", "harness", "timeLimitMinutes", "machineSize", "image"} {
		if s := field(inv, key, "source"); s != "dude" {
			t.Errorf("a brainstorm's investigator %s: %v", key, field(inv, key))
		}
	}
	if field(inv, "tier", "value", "id") == "mtr_small" || inv["projectNotes"] != nil {
		t.Errorf("a brainstorm's investigator: %v", inv)
	}
	mustExec(t, f.owner, `UPDATE sessions SET agent_machine = '{"sizeId":"msz_l","moveId":"mmv_1","askedBy":"run_i","reason":"r",
		"at":"2026-03-04T05:06:07Z"}' WHERE id = 'ssn_inv'`)
	if m := field(f.settingsOf(t, token, `{"section":"machines"}`), "settings", 0, "machines", "investigator"); field(m, "value", "id") != "msz_l" ||
		field(m, "source") != "session" {
		t.Errorf("a raised session's investigator machine: %v", m)
	}

	inv = roleIn(f.settingsOf(t, conductor, `{}`), "investigator")
	if field(inv, "tier", "value", "id") != "mtr_small" || field(inv, "tier", "source") != "project" ||
		field(inv, "machineSize", "source") != "project" || field(inv, "timeLimitMinutes", "value") != 30.0 ||
		field(inv, "timeLimitMinutes", "source") != "project" || field(inv, "timeLimitMinutes", "note") != nil {
		t.Errorf("a conductor's investigator: %v", inv)
	}
	// A task's investigator takes the project's saved prompt.
	mustExec(t, f.owner, `INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, created_by, created_at)
		VALUES ('pv_inv', $1, $2, 'investigator', 'replace', 'x', 'psn_ana', '2026-02-03T04:05:06Z')`, f.org, f.project)
	for _, p := range field(f.settingsOf(t, conductor, `{"section":"prompts"}`), "settings", 0, "prompts").([]any) {
		if m := p.(map[string]any); m["role"] == "investigator" && (m["organization"] != "dude" || m["note"] != nil || m["project"] != "replace" ||
			field(m, "projectSaved", "savedAt") != "2026-02-03T04:05:06Z") {
			t.Errorf("a conductor's investigator prompt: %v", m)
		}
	}
}

// A brainstorm with no linked project reads the organisation alone: no
// role field, prompt or network list is the project's, and each role
// agrees with /v1/settings/organization.
func TestABrainstormWithNoProjectReadsTheOrganisationAlone(t *testing.T) {
	f, _ := roleSettingsWorld(t)
	token := f.session(t, "ssn_org", "", "")
	out := f.settingsOf(t, token, `{}`)
	if field(out, "settings", 0, "project") != nil || len(out["settings"].([]any)) != 1 {
		t.Fatalf("scopes: %v", out["settings"])
	}
	impl, rev := roleIn(out, "implementer"), roleIn(out, "reviewer")
	if field(impl, "tier", "value", "id") != "mtr_big" || field(impl, "tier", "source") != "organization" || impl["projectNotes"] != nil {
		t.Errorf("implementer: %v", impl)
	}
	for _, r := range field(out, "settings", 0, "roles").([]any) {
		for _, key := range []string{"tier", "harness", "timeLimitMinutes", "machineSize", "image"} {
			if s := field(r, key, "source"); s == "project" {
				t.Errorf("%v %s: from project", field(r, "role"), key)
			}
		}
	}
	if s := field(rev, "tier", "source"); s != "dude" || field(rev, "tier", "value") != nil {
		t.Errorf("reviewer tier: %v", field(rev, "tier"))
	}
	for _, p := range field(out, "settings", 0, "prompts").([]any) {
		m := p.(map[string]any)
		if m["project"] != nil || m["role"] == "implementer" && m["organization"] != "saved" {
			t.Errorf("prompt: %v", m)
		}
	}
	net := field(out, "settings", 0, "network")
	if field(net, "projectEgress") != nil || field(net, "mode") != nil || field(net, "organizationEgress", "source") != "organization" ||
		!strings.Contains(fmt.Sprint(field(net, "organizationEgress", "value")), "api.example.com") {
		t.Errorf("network: %v", net)
	}

	page := f.settingsRoute(t, "/v1/settings/organization")
	roles := map[string]any{}
	for role, shown := range page["roles"].(map[string]any) {
		// A session investigator uses its per-run child prompt and has no enforced no-progress bound.
		if role != "investigator" {
			roles[role] = shown
		}
	}
	sameRolesAsThePage(t, out, map[string]any{"roles": roles})
	for role, shown := range roles {
		var p map[string]any
		for _, x := range field(out, "settings", 0, "prompts").([]any) {
			if m := x.(map[string]any); m["role"] == role {
				p = m
			}
		}
		if saved := field(shown, "prompt", "organization", "versionId") != nil; saved != (field(p, "organization") == "saved") {
			t.Errorf("%s prompt: page saved %v, tool %v", role, saved, field(p, "organization"))
		}
	}
	if fmt.Sprint(field(page, "network", "egress", "value")) != "[api.example.com]" {
		t.Errorf("page network: %v", page["network"])
	}
}

// Prompts say built-in or saved, added to or replaced, and when and by
// whom; network, capabilities and machines are each role's. Nothing the
// web app shows only to admins, and no prompt or notes text, appears.
func TestSettingsShowNoSecretAndNoPromptText(t *testing.T) {
	f, token := roleSettingsWorld(t)
	var raw json.RawMessage
	if status := f.postAs(t, token, "settings", `{}`, &raw); status != 200 {
		t.Fatalf("settings: %d %s", status, raw)
	}
	for _, s := range []string{"GH-CREDENTIAL-SECRET", "WEBHOOK-SECRET-VALUE", "OAUTH-CLIENT-SECRET-VALUE", "PROJECT-SECRET-VALUE",
		"HINT-SECRET", "ORG-PROMPT-BODY-TEXT", "PROJECT-PROMPT-BODY-TEXT", "ORG-NOTES-SECRET-TEXT", "PROJECT-NOTES-SECRET-TEXT",
		"TIER-HEADER-SECRET", "TIER-OPTIONS-SECRET"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("settings shows %s", s)
		}
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	prompts, _ := field(out, "settings", 0, "prompts").([]any)
	byRole := map[string]map[string]any{}
	for _, p := range prompts {
		m := p.(map[string]any)
		byRole[m["role"].(string)] = m
	}
	if p := byRole["implementer"]; p["organization"] != "saved" || field(p, "organizationSaved", "savedAt") != "2026-01-02T03:04:05Z" ||
		!strings.Contains(field(p, "organizationSaved", "savedBy").(string), "Ana") || p["project"] != "inherit" {
		t.Errorf("implementer's prompts: %v", p)
	}
	if p := byRole["reviewer"]; p["organization"] != "dude" || p["project"] != "replace" {
		t.Errorf("reviewer's prompts: %v", p)
	}
	if secrets := field(out, "settings", 0, "secrets"); fmt.Sprint(secrets) != "[map[name:DB_PASSWORD recipients:[implementer previews]]]" {
		t.Errorf("secrets: %v", secrets)
	}
	net := field(out, "settings", 0, "network")
	if field(net, "mode", "value") != "only" || field(net, "mode", "source") != "project" ||
		!strings.Contains(fmt.Sprint(field(net, "projectEgress", "value")), "docs.example.com") ||
		field(net, "brainstormAnywhere", "source") != "dude" || field(net, "web", 0, "webSearch") != true {
		t.Errorf("network: %v", net)
	}
	caps, _ := out["capabilities"].([]any)
	var yours []string
	for _, c := range caps {
		if m := c.(map[string]any); m["yours"] == true {
			for _, g := range m["granted"].([]any) {
				yours = append(yours, g.(map[string]any)["key"].(string))
			}
		}
	}
	if !slices.Contains(yours, "settings.read") || !slices.Contains(yours, "conduct") || len(caps) != 8 ||
		field(caps, 0, "granted", 0, "description") != nil {
		t.Errorf("capabilities: yours %v of %d roles, %v", yours, len(caps), field(caps, 0, "granted", 0))
	}
	// Each role's size is its machineSize under roles; machines lists the sizes.
	if field(out, "settings", 0, "machines") != nil || field(roleIn(out, "implementer"), "machineSize", "value", "id") != "msz_l" ||
		len(out["machineSizes"].([]any)) < 2 {
		t.Errorf("machines: %v %v", field(out, "settings", 0, "machines"), out["machineSizes"])
	}
	// Asked for alone, capabilities carry their descriptions and machines each role's size.
	if d, _ := field(f.settingsOf(t, token, `{"section":"capabilities"}`), "capabilities", 0, "granted", 0, "description").(string); d == "" {
		t.Errorf("section capabilities: no description")
	}
	if id := field(f.settingsOf(t, token, `{"section":"machines"}`), "settings", 0, "machines", "implementer", "value", "id"); id != "msz_l" {
		t.Errorf("section machines: implementer %v", id)
	}
}
