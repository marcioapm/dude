package phases

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Two Runs of one task: Run A recorded attempting /merges for commit C in
// the target repository. Run B publishing the same C finds no attempt of
// its own, so a merge of C on the branch is not B's to adopt.
func TestARunDoesNotTakeAnotherRunsMergeAttemptAsItsOwn(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status)
			VALUES ('run_a_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'fix', 'failed'),
			       ('run_b_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 2, 'fix', 'running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	runOf := func(id string) phaseRun {
		return phaseRun{ID: id + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "fix"}
	}
	a, b := runOf("run_a_"), runOf("run_b_")
	const commit = "c0ffee0000000000000000000000000000000000"
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		return s.event(ctx, tx, a, delivery.EvGitMergeAttempted, ledger.ActorAgent, map[string]any{"repo": "target", "commit": commit})
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		run  phaseRun
		want bool
	}{{a, true}, {b, false}} {
		got, err := s.mergeAttempted(ctx, c.run, "target", commit)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("Run %s attempted the merge of %.8s: %v, want %v", c.run.ID, commit, got, c.want)
		}
	}
}
