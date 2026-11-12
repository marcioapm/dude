package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// 262 applies over main's schema with conductor wakes of every kind
// already there, keeps them, and takes the workflow kind (its 600
// characters) and a dispatch row; a run is bound to one dispatch only.
func TestMigration262WorkflowDispatchesUpFromMain(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "262")
	ctx := context.Background()
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_262', 'O', 'o262')`)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_262', 'org_262', 'P', 'p262', 'P')`)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_262', 'org_262', 'prj_262', 1, 'T')`)
	mustExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_262', 'org_262', 'prj_262', 'target', 'https://github.com/acme/target.git')`)
	mustExec(t, owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
		VALUES ('cwk_a', 'org_262', 'wi_262', 'recovered', 'k1', $1), ('cwk_b', 'org_262', 'wi_262', 'checks', 'k2', 'x')`, strings.Repeat("r", 600))
	if _, err := owner.Exec(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
		VALUES ('cwk_c', 'org_262', 'wi_262', 'workflow', 'k3', 'x')`); err == nil {
		t.Fatal("a workflow wake before 262")
	}
	apply()

	if n := scalar[int](t, owner, `SELECT count(*) FROM conductor_wakes WHERE task_id = 'wi_262'`); n != 2 {
		t.Fatalf("%d wakes kept", n)
	}
	mustExec(t, owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
		VALUES ('cwk_c', 'org_262', 'wi_262', 'workflow', 'k3', $1)`, strings.Repeat("w", 600))
	if _, err := owner.Exec(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
		VALUES ('cwk_d', 'org_262', 'wi_262', 'workflow', 'k4', $1)`, strings.Repeat("w", 601)); err == nil {
		t.Fatal("a workflow wake past 600 characters")
	}
	mustExec(t, owner, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, run_id)
		VALUES ('wfd_1', 'org_262', 'wi_262', 'repo_262', 'ci.yml', 'main', 7)`)
	if _, err := owner.Exec(ctx, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, run_id)
		VALUES ('wfd_2', 'org_262', 'wi_262', 'repo_262', 'ci.yml', 'main', 7)`); err == nil {
		t.Fatal("one run bound to two dispatches")
	}
	if got := scalar[string](t, owner, `SELECT state || ' ' || failed_reads FROM workflow_dispatches WHERE id = 'wfd_1'`); got != "dispatching 0" {
		t.Errorf("a new dispatch reads %q", got)
	}
	// One answer, one dispatch; one GitHub refused (failed) does not spend it.
	mustExec(t, owner, `INSERT INTO questions (id, organization_id, task_id, prompt) VALUES ('q_262', 'org_262', 'wi_262', 'Run?')`)
	mustExec(t, owner, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, question_id, state)
		VALUES ('wfd_3', 'org_262', 'wi_262', 'repo_262', 'deploy.yml', 'main', 'q_262', 'failed'),
		       ('wfd_4', 'org_262', 'wi_262', 'repo_262', 'deploy.yml', 'main', 'q_262', 'dispatching')`)
	if _, err := owner.Exec(ctx, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, question_id)
		VALUES ('wfd_5', 'org_262', 'wi_262', 'repo_262', 'deploy.yml', 'main', 'q_262')`); err == nil {
		t.Fatal("one answer reserved by two dispatches")
	}
}

func scalar[T any](t *testing.T, c *pgx.Conn, sql string) T {
	t.Helper()
	var v T
	if err := c.QueryRow(context.Background(), sql).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
