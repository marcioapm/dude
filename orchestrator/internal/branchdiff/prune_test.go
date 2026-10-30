package branchdiff_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

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
	if n, err := branchdiff.Prune(ctx, app, nil); err != nil || n != 0 {
		t.Errorf("a pass with nothing to prune: %d (%v)", n, err)
	}
}

// A pass stops on a short batch: 5 to prune in batches of 2 is three
// calls (2, 2, 1), not a fourth. A test-only trigger makes another
// snapshot prunable as the short batch deletes its last row; only a
// fourth call would take it.
func TestPruneStopsOnAShortBatch(t *testing.T) {
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
	exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_pr', $1, 'prj_pr', 'api', 'https://example.com/acme/api'),
		('repo_pz', $1, 'prj_pr', 'web', 'https://example.com/acme/web')`, org)
	const insert = `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
		target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id, captured_at)
		VALUES ($1, $2, 'wi_pr', 1, $3, 'api', 'dude/wi_pr', 'main', $4, $4, $5, '[]', 'c', '{}', $6, $7,
		now() - make_interval(hours => 100 - $8::int))`
	prev := any(nil)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("tbs_pr%d", i)
		rel := "fast-forward"
		if i == 1 {
			rel = "initial"
		}
		exec(insert, id, org, "repo_pr", fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 100+i), rel, prev, i)
		prev = id
	}
	exec(`CREATE FUNCTION test_more_after_pr5() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.id = 'tbs_pr5' THEN
				INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
					target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id, captured_at)
				VALUES ('tbs_pz1', OLD.organization_id, 'wi_pr', 1, 'repo_pz', 'web', 'dude/wi_pr', 'main', OLD.target_sha, OLD.target_sha,
					repeat('e', 40), '[]', 'c', '{}', 'initial', NULL, now() - interval '90 hours'),
				       ('tbs_pz2', OLD.organization_id, 'wi_pr', 1, 'repo_pz', 'web', 'dude/wi_pr', 'main', OLD.target_sha, OLD.target_sha,
					repeat('f', 40), '[]', 'c', '{}', 'fast-forward', 'tbs_pz1', now() - interval '80 hours');
			END IF;
			RETURN NULL;
		END $$`)
	exec(`CREATE TRIGGER test_more_after_pr5 AFTER DELETE ON task_branch_snapshots FOR EACH ROW EXECUTE FUNCTION test_more_after_pr5()`)
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `DROP TRIGGER IF EXISTS test_more_after_pr5 ON task_branch_snapshots`)
		_, _ = owner.Exec(ctx, `DROP FUNCTION IF EXISTS test_more_after_pr5()`)
	})
	n, err := branchdiff.PruneUpTo(ctx, app, 2)
	if err != nil || n != 5 {
		t.Fatalf("pruned %d (%v), want 5 over three batches, the last short", n, err)
	}
	var left string
	if err := owner.QueryRow(ctx, `SELECT string_agg(id, ',' ORDER BY id) FROM task_branch_snapshots`).Scan(&left); err != nil ||
		left != "tbs_pr6,tbs_pz1,tbs_pz2" {
		t.Errorf("left %q (%v): a call after the short batch took tbs_pz1", left, err)
	}
}

// A pruner that fails says so: the loop logs it rather than taking the
// silence for "nothing to prune".
func TestPruneReportsAFailure(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `REVOKE EXECUTE ON FUNCTION prune_branch_snapshots(integer) FROM dude_sweeper`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `GRANT EXECUTE ON FUNCTION prune_branch_snapshots(integer) TO dude_sweeper`)
	})
	if n, err := branchdiff.PruneUpTo(ctx, app, 2); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("pruned %d with EXECUTE revoked: err %v, want permission denied", n, err)
	}
}

// A pass over a backlog logs how many went and reports 0, so the loop
// waits its interval instead of running a pass at once that finds nothing.
func TestPruneLogsThePassAndReportsItDone(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	seedChain(t, owner, org, 4)
	var out bytes.Buffer
	n, err := branchdiff.Prune(ctx, app, slog.New(slog.NewTextHandler(&out, nil)))
	if err != nil || n != 0 {
		t.Errorf("a finished pass: %d (%v), want 0", n, err)
	}
	if !strings.Contains(out.String(), "branch snapshots pruned") || !strings.Contains(out.String(), "pruned=3") {
		t.Errorf("log %q, want the 3 pruned", out.String())
	}
	var left int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM task_branch_snapshots`).Scan(&left); err != nil || left != 1 {
		t.Errorf("left %d (%v), want the latest", left, err)
	}
}

// seedChain records n snapshots of one lineage, a day apart, the newest
// two days old.
func seedChain(t *testing.T, owner *pgx.Conn, org string, n int) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_lg', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_lg', $1, 'prj_lg', 1, 'T', 'G')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_lg', $1, 'prj_lg', 'api', 'https://example.com/acme/api')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	prev := any(nil)
	for i := 1; i <= n; i++ {
		id, rel := fmt.Sprintf("tbs_lg%d", i), "fast-forward"
		if i == 1 {
			rel = "initial"
		}
		if _, err := owner.Exec(ctx, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
			target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id, captured_at)
			VALUES ($1, $2, 'wi_lg', 1, 'repo_lg', 'api', 'dude/wi_lg', 'main', $3, $3, $4, '[]', 'c', '{}', $5, $6,
			now() - make_interval(days => 2 + $7::int))`, id, org, fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 200+i), rel, prev, n-i); err != nil {
			t.Fatal(err)
		}
		prev = id
	}
}
