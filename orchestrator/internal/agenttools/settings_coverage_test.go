package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Route families are reviewed explicitly: Router exposes no registration metadata.
// New settings route families must be registered in this coverage inventory.
type coverageRule struct {
	Tool     string
	Excluded string
}

var modelCoverage = map[string]coverageRule{
	"tiers.*.id": {Tool: "id"}, "tiers.*.name": {Tool: "name"},
	"tiers.*.description": {Tool: "description"}, "tiers.*.model": {Tool: "model"},
	"tiers.*.effort": {Tool: "effort"}, "tiers.*.position": {Tool: "position"},
	"tiers.*.headers.X-Api-Key":         {Excluded: "credential-sensitive provider header"},
	"tiers.*.headers":                   {Excluded: "credential-sensitive provider headers (null)"},
	"tiers.*.options.apiKey":            {Excluded: "credential-sensitive provider options"},
	"tiers.*.options":                   {Excluded: "credential-sensitive provider options (null)"},
	"tiers.*.updatedAt":                 {Excluded: "audit metadata, not runtime configuration"},
	"tiers.*.updatedBy":                 {Excluded: "audit metadata, not runtime configuration"},
	"tiers.*.usedBy.*.kind":             {Excluded: "unbounded usage catalogue; role selections are in roles"},
	"tiers.*.usedBy.*.role":             {Excluded: "unbounded usage catalogue; role selections are in roles"},
	"tiers.*.usedBy.*.project":          {Excluded: "unbounded usage catalogue; role selections are in roles"},
	"tiers.*.usedBy.*.project.id":       {Excluded: "unbounded usage catalogue"},
	"tiers.*.usedBy.*.project.name":     {Excluded: "unbounded usage catalogue"},
	"tiers.*.usedBy.*.project.imageUrl": {Excluded: "UI-only project image"},
	"tiers.*.usedBy.*.inherited":        {Excluded: "usage catalogue; fixer inheritance is in roles"},
	"canEdit":                           {Excluded: "UI-only member edit permission"},
	"upgrade":                           {Excluded: "admin-only migration history; empty for members"},
}

func responseLeaves(v any) map[string][]any {
	out := map[string][]any{}
	var walk func(any, string)
	walk = func(v any, p string) {
		switch x := v.(type) {
		case map[string]any:
			if len(x) > 0 {
				for k, v := range x {
					q := k
					if p != "" {
						q = p + "." + k
					}
					walk(v, q)
				}
				return
			}
		case []any:
			if len(x) > 0 {
				for _, v := range x {
					walk(v, p+".*")
				}
				return
			}
		}
		out[p] = append(out[p], v)
	}
	walk(v, "")
	return out
}

func classifyResponse(v any, rules map[string]coverageRule) error {
	leaves := responseLeaves(v)
	var problems []string
	for p := range leaves {
		r, ok := rules[p]
		if !ok || (r.Tool == "") == (r.Excluded == "") {
			problems = append(problems, "unclassified "+p)
		}
	}
	for p := range rules {
		if _, ok := leaves[p]; !ok {
			problems = append(problems, "stale "+p)
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func TestSettingsCoverageClassifierRejectsDrift(t *testing.T) {
	rules := map[string]coverageRule{"items.*.value": {Tool: "value"}}
	original := map[string]any{"items": []any{map[string]any{"value": 1}}}
	if err := classifyResponse(original, rules); err != nil {
		t.Fatal(err)
	}
	extra := map[string]any{"items": []any{map[string]any{"value": 1, "newOptional": true}}}
	if err := classifyResponse(extra, rules); err == nil {
		t.Fatal("new leaf accepted")
	}
	if err := classifyResponse(map[string]any{"items": []any{}}, rules); err == nil {
		t.Fatal("stale mapping accepted")
	}
	excluded := map[string]coverageRule{"gone": {Excluded: "UI-only"}}
	if err := classifyResponse(map[string]any{}, excluded); err == nil {
		t.Fatal("stale exclusion accepted")
	}
}

func unwrapped(s string) string {
	if start := strings.Index(s, "⟧"); start >= 0 {
		if end := strings.LastIndex(s, "⟦/external "); end > start {
			return s[start+len("⟧") : end]
		}
	}
	return s
}

func TestSettingsModelsAndJudgementsMatchMemberRoutes(t *testing.T) {
	f, token := roleSettingsWorld(t)
	mustExec(t, f.owner, `UPDATE model_tiers SET description='A safe description', effort='high', position=4 WHERE id='mtr_big'`)
	mustExec(t, f.owner, `UPDATE model_tiers SET model=NULL, position=7 WHERE id='mtr_small'`)
	mustExec(t, f.owner, `UPDATE organizations SET judgement_tier_id='mtr_big' WHERE id=$1`, f.org)
	page := f.settingsRoute(t, "/v1/models/tiers")
	out := f.settingsOf(t, token, `{"section":"models"}`)
	// The seeded default tier also has an empty usage array. Record that shape explicitly.
	rules := make(map[string]coverageRule, len(modelCoverage)+1)
	for k, v := range modelCoverage {
		rules[k] = v
	}
	rules["tiers.*.usedBy"] = coverageRule{Excluded: "empty usage catalogue"}
	if err := classifyResponse(page, rules); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, v := range field(out, "models", "tiers").([]any) {
		m := v.(map[string]any)
		byID[m["id"].(string)] = m
	}
	for _, v := range page["tiers"].([]any) {
		shown := v.(map[string]any)
		got := byID[shown["id"].(string)]
		for path, rule := range rules {
			if rule.Tool == "" {
				continue
			}
			key := strings.TrimPrefix(path, "tiers.*.")
			want := shown[key]
			actual := got[rule.Tool]
			if s, ok := actual.(string); ok {
				if s != "" && !strings.HasPrefix(s, "⟦external ") && (key == "name" || key == "model" || key == "description") {
					t.Errorf("unwrapped %s: %q", key, s)
				}
				actual = unwrapped(s)
			}
			if want == nil && (key == "model" || key == "effort") {
				want = ""
			}
			if !reflect.DeepEqual(actual, want) {
				t.Errorf("%s %s: tool %v route %v", shown["id"], key, actual, want)
			}
		}
	}
	if field(out, "models", "source") != "organization" {
		t.Errorf("models source: %v", out)
	}
	schemaRules := map[string]coverageRule{}
	for _, k := range []string{"name", "description", "model", "effort"} {
		schemaRules[k] = coverageRule{Tool: k}
	}
	for _, k := range []string{"headers", "options"} {
		schemaRules[k] = coverageRule{Excluded: "credential-sensitive provider configuration"}
	}
	schema := f.settingsRoute(t, "/schemas/tier")
	if err := classifyResponse(schema, schemaRules); err != nil {
		t.Fatal(err)
	}
	schema["newOptional"] = true
	if err := classifyResponse(schema, schemaRules); err == nil {
		t.Fatal("added optional tier input accepted")
	}
	for _, tier := range []any{"mtr_big", "mtr_small", nil} {
		mustExec(t, f.owner, `UPDATE organizations SET judgement_tier_id=$2 WHERE id=$1`, f.org, tier)
		page := f.settingsRoute(t, "/v1/judgements/settings")
		rules := map[string]coverageRule{"tierId": {Tool: "tierId"}, "canEdit": {Excluded: "UI-only member edit permission"}}
		if err := classifyResponse(page, rules); err != nil {
			t.Fatal(err)
		}
		got := field(f.settingsOf(t, token, `{"section":"judgements"}`), "judgements")
		if !reflect.DeepEqual(field(got, "tierId"), page["tierId"]) || field(got, "source") != "organization" || field(got, "enabled") != (tier != nil) {
			t.Errorf("judgements: %v page %v", got, page)
		}
		want := map[any]string{nil: "off", "mtr_big": "configured", "mtr_small": "model-unset"}[tier]
		if field(got, "state") != want {
			t.Errorf("judgement state: %v", got)
		}
		if tier == "mtr_big" && (unwrapped(field(got, "tier", "name").(string)) != "Big" || unwrapped(field(got, "tier", "model").(string)) != "big-model") {
			t.Errorf("resolved judgement tier: %v", got)
		}
	}
}

func TestSettingsCataloguesArePagedBoundedAndTenantScoped(t *testing.T) {
	f, token := roleSettingsWorld(t)
	mustExec(t, f.owner, `INSERT INTO model_tiers(id,organization_id,name,description,model,position)
 SELECT 'mtr_page_'||lpad(i::text,4,'0'),$1,'Tier '||i,repeat('<',80),repeat('>',200),i/3 FROM generate_series(1,125) i`, f.org)
	other := dbtest.Org(t, f.owner)
	mustExec(t, f.owner, `INSERT INTO model_tiers(id,organization_id,name,model) VALUES('mtr_other',$1,'OTHER-SECRET','OTHER-MODEL')`, other)
	var cursor string
	seen := map[string]bool{}
	var previous map[string]any
	for {
		args, _ := json.Marshal(map[string]any{"section": "models", "limit": 50, "cursor": cursor})
		out := f.settingsOf(t, token, string(args))
		raw, _ := json.Marshal(out)
		if len(raw) > 64<<10 {
			t.Fatalf("catalogue %d bytes", len(raw))
		}
		t.Logf("catalogue page: %d bytes", len(raw))
		for _, v := range field(out, "models", "tiers").([]any) {
			m := v.(map[string]any)
			id := m["id"].(string)
			if seen[id] || id == "mtr_other" {
				t.Fatalf("duplicate or foreign tier %s", id)
			}
			if previous != nil && (m["position"].(float64) < previous["position"].(float64) || m["position"] == previous["position"] && id <= previous["id"].(string)) {
				t.Fatal("unstable order")
			}
			seen[id] = true
			previous = m
		}
		cursor, _ = field(out, "models", "next").(string)
		if cursor == "" {
			if len(seen) != int(field(out, "models", "count").(float64)) {
				t.Fatal("skipped tiers")
			}
			break
		}
	}
	for _, args := range []string{`{"section":"models","limit":51}`, `{"section":"models","limit":-1}`, `{"section":"models","cursor":"broken"}`} {
		if status, _ := f.post(t, token, "settings", args); status != 422 {
			t.Errorf("invalid catalogue args %s: %d", args, status)
		}
	}
	for _, section := range []string{"models", "judgements", "servers", "memory"} {
		args := `{"section":"` + section + `","project":"foreign"}`
		if status, _ := f.post(t, token, "settings", args); status != 422 {
			t.Errorf("foreign scope %s: %d", section, status)
		}
		out := f.settingsOf(t, token, `{"section":"`+section+`"}`)
		raw, _ := json.Marshal(out)
		for _, secret := range []string{"TIER-HEADER-SECRET", "TIER-OPTIONS-SECRET", "ORG-PROMPT-BODY-TEXT", "PROJECT-PROMPT-BODY-TEXT", "PROJECT-SECRET-VALUE", "HINT-SECRET", "OTHER-SECRET", "OTHER-MODEL"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s leaks %s", section, secret)
			}
		}
	}
	var before string
	snapshot := `SELECT md5(concat_ws('|', (SELECT row_to_json(o)::text FROM organizations o WHERE id=$1),
		(SELECT string_agg(row_to_json(t)::text, ',' ORDER BY id) FROM model_tiers t WHERE organization_id=$1),
		(SELECT string_agg(row_to_json(p)::text, ',' ORDER BY id) FROM projects p WHERE organization_id=$1),
		(SELECT string_agg(row_to_json(s)::text, ',' ORDER BY project_id, name) FROM project_servers s WHERE organization_id=$1)))`
	if err := f.owner.QueryRow(context.Background(), snapshot, f.org).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"", "models", "judgements", "servers", "memory"} {
		f.settingsOf(t, token, `{"section":"`+section+`"}`)
	}
	var after string
	if err := f.owner.QueryRow(context.Background(), snapshot, f.org).Scan(&after); err != nil || after != before {
		t.Fatalf("settings reads changed configuration: %v", err)
	}
	if got := field(f.settingsOf(t, token, `{}`), "models", "tiers"); got != nil {
		t.Fatal("default returned catalogue")
	}
}
