package delivery

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
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

// A plan shaped like a ladder, each step waiting on every task of the step
// before, has exponentially many paths but few tasks: the check visits each
// task once, so it answers in milliseconds, not minutes, under the
// organisation's relation lock. Asked for an edge between the two tops, it
// walks the whole ladder and finds no loop; asked from the bottom to the
// top, it names one shortest loop.
func TestTaskRelationWouldCycleIsLinearOnADiamondLadder(t *testing.T) {
	for _, width := range []int{2, 3} {
		layers := 46 / width
		w, in := newRelWorld(t, width*layers)
		task := func(layer, i int) int { return layer*width + i + 1 }
		for l := 0; l+1 < layers; l++ {
			for a := range width {
				for b := range width {
					w.blocks(task(l, a), task(l+1, b))
				}
			}
		}
		ask := func(src, dst int) ([]string, time.Duration) {
			var out []string
			start := time.Now()
			if err := in(func(tx pgx.Tx) error {
				// The path-enumerating walk takes minutes here; fail in seconds instead.
				if _, err := tx.Exec(context.Background(), `SET LOCAL statement_timeout = '5s'`); err != nil {
					return err
				}
				return tx.QueryRow(context.Background(), `SELECT task_relation_would_cycle($1, $2)`, w.id(src), w.id(dst)).Scan(&out)
			}); err != nil {
				t.Fatalf("%d-wide ladder: %v", width, err)
			}
			return out, time.Since(start)
		}
		got, took := ask(task(0, 1), task(0, 0))
		if got != nil {
			t.Fatalf("%d-wide ladder: an edge between the two tops is no loop, got %v", width, got)
		}
		if took > 2*time.Second {
			t.Fatalf("%d-wide ladder of %d tasks: the check took %v", width, width*layers, took)
		}
		bottom := task(layers-1, 0)
		got, _ = ask(bottom, task(0, 0))
		if len(got) != layers+1 || got[0] != "CP-1" || got[len(got)-1] != "CP-1" || got[len(got)-2] != "CP-"+strconv.Itoa(bottom) {
			t.Fatalf("%d-wide ladder: loop %v, want CP-1 down %d layers to CP-%d and back", width, got, layers, bottom)
		}
	}
}

type requestFailingStarter struct{ cause error }

func (s requestFailingStarter) StartInTx(context.Context, pgx.Tx, workflow.StartOptions) (string, bool, error) {
	return "", false, s.cause
}

func TestStaleAutoStartSettlementLeavesReplacementUntouched(t *testing.T) {
	for _, cause := range []error{errors.New("unknown start failure"), PolicyError{errors.New("invalid policy")}, &pgconn.PgError{Code: "40001"}} {
		t.Run(cause.Error(), func(t *testing.T) {
			w, in := newRelWorld(t, 2)
			ctx := context.Background()
			w.exec(`INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person, due_at)
				VALUES ($1||'_2', $1, 'deliver', 'per_'||$1, now())`)
			a := &AutoStarts{Starter: requestFailingStarter{cause}}
			var generation string
			if err := in(func(tx pgx.Tx) error { return a.fire(ctx, tx, w.org, w.id(2), &generation) }); !errors.Is(err, cause) {
				t.Fatalf("fire error: %v", err)
			}
			if generation == "" {
				t.Fatal("fire did not claim the request")
			}
			// The fire transaction has rolled back. A new blocker permits the owner's reset.
			w.blocks(1, 2)
			w.exec(`UPDATE task_auto_starts SET how = 'talk', generation = gen_random_uuid(),
				set_at = now(), due_at = NULL, fire_attempts = 0 WHERE task_id = $1||'_2'`)
			w.pr(1, "merged")
			if err := in(func(tx pgx.Tx) error {
				_, err := SetTaskStatusTx(ctx, tx, w.org, "prj_"+w.org, w.id(1), "received", "done", "merged")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			type request struct {
				how, generation string
				setAt, dueAt    time.Time
				attempts        int
			}
			read := func() request {
				var r request
				if err := w.owner.QueryRow(ctx, `SELECT how, generation::text, set_at, due_at, fire_attempts
					FROM task_auto_starts WHERE task_id = $1`, w.id(2)).Scan(&r.how, &r.generation, &r.setAt, &r.dueAt, &r.attempts); err != nil {
					t.Fatal(err)
				}
				return r
			}
			before := read()
			if before.generation == generation || before.how != "talk" || before.attempts != 0 {
				t.Fatalf("replacement: %+v", before)
			}
			if err := in(func(tx pgx.Tx) error { return a.notFired(ctx, tx, w.org, w.id(2), generation, cause) }); err != nil {
				t.Fatal(err)
			}
			if after := read(); after != before {
				t.Fatalf("stale settlement changed replacement: before %+v after %+v", before, after)
			}
			if err := in(func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `DELETE FROM task_auto_starts WHERE task_id = $1`, w.id(2))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := in(func(tx pgx.Tx) error { return a.notFired(ctx, tx, w.org, w.id(2), generation, cause) }); err != nil {
				t.Fatalf("deleted request settlement: %v", err)
			}
			var notices int
			if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE task_id = $1
				AND (event_type = 'task.auto_start_cancelled' OR payload->>'about' = 'auto_start_failed')`, w.id(2)).Scan(&notices); err != nil || notices != 0 {
				t.Fatalf("failure events: %d, %v", notices, err)
			}
		})
	}
}

func TestAutoStartFireBeforeClaimDoesNotSettle(t *testing.T) {
	w, in := newRelWorld(t, 1)
	ctx := context.Background()
	w.exec(`INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person, due_at)
		VALUES ($1||'_1', $1, 'deliver', 'per_'||$1, now())`)
	a := &AutoStarts{}
	generation := ""
	if err := in(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 / 0`); err == nil {
			t.Fatal("expected transaction error")
		}
		return a.fire(ctx, tx, w.org, w.id(1), &generation)
	}); err == nil || generation != "" {
		t.Fatalf("before-claim error %v, generation %q", err, generation)
	}
	if err := in(func(tx pgx.Tx) error {
		return a.notFired(ctx, tx, w.org, w.id(1), generation, PolicyError{errors.New("failed")})
	}); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := w.owner.QueryRow(ctx, `SELECT fire_attempts FROM task_auto_starts WHERE task_id = $1`, w.id(1)).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("unclaimed request: attempts %d, %v", attempts, err)
	}
}

func TestAutoStartFireDoesNotClaimFutureRequest(t *testing.T) {
	w, in := newRelWorld(t, 1)
	w.exec(`INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person, due_at)
		VALUES ($1||'_1', $1, 'deliver', 'per_'||$1, now() + interval '1 hour')`)
	var generation string
	a := &AutoStarts{Starter: requestFailingStarter{errors.New("must not start")}}
	if err := in(func(tx pgx.Tx) error { return a.fire(context.Background(), tx, w.org, w.id(1), &generation) }); err != nil || generation != "" {
		t.Fatalf("future fire: %v, claimed %q", err, generation)
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
