// Package dbtest gives each test its own database, migrated like production.
//
// Its own, not a shared one: dude's background loops are cross-tenant by
// design, so a test's loops would claim another test's rows — the reason
// shared-database tests once failed at random. Each database is cloned from
// a template migrated once per test binary, which takes milliseconds.
package dbtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

var (
	once     sync.Once
	template string
	setupErr error
	// No database to reach at all: tests skip. Any other setup failure
	// fails them, or a broken migration would pass as "ok".
	unreachable bool
	counter     atomic.Int64
)

func host() string {
	if h := os.Getenv("DUDE_TEST_PG"); h != "" {
		return h
	}
	return "localhost:5433"
}

func adminConn(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, fmt.Sprintf("postgres://dude:dude@%s/postgres", host()))
}

// Open returns an app-role connection to a new, migrated database, and an
// owner-role connection for seeding and assertions. Both are dropped when
// the test ends. Skips the test when Postgres is not reachable.
func Open(t *testing.T) (app *db.DB, owner *pgx.Conn) {
	t.Helper()
	once.Do(setup)
	if setupErr != nil && unreachable {
		t.Skipf("no test database: %v", setupErr)
	}
	if setupErr != nil {
		t.Fatalf("setting up the test database: %v", setupErr)
	}
	ctx := context.Background()
	name := fmt.Sprintf("%s_%d", template, counter.Add(1))
	admin, err := adminConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s OWNER dude", name, template)); err != nil {
		t.Fatal(err)
	}

	app, err = db.Open(ctx, fmt.Sprintf("postgres://dude_app:dude_app@%s/%s", host(), name))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = pgx.Connect(ctx, fmt.Sprintf("postgres://dude:dude@%s/%s", host(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		app.Close()
		owner.Close(context.Background())
		if a, err := adminConn(context.Background()); err == nil {
			_, _ = a.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			a.Close(context.Background())
		}
	})
	return app, owner
}

func setup() {
	ctx := context.Background()
	admin, err := adminConn(ctx)
	if err != nil {
		setupErr, unreachable = err, true
		return
	}
	defer admin.Close(ctx)
	// Test binaries run in parallel, one per package, and migrations create
	// cluster-wide roles: two at once fail with "tuple concurrently
	// updated". One migrates at a time.
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock(hashtext('dude_gotest_migrate'))"); err != nil {
		setupErr = err
		return
	}
	defer func() { _, _ = admin.Exec(ctx, "SELECT pg_advisory_unlock(hashtext('dude_gotest_migrate'))") }()
	template = fmt.Sprintf("dude_gotest_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+template+" OWNER dude"); err != nil {
		setupErr = err
		return
	}
	// Migrated by the same runner production uses, so the schema under test
	// is the schema that ships.
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "../../..")
	cmd := exec.Command("bun", "run", "apps/control-plane/src/db/migrate.ts")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), fmt.Sprintf("DATABASE_URL=postgres://dude:dude@%s/%s", host(), template))
	if out, err := cmd.CombinedOutput(); err != nil {
		setupErr = fmt.Errorf("migrate: %v\n%s", err, out)
		return
	}
	// A template must have no connections when it is copied.
	_, _ = admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", template)
}

// Upgrade gives a migration test a database of its own, migrated by the
// files before version (e.g. "071") and nothing else, as a deploy meets
// one; apply runs the rest. owner is its owner-role connection.
func Upgrade(t *testing.T, version string) (owner *pgx.Conn, apply func()) {
	t.Helper()
	once.Do(setup)
	if setupErr != nil && unreachable {
		t.Skipf("no test database: %v", setupErr)
	}
	ctx := context.Background()
	name := fmt.Sprintf("dude_gotest_upgrade_%d_%d", time.Now().UnixNano(), counter.Add(1))
	admin, err := adminConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" OWNER dude"); err != nil {
		t.Fatal(err)
	}
	owner, err = pgx.Connect(ctx, fmt.Sprintf("postgres://dude:dude@%s/%s", host(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		owner.Close(context.Background())
		if a, err := adminConn(context.Background()); err == nil {
			_, _ = a.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			a.Close(context.Background())
		}
	})
	_, file, _, _ := runtime.Caller(0)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(file), "../../../migrations/*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(before bool) {
		for _, f := range files {
			if (filepath.Base(f) < version) != before {
				continue
			}
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("%s: %v", filepath.Base(f), err)
			}
		}
	}
	// As the runner has it before the first file (migrate.ts).
	if _, err := owner.Exec(ctx, `CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
		checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	// Migrations create cluster-wide roles: one migrates at a time.
	lock := func() func() {
		if _, err := owner.Exec(ctx, "SELECT pg_advisory_lock(hashtext('dude_gotest_migrate'))"); err != nil {
			t.Fatal(err)
		}
		return func() { _, _ = owner.Exec(ctx, "SELECT pg_advisory_unlock(hashtext('dude_gotest_migrate'))") }
	}
	unlock := lock()
	run(true)
	unlock()
	return owner, func() {
		defer lock()()
		run(false)
	}
}

// Builder connects to owner's database as dude-image-builder's role
// (migration 068), which aiverse gives a password in production.
func Builder(t *testing.T, owner *pgx.Conn) *db.DB {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `ALTER ROLE dude_builder PASSWORD 'dude_builder'`); err != nil {
		t.Fatal(err)
	}
	b, err := db.Open(ctx, fmt.Sprintf("postgres://dude_builder:dude_builder@%s/%s", host(), owner.Config().Database))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

// Org inserts an organization and returns its id.
func Org(t *testing.T, owner *pgx.Conn) string {
	t.Helper()
	id := fmt.Sprintf("org_t%d", time.Now().UnixNano())
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, id); err != nil {
		t.Fatal(err)
	}
	return id
}
