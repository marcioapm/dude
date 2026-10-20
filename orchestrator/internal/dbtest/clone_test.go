package dbtest

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	code := m.Run()
	dropTemplate()
	os.Exit(code)
}

func databases(t *testing.T, conn *pgx.Conn, name string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A clone whose configuration fails after CREATE DATABASE succeeded is
// dropped, not left on the server with no one knowing its name.
func TestACloneThatFailsToBeConfiguredIsDropped(t *testing.T) {
	once.Do(setup)
	if setupErr != nil && unreachable {
		t.Skipf("no test database: %v", setupErr)
	}
	if setupErr != nil {
		t.Fatalf("setting up the test database: %v", setupErr)
	}
	refused := errors.New("configuration refused")
	var configured string
	existed := 0
	real := configure
	configure = func(_ context.Context, admin *pgx.Conn, name string) error {
		configured = name
		existed = databases(t, admin, name)
		return refused
	}
	t.Cleanup(func() { configure = real })

	c := clone()
	if !errors.Is(c.err, refused) {
		t.Fatalf("clone error = %v, want %v", c.err, refused)
	}
	if configured == "" || existed != 1 {
		t.Fatalf("configure saw %q existing %d times, want the new clone once", configured, existed)
	}
	t.Cleanup(func() { drop(configured) })
	admin, err := adminConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if n := databases(t, admin, configured); n != 0 {
		t.Fatalf("%s is still on the server", configured)
	}
}

// A test's connections, the owner's and the app pool's, reuse one generic
// plan per prepared statement from its first execution: none is
// custom-planned.
func TestACloneReusesGenericPlans(t *testing.T) {
	app, owner := Open(t)
	ctx := context.Background()
	pooled, err := app.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer pooled.Release()
	check := func(name string, conn *pgx.Conn) {
		// A statement without parameters is always planned generically:
		// the $1 is what tells force_generic_plan from auto.
		if _, err := conn.Prepare(ctx, "by_id", `SELECT count(*) FROM pg_class WHERE oid = $1`); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for range 3 {
			if _, err := conn.Exec(ctx, "by_id", 1259); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		var generic, custom int64
		if err := conn.QueryRow(ctx, `SELECT generic_plans, custom_plans FROM pg_prepared_statements WHERE name = 'by_id'`).
			Scan(&generic, &custom); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if generic != 3 || custom != 0 {
			t.Errorf("%s connection: generic plans %d, custom plans %d over 3 executions; want 3 and 0", name, generic, custom)
		}
	}
	check("owner", owner)
	check("app pool", pooled.Conn())
}
