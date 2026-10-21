package agenttools_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// coverageRule classifies one response leaf: Mapped when settings shows it
// (each test compares the values itself), or Excluded with why it does not.
type coverageRule struct {
	Mapped   bool
	Excluded string
}

var mapped = coverageRule{Mapped: true}

// settingsRouteFamilies is every member-readable GET route family of the
// settings pages: covered ones are fetched by settingsRoute and classified
// leaf by leaf; the others say why settings does not project them. The
// control plane's Router keeps no route metadata, so nothing discovers a new
// family: one added to the settings pages must be added here by hand.
var settingsRouteFamilies = map[string]string{
	"/v1/settings/organization":        "",
	"/v1/projects/:id/settings":        "",
	"/v1/models/tiers":                 "",
	"/v1/judgements/settings":          "",
	"/v1/projects/:id/servers":         "",
	"/v1/memory/index":                 "",
	"/v1/images":                       "",
	"/v1/machines/sizes":               "",
	"/v1/forge/settings":               "",
	"/v1/images/:id":                   "versions with Containerfile bodies, builds and history; the library summary is /v1/images",
	"/v1/images/picker":                "a UI picker over the same library as /v1/images",
	"/v1/images/builds/:buildId":       "a build's log and error: build output, not configuration",
	"/v1/machines/pools":               "lux's live pools as the orchestrator reads them: operator infrastructure, not a setting",
	"/v1/projects/:id/network/refused": "hosts agents were refused: run history, not configuration",
	"/v1/prompts/:role/history":        "prompt bodies and their history: never shown",
	"/v1/judgements":                   "judgement call history: requests and responses, not configuration",
	"/v1/judgements/:id":               "one judgement call's request and response, not configuration",
	"/v1/models/proxy":                 "the LLM proxy's model list: the operator's catalogue, not the organisation's tiers",
	"/v1/forge/webhook-secret":         "admin-only secret",
	"/v1/forge/credential":             "the GitHub connection and its credential, not a setting",
	"/v1/projects/:id/secrets":         "values are never shown; names and recipients come from project_secrets with roles (TestSettingsShowNoSecretAndNoPromptText)",
}

// routesFetched and routesClassified record, by family, what settingsRoute
// fetched and which of its answers classifyResponse accepted. fetchedPages
// holds each answer so its identity stays its own for the run.
var (
	routesFetched    = map[string]bool{}
	routesClassified = map[string]bool{}
	fetchedPages     []fetchedPage
)

type fetchedPage struct {
	page   map[string]any
	family string
}

func routeFamily(path string) string {
	path, _, _ = strings.Cut(path, "?")
	parts := strings.Split(path, "/")
	if len(parts) > 3 && parts[2] == "projects" {
		parts[3] = ":id"
	}
	return strings.Join(parts, "/")
}

func recordRoute(path string, page map[string]any) {
	if strings.HasPrefix(path, "/v1/") {
		routesFetched[routeFamily(path)] = true
		fetchedPages = append(fetchedPages, fetchedPage{page, routeFamily(path)})
	}
}

// TestMain checks, after a run of every test, that each covered family was
// fetched and classified and that every fetched family is in the table.
func TestMain(m *testing.M) {
	code := m.Run()
	if f := flag.Lookup("test.run"); code == 0 && f != nil && f.Value.String() == "" && len(routesFetched) > 0 {
		if problems := routeInventoryProblems(); len(problems) > 0 {
			fmt.Fprintln(os.Stderr, "settings route inventory:", strings.Join(problems, "; "))
			code = 1
		}
	}
	os.Exit(code)
}

func routeInventoryProblems() []string {
	var problems []string
	for family, excluded := range settingsRouteFamilies {
		if excluded == "" && !routesClassified[family] {
			problems = append(problems, "covered but never classified "+family)
		}
		if excluded != "" && routesFetched[family] {
			problems = append(problems, "excluded but fetched "+family)
		}
	}
	for family := range routesFetched {
		if _, ok := settingsRouteFamilies[family]; !ok {
			problems = append(problems, "fetched but not in the inventory "+family)
		}
	}
	sort.Strings(problems)
	return problems
}

func TestSettingsRouteInventoryCatchesAnUnlistedOrUnclassifiedFamily(t *testing.T) {
	fetched, classified := routesFetched, routesClassified
	defer func() { routesFetched, routesClassified = fetched, classified }()
	routesFetched, routesClassified = map[string]bool{}, map[string]bool{}
	for family, excluded := range settingsRouteFamilies {
		if excluded == "" {
			routesFetched[family], routesClassified[family] = true, true
		}
	}
	if p := routeInventoryProblems(); len(p) > 0 {
		t.Fatal(p)
	}
	routesFetched["/v1/projects/:id/brand-new"] = true
	delete(routesClassified, "/v1/images")
	if p := routeInventoryProblems(); len(p) != 2 {
		t.Fatalf("problems: %v", p)
	}
}

var modelCoverage = map[string]coverageRule{
	"tiers.*.id": mapped, "tiers.*.name": mapped, "tiers.*.description": mapped, "tiers.*.model": mapped,
	"tiers.*.effort": mapped, "tiers.*.position": mapped,
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
		if !ok || r.Mapped == (r.Excluded != "") {
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
	if m, ok := v.(map[string]any); ok {
		for _, p := range fetchedPages {
			if reflect.ValueOf(p.page).Pointer() == reflect.ValueOf(m).Pointer() {
				routesClassified[p.family] = true
			}
		}
	}
	return nil
}

func TestSettingsCoverageClassifierRejectsDrift(t *testing.T) {
	rules := map[string]coverageRule{"items.*.value": mapped}
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
	// Equal positions, ids in the reverse of the names' order.
	mustExec(t, f.owner, `INSERT INTO model_tiers(id,organization_id,name,model,position) VALUES
		('mtr_tie_a',$1,'Zeta','z',9),('mtr_tie_b',$1,'mid','m',9),('mtr_tie_c',$1,'Alpha','a',9)`, f.org)
	page := f.settingsRoute(t, "/v1/models/tiers")
	out := f.settingsOf(t, token, `{"section":"models"}`)
	var toolOrder, routeOrder []any
	for _, v := range field(out, "models", "tiers").([]any) {
		toolOrder = append(toolOrder, field(v, "id"))
	}
	for _, v := range page["tiers"].([]any) {
		routeOrder = append(routeOrder, field(v, "id"))
	}
	if !reflect.DeepEqual(toolOrder, routeOrder) {
		t.Errorf("order: tool %v, route %v", toolOrder, routeOrder)
	}
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
			if !rule.Mapped {
				continue
			}
			key := strings.TrimPrefix(path, "tiers.*.")
			want := shown[key]
			actual := got[key]
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
		schemaRules[k] = mapped
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
		rules := map[string]coverageRule{"tierId": mapped, "canEdit": {Excluded: "UI-only member edit permission"}}
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
			if previous != nil && (m["position"].(float64) < previous["position"].(float64) ||
				m["position"] == previous["position"] && strings.ToLower(unwrapped(m["name"].(string))) < strings.ToLower(unwrapped(previous["name"].(string)))) {
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
	mustExec(t, f.owner, `INSERT INTO images (id, organization_id, name) VALUES ('img_second', $1, 'second')`, f.org)
	images, _ := field(f.settingsOf(t, token, `{"section":"images","limit":1}`), "images", "next").(string)
	first, _ := field(f.settingsOf(t, token, `{"section":"models","limit":1}`), "models", "next").(string)
	var c map[string]any
	if raw, err := base64.RawURLEncoding.DecodeString(first); err != nil || json.Unmarshal(raw, &c) != nil {
		t.Fatalf("cursor %q: %v", first, err)
	}
	c["org"] = other
	forged, _ := json.Marshal(c)
	if images == "" {
		t.Fatal("no images cursor")
	}
	for _, args := range []string{`{"section":"models","limit":51}`, `{"section":"models","limit":-1}`, `{"section":"models","cursor":"broken"}`,
		`{"section":"models","cursor":"` + images + `"}`, `{"section":"models","cursor":"` + base64.RawURLEncoding.EncodeToString(forged) + `"}`} {
		if status, _ := f.post(t, token, "settings", args); status != 422 {
			t.Errorf("invalid catalogue args %s: %d", args, status)
		}
	}
	// Another organisation's project, with a recipe and a judgement tier.
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_oth', $1, 'Theirs', 'theirs', 'OTH')`, other)
	mustExec(t, f.owner, `INSERT INTO project_servers (project_id, organization_id, name, port, command) VALUES ('prj_oth', $1, 'web', 3000, 'OTHER-RECIPE-CANARY')`, other)
	mustExec(t, f.owner, `UPDATE organizations SET judgement_tier_id = 'mtr_other' WHERE id = $1`, other)
	for _, section := range []string{"models", "judgements", "servers", "memory"} {
		for _, project := range []string{"foreign", "OTH", "prj_oth", "Theirs"} {
			args := `{"section":"` + section + `","project":"` + project + `"}`
			status, got := f.post(t, token, "settings", args)
			if raw, _ := json.Marshal(got); status != 422 || strings.Contains(string(raw), "OTHER-RECIPE-CANARY") {
				t.Errorf("foreign scope %s: %d %s", args, status, raw)
			}
		}
		out := f.settingsOf(t, token, `{"section":"`+section+`"}`)
		raw, _ := json.Marshal(out)
		for _, secret := range []string{"TIER-HEADER-SECRET", "TIER-OPTIONS-SECRET", "ORG-PROMPT-BODY-TEXT", "PROJECT-PROMPT-BODY-TEXT", "PROJECT-SECRET-VALUE", "HINT-SECRET", "OTHER-SECRET", "OTHER-MODEL", "OTHER-RECIPE-CANARY", "mtr_other"} {
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
