package phases

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Only a git.sync lux reports done, naming a full lowercase commit, is kept
// as the Run's base for its repository and ref.
func TestOnlyACompletedSyncNamingACommitIsKept(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1)`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	run := "run_" + org
	good := strings.Repeat("ab", 20)
	for _, c := range []struct {
		name string
		sync map[string]any
		want string
	}{
		{"a failed sync", map[string]any{"repo": "target", "ref": "main", "status": "failed", "to": good}, `{}`},
		{"an abbreviated commit", map[string]any{"repo": "target", "ref": "main", "status": "fetched", "to": good[:12]}, `{}`},
		{"an uppercase commit", map[string]any{"repo": "target", "ref": "main", "status": "fetched", "to": strings.ToUpper(good)}, `{}`},
		{"a fetched commit", map[string]any{"repo": "target", "ref": "main", "status": "fetched", "to": good}, `{"target": {"main": "` + good + `"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := owner.Exec(ctx, `UPDATE runs SET synced_bases = '{}' WHERE id = $1`, run); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(c.sync)
			if err := app.InOrg(ctx, org, func(tx pgx.Tx) error { return recordSyncedBase(ctx, tx, run, raw) }); err != nil {
				t.Fatal(err)
			}
			var same bool
			if err := owner.QueryRow(ctx, `SELECT synced_bases = $2::jsonb FROM runs WHERE id = $1`, run, c.want).Scan(&same); err != nil {
				t.Fatal(err)
			}
			if !same {
				t.Fatalf("synced_bases is not %s", c.want)
			}
		})
	}
}
