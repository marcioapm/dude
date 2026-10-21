package agenttools_test

import (
	"reflect"
	"testing"
	"time"
)

// Exact role and policy names make newly introduced settings fail classification.
var coveredRoleFields = []string{"tier", "harness", "timeLimitMinutes", "machineSize", "image"}
var coveredDeliveryFields = []string{"maxReviewIterations", "maxAttemptsPerFinding", "maxPrFixIterations", "requiredReviewers", "blockingSeverities", "simplify", "test", "parkAfterMinutes", "idleNudgeMinutes", "conductorWarmMinutes", "conductorEditFiles", "conductorEditLines"}

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
	token := conductor
	for _, project := range []bool{false, true} {
		path := "/v1/settings/organization"
		args := `{}`
		if project {
			path = "/v1/projects/" + f.project + "/settings"
		} else {
			session := f.session(t, "ssn_coverage", "", "")
			token = session
		}
		if project {
			token = conductor
		}
		page := f.settingsRoute(t, path)
		out := f.settingsOf(t, token, args)
		rules := map[string]coverageRule{
			"organization.id":     {Excluded: "scope identifier; caller token fixes organisation"},
			"organization.name":   {Tool: "organization"},
			"canEdit":             {Excluded: "UI-only member edit permission"},
			"network.operator":    {Excluded: "deployment network defaults not wired to tools"},
			"network.always.*":    {Excluded: "deployment network defaults not wired to tools"},
			"network.effective.*": {Excluded: "derived deployment egress not wired to tools"},
		}
		if project {
			rules["project.id"] = coverageRule{Excluded: "scope identifier; project key identifies output"}
			rules["project.name"] = coverageRule{Tool: "project.name"}
		}
		for _, key := range coveredDeliveryFields {
			rules["delivery."+key+".value"] = coverageRule{Tool: "delivery." + key}
			if key == "requiredReviewers" || key == "blockingSeverities" {
				delete(rules, "delivery."+key+".value")
				rules["delivery."+key+".value.*"] = coverageRule{Tool: "delivery." + key}
			}
			rules["delivery."+key+".source"] = coverageRule{Tool: "delivery." + key}
		}
		for _, role := range []string{"conductor", "brainstorm", "investigator", "implementer", "reviewer", "fixer", "simplifier", "qa_browser"} {
			prefix := "roles." + role + "."
			for _, key := range coveredRoleFields {
				rules[prefix+key+".value"] = coverageRule{Tool: "roles." + role + "." + key}
				rules[prefix+key+".source"] = coverageRule{Tool: "roles." + role + "." + key}
				if project && key != "timeLimitMinutes" {
					rules[prefix+key+".organization"] = coverageRule{Excluded: "UI reset reference; organisation scope separately exposes this value"}
				}
				if role == "fixer" && key != "timeLimitMinutes" {
					rules[prefix+key+".followsImplementer"] = coverageRule{Tool: "roles.fixer." + key}
				}
			}
			rules[prefix+"enabled"] = coverageRule{Excluded: "always-on role has no toggle"}
			if role == "simplifier" || role == "qa_browser" {
				delete(rules, prefix+"enabled")
				rules[prefix+"enabled.value"] = coverageRule{Tool: "delivery toggle"}
				rules[prefix+"enabled.source"] = coverageRule{Tool: "delivery toggle"}
			}
			for _, layer := range []string{"organization", "project"} {
				if layer == "project" && !project {
					continue
				}
				p := prefix + "prompt." + layer + "."
				rules[p+"body"] = coverageRule{Excluded: "prompt body not approved for exposure"}
				rules[p+"versionId"] = coverageRule{Tool: "prompt saved state"}
				rules[p+"updatedAt"] = coverageRule{Tool: "prompt saved timestamp"}
				rules[p+"updatedBy"] = coverageRule{Excluded: "null prompt author"}
				// Both null and non-null author shapes occur across seeded roles.
				if role == "implementer" && layer == "organization" || project && layer == "project" && (role == "implementer" || role == "reviewer") {
					delete(rules, p+"updatedBy")
					rules[p+"updatedBy.id"] = coverageRule{Excluded: "audit author identifier"}
					rules[p+"updatedBy.name"] = coverageRule{Tool: "prompt saved author"}
				}
				rules[p+"versions"] = coverageRule{Excluded: "prompt history count, not configuration"}
				if layer == "project" {
					rules[p+"mode"] = coverageRule{Tool: "prompt project mode"}
				}
			}
		}
		rules["network.egress.value.*"] = coverageRule{Tool: "network egress"}
		rules["network.egress.source"] = coverageRule{Tool: "network egress source"}
		if project {
			rules["network.mode.value"] = coverageRule{Tool: "network mode"}
			rules["network.mode.source"] = coverageRule{Tool: "network mode source"}
			rules["network.organizationEgress.*"] = coverageRule{Tool: "network organisation egress"}
		} else {
			rules["network.brainstorm.anywhere"] = coverageRule{Tool: "network brainstorm anywhere"}
		}
		if err := classifyResponse(page, rules); err != nil {
			t.Fatal(path, err)
		}
		comparison := out
		if !project {
			// The session's investigator has intentionally different runtime semantics.
			comparison = f.settingsOf(t, conductor, `{}`)
			orgPage := page["roles"].(map[string]any)
			copyRoles := map[string]any{}
			for role, value := range orgPage {
				if role != "investigator" {
					copyRoles[role] = value
				}
			}
			copyPage := map[string]any{"roles": copyRoles}
			sameRolesAsThePage(t, out, copyPage)
		} else {
			sameRolesAsThePage(t, comparison, page)
		}
		if unwrapped(out["organization"].(string)) != field(page, "organization", "name") {
			t.Error("organisation name differs")
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
				if field(shown, "enabled", "value") != field(out, "settings", 0, "delivery", k, "value") {
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
		for i, v := range got {
			if unwrapped(v.(string)) != want[i] {
				t.Error("egress differs")
			}
		}
	}
	roleRules := map[string]coverageRule{}
	for _, k := range coveredRoleFields {
		roleRules[k] = coverageRule{Tool: k}
	}
	for k, reason := range map[string]string{"model": "removed legacy input; rejected by schema", "context": "notes body not approved for exposure", "maxTokens": "not exposed by member settings response", "temperature": "not exposed by member settings response", "costLimitUsd": "not exposed by member settings response"} {
		roleRules[k] = coverageRule{Excluded: reason}
	}
	if err := classifyResponse(f.settingsRoute(t, "/schemas/roles"), roleRules); err != nil {
		t.Fatal(err)
	}
	deliveryRules := map[string]coverageRule{}
	for _, k := range coveredDeliveryFields {
		deliveryRules[k] = coverageRule{Tool: k}
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
