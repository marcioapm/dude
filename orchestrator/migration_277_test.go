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
