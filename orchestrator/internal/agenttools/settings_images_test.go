package agenttools_test

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// imageLibraryWorld adds to roleSettingsWorld's img_web: a published default
// base that can run containers; an app on base with a failed newer build
// (its log and error recorded) and a draft; an archived image.
func imageLibraryWorld(t *testing.T) (*fixture, string) {
	f, token := roleSettingsWorld(t)
	for _, q := range []string{
		`INSERT INTO images (id, organization_id, name, description, archived_at) VALUES
			('img_base', $1, 'base', 'The shared base', NULL), ('img_app', $1, 'app', '', NULL),
			('img_old', $1, 'old', 'Retired <image>', '2026-03-01T00:00:00Z')`,
		`INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state, user_ref, built_at, can_run_containers, error) VALUES
			('imv_b1', $1, 'img_base', 1, 'FROM debian
RUN echo CONTAINERFILE-CANARY', 'published', 'reg.example/base@sha256:b1', '2026-04-01T00:00:00Z', true, NULL),
			('imv_a1', $1, 'img_app', 1, 'FROM image:base', 'published', 'reg.example/app@sha256:a1', '2026-04-02T00:00:00Z', false, NULL),
			('imv_a2', $1, 'img_app', 2, 'FROM image:base
RUN CONTAINERFILE-CANARY', 'failed', NULL, NULL, false, 'BUILD-ERROR-CANARY'),
			('imv_ad', $1, 'img_app', NULL, 'FROM image:base
RUN DRAFT-CONTAINERFILE-CANARY', 'draft', NULL, NULL, false, NULL)`,
		`UPDATE images SET published_version_id = CASE id WHEN 'img_base' THEN 'imv_b1' WHEN 'img_app' THEN 'imv_a1' END
			WHERE organization_id = $1 AND id IN ('img_base', 'img_app')`,
		`INSERT INTO image_version_parents (organization_id, version_id, parent_image_id, parent_version_id) VALUES
			($1, 'imv_a1', 'img_base', 'imv_b1'), ($1, 'imv_ad', 'img_base', NULL)`,
		`UPDATE organizations SET default_image_id = 'img_base' WHERE id = $1`,
		`INSERT INTO image_builds (id, organization_id, image_version_id, kind, state, error, log_total)
			VALUES ('imb_a2', $1, 'imv_a2', 'build', 'failed', 'BUILD-ERROR-CANARY', 16)`,
		`INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk) VALUES ($1, 'imb_a2', 0, 'BUILD-LOG-CANARY')`,
	} {
		mustExec(t, f.owner, q, f.org)
	}
	return f, token
}

// imageRules classifies every leaf GET /v1/images answers a member over
// imageLibraryWorld: mapped to the images section, or excluded with why.
func imageRules() map[string]coverageRule {
	r := map[string]coverageRule{}
	for _, p := range []string{"id", "name", "description", "archivedAt", "isDefault", "published", "published.versionId",
		"published.number", "published.builtAt", "published.userRef", "published.canRunContainers", "pending",
		"pending.versionId", "pending.number", "pending.state", "draft", "parents", "parents.*.id",
		"parents.*.name"} {
		r["images.*."+p] = mapped
	}
	for p, why := range map[string]string{
		"draft.versionId":     "the section says only that a draft exists",
		"pending.error":       "build error text: build output, not configuration",
		"draft.updatedAt":     "draft edit audit; the section says only that a draft exists",
		"draft.updatedBy":     "draft edit audit (null author in fixture)",
		"createdAt":           "audit metadata",
		"createdBy":           "audit metadata (null author in fixture)",
		"from":                "derived from the Containerfile body (firstFrom); parents is the reviewed base relation",
		"lastChange.at":       "history",
		"lastChange.by":       "history (null author in fixture)",
		"lastChange.source":   "history",
		"usedBy":              "usage list; each role's image is in roles",
		"usedBy.*.kind":       "usage list; each role's image is in roles",
		"usedBy.*.role":       "usage list; each role's image is in roles",
		"usedBy.*.image.id":   "usage list (child images); parents gives the relation from the child",
		"usedBy.*.image.name": "usage list (child images); parents gives the relation from the child",
	} {
		r["images.*."+p] = coverageRule{Excluded: why}
	}
	r["defaultImageId"] = mapped
	r["queue"] = coverageRule{Excluded: "builder queue (empty: the fixture's build failed), not configuration"}
	r["canEdit"] = coverageRule{Excluded: "UI-only member edit permission"}
	for _, k := range []string{"available", "layer", "cpus", "memoryMiB", "lastSeenAt", "offline"} {
		r["builder."+k] = coverageRule{Excluded: "operator builder status, not organisation configuration"}
	}
	return r
}

func TestSettingsImagesMatchTheMemberLibrary(t *testing.T) {
	f, token := imageLibraryWorld(t)
	page := f.settingsRoute(t, "/v1/images")
	if err := classifyResponse(page, imageRules()); err != nil {
		t.Fatal(err)
	}
	out := f.settingsOf(t, token, `{"section":"images","limit":50}`)
	if field(out, "images", "next") != nil || field(out, "images", "source") != "organization" {
		t.Errorf("one page: %v", out["images"])
	}
	byID := map[string]any{}
	var order []string
	for _, img := range field(out, "images", "images").([]any) {
		byID[field(img, "id").(string)] = img
		order = append(order, field(img, "id").(string))
	}
	var routeOrder []string
	for _, shown := range page["images"].([]any) {
		id := field(shown, "id").(string)
		routeOrder = append(routeOrder, id)
		got := byID[id]
		text := func(path ...any) any {
			if s, ok := field(got, path...).(string); ok {
				if !strings.HasPrefix(s, "⟦external ") {
					t.Errorf("%s %v not external text: %q", id, path, s)
				}
				return unwrapped(s)
			}
			return field(got, path...)
		}
		if text("name") != field(shown, "name") || field(shown, "description") != "" && text("description") != field(shown, "description") ||
			field(shown, "description") == "" && field(got, "description") != nil ||
			(field(shown, "archivedAt") == nil) != (field(got, "archivedAt") == nil) ||
			field(shown, "archivedAt") != nil && !sameInstant(field(shown, "archivedAt"), field(got, "archivedAt")) ||
			field(shown, "isDefault") != field(got, "organizationDefault") || (field(shown, "draft") != nil) != field(got, "draft") {
			t.Errorf("%s: tool %v, route %v", id, got, shown)
		}
		for _, k := range []string{"versionId", "number", "canRunContainers"} {
			if !reflect.DeepEqual(field(shown, "published", k), field(got, "published", k)) {
				t.Errorf("%s published.%s: %v, route %v", id, k, field(got, "published", k), field(shown, "published", k))
			}
		}
		if field(shown, "published") != nil && (field(shown, "published", "userRef") != field(got, "published", "ref") ||
			!sameInstant(field(shown, "published", "builtAt"), field(got, "published", "builtAt"))) {
			t.Errorf("%s published: %v, route %v", id, field(got, "published"), field(shown, "published"))
		}
		for _, k := range []string{"versionId", "number", "state"} {
			if !reflect.DeepEqual(field(shown, "pending", k), field(got, "pending", k)) {
				t.Errorf("%s pending.%s: %v, route %v", id, k, field(got, "pending", k), field(shown, "pending", k))
			}
		}
		parents := field(shown, "parents").([]any)
		if len(parents) != len(field(got, "parents").([]any)) {
			t.Errorf("%s parents: %v, route %v", id, field(got, "parents"), parents)
		}
		for i, p := range parents {
			if field(p, "id") != field(got, "parents", i, "id") || field(p, "name") != text("parents", i, "name") {
				t.Errorf("%s parent %d: %v, route %v", id, i, field(got, "parents", i), p)
			}
		}
	}
	if !reflect.DeepEqual(order, routeOrder) {
		t.Errorf("order: tool %v, route %v", order, routeOrder)
	}
	if field(page, "defaultImageId") != "img_base" || field(byID["img_base"], "organizationDefault") != true {
		t.Errorf("default: route %v, tool %v", page["defaultImageId"], byID["img_base"])
	}
	// The fixture holds what each mapped leaf can hold: an archived image, a
	// pending build, a draft and a parent.
	if field(byID["img_old"], "archivedAt") == nil || field(byID["img_app"], "pending", "state") != "failed" ||
		field(byID["img_app"], "draft") != true || field(byID["img_app"], "parents", 0, "id") != "img_base" {
		t.Errorf("fixture coverage: %v", byID)
	}

	schemaRules := map[string]coverageRule{
		"new.name": mapped, "new.description": mapped, "patch.description": mapped,
		"patch.archived": mapped, "new.canRunContainers": mapped,
		"new.containerfile": {Excluded: "Containerfile body"}, "new.buildArgs": {Excluded: "build input, shown in history only"},
		"new.note": {Excluded: "version note: history"},
	}
	schema := f.settingsRoute(t, "/schemas/images")
	if err := classifyResponse(schema, schemaRules); err != nil {
		t.Fatal(err)
	}

	// Explicit only: by default each role's image stays in roles.
	if def := f.settingsOf(t, token, `{}`); def["images"] != nil || field(roleIn(def, "implementer"), "image", "value", "id") != "img_web" {
		t.Errorf("default output: images %v, implementer image %v", def["images"], field(roleIn(def, "implementer"), "image"))
	}
	var raw json.RawMessage
	for _, args := range []string{`{}`, `{"section":"images"}`, `{"section":"roles"}`} {
		if status := f.postAs(t, token, "settings", args, &raw); status != 200 {
			t.Fatalf("%s: %d", args, status)
		}
		for _, canary := range []string{"CONTAINERFILE-CANARY", "BUILD-LOG-CANARY", "BUILD-ERROR-CANARY", "FROM debian", "FROM image:"} {
			if strings.Contains(string(raw), canary) {
				t.Errorf("%s shows %s", args, canary)
			}
		}
	}
}

// A large library pages completely in the route's order, each page under the
// tool result bound; another organisation's images and cursors stay out.
func TestSettingsImagesPageALargeLibraryAndStayInTheOrganisation(t *testing.T) {
	f, token := imageLibraryWorld(t)
	mustExec(t, f.owner, `INSERT INTO images (id, organization_id, name, description, archived_at)
		SELECT 'img_p' || i, $1, 'p' || lpad(i::text, 3, '0') || repeat('-', 58), repeat('<', 500),
			CASE WHEN i % 7 = 0 THEN now() END
		FROM generate_series(1, 130) i`, f.org)
	other := dbtest.Org(t, f.owner)
	mustExec(t, f.owner, `INSERT INTO images (id, organization_id, name) VALUES ('img_theirs', $1, 'other-org-image')`, other)
	route := f.settingsRoute(t, "/v1/images")
	var want []string
	for _, img := range route["images"].([]any) {
		want = append(want, field(img, "id").(string))
	}
	var got []string
	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		args, _ := json.Marshal(map[string]any{"section": "images", "limit": 50, "cursor": cursor})
		var raw json.RawMessage
		if status := f.postAs(t, token, "settings", string(args), &raw); status != 200 {
			t.Fatalf("page %d: %d %s", pages, status, raw)
		}
		if len(raw) > 64<<10 {
			t.Fatalf("page %d: %d bytes", pages, len(raw))
		}
		if strings.Contains(string(raw), "other-org-image") || strings.Contains(string(raw), "img_theirs") {
			t.Fatal("another organisation's image")
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		for _, img := range field(out, "images", "images").([]any) {
			id := field(img, "id").(string)
			if seen[id] {
				t.Fatalf("%s twice", id)
			}
			seen[id] = true
			got = append(got, id)
		}
		pages++
		t.Logf("images page %d: %d bytes, %d images", pages, len(raw), len(field(out, "images", "images").([]any)))
		if cursor, _ = field(out, "images", "next").(string); cursor == "" {
			break
		}
	}
	if !reflect.DeepEqual(got, want) || len(got) != 134 || pages < 3 {
		t.Errorf("paged %d images over %d pages, route lists %d; same order: %v", len(got), pages, len(want), reflect.DeepEqual(got, want))
	}
	// A cursor of another section, of another organisation, or not a cursor is refused.
	models, _ := field(f.settingsOf(t, token, `{"section":"models","limit":1}`), "models", "next").(string)
	first, _ := field(f.settingsOf(t, token, `{"section":"images","limit":1}`), "images", "next").(string)
	var c map[string]any
	if raw, err := base64.RawURLEncoding.DecodeString(first); err != nil || json.Unmarshal(raw, &c) != nil {
		t.Fatalf("cursor %q: %v", first, err)
	}
	c["org"] = other
	forged, _ := json.Marshal(c)
	for _, c := range []string{models, base64.RawURLEncoding.EncodeToString(forged), "broken"} {
		args, _ := json.Marshal(map[string]any{"section": "images", "cursor": c})
		if status, out := f.post(t, token, "settings", string(args)); status != 422 {
			t.Errorf("cursor %q: %d %v", c, status, out)
		}
	}
}
