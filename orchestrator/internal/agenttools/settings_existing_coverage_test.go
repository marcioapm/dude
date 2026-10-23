package agenttools_test

import (
	"reflect"
	"testing"
	"time"
)

// Exact role and policy names make newly introduced settings fail classification.
var coveredRoleFields = []string{"tier", "harness", "timeLimitMinutes", "machineSize", "image"}
var coveredDeliveryFields = []string{"maxReviewIterations", "maxAttemptsPerFinding", "maxStillRounds", "maxRegressionRounds", "maxPrFixIterations", "requiredReviewers", "blockingSeverities", "simplify", "test", "parkAfterMinutes", "idleNudgeMinutes", "conductorWarmMinutes", "conductorEditFiles", "conductorEditLines"}

func sameInstant(a, b any) bool {
	x, err := time.Parse(time.RFC3339Nano, a.(string))
	if err != nil {
		return false
	}
	y, err := time.Parse(time.RFC3339Nano, b.(string))
	return err == nil && x.Equal(y)
}

func TestSettingsExistingMemberContractLeavesAreClassified(t *testing.T) {
	f, conductor := roleSettingsWorld(t)
	mustExec(t, f.owner, `UPDATE organizations SET brainstorm_anywhere = true WHERE id = $1`, f.org)
	for _, project := range []bool{false, true} {
		token := conductor
		path := "/v1/settings/organization"
		if project {
			path = "/v1/projects/" + f.project + "/settings"
		} else {
			token = f.session(t, "ssn_coverage", "", "")
		}
		page := f.settingsRoute(t, path)
		out := f.settingsOf(t, token, `{}`)
		rules := map[string]coverageRule{
			"organization.id":     {Excluded: "scope identifier; caller token fixes organisation"},
			"organization.name":   mapped,
			"canEdit":             {Excluded: "UI-only member edit permission"},
			"network.operator":    {Excluded: "deployment network defaults not wired to tools"},
			"network.always.*":    {Excluded: "deployment network defaults not wired to tools"},
			"network.effective.*": {Excluded: "derived deployment egress not wired to tools"},
		}
		if project {
			rules["project.id"] = coverageRule{Excluded: "scope identifier; project key identifies output"}
			rules["project.name"] = mapped
		}
		for _, key := range coveredDeliveryFields {
			valuePath := "delivery." + key + ".value"
			if key == "requiredReviewers" || key == "blockingSeverities" {
				valuePath += ".*"
			}
			rules[valuePath] = mapped
			rules["delivery."+key+".source"] = mapped
		}
		for _, role := range []string{"conductor", "brainstorm", "investigator", "implementer", "reviewer", "fixer", "simplifier", "qa_browser"} {
			prefix := "roles." + role + "."
			for _, key := range coveredRoleFields {
				rules[prefix+key+".value"] = mapped
				rules[prefix+key+".source"] = mapped
				if project && key != "timeLimitMinutes" {
					rules[prefix+key+".organization"] = coverageRule{Excluded: "UI reset reference; organisation scope separately exposes this value"}
				}
				if role == "fixer" && key != "timeLimitMinutes" {
					rules[prefix+key+".followsImplementer"] = mapped
				}
			}
			if role == "simplifier" || role == "qa_browser" {
				rules[prefix+"enabled.value"] = mapped
				rules[prefix+"enabled.source"] = mapped
			} else {
				rules[prefix+"enabled"] = coverageRule{Excluded: "always-on role has no toggle"}
			}
			for _, layer := range []string{"organization", "project"} {
				if layer == "project" && !project {
					continue
				}
				p := prefix + "prompt." + layer + "."
				rules[p+"body"] = coverageRule{Excluded: "prompt body not approved for exposure"}
				rules[p+"versionId"] = mapped
				rules[p+"updatedAt"] = mapped
				// Both null and non-null author shapes occur across seeded roles.
				if role == "implementer" && layer == "organization" || project && layer == "project" && (role == "implementer" || role == "reviewer") {
					rules[p+"updatedBy.id"] = coverageRule{Excluded: "audit author identifier"}
					rules[p+"updatedBy.name"] = mapped
				} else {
					rules[p+"updatedBy"] = coverageRule{Excluded: "null prompt author"}
				}
				rules[p+"versions"] = coverageRule{Excluded: "prompt history count, not configuration"}
				if layer == "project" {
					rules[p+"mode"] = mapped
				}
			}
		}
		rules["network.egress.value.*"] = mapped
		rules["network.egress.source"] = mapped
		if project {
			rules["network.mode.value"] = mapped
			rules["network.mode.source"] = mapped
			rules["network.organizationEgress.*"] = mapped
		} else {
			rules["network.brainstorm.anywhere"] = mapped
		}
		if err := classifyResponse(page, rules); err != nil {
			t.Fatal(path, err)
		}
		if !project {
			// Session children intentionally have no saved prompt or enforced no-progress bound.
			orgPage := page["roles"].(map[string]any)
			copyRoles := map[string]any{}
			for role, value := range orgPage {
				if role != "investigator" {
					copyRoles[role] = value
				}
			}
			sameRolesAsThePage(t, out, map[string]any{"roles": copyRoles})
		} else {
			sameRolesAsThePage(t, out, page)
		}
		if unwrapped(out["organization"].(string)) != field(page, "organization", "name") {
			t.Error("organisation name differs")
		}
		if project && unwrapped(field(out, "settings", 0, "project", "name").(string)) != field(page, "project", "name") {
			t.Error("project name differs")
		}
		for _, key := range coveredRoleFields {
			if key == "timeLimitMinutes" {
				continue
			}
			shown := field(page, "roles", "fixer", key, "followsImplementer") == true
			if got := field(roleIn(out, "fixer"), key, "followsImplementer") == true; got != shown {
				t.Errorf("fixer %s followsImplementer: tool %v, page %v", key, got, shown)
			}
		}
		for key, shown := range page["delivery"].(map[string]any) {
			actual := field(out, "settings", 0, "delivery", key, "value")
			if !reflect.DeepEqual(actual, field(shown, "value")) {
				t.Errorf("delivery %s: %v != %v", key, actual, field(shown, "value"))
			}
			if (field(shown, "source") == "project") != (field(out, "settings", 0, "delivery", key, "source") == "project") {
				t.Errorf("delivery %s source", key)
			}
		}
		for role, v := range page["roles"].(map[string]any) {
			shown := v.(map[string]any)
			var p any
			for _, row := range field(out, "settings", 0, "prompts").([]any) {
				if field(row, "role") == role {
					p = row
				}
			}
			for _, layer := range []string{"organization", "project"} {
				if layer == "project" && (!project || role == "brainstorm") {
					continue
				}
				shown := field(shown, "prompt", layer)
				saved := field(shown, "versionId") != nil
				key := "organizationSaved"
				if layer == "project" {
					key = "projectSaved"
				}
				if saved != (field(p, key) != nil) {
					t.Errorf("%s %s saved state differs", role, layer)
				}
				if saved && (!sameInstant(field(p, key, "savedAt"), field(shown, "updatedAt")) || unwrapped(field(p, key, "savedBy").(string)) != field(shown, "updatedBy", "name")) {
					t.Errorf("%s %s saved metadata differs", role, layer)
				}
				if layer == "project" && field(p, "project") != field(shown, "mode") {
					t.Errorf("%s project prompt mode", role)
				}
			}
			if role == "simplifier" || role == "qa_browser" {
				k := "simplify"
				if role == "qa_browser" {
					k = "test"
				}
				if field(shown, "enabled", "value") != field(out, "settings", 0, "delivery", k, "value") ||
					(field(shown, "enabled", "source") == "project") != (field(out, "settings", 0, "delivery", k, "source") == "project") {
					t.Error("enabled differs")
				}
			}
		}
		net := field(out, "settings", 0, "network")
		key := "organizationEgress"
		if project {
			key = "projectEgress"
		}
		got := field(net, key, "value").([]any)
		want := field(page, "network", "egress", "value").([]any)
		if len(got) != len(want) {
			t.Error("egress length differs")
		}
		if (field(page, "network", "egress", "source") == "project") != (field(net, key, "source") == "project") {
			t.Errorf("egress source: tool %v, page %v", field(net, key, "source"), field(page, "network", "egress", "source"))
		}
		for i, v := range got {
			if unwrapped(v.(string)) != want[i] {
				t.Error("egress differs")
			}
		}
		if project {
			if field(net, "mode", "value") != field(page, "network", "mode", "value") ||
				(field(net, "mode", "source") == "project") != (field(page, "network", "mode", "source") == "project") {
				t.Errorf("mode: tool %v, page %v", field(net, "mode"), field(page, "network", "mode"))
			}
			var orgEgress []any
			for _, v := range field(net, "organizationEgress", "value").([]any) {
				orgEgress = append(orgEgress, unwrapped(v.(string)))
			}
			if !reflect.DeepEqual(orgEgress, field(page, "network", "organizationEgress")) {
				t.Errorf("organisation egress: tool %v, page %v", orgEgress, field(page, "network", "organizationEgress"))
			}
		} else if anywhere := field(page, "network", "brainstorm", "anywhere"); anywhere != true ||
			field(net, "brainstormAnywhere", "value") != anywhere || field(net, "brainstormAnywhere", "source") != "organization" {
			t.Errorf("brainstorm anywhere: tool %v, page %v", field(net, "brainstormAnywhere"), anywhere)
		}
	}
	roleRules := map[string]coverageRule{}
	for _, k := range coveredRoleFields {
		roleRules[k] = mapped
	}
	for k, reason := range map[string]string{"model": "removed legacy input; rejected by schema", "context": "notes body not approved for exposure", "maxTokens": "not exposed by member settings response", "temperature": "not exposed by member settings response", "costLimitUsd": "not exposed by member settings response"} {
		roleRules[k] = coverageRule{Excluded: reason}
	}
	if err := classifyResponse(f.settingsRoute(t, "/schemas/roles"), roleRules); err != nil {
		t.Fatal(err)
	}
	deliveryRules := map[string]coverageRule{}
	for _, k := range coveredDeliveryFields {
		deliveryRules[k] = mapped
	}
	if err := classifyResponse(f.settingsRoute(t, "/schemas/delivery"), deliveryRules); err != nil {
		t.Fatal(err)
	}
	// Mutation proves classification depends on the schema's actual keys, not fixture presence.
	schema := f.settingsRoute(t, "/schemas/roles")
	schema["optionalFutureSetting"] = true
	if err := classifyResponse(schema, roleRules); err == nil {
		t.Fatal("new optional role setting accepted")
	}
}

// machineSizes is GET /v1/machines/sizes's sizes, and github is GET
// /v1/forge/settings, leaf for leaf.
func TestSettingsMachineSizesAndGitHubMatchTheirPages(t *testing.T) {
	f, token := roleSettingsWorld(t)
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ('psn_member', $1, 'Member', 'member')`, f.org)
	mustExec(t, f.owner, `UPDATE forge_credentials SET settings = '{"fixRoundsPerPr":8,"requestReviewFrom":"logins","reviewLogins":["octo"]}'
		WHERE id = 'fc_r'`)
	// A project's own use of a size, beside the organisation's.
	mustExec(t, f.owner, `UPDATE projects SET agent_models = agent_models || '{"qa_browser":{"machineSize":"msz_l"}}' WHERE id = $1`, f.project)
	out := f.settingsOf(t, token, `{}`)

	sizes := f.settingsRoute(t, "/v1/machines/sizes")
	rules := map[string]coverageRule{"canEdit": {Excluded: "UI-only member edit permission"}}
	for _, k := range []string{"id", "name", "cpus", "memoryMiB", "diskGiB", "isDefault"} {
		rules["sizes.*."+k] = mapped
	}
	for k, why := range map[string]string{
		"poolId": "lux pool placement: operator infrastructure (null in fixture)", "poolName": "lux pool name: operator infrastructure (null in fixture)",
		"updatedAt": "audit metadata", "updatedBy": "audit metadata (null author in fixture)",
		"usedBy":                    "usage list; each role's size is in roles (empty for the seeded default)",
		"usedBy.*.kind":             "usage list; each role's size is in roles",
		"usedBy.*.role":             "usage list; each role's size is in roles",
		"usedBy.*.project":          "usage list; each role's size is in roles",
		"usedBy.*.project.id":       "usage list; each role's size is in roles",
		"usedBy.*.project.name":     "usage list; each role's size is in roles",
		"usedBy.*.project.imageUrl": "UI-only project image",
		"usedBy.*.inherited":        "usage list; fixer inheritance is in roles",
	} {
		rules["sizes.*."+k] = coverageRule{Excluded: why}
	}
	if err := classifyResponse(sizes, rules); err != nil {
		t.Fatal(err)
	}
	shown := sizes["sizes"].([]any)
	got, _ := out["machineSizes"].([]any)
	if len(got) != len(shown) {
		t.Fatalf("sizes: tool %v, page %v", got, shown)
	}
	for i, s := range shown {
		for _, k := range []string{"id", "cpus", "memoryMiB", "diskGiB"} {
			if field(got, i, k) != field(s, k) {
				t.Errorf("size %d %s: tool %v, page %v", i, k, field(got, i, k), field(s, k))
			}
		}
		if unwrapped(field(got, i, "name").(string)) != field(s, "name") || (field(got, i, "default") == true) != (field(s, "isDefault") == true) {
			t.Errorf("size %d: tool %v, page %v", i, field(got, i), s)
		}
	}

	gh := f.settingsRoute(t, "/v1/forge/settings")
	ghRules := map[string]coverageRule{"reviewLogins.*": mapped}
	for _, k := range []string{"whoCanWake", "openAs", "requestReviewFrom", "mergeMethod", "whenBehind", "fixRoundsPerPr", "ciStuckMinutes", "checksFixRounds"} {
		ghRules[k] = mapped
	}
	if err := classifyResponse(gh, ghRules); err != nil {
		t.Fatal(err)
	}
	if len(gh) != len(field(out, "github").(map[string]any)) {
		t.Errorf("github keys: tool %v, page %v", out["github"], gh)
	}
	for k, want := range gh {
		actual := field(out, "github", k, "value")
		if k == "reviewLogins" {
			actual = []any{unwrapped(field(actual, 0).(string))}
		}
		if !reflect.DeepEqual(actual, want) {
			t.Errorf("github %s: tool %v, page %v", k, actual, want)
		}
	}
}
