package phases

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A recovery checks that its dead Run's copy is there and reads the values
// to scan for in one snapshot: a copy deleted between the two (the
// backstop, a collection, committing meanwhile) is still read whole, so
// the scan looks for the value the Run was given and that was rotated
// since, never for the value in force alone.
func TestARecoveryReadsTheCopyItChecked(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	const given, rotated = "given-value-0123456789", "rotated-value-9876543210"
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, ended_at, project_secret_names)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'failed', now(), '{IMPL_TOKEN}')`,
		`INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_'||$1, $1, 'IMPL_TOKEN', '` + given + `')`,
		`INSERT INTO project_secrets (project_id, organization_id, name, value, hint) VALUES ('prj_'||$1, $1, 'IMPL_TOKEN', '` + rotated + `', '3210')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	deleted := false
	recoveryChecked = func() {
		if _, err := owner.Exec(ctx, `DELETE FROM run_project_secrets WHERE run_id = $1`, "run_"+org); err != nil {
			t.Error(err)
		}
		deleted = true
	}
	t.Cleanup(func() { recoveryChecked = nil })
	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	red, err := s.recoveryRedactor(ctx, org, "run_"+org)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("the copy was not deleted between the check and the read")
	}
	if _, ok := red.Contains("x " + given + " x"); !ok {
		t.Error("the scan would not look for the value the Run was given: its copy, deleted after the check, was not read")
	}
	if _, ok := red.Contains("x " + rotated + " x"); !ok {
		t.Error("the scan would not look for the value in force")
	}
	// Checked again, the copy is gone: refused.
	if _, err := s.recoveryRedactor(ctx, org, "run_"+org); err != errSecretsExpired {
		t.Errorf("with the copy gone: %v, want errSecretsExpired", err)
	}
}
