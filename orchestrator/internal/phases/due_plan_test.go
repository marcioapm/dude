package phases

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// due's query runs on a generic plan without JIT, and the connection has
// the server's own settings back once due's transaction ends.
func TestDueRunsOnAGenericPlanWithoutJITForItsTransactionOnly(t *testing.T) {
	ctx := context.Background()
	app, _ := dbtest.Open(t)
	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	conn, err := app.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var server [2]string
	if err := conn.QueryRow(ctx, `SELECT current_setting('plan_cache_mode'), current_setting('jit')`).Scan(&server[0], &server[1]); err != nil {
		t.Fatal(err)
	}
	if server == [2]string{"force_generic_plan", "off"} {
		t.Fatalf("the server already runs %v: nothing here would tell", server)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Twice: under the default plan_cache_mode the first five executions
	// of a prepared statement take custom plans.
	for range 2 {
		if _, err := s.dueIn(ctx, tx, sweepBatch); err != nil {
			t.Fatal(err)
		}
	}
	var generic, custom int
	var jit string
	if err := tx.QueryRow(ctx, `SELECT generic_plans, custom_plans, current_setting('jit') FROM pg_prepared_statements
		WHERE statement = $1`, dueQuery).Scan(&generic, &custom, &jit); err != nil {
		t.Fatal(err)
	}
	if generic != 2 || custom != 0 || jit != "off" {
		t.Errorf("due's query: %d generic plans, %d custom, jit %q; want 2, 0 and off", generic, custom, jit)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var after [2]string
	if err := conn.QueryRow(ctx, `SELECT current_setting('plan_cache_mode'), current_setting('jit')`).Scan(&after[0], &after[1]); err != nil {
		t.Fatal(err)
	}
	if after != server {
		t.Errorf("after due's transaction the connection has %v, want the server's %v", after, server)
	}
}
