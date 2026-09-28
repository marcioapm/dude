package phases

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A final diff and a live read can arrive in either order: the collector
// takes a stopped placement's patches whenever lux has them, and a live
// read may land as the container stops. The newer placement wins, and
// within one the final diff does.
func TestTheNewestPlacementsDiffWinsAndItsFinalOneOverAnyLiveRead(t *testing.T) {
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
	diff := func(path string) string {
		return "# dude-diff target aaaa\ndiff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+x\n"
	}
	record := func(path string, epoch int, final bool) bool {
		changed, err := recordRunDiff(ctx, app.InOrg, org, "prj_"+org, "wi_"+org, "run_"+org, diff(path), epoch, final)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	shown := func() (path string, final bool) {
		_ = owner.QueryRow(ctx, `SELECT files->0->>'path', final FROM run_diffs WHERE run_id = $1`, "run_"+org).Scan(&path, &final)
		return path, final
	}

	record("live1", 1, false)
	if !record("final1", 1, true) {
		t.Fatal("a placement's final diff did not replace its live one")
	}
	if record("late1", 1, false) {
		t.Error("a live read that landed as the container stopped replaced its final diff")
	}
	if !record("live2", 2, false) {
		t.Fatal("the resumed placement's live read did not replace the last one's final diff")
	}
	if record("final1", 1, true) {
		t.Error("the last placement's final diff, collected late, replaced the resumed one's live diff")
	}
	if path, final := shown(); path != "live2" || final {
		t.Errorf("shown %s (final %v), want live2", path, final)
	}
	if record("live2", 2, false) {
		t.Error("an identical read was written again")
	}
}
