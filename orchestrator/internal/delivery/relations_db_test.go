package delivery

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// relWorld is an organisation with one project (key CP) and tasks CP-1…CP-n,
// for the relation tests in this package.
type relWorld struct {
	t     *testing.T
	owner *pgx.Conn
	org   string
}

func newRelWorld(t *testing.T, tasks int) (*relWorld, func(fn func(pgx.Tx) error) error) {
	t.Helper()
	app, owner := dbtest.Open(t)
	w := &relWorld{t: t, owner: owner, org: dbtest.Org(t, owner)}
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'CP', 'cp-'||$1, 'CP')`)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_'||$1, $1, 'prj_'||$1, 'target', 'git://x/target.git', 'main')`)
	w.exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_'||$1, $1, 'Ana')`)
	for i := 1; i <= tasks; i++ {
		w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ($1||'_'||$2::int, $1, 'prj_'||$1, $2::int, 'T'||$2::int, 'g')`, i)
	}
	return w, func(fn func(pgx.Tx) error) error { return app.InOrg(context.Background(), w.org, fn) }
}

func (w *relWorld) id(n int) string { return w.org + "_" + strconv.Itoa(n) }

func (w *relWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, append([]any{w.org}, args...)...); err != nil {
		w.t.Fatal(err)
	}
}

// blocks writes the edge a → b as the owner role, bypassing the writer's checks.
func (w *relWorld) blocks(a, b int) {
	w.t.Helper()
	w.exec(`INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $1||'_'||$2::int, $1||'_'||$3::int, 'blocks', 'per_'||$1)`, a, b)
}

func (w *relWorld) status(n int, status string) {
	w.exec(`UPDATE tasks SET status = $3::task_status WHERE id = $1||'_'||$2::int`, n, status)
}

func (w *relWorld) pr(n int, state string) {
	w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch,
			head_sha, title, state)
		VALUES ('pr_'||$1||'_'||$2::int||$3, $1, 'prj_'||$1, $1||'_'||$2::int, 'repo_'||$1, $2::int, 'u', 'h', 'main', 's', 't',
			$3::pull_request_state)`, n, state)
}

func blockerKeys(t *testing.T, in func(func(pgx.Tx) error) error, task string) []string {
	t.Helper()
	var keys []string
	if err := in(func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT key || ':' || status FROM task_blocked_by($1)`, task)
		if err != nil {
			return err
		}
		keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return keys
}

// task_blocked_by is the one rule: a blocker is released only when it is
// done and has a merged pull request. In review, failed, and done with
// nothing merged all still block; a merged one does not.
func TestTaskBlockedByReleasesOnlyMergedWork(t *testing.T) {
	w, in := newRelWorld(t, 5)
	w.blocks(1, 5)
	w.blocks(2, 5)
	w.blocks(3, 5)
	w.blocks(4, 5)
	w.status(1, "review")
	w.pr(1, "open")
	w.status(2, "failed")
	w.status(3, "done") // marked done, nothing merged
	w.status(4, "done")
	w.pr(4, "merged")
	got := blockerKeys(t, in, w.id(5))
	if want := []string{"CP-1:review", "CP-2:failed", "CP-3:done"}; !slices.Equal(got, want) {
		t.Fatalf("open blockers %v, want %v", got, want)
	}
	// A closed pull request is not a merge either.
	w.pr(3, "closed")
	if got := blockerKeys(t, in, w.id(5)); !slices.Contains(got, "CP-3:done") {
		t.Fatalf("done with a closed pull request released its dependant: %v", got)
	}
	// Related edges gate nothing.
	w.exec(`INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $1||'_1', $1||'_5', 'related', 'per_'||$1)`)
	if got := blockerKeys(t, in, w.id(1)); len(got) != 0 {
		t.Fatalf("CP-1 is blocked by %v", got)
	}
}

// The cycle check names the loop from the new edge's target back round to
// it, in key order along the edges; with no loop, NULL.
func TestTaskRelationWouldCycleNamesTheLoop(t *testing.T) {
	w, in := newRelWorld(t, 4)
	w.blocks(1, 2) // CP-1 blocks CP-2
	w.blocks(2, 3) // CP-2 blocks CP-3
	loop := func(src, dst int) []string {
		var out []string
		if err := in(func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT task_relation_would_cycle($1, $2)`, w.id(src), w.id(dst)).Scan(&out)
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// CP-3 blocks CP-1 would close CP-1 → CP-2 → CP-3 → CP-1.
	if got, want := loop(3, 1), []string{"CP-1", "CP-2", "CP-3", "CP-1"}; !slices.Equal(got, want) {
		t.Fatalf("loop %v, want %v", got, want)
	}
	if got := loop(1, 3); got != nil {
		t.Fatalf("CP-1 blocks CP-3 is no loop, got %v", got)
	}
	if got := loop(4, 1); got != nil {
		t.Fatalf("CP-4 blocks CP-1 is no loop, got %v", got)
	}
}

// The table refuses a self-relation and a related edge stored the wrong way
// round; another organisation's edges are not visible.
func TestTaskRelationsConstraintsAndIsolation(t *testing.T) {
	w, in := newRelWorld(t, 2)
	err := in(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
			VALUES ($1, $2, $2, 'blocks', 'per_'||$1)`, w.org, w.id(1))
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "task_relations_self" {
		t.Fatalf("a self-relation: %v", err)
	}
	err = in(func(tx pgx.Tx) error {
		lo, hi := w.id(1), w.id(2)
		if lo < hi {
			lo, hi = hi, lo
		}
		_, err := tx.Exec(context.Background(), `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
			VALUES ($1, $2, $3, 'related', 'per_'||$1)`, w.org, lo, hi)
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "task_relations_related_order" {
		t.Fatalf("related stored higher id first: %v", err)
	}
	w.blocks(1, 2)
	other := dbtest.Org(t, w.owner)
	var seen int
	if err := in(func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `SELECT set_config('app.organization_id', $1, true)`, other); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM task_relations`).Scan(&seen)
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Fatalf("another organisation sees %d relations", seen)
	}
}
