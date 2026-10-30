package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A task attempt's branch snapshot is unique per head and merge base, is
// its organization's alone, and a move of the task branch — an agent's
// publish, a conductor's, a pull request opened or moved — asks for one.
func TestBranchSnapshotsAreUniqueScopedAndAskedForWhenTheBranchMoves(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	other := dbtest.Org(t, owner)
	ctx := context.Background()
	sha := func(c string) string { return strings.Repeat(c, 40) }
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_s', $1, 'P', 'p', 'P')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_s', $1, 'prj_s', 1, 'T', 'G')`, org)
	mustExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		('repo_api', $1, 'prj_s', 'api', 'https://github.com/acme/api'), ('repo_web', $1, 'prj_s', 'web', 'https://github.com/acme/web')`, org)
	mustExec(t, owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES
		('wi_s', 'repo_api', $1, 'write'), ('wi_s', 'repo_web', $1, 'write')`, org)

	insert := `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
		target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id)
		VALUES ($1, $2, 'wi_s', 1, 'repo_api', 'api', 'dude/wi_s/attempt-1', 'main', $3, $4, $5, '[]', 'c', '{}', $6, $7)`
	mustExec(t, owner, insert, "tbs_1", org, sha("a"), sha("b"), sha("c"), "initial", nil)
	if _, err := owner.Exec(ctx, insert, "tbs_2", org, sha("d"), sha("b"), sha("c"), "fast-forward", "tbs_1"); err == nil ||
		!strings.Contains(err.Error(), "task_branch_snapshots_task_id_attempt_repository_id_head_sh_key") {
		t.Errorf("the same head and merge base twice: %v", err)
	}
	if _, err := owner.Exec(ctx, insert, "tbs_3", org, sha("a"), sha("b"), sha("e"), "initial", "tbs_1"); err == nil {
		t.Error("an initial snapshot with one before it was taken")
	}
	if _, err := owner.Exec(ctx, insert, "tbs_4", org, sha("a"), sha("b"), "HEAD", "fast-forward", "tbs_1"); err == nil {
		t.Error("a head that is not a SHA was taken")
	}
	mustExec(t, owner, insert, "tbs_5", org, sha("a"), sha("b"), sha("f"), "fast-forward", "tbs_1")

	// Row-level security: another organization reads none, and writes none in this one's name.
	count := func(in string, q string) int {
		var n int
		if err := app.InOrg(ctx, in, func(tx pgx.Tx) error { return tx.QueryRow(ctx, q).Scan(&n) }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(org, `SELECT count(*) FROM task_branch_snapshots`); n != 2 {
		t.Errorf("its organization reads %d snapshots, want 2", n)
	}
	if n := count(other, `SELECT count(*) FROM task_branch_snapshots`); n != 0 {
		t.Errorf("another organization reads %d snapshots", n)
	}
	err := app.InOrg(ctx, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insert, "tbs_x", org, sha("a"), sha("b"), sha("9"), "initial", nil)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("another organization writing one: %v", err)
	}
	// Recorded once: the app never changes one.
	err = app.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE task_branch_snapshots SET checksum = 'x'`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the app changing a snapshot: %v", err)
	}

	requests := func() map[string]string {
		t.Helper()
		rows, err := owner.Query(ctx, `SELECT repository_id || '@' || attempt, COALESCE(head_sha, '') || ':' || status
			FROM task_branch_capture_requests WHERE task_id = 'wi_s'`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			out[k] = v
		}
		return out
	}
	// An agent Run's publish: each named repository of the task, its attempt.
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status)
		VALUES ('run_s', $1, 'prj_s', 'wi_s', 2, 'agent', 'running')`, org)
	if got := requests(); len(got) != 0 {
		t.Fatalf("asked before anything moved: %v", got)
	}
	mustExec(t, owner, `UPDATE runs SET heads = jsonb_build_object('api', jsonb_build_object('sha', $1::text), 'other', jsonb_build_object('sha', 'x'))
		WHERE id = 'run_s'`, sha("1"))
	if got := requests(); len(got) != 1 || got["repo_api@2"] != sha("1")+":pending" {
		t.Errorf("after a publish: %v", got)
	}
	// Settled, then the pull request moves (someone else's push): pending again with the new head.
	mustExec(t, owner, `UPDATE task_branch_capture_requests SET status = 'done', settled_at = now()`)
	mustExec(t, owner, `UPDATE runs SET heads = heads WHERE id = 'run_s'`)
	if got := requests(); got["repo_api@2"] != sha("1")+":done" {
		t.Errorf("unchanged heads rearmed capture: %v", got)
	}
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status)
		VALUES ('run_nonagent', $1, 'prj_s', 'wi_s', 2, 'preview', 'running')`, org)
	mustExec(t, owner, `UPDATE runs SET heads = '{"web":{"sha":"ignored"}}' WHERE id = 'run_nonagent'`)
	if got := requests(); len(got) != 1 {
		t.Errorf("non-agent heads asked for capture: %v", got)
	}
	mustExec(t, owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
		VALUES ('pr_s', $1, 'prj_s', 'wi_s', 'repo_web', 7, 'https://github.com/acme/web/pull/7', 'dude/wi_s/attempt-2', 'main', $2, 'T')`, org, sha("2"))
	if got := requests(); got["repo_web@2"] != sha("2")+":pending" || got["repo_api@2"] != sha("1")+":done" {
		t.Errorf("after a pull request opened: %v", got)
	}
	mustExec(t, owner, `UPDATE pull_requests SET head_sha = $1 WHERE id = 'pr_s'`, sha("3"))
	if got := requests(); got["repo_web@2"] != sha("3")+":pending" {
		t.Errorf("after its head moved: %v", got)
	}
	// Closed pull requests never ask, even when their head changes.
	mustExec(t, owner, `UPDATE task_branch_capture_requests SET status = 'done', settled_at = now()`)
	mustExec(t, owner, `UPDATE pull_requests SET state = 'closed', head_sha = $1 WHERE id = 'pr_s'`, sha("7"))
	if got := requests(); got["repo_web@2"] != sha("3")+":done" {
		t.Errorf("closed pull request rearmed capture: %v", got)
	}
	// A conductor's publish settling.
	mustExec(t, owner, `UPDATE task_branch_capture_requests SET status = 'done', settled_at = now()`)
	mustExec(t, owner, `INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, attempt, status)
		VALUES ('cpb_s', $1, 'wi_s', 'run_s', 'req_s', 2, 'moving')`, org)
	mustExec(t, owner, `UPDATE conductor_publishes SET status = 'published', heads = jsonb_build_object('api', jsonb_build_object('sha', $1::text))`, sha("4"))
	if got := requests(); got["repo_api@2"] != sha("4")+":pending" || got["repo_web@2"] != sha("3")+":done" {
		t.Errorf("after a conductor's publish: %v", got)
	}
	// A delivery from before attempts were kept publishes with attempt 0:
	// the publish settles, and asks for the task's current attempt.
	mustExec(t, owner, `UPDATE task_branch_capture_requests SET status = 'done', settled_at = now()`)
	mustExec(t, owner, `INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, attempt, status)
		VALUES ('cpb_0', $1, 'wi_s', 'run_s', 'req_0', 0, 'moving')`, org)
	mustExec(t, owner, `UPDATE conductor_publishes SET status = 'published', heads = jsonb_build_object('web', jsonb_build_object('sha', $1::text))
		WHERE id = 'cpb_0'`, sha("5"))
	if got := requests(); len(got) != 2 || got["repo_web@2"] != sha("5")+":pending" {
		t.Errorf("after an attempt-0 publish: %v", got)
	}
	// An ask that cannot be made is skipped, never raised: no attempt, or
	// a task or repository not of the organization named.
	for _, args := range [][]any{{org, "wi_s", 0, "repo_api"}, {other, "wi_s", 2, "repo_api"}, {org, "wi_s", 3, "repo_nope"}} {
		mustExec(t, owner, `SELECT want_branch_capture($1, $2, $3, $4, '')`, args...)
	}
	if got := requests(); len(got) != 2 {
		t.Errorf("asks that cannot be made made requests: %v", got)
	}

	// A code item names a Run's diff or a snapshot, never both.
	mustExec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_s', $1, 'Sam')`, org)
	if _, err := owner.Exec(ctx, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, run_id, branch_snapshot_id, anchor)
		VALUES ('fbi_s', $1, 'wi_s', 1, 'per_s', 'b', 'code', 'run_s', 'tbs_1', '{}')`, org); err == nil ||
		!strings.Contains(err.Error(), "feedback_items_one_diff") {
		t.Errorf("an item on both a Run and a snapshot: %v", err)
	}
	mustExec(t, owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
		VALUES ('fbi_s', $1, 'wi_s', 1, 'per_s', 'b', 'code', 'tbs_1', '{}')`, org)
}

// A publish recording its pull requests' heads (in Go's sort order of the
// repository names, phases/publish.go) and a Run recording the same heads
// at once ask for the same capture requests: both take them in that one
// order, so neither waits on the other in a cycle. jsonb lists "web"
// before "api-gateway" (shorter keys first), the opposite of that order.
func TestTwoWritersAskingForTheSameCapturesNeverDeadlock(t *testing.T) {
	_, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	sha := func(c string) string { return strings.Repeat(c, 40) }
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_d', $1, 'P', 'p', 'P')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_d', $1, 'prj_d', 1, 'T', 'G')`, org)
	mustExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		('repo_gw', $1, 'prj_d', 'api-gateway', 'https://github.com/acme/api-gateway'), ('repo_web', $1, 'prj_d', 'web', 'https://github.com/acme/web')`, org)
	mustExec(t, owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES
		('wi_d', 'repo_gw', $1, 'write'), ('wi_d', 'repo_web', $1, 'write')`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status)
		VALUES ('run_d', $1, 'prj_d', 'wi_d', 1, 'agent', 'running')`, org)
	mustExec(t, owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
		VALUES ('pr_gw', $1, 'prj_d', 'wi_d', 'repo_gw', 1, 'https://github.com/acme/api-gateway/pull/1', 'dude/wi_d/attempt-1', 'main', $2, 'T'),
		       ('pr_web', $1, 'prj_d', 'wi_d', 'repo_web', 2, 'https://github.com/acme/web/pull/2', 'dude/wi_d/attempt-1', 'main', $2, 'T')`, org, sha("1"))

	connect := func() *pgx.Conn {
		c, err := pgx.ConnectConfig(ctx, owner.Config().Copy())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close(context.Background()) })
		mustExec(t, c, `SET lock_timeout = '5s'`)
		return c
	}
	publish, run := connect(), connect()
	var runPID uint32 = run.PgConn().PID()

	// The publish: api-gateway's pull request first.
	tx, err := publish.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = $1 WHERE id = 'pr_gw'`, sha("2")); err != nil {
		t.Fatal(err)
	}
	// The Run records both heads meanwhile, and waits on the publish.
	ran := make(chan error, 1)
	go func() {
		_, err := run.Exec(ctx, `UPDATE runs SET heads = jsonb_build_object('web', jsonb_build_object('sha', $1::text),
			'api-gateway', jsonb_build_object('sha', $1::text)) WHERE id = 'run_d'`, sha("3"))
		ran <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for waiting := false; !waiting; {
		if time.Now().After(deadline) {
			t.Fatal("the Run never waited on the publish")
		}
		select {
		case err := <-ran:
			t.Fatalf("the Run did not wait on the publish: %v", err)
		default:
		}
		if err := owner.QueryRow(ctx, `SELECT coalesce(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1`, runPID).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	// The publish goes on to web's pull request, and commits.
	if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = $1 WHERE id = 'pr_web'`, sha("2")); err != nil {
		t.Fatalf("the publish's second pull request: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-ran; err != nil {
		t.Fatalf("the Run's heads: %v", err)
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM task_branch_capture_requests WHERE task_id = 'wi_d' AND status = 'pending'
		AND head_sha = $1`, sha("3")).Scan(&n); err != nil || n != 2 {
		t.Errorf("%d requests at the Run's heads (%v), want 2", n, err)
	}
}
