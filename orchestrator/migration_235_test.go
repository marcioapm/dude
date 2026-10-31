package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Migration 235 adds its CHECKs NOT VALID, as 075 and 096 do: no scan of
// review_findings or runs under ACCESS EXCLUSIVE. Each still holds for
// every row written after it.
func TestMigration235ChecksAreAddedWithoutAScan(t *testing.T) {
	_, owner := dbtest.Open(t)
	for _, c := range []struct{ table, name, value string }{
		{"review_findings", "review_findings_evidence_kind_check", "'query_plan'"},
		{"runs", "runs_writer_outcome_check", "'blocked'"},
	} {
		var validated bool
		if err := owner.QueryRow(context.Background(), `SELECT convalidated FROM pg_constraint
			WHERE conrelid = $1::regclass AND conname = $2 AND contype = 'c'`, c.table, c.name).Scan(&validated); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if validated {
			t.Errorf("%s was validated as it was added: a scan of %s under ACCESS EXCLUSIVE", c.name, c.table)
		}
		var def string
		if err := owner.QueryRow(context.Background(), `SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = $1::regclass AND conname = $2`, c.table, c.name).Scan(&def); err != nil || !strings.Contains(def, c.value) || !strings.Contains(def, "NOT VALID") {
			t.Fatalf("%s definition: %q %v", c.name, def, err)
		}
	}
}
