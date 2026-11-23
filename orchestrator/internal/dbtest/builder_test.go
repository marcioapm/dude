package dbtest

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// breakBuilderPassword gives dude_builder a password Builder does not use,
// under Builder's lock, and the right one back when the test ends.
func breakBuilderPassword(t *testing.T) {
	t.Helper()
	set := func(password string) error {
		ctx := context.Background()
		return withBuilderRoleLock(ctx, func(admin *pgx.Conn) error {
			return alterBuilderRole(ctx, admin, password)
		})
	}
	if err := set("other"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := set("dude_builder"); err != nil {
			t.Error(err)
		}
	})
}

func selectOne(t *testing.T, b interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) {
	t.Helper()
	var one int
	if err := b.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Errorf("SELECT 1 as dude_builder: %d, %v", one, err)
	}
}

// A wrong password, as a deploy or another tool may leave it, is repaired.
func TestBuilderRepairsTheRolesPassword(t *testing.T) {
	_, owner := Open(t)
	breakBuilderPassword(t)
	selectOne(t, Builder(t, owner).Pool)
}

// Sixteen Builders at once, each on a database of its own as test binaries
// running side by side are, all connect, with the role's password wrong
// when they start: the race of #458, where each ALTER ROLE'd it and some
// failed with "tuple concurrently updated". A lock taken on a test's own
// database would not keep them apart: advisory locks are per database.
func TestBuildersAtOnceAllConnect(t *testing.T) {
	const n = 16
	owners := make([]*pgx.Conn, n)
	for i := range owners {
		app, owner := Open(t)
		// Builder needs owner's database, not its connection: holding none
		// keeps sixteen of these, and their waiters on Builder's lock, well
		// inside the cluster's connection limit beside other test binaries.
		app.Close()
		owner.Close(context.Background())
		owners[i] = owner
	}
	breakBuilderPassword(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, o := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			b := Builder(t, o)
			selectOne(t, b.Pool)
			b.Close()
		}()
	}
	close(start)
	wg.Wait()
}
