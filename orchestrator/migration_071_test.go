package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// sizeRow is one of an organisation's machine sizes, as migration 071
// leaves it.
type sizeRow struct {
	ID, Name        string
	CPUs            float64
	MemoryMiB, Disk int
	Default         bool
}

func sizesOf(t *testing.T, owner *pgx.Conn, org string) map[string]sizeRow {
	t.Helper()
	rows, err := owner.Query(context.Background(), `SELECT id, name, cpus::float8, memory_mib, disk_gib, is_default
		FROM machine_sizes WHERE organization_id = $1`, org)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[sizeRow])
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]sizeRow{}
	for _, s := range got {
		out[s.Name] = s
	}
	return out
}

func conductorSize(t *testing.T, owner *pgx.Conn, org string) string {
	t.Helper()
	var id *string
	if err := owner.QueryRow(context.Background(),
		`SELECT default_agent_models->'conductor'->>'machineSize' FROM organizations WHERE id = $1`, org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == nil {
		return ""
	}
	return *id
}

// Migration 071 renames the orchestrator role to conductor in what is
// stored — the enum's rows, the role keys of settings, the ledger's role —
// and gives every organisation the Small size and the Thinker tier as the
// conductor's, reusing a size already named Small, while Standard stays the default.
func TestTheConductorMigrationRenamesTheRoleAndSeedsSmall(t *testing.T) {
	ctx := context.Background()
	owner, apply := dbtest.Upgrade(t, "071")
	// Settings as 069 leaves them: roles name tiers. org_plain's
	// orchestrator is on its Coder tier, not the Thinker 071 gives one with
	// none, and its project overrides it with a tier of its own.
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES
		('org_plain', 'Plain', 'plain'), ('org_small', 'HasSmall', 'has-small'), ('org_named', 'Named', 'named')`)
	mustExec(t, owner, `INSERT INTO model_tiers (id, organization_id, name, model, position)
		VALUES ('mtr_custom', 'org_plain', 'Custom', 'custom-model', 3)`)
	var coder string
	if err := owner.QueryRow(ctx, `SELECT id FROM model_tiers WHERE organization_id = 'org_plain' AND name = 'Coder'`).Scan(&coder); err != nil {
		t.Fatal(err)
	}
	mustExec(t, owner, `UPDATE organizations SET default_agent_models = default_agent_models
		|| jsonb_build_object('orchestrator', jsonb_build_object('tier', $1::text, 'effort', 'high'))
		|| '{"reviewer":{"tier":"mtr_kept","effort":"low"}}' WHERE id = 'org_plain'`, coder)
	mustExec(t, owner, `UPDATE organizations SET default_agent_models = default_agent_models
		|| '{"orchestrator":{"machineSize":"msz_big"}}' WHERE id = 'org_named'`)
	mustExec(t, owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib)
		VALUES ('msz_mine', 'org_small', 'small', 1, 2048, 10), ('msz_big', 'org_named', 'Big', 8, 16384, 80)`)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, agent_models)
		VALUES ('prj_m', 'org_plain', 'P', 'p', 'P', '{"orchestrator":{"tier":"mtr_custom","effort":"low"},"implementer":{"tier":"mtr_custom"}}')`)
	mustExec(t, owner, `INSERT INTO model_tier_upgrade_notes (organization_id, project_id, role, old_model, tier_id, tier_name, model_changed)
		VALUES ('org_plain', NULL, 'orchestrator', 'llm-openai/o', $1, 'Coder', true),
		       ('org_plain', 'prj_m', 'reviewer', 'llm-openai/r', NULL, 'Thinker', false)`, coder)
	smallBefore := sizesOf(t, owner, "org_small")["small"]
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, title, goal, number)
		VALUES ('wi_m', 'org_plain', 'prj_m', 'T', 'A goal long enough to pass.', 1)`)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role)
		VALUES ('run_m', 'org_plain', 'prj_m', 'wi_m', 1, 'orchestrator')`)
	mustExec(t, owner, `INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
		VALUES ('ev_m', 'org_plain', 'session.started', 'run_m', 'system', 'x', 'test', '{"role":"orchestrator","model":"m"}')`)

	apply()

	var role, models, projectModels, payload string
	if err := owner.QueryRow(ctx, `SELECT r.role::text, o.default_agent_models::text, p.agent_models::text, e.payload::text
		FROM runs r JOIN organizations o ON o.id = r.organization_id JOIN projects p ON p.id = r.project_id
		JOIN events e ON e.run_id = r.id WHERE r.id = 'run_m'`).Scan(&role, &models, &projectModels, &payload); err != nil {
		t.Fatal(err)
	}
	if role != "conductor" {
		t.Fatalf("the run's role = %q", role)
	}
	var org, project map[string]map[string]any
	_ = json.Unmarshal([]byte(models), &org)
	_ = json.Unmarshal([]byte(projectModels), &project)
	var evp map[string]any
	_ = json.Unmarshal([]byte(payload), &evp)
	if _, old := org["orchestrator"]; old || org["conductor"]["tier"] != coder || org["conductor"]["effort"] != "high" ||
		org["reviewer"]["tier"] != "mtr_kept" || org["reviewer"]["effort"] != "low" {
		t.Fatalf("organisation settings after: %s", models)
	}
	if _, old := project["orchestrator"]; old || project["conductor"]["tier"] != "mtr_custom" || project["conductor"]["effort"] != "low" ||
		project["implementer"]["tier"] != "mtr_custom" {
		t.Fatalf("project settings after: %s", projectModels)
	}
	if evp["role"] != "conductor" || evp["model"] != "m" {
		t.Fatalf("the ledger's role after: %s", payload)
	}
	// The upgrade's notes name the role as settings now do.
	var noteRoles []string
	if err := owner.QueryRow(ctx, `SELECT array_agg(role ORDER BY role) FROM model_tier_upgrade_notes
		WHERE organization_id = 'org_plain'`).Scan(&noteRoles); err != nil {
		t.Fatal(err)
	}
	if len(noteRoles) != 2 || noteRoles[0] != "conductor" || noteRoles[1] != "reviewer" {
		t.Fatalf("upgrade notes' roles after: %v", noteRoles)
	}

	// Small for each, the conductor's; Standard stays the default.
	plain := sizesOf(t, owner, "org_plain")
	small, ok := plain["Small"]
	if !ok || small.CPUs != 0.5 || small.MemoryMiB != 1024 || small.Disk != 10 || small.Default || !plain["Standard"].Default {
		t.Fatalf("org_plain sizes: %+v", plain)
	}
	if got := conductorSize(t, owner, "org_plain"); got != small.ID {
		t.Fatalf("org_plain's conductor size = %q, want Small %q", got, small.ID)
	}
	// An organisation with a size named Small keeps it as it was, and the conductor uses it.
	if s := sizesOf(t, owner, "org_small"); len(s) != 2 || s["small"] != smallBefore || smallBefore.ID != "msz_mine" ||
		conductorSize(t, owner, "org_small") != "msz_mine" {
		t.Fatalf("org_small: sizes %+v (before %+v), conductor %q", s, smallBefore, conductorSize(t, owner, "org_small"))
	}
	// One that already named the conductor's size keeps its choice.
	if got := conductorSize(t, owner, "org_named"); got != "msz_big" {
		t.Fatalf("org_named's conductor size = %q", got)
	}

	// A new organisation is seeded the same way, with Small's own resources.
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_new', 'New', 'new')`)
	fresh := sizesOf(t, owner, "org_new")
	newSmall, ok := fresh["Small"]
	if !ok || len(fresh) != 2 || !fresh["Standard"].Default || newSmall.Default ||
		newSmall.CPUs != 0.5 || newSmall.MemoryMiB != 1024 || newSmall.Disk != 10 ||
		conductorSize(t, owner, "org_new") != newSmall.ID {
		t.Fatalf("a new organisation: %+v, conductor %q", fresh, conductorSize(t, owner, "org_new"))
	}

	// The conductor of an organisation that gave it no tier runs on its
	// Thinker tier: ones from before 071, and one created after.
	for _, org := range []string{"org_small", "org_named", "org_new"} {
		var tier, thinker *string
		if err := owner.QueryRow(ctx, `SELECT o.default_agent_models->'conductor'->>'tier', t.id
			FROM organizations o JOIN model_tiers t ON t.organization_id = o.id AND t.name = 'Thinker'
			WHERE o.id = $1`, org).Scan(&tier, &thinker); err != nil {
			t.Fatal(err)
		}
		if tier == nil || thinker == nil || *tier != *thinker {
			t.Fatalf("%s's conductor tier = %v, want Thinker %v", org, tier, thinker)
		}
	}
}

// seed_conductor_size runs as its owner across tenants: the app role
// cannot call it, for another organisation or its own.
func TestTheAppCannotSeedAnotherOrganisationsConductorSize(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	mine, other := dbtest.Org(t, owner), dbtest.Org(t, owner)
	mustExec(t, owner, `UPDATE organizations SET default_agent_models = default_agent_models #- '{conductor,machineSize}' WHERE id = $1`, other)
	mustExec(t, owner, `DELETE FROM machine_sizes WHERE organization_id = $1 AND name = 'Small'`, other)

	for _, org := range []string{other, mine} {
		err := app.InOrg(ctx, mine, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT seed_conductor_size($1)`, org)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied for function seed_conductor_size") {
			t.Fatalf("seed_conductor_size(%s) as the app: %v", org, err)
		}
	}
	if got := conductorSize(t, owner, other); got != "" {
		t.Fatalf("the other organisation's conductor size = %q", got)
	}
	if _, ok := sizesOf(t, owner, other)["Small"]; ok {
		t.Fatal("the other organisation was given a Small size")
	}
}
