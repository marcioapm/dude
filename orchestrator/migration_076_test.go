package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The Go migrator applies 075's columns and 076's index built concurrently
// over existing Runs, as the runner does outside a transaction: the index
// is valid and serves conductor_run_id.
func TestTheConductorRunsIndexIsBuiltOnAnUpgrade(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "075")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_u', 'U', 'u')`)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_u', 'org_u', 'P', 'p', 'P')`)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_u', 'org_u', 'prj_u', 1, 'T', 'G')`)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status)
		SELECT 'run_u' || g, 'org_u', 'prj_u', 'wi_u', 1, 'completed' FROM generate_series(1, 50) g`)
	apply()
	var valid bool
	if err := owner.QueryRow(context.Background(), `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'runs_conductor_run_idx'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("runs_conductor_run_idx valid=%v: %v", valid, err)
	}
	mustExec(t, owner, `UPDATE runs SET conductor_run_id = 'run_u1' WHERE id = 'run_u2'`)
	var n int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE conductor_run_id = 'run_u1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d Runs under run_u1: %v", n, err)
	}
}
