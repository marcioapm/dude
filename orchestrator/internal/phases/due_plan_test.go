package phases

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestDueDisablesJITForItsTransactionOnly(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	// A volatile predicate observes JIT while dueIn actually reads its Run.
	if _, err := owner.Exec(ctx, `
		CREATE SCHEMA due_jit;
		CREATE FUNCTION due_jit.check_jit() RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$
		BEGIN
			IF current_setting('jit') <> 'off' THEN
				RAISE EXCEPTION 'due query ran with JIT enabled';
			END IF;
			RETURN true;
		END $$;
		CREATE VIEW due_jit.runs AS SELECT * FROM public.runs WHERE due_jit.check_jit();
		GRANT USAGE ON SCHEMA due_jit TO dude_sweeper;
		GRANT SELECT ON due_jit.runs TO dude_sweeper;
		INSERT INTO organizations (id, name, slug) VALUES ('org_jit', 'jit', 'jit');
		INSERT INTO sessions (id, organization_id, titled_by) VALUES ('ses_jit', 'org_jit', NULL);
		INSERT INTO runs (id, organization_id, session_id, attempt, status, control, role, kind)
		VALUES ('run_jit', 'org_jit', 'ses_jit', 1, 'pending', 'none', 'brainstorm', 'agent');
	`); err != nil {
		t.Fatal(err)
	}
	conn, err := app.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var baseline string
	if err := conn.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	if baseline != "on" {
		t.Fatalf("JIT baseline is %q, want on so removing the setting is observable", baseline)
	}
	for _, end := range []string{"commit", "rollback", "in-batch-error"} {
		t.Run(end, func(t *testing.T) {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SELECT set_config('role', 'dude_sweeper', true), set_config('search_path', 'due_jit, public', true)`); err != nil {
				t.Fatal(err)
			}
			s := &Syncer{}
			runs, err := s.dueIn(ctx, tx, sweepBatch)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 || runs[0].ID != "run_jit" {
				t.Fatalf("due returned %+v, want run_jit", runs)
			}
			var jit string
			if err := tx.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&jit); err != nil {
				t.Fatal(err)
			}
			if jit != "off" {
				t.Errorf("inside due transaction jit is %q, want off", jit)
			}
			if end == "in-batch-error" {
				runs, err = s.dueIn(ctx, tx, -1)
				pgerr, ok := err.(*pgconn.PgError)
				if !ok || pgerr.Code != "2201W" || len(runs) != 0 {
					t.Fatalf("negative LIMIT: runs=%v error=%v, want no Runs and SQLSTATE 2201W", runs, err)
				}
			}
			if end == "commit" {
				err = tx.Commit(ctx)
			} else {
				err = tx.Rollback(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&jit); err != nil {
				t.Fatal(err)
			}
			if jit != baseline {
				t.Errorf("after %s jit is %q, want baseline %q", end, jit, baseline)
			}
		})
	}
}
