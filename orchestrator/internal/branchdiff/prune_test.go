package branchdiff_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/branchdiff"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Prune calls the pruner a batch at a time until a batch comes back short:
// one pass takes a backlog larger than a batch, and the latest stays.
func TestPruneTakesEveryBatchOfABacklog(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_pr', $1, 'P', 'p', 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_pr', $1, 'prj_pr', 1, 'T', 'G')`, org)
	exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_pr', $1, 'prj_pr', 'api', 'https://example.com/acme/api')`, org)
	prev := any(nil)
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("tbs_pr%d", i)
		rel := "fast-forward"
		if i == 1 {
			rel = "initial"
		}
		exec(`INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
			target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id, captured_at)
			VALUES ($1, $2, 'wi_pr', 1, 'repo_pr', 'api', 'dude/wi_pr', 'main', $3, $3, $4, '[]', 'c', '{}', $5, $6,
			now() - make_interval(hours => 100 - $7::int))`,
			id, org, fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 100+i), rel, prev, i)
		prev = id
	}
	n, err := branchdiff.PruneUpTo(ctx, app, 2)
	if err != nil || n != 6 {
		t.Fatalf("pruned %d (%v), want 6 over four batches", n, err)
	}
	var left string
	var steps int
	if err := owner.QueryRow(ctx, `SELECT string_agg(id, ',') , max(steps) FROM task_branch_snapshots`).Scan(&left, &steps); err != nil ||
		left != "tbs_pr7" || steps != 7 {
		t.Errorf("left %q with steps %d (%v), want tbs_pr7 over 7", left, steps, err)
	}
	if n, err := branchdiff.Prune(ctx, app); err != nil || n != 0 {
		t.Errorf("a pass with nothing to prune: %d (%v)", n, err)
	}
}
