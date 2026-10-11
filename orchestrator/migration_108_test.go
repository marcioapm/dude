package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The Go migrator builds 108's index concurrently over existing
// directives, as the runner does outside a transaction: it is valid, and
// the replay's lookup of a directive's resends finds them.
func TestTheResendsIndexIsBuiltOnAnUpgrade(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "108")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_u', 'U', 'u')`)
	mustExec(t, owner, `INSERT INTO sessions (id, organization_id, title) VALUES ('ses_u', 'org_u', 'S')`)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, base_refs)
		VALUES ('run_u', 'org_u', 'ses_u', 1, 'completed', 'agent', 'brainstorm', '{}')`)
	mustExec(t, owner, `INSERT INTO directives (id, organization_id, run_id, text)
		SELECT 'dir_u' || g, 'org_u', 'run_u', 'x' FROM generate_series(1, 50) g`)
	mustExec(t, owner, `INSERT INTO directives (id, organization_id, run_id, text, supersedes, interrupt, resends, delivered_at)
		VALUES ('dir_r', 'org_u', 'run_u', 'x', 'dir_u7', true, 'dir_u7', now())`)
	apply()
	var valid bool
	if err := owner.QueryRow(context.Background(), `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'directives_resends_idx'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("directives_resends_idx valid=%v: %v", valid, err)
	}
	var id string
	if err := owner.QueryRow(context.Background(), `SELECT id FROM directives WHERE resends = 'dir_u7'
		AND run_id = 'run_u' AND delivered_at IS NOT NULL`).Scan(&id); err != nil || id != "dir_r" {
		t.Fatalf("the resend of dir_u7: %q, %v", id, err)
	}
}
