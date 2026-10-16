package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestRunSecretCopiesAreTenantScopedAndSurviveRotation(t *testing.T) {
	database, owner := dbtest.Open(t)
	org, other := dbtest.Org(t, owner), dbtest.Org(t, owner)
	ctx := context.Background()
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_copy', $1, 'Acme', 'acme', 'ACME')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('tsk_copy', $1, 'prj_copy', 1, 'Test copies', 'Test fixture')`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_copy', $1, 'prj_copy', 'tsk_copy', 1)`, org)
	const value = "sf-test-not-a-real-password-123"
	if err := database.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_copy', $1, 'SNOWFLAKE_PASSWORD', $2)`, org, value)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, owner, `INSERT INTO project_secrets (project_id, organization_id, name, value, hint) VALUES ('prj_copy', $1, 'SNOWFLAKE_PASSWORD', 'rotated-test-not-a-real-password', 'word')`, org)
	var got string
	if err := database.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT value FROM run_project_secrets WHERE run_id = 'run_copy'`).Scan(&got)
	}); err != nil || got != value {
		t.Fatalf("Run copy changed after rotation: %v", err)
	}
	if err := database.InOrg(ctx, other, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM run_project_secrets`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Error("another tenant read Run copies")
		}
		tag, err := tx.Exec(ctx, `DELETE FROM run_project_secrets WHERE run_id = 'run_copy'`)
		if err == nil && tag.RowsAffected() != 0 {
			t.Error("another tenant deleted Run copies")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{org, other} {
		if err := database.InOrg(ctx, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_copy', $1, 'POISON', $2)`, other, value)
			return err
		}); err == nil {
			t.Error("accepted a copy whose organization differs from its Run")
		}
	}
	if err := database.InSystem(ctx, "secret-copy-test", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT value FROM run_project_secrets`)
		return err
	}); err == nil {
		t.Error("sweeper read secret values")
	}
	mustExec(t, owner, `DELETE FROM runs WHERE id = 'run_copy'`)
	if err := owner.QueryRow(ctx, `SELECT count(*)::text FROM run_project_secrets WHERE run_id = 'run_copy'`).Scan(&got); err != nil || got != "0" {
		t.Fatalf("Run deletion retained copies: %v", err)
	}
}

func TestARunsCopyHoldsSeveralValuesPerNameOnceEach(t *testing.T) {
	database, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_multi', $1, 'Acme', 'acme', 'ACME')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('tsk_multi', $1, 'prj_multi', 1, 'Test copies', 'Test fixture')`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_multi', $1, 'prj_multi', 'tsk_multi', 1)`, org)
	insert := func(value string) (int64, error) {
		var n int64
		err := database.InOrg(ctx, org, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_multi', $1, 'TOKEN', $2)
				ON CONFLICT DO NOTHING`, org, value)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}
	// A backslash and a multi-byte character: the key's digest is of the
	// value's own bytes, so these two differ and each repeats as itself.
	const a, b = `tok-test-\101-é-AAAA`, `tok-test-A-é-AAAA`
	for i, c := range []struct {
		value string
		rows  int64
	}{{a, 1}, {b, 1}, {a, 0}, {b, 0}, {strings.Repeat("x", 32768), 1}} {
		if n, err := insert(c.value); err != nil || n != c.rows {
			t.Errorf("insert %d: %d rows, %v; want %d", i, n, err, c.rows)
		}
	}
	if _, err := insert(strings.Repeat("x", 32769)); err == nil {
		t.Error("accepted a value over 32 KiB")
	}
	var values []string
	if err := owner.QueryRow(ctx, `SELECT array_agg(value) FROM run_project_secrets WHERE run_id = 'run_multi'
		AND value_sha256 = sha256(convert_to(value, 'UTF8'))`).Scan(&values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || !slices.Contains(values, a) || !slices.Contains(values, b) {
		t.Errorf("copy holds %d values whose key is their UTF-8 digest, want 3", len(values))
	}
}

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
