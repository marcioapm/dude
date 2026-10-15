package orchestrator_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestProjectSecretRecipientsAndRunNames(t *testing.T) {
	database, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	other := dbtest.Org(t, owner)
	ctx := context.Background()
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_secrets', $1, 'Acme', 'acme', 'ACME')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('tsk_secrets', $1, 'prj_secrets', 1, 'Test credentials', 'Test fixture')`, org)
	mustExec(t, owner, `INSERT INTO project_secrets (project_id, organization_id, name, value, hint) VALUES ('prj_secrets', $1, 'SNOWFLAKE_PASSWORD', 'sf-test-not-a-real-password-123', '-123')`, org)
	var recipients []string
	if err := owner.QueryRow(ctx, `SELECT recipients FROM project_secrets WHERE project_id = 'prj_secrets'`).Scan(&recipients); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recipients, []string{"previews"}) {
		t.Fatalf("legacy insert recipients = %v; want previews only", recipients)
	}
	for _, names := range [][]string{{}, {"conductor"}, {"brainstorm"}, {"tester", "invalid"}, {"tester", "tester"}, {"tester", ""}} {
		if _, err := owner.Exec(ctx, `UPDATE project_secrets SET recipients = $1 WHERE project_id = 'prj_secrets'`, names); err == nil {
			t.Errorf("accepted recipients %v", names)
		}
	}
	if _, err := owner.Exec(ctx, `UPDATE project_secrets SET recipients = NULL WHERE project_id = 'prj_secrets'`); err == nil {
		t.Error("accepted NULL recipients")
	}
	for _, name := range []string{"previews", "tester", "implementer", "fixer", "simplifier", "reviewer"} {
		mustExec(t, owner, `UPDATE project_secrets SET recipients = ARRAY[$1] WHERE project_id = 'prj_secrets'`, name)
	}
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, project_secret_names)
		VALUES ('run_secrets', $1, 'prj_secrets', 'tsk_secrets', 1, ARRAY['SNOWFLAKE_PASSWORD'])`, org)
	if err := database.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT project_secret_names FROM runs WHERE id = 'run_secrets'`).Scan(&recipients)
	}); err != nil || !slices.Equal(recipients, []string{"SNOWFLAKE_PASSWORD"}) {
		t.Fatalf("Run names = %v: %v", recipients, err)
	}
	if err := database.InOrg(ctx, other, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_secrets WHERE project_id = 'prj_secrets'`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Error("another organization read the secret")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.InOrg(ctx, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients)
			VALUES ('prj_secrets', $1, 'POISON', 'not-a-real-secret', 'cret', ARRAY['tester'])`, org)
		return err
	}); err == nil {
		t.Error("another organization wrote the secret")
	}
}
