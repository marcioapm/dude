package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The Go migrator builds 108's index concurrently over existing
// directives, as the runner does outside a transaction: it is valid and
// partial on resends IS NOT NULL, and the directives are still there.
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
	var pred *string
	if err := owner.QueryRow(context.Background(), `SELECT i.indisvalid, pg_get_expr(i.indpred, i.indrelid) FROM pg_index i
		WHERE i.indexrelid = 'directives_resends_idx'::regclass`).Scan(&valid, &pred); err != nil || !valid || pred == nil || *pred != "(resends IS NOT NULL)" {
		t.Fatalf("directives_resends_idx valid=%v predicate=%v: %v", valid, pred, err)
	}
	// The resend reads back after the build; on 51 rows this does not show the index is used.
	var id string
	if err := owner.QueryRow(context.Background(), `SELECT id FROM directives WHERE resends = 'dir_u7'
		AND run_id = 'run_u' AND delivered_at IS NOT NULL`).Scan(&id); err != nil || id != "dir_r" {
		t.Fatalf("the resend of dir_u7: %q, %v", id, err)
	}
}
