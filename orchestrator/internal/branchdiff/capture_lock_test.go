package branchdiff

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A capture whose head already has an old snapshot (the branch went back
// to it) holds that snapshot from its read until the request names it: a
// prune in between skips it, never deletes it under the capture.
func TestTheCapturesReadOfAnExistingSnapshotKeepsItFromThePruner(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_ks', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_ks', $1, 'prj_ks', 1, 'T', 'G')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_ks', $1, 'prj_ks', 'api', 'https://example.com/acme/api')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	base, oldHead := fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 401)
	// tbs_ksx (two days old, superseded a day and a half ago) is prunable.
	for _, s := range []struct {
		id, head, rel string
		prev          any
		hours         int
	}{{"tbs_ksx", oldHead, "initial", nil, 48}, {"tbs_ksy", fmt.Sprintf("%040x", 402), "fast-forward", "tbs_ksx", 36}} {
		if _, err := owner.Exec(ctx, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
			target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id, captured_at)
			VALUES ($1, $2, 'wi_ks', 1, 'repo_ks', 'api', 'dude/wi_ks', 'main', $3, $3, $4, '[]', 'c', '{}', $5, $6,
			now() - make_interval(hours => $7))`, s.id, org, base, s.head, s.rel, s.prev, s.hours); err != nil {
			t.Fatal(err)
		}
	}
	// Session A: the capture's transaction, past its read of tbs_ksx.
	capture, err := app.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Rollback(ctx)
	if _, err := capture.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, org); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := capture.QueryRow(ctx, existingSnapshot, "wi_ks", 1, "repo_ks", oldHead, base, "main").Scan(&id); err != nil || id != "tbs_ksx" {
		t.Fatalf("the capture read %q (%v)", id, err)
	}
	var n int
	if err := app.InSystem(ctx, "test", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT prune_branch_snapshots(100)`).Scan(&n)
	}); err != nil {
		t.Fatalf("the pruner waited on the capture: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned %d with the capture holding tbs_ksx, want 0", n)
	}
	var left int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM task_branch_snapshots WHERE id = 'tbs_ksx'`).Scan(&left); err != nil || left != 1 {
		t.Errorf("tbs_ksx rows %d (%v), want 1", left, err)
	}
}

// existing_branch_snapshot is a definer, past row-level security: another
// organization's caller neither reads nor locks a snapshot, even with its
// exact tuple.
func TestExistingSnapshotIsOnlyTheCallersOrganizations(t *testing.T) {
	app, owner := dbtest.Open(t)
	orgA, orgB := dbtest.Org(t, owner), dbtest.Org(t, owner)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_eo', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_eo', $1, 'prj_eo', 1, 'T', 'G')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_eo', $1, 'prj_eo', 'api', 'https://example.com/acme/api')`,
	} {
		if _, err := owner.Exec(ctx, q, orgA); err != nil {
			t.Fatal(err)
		}
	}
	base, head := fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 501)
	if _, err := owner.Exec(ctx, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
		target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation)
		VALUES ('tbs_eo', $1, 'wi_eo', 1, 'repo_eo', 'api', 'dude/wi_eo', 'main', $2, $2, $3, '[]', 'c', '{}', 'initial')`, orgA, base, head); err != nil {
		t.Fatal(err)
	}
	read := func(org string) (string, error) {
		var id string
		err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, existingSnapshot, "wi_eo", 1, "repo_eo", head, base, "main").Scan(&id)
		})
		return id, err
	}
	if id, err := read(orgA); err != nil || id != "tbs_eo" {
		t.Fatalf("its own organization read %q (%v), want tbs_eo", id, err)
	}
	if id, err := read(orgB); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("another organization read %q (%v), want no rows", id, err)
	}
}
