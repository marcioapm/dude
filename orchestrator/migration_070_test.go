package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// sizeRow is one of an organisation's machine sizes, as migration 070
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

// Migration 070 renames the orchestrator role to conductor in what is
// stored — the enum's rows, the role keys of settings, the ledger's role —
// and gives every organisation the Small size and the Thinker tier as the
// conductor's, reusing a size already named Small, while Standard stays the default.
func TestTheConductorMigrationRenamesTheRoleAndSeedsSmall(t *testing.T) {
	ctx := context.Background()
	owner, apply := dbtest.Upgrade(t, "070")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug, default_agent_models) VALUES
		('org_plain', 'Plain', 'plain', '{"orchestrator":{"model":"llm-openai/o","effort":"high"},"reviewer":{"model":"llm-openai/r"}}'),
		('org_small', 'HasSmall', 'has-small', '{}'),
		('org_named', 'Named', 'named', '{"orchestrator":{"machineSize":"msz_big"}}')`)
	mustExec(t, owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib)
		VALUES ('msz_mine', 'org_small', 'small', 1, 2048, 10), ('msz_big', 'org_named', 'Big', 8, 16384, 80)`)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, agent_models)
		VALUES ('prj_m', 'org_plain', 'P', 'p', 'P', '{"orchestrator":{"model":"llm-openai/p"},"implementer":{"model":"llm-openai/i"}}')`)
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
	if _, old := org["orchestrator"]; old || org["conductor"]["model"] != "llm-openai/o" || org["conductor"]["effort"] != "high" ||
		org["reviewer"]["model"] != "llm-openai/r" {
		t.Fatalf("organisation settings after: %s", models)
	}
	if _, old := project["orchestrator"]; old || project["conductor"]["model"] != "llm-openai/p" || project["implementer"]["model"] != "llm-openai/i" {
		t.Fatalf("project settings after: %s", projectModels)
	}
	if evp["role"] != "conductor" || evp["model"] != "m" {
		t.Fatalf("the ledger's role after: %s", payload)
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
	// An organisation with a size named Small keeps it, and the conductor uses it.
	if s := sizesOf(t, owner, "org_small"); len(s) != 2 || conductorSize(t, owner, "org_small") != "msz_mine" {
		t.Fatalf("org_small: sizes %+v, conductor %q", s, conductorSize(t, owner, "org_small"))
	}
	// One that already named the conductor's size keeps its choice.
	if got := conductorSize(t, owner, "org_named"); got != "msz_big" {
		t.Fatalf("org_named's conductor size = %q", got)
	}

	// A new organisation is seeded the same way.
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_new', 'New', 'new')`)
	fresh := sizesOf(t, owner, "org_new")
	if len(fresh) != 2 || !fresh["Standard"].Default || fresh["Small"].Default || conductorSize(t, owner, "org_new") != fresh["Small"].ID {
		t.Fatalf("a new organisation: %+v, conductor %q", fresh, conductorSize(t, owner, "org_new"))
	}

	// Every organisation's conductor runs on its Thinker tier: one seeded
	// before 070 without an orchestrator key, and one created after.
	for _, org := range []string{"org_plain", "org_small", "org_named", "org_new"} {
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
