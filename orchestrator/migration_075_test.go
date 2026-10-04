package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// 075 on an installation with Runs: its transaction, which holds the Runs
// table's exclusive lock, reads none of them. The conductor's note is
// still bounded for every write after it.
func TestTheConductorNoteMigrationReadsNoRun(t *testing.T) {
	ctx := context.Background()
	owner, _ := dbtest.Upgrade(t, "075")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_u', 'U', 'u')`)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_u', 'org_u', 'P', 'p', 'P')`)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_u', 'org_u', 'prj_u', 1, 'T', 'G')`)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status)
		SELECT 'run_u' || g, 'org_u', 'prj_u', 'wi_u', 1, 'completed' FROM generate_series(1, 1000) g`)

	files, err := filepath.Glob("../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rest []string
	for _, f := range files {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "075_"):
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("%s: %v", base, err)
			}
		case base > "075":
			rest = append(rest, f)
		}
	}
	var scanned int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(seq_tup_read), 0) FROM pg_stat_xact_user_tables WHERE relname = 'runs'`).
		Scan(&scanned); err != nil {
		t.Fatal(err)
	}
	if scanned != 0 {
		t.Errorf("075 read %d Runs under the table's lock, want none", scanned)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, f := range rest {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
	}

	mustExec(t, owner, `UPDATE runs SET conductor_note = repeat('n', 4000) WHERE id = 'run_u1'`)
	_, err = owner.Exec(ctx, `UPDATE runs SET conductor_note = repeat('n', 4001) WHERE id = 'run_u2'`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Errorf("a 4001-character note: %v, want a check violation", err)
	}
}
