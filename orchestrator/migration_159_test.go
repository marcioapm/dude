package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestSessionChildCreationKeyIndexIsUnique(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "160")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_159', 'O', 'o')`)
	mustExec(t, owner, `INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_159', 'org_159', 'S')`)
	apply()

	var valid, unique bool
	var predicate *string
	if err := owner.QueryRow(context.Background(), `SELECT i.indisvalid, i.indisunique, pg_get_expr(i.indpred, i.indrelid)
		FROM pg_index i WHERE i.indexrelid = 'runs_session_creation_key_idx'::regclass`).Scan(&valid, &unique, &predicate); err != nil {
		t.Fatal(err)
	}
	if !valid || !unique || predicate == nil || *predicate != "((session_id IS NOT NULL) AND (creation_key IS NOT NULL))" {
		t.Fatalf("session creation index valid=%v unique=%v predicate=%v", valid, unique, predicate)
	}
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, phase, role, creation_key)
		VALUES ('run_159_a', 'org_159', 'ssn_159', 1, 'pending', 'investigate', 'investigator', 'child:duplicate')`)
	if _, err := owner.Exec(context.Background(), `INSERT INTO runs (id, organization_id, session_id, attempt, status, phase, role, creation_key)
		VALUES ('run_159_b', 'org_159', 'ssn_159', 1, 'pending', 'investigate', 'investigator', 'child:duplicate')`); err == nil {
		t.Fatal("duplicate session creation key was accepted")
	}
}
