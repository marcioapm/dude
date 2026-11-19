package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// 273 installs the compatibility writer before 277's bounded backfill. An
// event written by an old writer after 273 is populated by the trigger, while
// historical NULL rows are filled by the resumable backfill.
func TestMigration273CompatibilityWriterAnd277Backfill(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "273")
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
}
