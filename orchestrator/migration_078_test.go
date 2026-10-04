package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The Go migrator builds 078's index concurrently over existing events, as
// the runner does outside a transaction: it is valid, and the steer tool's
// lookup of where a Run's input lands finds the latest landing through it.
func TestTheLandsIndexIsBuiltOnAnUpgrade(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "078")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_u', 'U', 'u')`)
	mustExec(t, owner, `INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
		VALUES ('evt_p', 'org_u', 'agent.prompt.delivered', 'run_u', 'system', 'x', 'runner', '{"lands":"next_turn"}'),
		       ('evt_m', 'org_u', 'agent.message', 'run_u', 'agent', 'x', 'runner', '{}'),
		       ('evt_a', 'org_u', 'run.directive.accepted', 'run_u', 'system', 'x', 'runner', '{"lands":"next_step"}'),
		       ('evt_n', 'org_u', 'run.directive.accepted', 'run_u', 'system', 'x', 'runner', '{}')`)
	apply()
	var valid bool
	if err := owner.QueryRow(context.Background(), `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'events_run_lands_idx'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("events_run_lands_idx valid=%v: %v", valid, err)
	}
	var lands string
	if err := owner.QueryRow(context.Background(), `SELECT payload->>'lands' FROM events WHERE run_id = 'run_u'
		AND event_type IN ('run.directive.accepted', 'agent.prompt.delivered') AND payload ? 'lands'
		ORDER BY cursor DESC LIMIT 1`).Scan(&lands); err != nil || lands != "next_step" {
		t.Fatalf("lands %q: %v", lands, err)
	}
}
