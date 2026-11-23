package dbtest

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// breakBuilderPassword gives dude_builder a wrong password, under Builder's
// lock, until the test ends.
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

func selectOne(t *testing.T, b *db.DB) {
	t.Helper()
	var one int
	if err := b.Pool.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Errorf("SELECT 1 as dude_builder: %d, %v", one, err)
	}
}

// A wrong password, as a deploy or another tool may leave it, is repaired.
func TestBuilderRepairsTheRolesPassword(t *testing.T) {
	_, owner := Open(t)
	breakBuilderPassword(t)
	selectOne(t, Builder(t, owner))
}

// Sixteen Builders at once, each on its own database like parallel test
// binaries, all connect with the password wrong when they start: the race
// of #458. A lock on each test's own database would not serialize them.
func TestBuildersAtOnceAllConnect(t *testing.T) {
	const n = 16
	owners := make([]*pgx.Conn, n)
	for i := range owners {
		app, owner := Open(t)
		// Builder needs only owner's database name; closing the connections
		// keeps sixteen lanes inside the cluster's connection limit.
		app.Close()
		owner.Close(context.Background())
		owners[i] = owner
	}
	breakBuilderPassword(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, o := range owners {
		wg.Go(func() {
			<-start
			b := Builder(t, o)
			selectOne(t, b)
			b.Close()
		})
	}
	close(start)
	wg.Wait()
}
