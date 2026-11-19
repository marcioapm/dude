package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// 273 installs the compatibility writer before 277's bounded backfill. An
// event written by an old writer after 273 is populated by the trigger, while
// historical NULL rows are filled by the resumable backfill.
func TestMigration273CompatibilityWriterAnd277Backfill(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "273")
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	mustExec(t, owner, `INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, payload)
		VALUES ('evt_old', $1, 'chat.message', 'agent', 'old', 'orchestrator', '{"github":{"answerId":"answer_old"}}')`, org)
	apply()
	if got := scalar[string](t, owner, `SELECT causation_id FROM events WHERE id = 'evt_old'`); got != "answer_old" {
		t.Fatalf("compatibility writer set causation_id to %q", got)
	}
	mustExec(t, owner, `INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, payload)
		VALUES ('evt_new', $1, 'chat.message', 'agent', 'old', 'orchestrator', '{"answerId":"answer_new"}')`, org)
	if got := scalar[string](t, owner, `SELECT causation_id FROM events WHERE id = 'evt_new'`); got != "answer_new" {
		t.Fatalf("old-writer insert after migration set causation_id to %q", got)
	}
	// An old writer's reply names its answer under github; a record that
	// names its own cause keeps it.
	mustExec(t, owner, `INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, payload, causation_id) VALUES
		('evt_gh', $1, 'chat.message', 'agent', 'old', 'orchestrator', '{"github":{"answerId":"answer_gh"}}', NULL),
		('evt_own', $1, 'pull_request.action', 'agent', 'new', 'orchestrator', '{"answerId":"answer_x"}', 'own_cause')`, org)
	if got := scalar[string](t, owner, `SELECT causation_id FROM events WHERE id = 'evt_gh'`); got != "answer_gh" {
		t.Fatalf("old-writer github reply after migration set causation_id to %q", got)
	}
	if got := scalar[string](t, owner, `SELECT causation_id FROM events WHERE id = 'evt_own'`); got != "own_cause" {
		t.Fatalf("the compatibility writer replaced a record's own cause with %q", got)
	}
	var predicate string
	if err := owner.QueryRow(ctx, `SELECT pg_get_expr(i.indpred, i.indrelid) FROM pg_index i WHERE i.indexrelid = 'events_run_answer_causation_idx'::regclass`).Scan(&predicate); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(predicate, "causation_id IS NOT NULL") {
		t.Fatalf("causation index predicate %q omits the non-NULL guard", predicate)
	}
}

// ApplyMigration fails closed as the runner does: a batch that does not
// report exactly one integer batch_count, or a file with two sections of
// one kind, is an error, never "nothing left to fill".
func TestApplyMigrationRefusesABatchWithoutBatchCount(t *testing.T) {
	owner, _ := dbtest.Upgrade(t, "000")
	ctx := context.Background()
	file := func(body string) string {
		return "-- dude:batch-sql\n-- dude:setup\n" +
			"CREATE TABLE IF NOT EXISTS t_fill (id int PRIMARY KEY, filled bool NOT NULL DEFAULT false);\n" +
			"INSERT INTO t_fill (id) SELECT g FROM generate_series(1, 5) g ON CONFLICT DO NOTHING;\n" +
			"-- dude:batch\n" + body + "\n"
	}
	const fill = "WITH u AS (UPDATE t_fill SET filled = true WHERE id IN (SELECT id FROM t_fill WHERE NOT filled ORDER BY id LIMIT 2) RETURNING id)"
	for _, c := range []struct{ label, body, refusal string }{
		{"no batch_count column", fill + " SELECT count(*)::int AS n FROM u;", "returns no batch_count column"},
		{"a misnamed column", fill + " SELECT count(*)::int AS batch_counts FROM u;", "returns no batch_count column"},
		{"no row", fill + " SELECT count(*)::int AS batch_count FROM u WHERE false GROUP BY id;", "returns 0 rows, not exactly one"},
		{"two rows", fill + " SELECT 1 AS batch_count FROM u;", "returns 2 rows, not exactly one"},
		{"a NULL batch_count", fill + " SELECT NULL::int AS batch_count FROM (SELECT count(*) FROM u) c;", "batch_count is NULL"},
		{"a fractional batch_count", fill + " SELECT 1.5::numeric AS batch_count FROM (SELECT count(*) FROM u) c;", "not an integer"},
	} {
		mustExec(t, owner, `DROP TABLE IF EXISTS t_fill`)
		err := dbtest.ApplyMigration(ctx, owner, file(c.body))
		if err == nil || !strings.Contains(err.Error(), c.refusal) {
			t.Fatalf("%s: ApplyMigration returned %v, want an error containing %q", c.label, err, c.refusal)
		}
	}
	for _, twice := range []string{"setup", "batch", "cleanup"} {
		mustExec(t, owner, `DROP TABLE IF EXISTS t_fill`)
		contents := file(fill+" SELECT count(*)::int AS batch_count FROM u;") + "-- dude:cleanup\nSELECT 1;\n-- dude:" + twice + "\nSELECT 1;\n"
		err := dbtest.ApplyMigration(ctx, owner, contents)
		if want := "more than one " + twice + " section"; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("two %s sections: ApplyMigration returned %v, want %q", twice, err, want)
		}
		if absent := scalar[bool](t, owner, `SELECT to_regclass('t_fill') IS NULL`); !absent {
			t.Fatalf("two %s sections: the setup ran before the file was refused", twice)
		}
	}
	if err := dbtest.ApplyMigration(ctx, owner, file(fill+" SELECT count(*)::int AS batch_count FROM u;")); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int64](t, owner, `SELECT count(*) FROM t_fill WHERE NOT filled`); n != 0 {
		t.Fatalf("%d rows left unfilled", n)
	}
}
