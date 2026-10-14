package db_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// projectsSeen is the projects fn's transaction sees, with no tenant filter.
func projectsSeen(ctx context.Context, t *testing.T, tx pgx.Tx) []string {
	t.Helper()
	rows, err := tx.Query(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// sessionState is the connection's tenant and role outside any transaction.
func sessionState(ctx context.Context, t *testing.T, conn *pgxpool.Conn) (org, role string) {
	t.Helper()
	if err := conn.QueryRow(ctx, `SELECT coalesce(current_setting('app.organization_id', true), ''), current_user`).
		Scan(&org, &role); err != nil {
		t.Fatal(err)
	}
	return org, role
}

// One session, as a re-request holds for its lock, runs one tenant's
// transaction after another's, committed, rolled back or panicking: each
// sees its own tenant's rows only, and nothing of either is left on the
// session after it.
func TestTransactionsOnOneSessionDoNotLeakTheirTenant(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	orgs := []string{dbtest.Org(t, owner), dbtest.Org(t, owner)}
	for i, org := range orgs {
		if _, err := owner.Exec(ctx, `INSERT INTO projects (id, organization_id, name, slug, key_prefix)
			VALUES ($1, $2, 'P', 'p', 'P')`, []string{"prj_a", "prj_b"}[i], org); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := app.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	refused := errors.New("refused")
	ends := map[string]func() error{
		"commit":   func() error { return nil },
		"rollback": func() error { return refused },
		"panic":    func() error { panic(refused) },
	}
	for _, end := range []string{"commit", "rollback", "panic", "commit"} {
		for i, org := range orgs {
			want := []string{[]string{"prj_a", "prj_b"}[i]}
			var called bool
			var gotErr error
			var gotPanic any
			func() {
				defer func() { gotPanic = recover() }()
				gotErr = db.InOrgOn(ctx, conn, org, func(tx pgx.Tx) error {
					called = true
					if got := projectsSeen(ctx, t, tx); !slices.Equal(got, want) {
						t.Errorf("%s in %s sees %v, want %v", end, org, got, want)
					}
					return ends[end]()
				})
			}()
			if !called {
				t.Fatalf("%s in %s: the transaction's callback did not run (err %v, panic %v)", end, org, gotErr, gotPanic)
			}
			switch end {
			case "commit":
				if gotErr != nil || gotPanic != nil {
					t.Fatalf("commit in %s: err %v, panic %v; want neither", org, gotErr, gotPanic)
				}
			case "rollback":
				if !errors.Is(gotErr, refused) || gotPanic != nil {
					t.Fatalf("rollback in %s: err %v, panic %v; want the callback's error", org, gotErr, gotPanic)
				}
			case "panic":
				if gotPanic != refused {
					t.Fatalf("panic in %s: recovered %v, want the callback's panic", org, gotPanic)
				}
			}
			if got, role := sessionState(ctx, t, conn); got != "" || role != "dude_app" {
				t.Fatalf("after %s in %s: organization %q, role %q on the session", end, org, got, role)
			}
		}
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM projects`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("outside a transaction the session sees %d projects (%v), want none", n, err)
	}
}

// InSystem's role is its transaction's: a one-connection pool's next
// InOrg, after it commits or rolls back, is dude_app again and sees one
// tenant.
func TestInSystemLeavesNoRoleOnTheConnection(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	orgs := []string{dbtest.Org(t, owner), dbtest.Org(t, owner)}
	for i, org := range orgs {
		if _, err := owner.Exec(ctx, `INSERT INTO projects (id, organization_id, name, slug, key_prefix)
			VALUES ($1, $2, 'P', 'p', 'P')`, []string{"prj_a", "prj_b"}[i], org); err != nil {
			t.Fatal(err)
		}
	}
	cfg := app.Pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	one := &db.DB{Pool: pool}
	refused := errors.New("refused")
	for _, fail := range []bool{false, true} {
		called := false
		err := one.InSystem(ctx, "test", func(tx pgx.Tx) error {
			called = true
			if got := projectsSeen(ctx, t, tx); !slices.Equal(got, []string{"prj_a", "prj_b"}) {
				t.Errorf("the sweeper sees %v, want both tenants'", got)
			}
			if fail {
				return refused
			}
			return nil
		})
		if !called || (fail && !errors.Is(err, refused)) || (!fail && err != nil) {
			t.Fatalf("InSystem (failing %v): callback ran %v, err %v", fail, called, err)
		}
		called = false
		err = one.InOrg(ctx, orgs[0], func(tx pgx.Tx) error {
			called = true
			var role string
			if err := tx.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil {
				return err
			}
			if got := projectsSeen(ctx, t, tx); role != "dude_app" || !slices.Equal(got, []string{"prj_a"}) {
				t.Errorf("after InSystem (failed %v): role %q sees %v", fail, role, got)
			}
			return nil
		})
		if err != nil || !called {
			t.Fatalf("InOrg after InSystem: callback ran %v, err %v", called, err)
		}
	}
}

// InSystem's role and name hold for its transaction only: on the same
// pooled connection, the next transaction, after a commit or a rollback, is
// the app role again, scoped by RLS, under the connection's own name.
func TestInSystemsRoleAndNameEndWithItsTransaction(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	// One connection, so every transaction below reuses the one before's.
	url := app.Pool.Config().ConnString()
	if strings.Contains(url, "?") {
		url += "&pool_max_conns=1"
	} else {
		url += "?pool_max_conns=1"
	}
	one, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(one.Close)
	orgA, orgB := dbtest.Org(t, owner), dbtest.Org(t, owner)
	for _, org := range []string{orgA, orgB} {
		if _, err := owner.Exec(ctx, `INSERT INTO projects (id, organization_id, name, slug, key_prefix)
			VALUES ($1, $2, 'P', $1, 'P')`, "prj_"+org, org); err != nil {
			t.Fatal(err)
		}
	}

	type seen struct {
		user, name string
		orgs       int
	}
	look := func(tx pgx.Tx) (s seen) {
		if err := tx.QueryRow(ctx, `SELECT current_user::text, current_setting('application_name'),
			(SELECT count(*) FROM projects WHERE organization_id IN ($1, $2))`, orgA, orgB).Scan(&s.user, &s.name, &s.orgs); err != nil {
			t.Fatal(err)
		}
		return s
	}
	var before seen
	if err := one.InOrg(ctx, orgA, func(tx pgx.Tx) error { before = look(tx); return nil }); err != nil {
		t.Fatal(err)
	}
	if before.user != "dude_app" || before.name == "dude:sweep-test" || before.orgs != 1 {
		t.Fatalf("before InSystem: %+v, want dude_app seeing its own organization's project only", before)
	}

	for _, end := range []struct {
		how string
		err error
	}{{"commit", nil}, {"rollback", errors.New("the sweep failed")}} {
		var during seen
		err := one.InSystem(ctx, "sweep-test", func(tx pgx.Tx) error { during = look(tx); return end.err })
		if !errors.Is(err, end.err) {
			t.Fatalf("%s: InSystem returned %v", end.how, err)
		}
		if during != (seen{"dude_sweeper", "dude:sweep-test", 2}) {
			t.Errorf("%s: inside InSystem: %+v, want dude_sweeper named dude:sweep-test seeing both organizations' projects", end.how, during)
		}
		var after seen
		if err := one.InOrg(ctx, orgB, func(tx pgx.Tx) error { after = look(tx); return nil }); err != nil {
			t.Fatal(err)
		}
		if after != (seen{"dude_app", before.name, 1}) {
			t.Errorf("after InSystem's %s: %+v, want %+v", end.how, after, seen{"dude_app", before.name, 1})
		}
	}
}
