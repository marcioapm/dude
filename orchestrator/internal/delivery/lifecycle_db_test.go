package delivery

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// duplicates writes the edge a duplicates b as the owner role, past Close's checks.
func (w *relWorld) duplicates(a, b int) {
	w.t.Helper()
	w.exec(`INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $1||'_'||$2::int, $1||'_'||$3::int, 'duplicates', 'per_'||$1)`, a, b)
}

// closeAs ends task n as a person's Close would leave it: aborted, with a resolution.
func (w *relWorld) closeAs(n int, resolution string) {
	w.exec(`UPDATE tasks SET status = 'aborted', resolution = $3::task_resolution WHERE id = $1||'_'||$2::int`, n, resolution)
}

func startRefusal(t *testing.T, in func(func(pgx.Tx) error) error, task string) *string {
	t.Helper()
	var out *string
	if err := in(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT task_start_refusal($1)`, task).Scan(&out)
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func wouldCycle(t *testing.T, w *relWorld, in func(func(pgx.Tx) error) error, src, dst int) []string {
	t.Helper()
	var out []string
	if err := in(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT task_relation_would_cycle($1, $2)`, w.id(src), w.id(dst)).Scan(&out)
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// A blocker closed as a duplicate hands its blocking to the task it
// duplicates: CP-3 waited on CP-2; CP-2 closed as a duplicate of CP-1, CP-3
// waits on CP-1 and is released when CP-1 merges, not by CP-2's state.
func TestTaskBlockedByFollowsADuplicateToItsCanonicalTask(t *testing.T) {
	w, in := newRelWorld(t, 4)
	w.blocks(2, 3)
	if got := blockerKeys(t, in, w.id(3)); !slices.Equal(got, []string{"CP-2:received"}) {
		t.Fatalf("before the close: %v", got)
	}
	w.closeAs(2, "duplicate")
	w.duplicates(2, 1)
	w.status(1, "review")
	if got := blockerKeys(t, in, w.id(3)); !slices.Equal(got, []string{"CP-1:review"}) {
		t.Fatalf("CP-2 closed as a duplicate of CP-1: blockers %v, want CP-1's", got)
	}
	// Two duplicates of one task name it once.
	w.blocks(4, 3)
	w.closeAs(4, "duplicate")
	w.duplicates(4, 1)
	if got := blockerKeys(t, in, w.id(3)); !slices.Equal(got, []string{"CP-1:review"}) {
		t.Fatalf("two duplicates of CP-1: blockers %v", got)
	}
	w.status(1, "done")
	w.pr(1, "merged")
	if got := blockerKeys(t, in, w.id(3)); len(got) != 0 {
		t.Fatalf("CP-1 merged, CP-3 still blocked by %v", got)
	}
	// Reopen removes the edge: CP-2 blocks again, as itself.
	w.exec(`DELETE FROM task_relations WHERE from_task_id = $1||'_2' AND kind = 'duplicates'`)
	if got := blockerKeys(t, in, w.id(3)); !slices.Equal(got, []string{"CP-2:aborted"}) {
		t.Fatalf("the duplicate edge removed: %v", got)
	}
}

// A Won't do blocker keeps blocking: it releases nothing.
func TestTaskBlockedByKeepsAWontDoBlocker(t *testing.T) {
	w, in := newRelWorld(t, 2)
	w.blocks(1, 2)
	w.closeAs(1, "wont_do")
	if got := blockerKeys(t, in, w.id(2)); !slices.Equal(got, []string{"CP-1:aborted"}) {
		t.Fatalf("a Won't do blocker: %v", got)
	}
}

// The loop check walks what a duplicate handed over: CP-3 blocks CP-1 and
// CP-2 duplicates CP-1 while blocking CP-3; so CP-1 effectively blocks
// CP-3, and CP-3 → CP-1 → CP-3 is a loop.
func TestTaskRelationWouldCycleThroughADuplicate(t *testing.T) {
	w, in := newRelWorld(t, 4)
	w.blocks(2, 3)
	w.closeAs(2, "duplicate")
	w.duplicates(2, 1)
	// The edge CP-3 blocks CP-1, written now, would close a loop through the handover.
	if got, want := wouldCycle(t, w, in, 3, 1), []string{"CP-1", "CP-3", "CP-1"}; !slices.Equal(got, want) {
		t.Fatalf("loop %v, want %v", got, want)
	}
	// Edges into the handover's ends that close nothing are no loop.
	if got := wouldCycle(t, w, in, 4, 3); got != nil {
		t.Fatalf("CP-4 blocks CP-3 is no loop: %v", got)
	}
}

// The refusal order: archived, then closed, then blocked; a done task with
// nothing merged is not closed; none, NULL.
func TestTaskStartRefusalOrder(t *testing.T) {
	w, in := newRelWorld(t, 3)
	say := func(n int) string {
		if r := startRefusal(t, in, w.id(n)); r != nil {
			return *r
		}
		return ""
	}
	if got := say(2); got != "" {
		t.Fatalf("a free task: %q", got)
	}
	w.blocks(1, 2)
	if got := say(2); got != "blocked" {
		t.Fatalf("blocked: %q", got)
	}
	w.closeAs(2, "cant_reproduce")
	if got := say(2); got != "closed" {
		t.Fatalf("closed and blocked: %q, want closed first", got)
	}
	w.exec(`UPDATE tasks SET archived_at = now(), archived_by = 'per_'||$1 WHERE id = $1||'_2'`)
	if got := say(2); got != "archived" {
		t.Fatalf("archived, closed and blocked: %q, want archived first", got)
	}
	w.exec(`UPDATE tasks SET status = 'done', resolution = 'nothing_to_merge' WHERE id = $1||'_3'`)
	if got := say(3); got != "" {
		t.Fatalf("done with nothing to merge: %q", got)
	}
}

// The table holds the lifecycle's two invariants: a resolution only on an
// ended status, and an archived task always ended.
func TestTaskLifecycleConstraints(t *testing.T) {
	w, in := newRelWorld(t, 3)
	ctx := context.Background()
	try := func(sql string) string {
		err := in(func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, w.id(1)); return err })
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.ConstraintName
		}
		if err != nil {
			t.Fatal(err)
		}
		return ""
	}
	if got := try(`UPDATE tasks SET resolution = 'wont_do' WHERE id = $1`); got != "tasks_resolution_status" {
		t.Fatalf("a resolution on a received task: %q", got)
	}
	if got := try(`UPDATE tasks SET archived_at = now() WHERE id = $1`); got != "tasks_archived_ended" {
		t.Fatalf("an unended task archived: %q", got)
	}
	if got := try(`UPDATE tasks SET status = 'aborted', resolution = 'wont_do', archived_at = now() WHERE id = $1`); got != "" {
		t.Fatalf("a closed task archived: %q", got)
	}
	// One duplicates edge out of a task.
	w.duplicates(1, 2)
	err := in(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
			VALUES ($1, $2, $3, 'duplicates', 'per_'||$1)`, w.org, w.id(1), w.id(3))
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "task_relations_one_duplicate" {
		t.Fatalf("a second duplicates edge out of one task: %v", err)
	}
}

// 143 names today's "done, nothing merged": a done task with no merged
// pull request gets nothing_to_merge; a merged one and an open one don't.
func TestMigration143BackfillsNothingToMerge(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "143")
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, append([]any{org}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'CP', 'cp', 'CP')`)
	exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_'||$1, $1, 'prj_'||$1, 'target', 'git://x/target.git', 'main')`)
	for n, status := range map[int]string{1: "done", 2: "done", 3: "running"} {
		exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ($1||'_'||$2::int, $1, 'prj_'||$1, $2::int, 't', 'g', $3::task_status)`, n, status)
	}
	exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title, state)
		VALUES ('pr_'||$1, $1, 'prj_'||$1, $1||'_2', 'repo_'||$1, 1, 'u', 'h', 'main', 's', 't', 'merged')`)
	apply()
	got := map[string]string{}
	rows, err := owner.Query(ctx, `SELECT id, COALESCE(resolution::text, '') FROM tasks WHERE organization_id = $1`, org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, r string
		if err := rows.Scan(&id, &r); err != nil {
			t.Fatal(err)
		}
		got[id] = r
	}
	if got[org+"_1"] != "nothing_to_merge" || got[org+"_2"] != "" || got[org+"_3"] != "" {
		t.Fatalf("backfill: %v", got)
	}
}
