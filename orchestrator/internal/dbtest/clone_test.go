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
	if template != "" {
		drop(template)
	}
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
