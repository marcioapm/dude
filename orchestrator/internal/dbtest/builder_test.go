package dbtest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// breakBuilderPassword gives dude_builder a password Builder does not use,
// under Builder's lock, and the right one back when the test ends.
func breakBuilderPassword(t *testing.T, owner *pgx.Conn) {
	t.Helper()
	set := func(password string) error {
		ctx := context.Background()
		if _, err := owner.Exec(ctx, "SELECT pg_advisory_lock(hashtext('dude_gotest_builder_role'))"); err != nil {
			return err
		}
		defer func() { _, _ = owner.Exec(ctx, "SELECT pg_advisory_unlock(hashtext('dude_gotest_builder_role'))") }()
		_, err := owner.Exec(ctx, "ALTER ROLE dude_builder PASSWORD '"+password+"'")
		return err
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
	breakBuilderPassword(t, owner)
	selectOne(t, Builder(t, owner).Pool)
}

// Sixteen Builders at once, on owner connections of their own, all connect,
// with the role's password wrong when they start: the race of #458, where
// each ALTER ROLE'd it and some failed with "tuple concurrently updated".
func TestBuildersAtOnceAllConnect(t *testing.T) {
	_, owner := Open(t)
	breakBuilderPassword(t, owner)
	ctx := context.Background()
	const n = 16
	owners := make([]*pgx.Conn, n)
	for i := range owners {
		c, err := pgx.Connect(ctx, fmt.Sprintf("postgres://dude:dude@%s/%s", host(), owner.Config().Database))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close(context.Background()) })
		owners[i] = c
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, o := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			selectOne(t, Builder(t, o).Pool)
		}()
	}
	close(start)
	wg.Wait()
}
