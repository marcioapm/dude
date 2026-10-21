package agenttools_test

import (
	"encoding/json"
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
		rules["servers.*."+k] = coverageRule{Tool: k}
	}
	rules["servers.*.env.*.name"] = coverageRule{Tool: "envNames"}
	rules["servers.*.env.*.value"] = coverageRule{Excluded: "environment values may contain credentials"}
	rules["servers.*.updatedAt"] = coverageRule{Excluded: "audit metadata"}
	rules["servers.*.updatedBy"] = coverageRule{Excluded: "audit metadata"}
	previewKeys := []string{"image", "imageId", "egress", "machineSize", "idleTimeoutMinutes", "firstWakeTimeoutMinutes", "wakeTimeoutMinutes"}
	for _, k := range previewKeys {
		path := "previews." + k
		if k == "egress" {
			path += ".*"
		}
		rules[path] = coverageRule{Tool: k}
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
		schemaRules[k] = coverageRule{Tool: k}
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
}
