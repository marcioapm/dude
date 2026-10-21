package agenttools_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSettingsServersMatchMemberRouteWithoutEnvironmentValues(t *testing.T) {
	f, token := roleSettingsWorld(t)
	mustExec(t, f.owner, `INSERT INTO project_servers(project_id,organization_id,name,port,command,setup,workdir,env,autostart_in_previews)
 VALUES($1,$2,'web',3000,'bun run dev','bun install','app','[{"name":"PUBLIC_HOST","value":"RECIPE-ENV-SECRET"}]',true)`, f.project, f.org)
	mustExec(t, f.owner, `UPDATE projects SET preview_image_id='img_web',preview_settings='{"idleTimeoutMinutes":37,"firstWakeTimeoutMinutes":42,"wakeTimeoutMinutes":9,"egress":["example.com"],"machineSize":"msz_l"}' WHERE id=$1`, f.project)
	route := f.settingsRoute(t, "/v1/projects/"+f.project+"/servers")
	rules := map[string]coverageRule{}
	for _, k := range []string{"name", "command", "setup", "workdir", "port", "autostartInPreviews"} {
		rules["servers.*."+k] = mapped
	}
	rules["servers.*.env.*.name"] = mapped
	rules["servers.*.env.*.value"] = coverageRule{Excluded: "environment values may contain credentials"}
	rules["servers.*.updatedAt"] = coverageRule{Excluded: "audit metadata"}
	rules["servers.*.updatedBy"] = coverageRule{Excluded: "audit metadata"}
	previewKeys := []string{"image", "imageId", "egress", "machineSize", "idleTimeoutMinutes", "firstWakeTimeoutMinutes", "wakeTimeoutMinutes"}
	for _, k := range previewKeys {
		path := "previews." + k
		if k == "egress" {
			path += ".*"
		}
		rules[path] = mapped
	}
	if err := classifyResponse(route, rules); err != nil {
		t.Fatal(err)
	}
	out := f.settingsOf(t, token, `{"section":"servers"}`)
	got := field(out, "settings", 0, "servers")
	recipe := field(got, "recipes", 0)
	shown := field(route, "servers", 0)
	for _, k := range []string{"name", "command", "setup", "workdir", "port", "autostartInPreviews"} {
		actual := field(recipe, k)
		if s, ok := actual.(string); ok {
			actual = unwrapped(s)
		}
		if !reflect.DeepEqual(actual, field(shown, k)) {
			t.Errorf("recipe %s: %v != %v", k, actual, field(shown, k))
		}
	}
	if unwrapped(field(recipe, "envNames", 0).(string)) != field(shown, "env", 0, "name") {
		t.Error("env name differs")
	}
	for _, k := range previewKeys {
		actual := field(got, "previews", k, "value")
		want := field(route, "previews", k)
		if s, ok := actual.(string); ok {
			actual = unwrapped(s)
		}
		if k == "egress" {
			actual = []any{unwrapped(field(actual, 0).(string))}
		}
		if !reflect.DeepEqual(actual, want) {
			t.Errorf("preview %s: %v != %v", k, actual, want)
		}
		source := "project"
		if k == "image" {
			source = "dude"
		}
		if field(got, "previews", k, "source") != source {
			t.Errorf("preview %s source: %v", k, field(got, "previews", k))
		}
	}
	schemaRules := map[string]coverageRule{}
	for _, k := range previewKeys {
		schemaRules[k] = mapped
	}
	if err := classifyResponse(f.settingsRoute(t, "/schemas/preview"), schemaRules); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"", "servers"} {
		out := f.settingsOf(t, token, `{"section":"`+section+`"}`)
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "RECIPE-ENV-SECRET") {
			t.Fatal("recipe env value leaked")
		}
		if section == "" && field(out, "settings", 0, "servers", "recipes") != nil {
			t.Fatal("default lists recipes")
		}
	}
	session := f.session(t, "ssn_servers", "", "")
	if got := field(f.settingsOf(t, session, `{"section":"servers"}`), "settings", 0, "servers"); field(got, "source") != "no-project" || field(got, "recipes") != nil {
		t.Errorf("org-only scope: %v", got)
	}
	mustExec(t, f.owner, `UPDATE project_servers SET command=repeat('x',2000) WHERE project_id=$1`, f.project)
	command := field(f.settingsOf(t, token, `{"section":"servers"}`), "settings", 0, "servers", "recipes", 0, "command").(string)
	if !strings.HasPrefix(command, "⟦external ") || len(unwrapped(command)) != 1000 || !strings.Contains(command, "2000") {
		t.Errorf("truncated command: %s", command)
	}
	// The default read counts recipes without decoding one: an env column
	// no recipe decoder accepts is never read there.
	mustExec(t, f.owner, `UPDATE project_servers SET env = '"not a list"' WHERE project_id = $1`, f.project)
	if got := field(f.settingsOf(t, token, `{}`), "settings", 0, "servers"); field(got, "count") != 1.0 || field(got, "previews") != nil {
		t.Errorf("default servers: %v", got)
	}
}

// Three linked projects of ten recipes each, every command and setup 1000
// bytes of JSON-escaped characters and thirty env names: the servers section
// stays under a tool result's 64 KiB, keeps each count, and says how many
// recipes it shows.
func TestSettingsServersAreBoundedForThreeFullProjects(t *testing.T) {
	f := setup(t)
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
		('prj_s2', $1, 'Two', 's2', 'STWO'), ('prj_s3', $1, 'Three', 's3', 'STHREE')`, f.org)
	token := f.session(t, "ssn_srv", "", "")
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id)
		SELECT 'ssn_srv', id, organization_id FROM projects WHERE organization_id = $1`, f.org)
	mustExec(t, f.owner, `INSERT INTO project_servers (project_id, organization_id, name, port, command, setup, env)
		SELECT p.id, $1, 'srv' || i, 3000 + i, left(repeat('&<>', 334), 1000), left(repeat('<&>', 334), 1000),
			(SELECT jsonb_agg(jsonb_build_object('name', 'ENVIRONMENT_VARIABLE_NAME_NUMBER_' || e, 'value', 'v')) FROM generate_series(1, 30) e)
		FROM projects p, generate_series(1, 10) i WHERE p.organization_id = $1`, f.org)
	var raw json.RawMessage
	if status := f.postAs(t, token, "settings", `{"section":"servers"}`, &raw); status != 200 {
		t.Fatalf("settings: %d %s", status, raw)
	}
	t.Logf("servers of three full projects: %d bytes", len(raw))
	if len(raw) > 64<<10 {
		t.Fatalf("servers: %d bytes", len(raw))
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	scopes := out["settings"].([]any)
	shown, noted := 0, false
	for _, s := range scopes {
		servers := field(s, "servers")
		recipes, _ := field(servers, "recipes").([]any)
		shown += len(recipes)
		if field(servers, "count") != 10.0 {
			t.Errorf("count: %v", field(servers, "count"))
		}
		if note, _ := field(servers, "note").(string); len(recipes) < 10 {
			noted = true
			if !strings.Contains(note, fmt.Sprintf("%d of 10 recipes shown", len(recipes))) || !strings.Contains(note, "project") {
				t.Errorf("note: %q", note)
			}
		} else if note != "" {
			t.Errorf("a complete scope's note: %q", note)
		}
	}
	if len(scopes) != 3 || shown == 0 || shown == 30 || !noted {
		t.Errorf("%d scopes, %d recipes shown, noted %v", len(scopes), shown, noted)
	}
}
