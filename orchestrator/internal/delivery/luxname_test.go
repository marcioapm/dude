package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lux_name (migration 090), which queries over runs.lux_repositories use,
// names every repository as lux.SpecName does, which the spec is built with.
func TestLuxNameInSQLIsSpecName(t *testing.T) {
	_, owner := dbtest.Open(t)
	for _, name := range []string{"web", "bl-target", "BILL-billing-api", "WC-Web", "WC-web.app", "-x", "_x", ".", "Ü-ß",
		"BILL-" + strings.Repeat("payments-ledger-", 4), strings.Repeat("a", 32), strings.Repeat("a", 33)} {
		var got string
		if err := owner.QueryRow(context.Background(), `SELECT lux_name($1)`, name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := lux.SpecName(name); got != want {
			t.Errorf("lux_name(%q) = %q, lux.SpecName = %q", name, got, want)
		}
	}
	// A session's repository, by the expression the syncer matches on.
	r := SessionRepo{Key: "BILL", Name: "billing-api"}
	var got string
	if err := owner.QueryRow(context.Background(), `SELECT `+SessionSpecNameSQL+` FROM (SELECT $1::text AS key_prefix) p,
		(SELECT $2::text AS name) repo`, r.Key, r.Name).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != r.SpecName() || !lux.NameRe.MatchString(got) {
		t.Errorf("SessionSpecNameSQL = %q, SpecName = %q", got, r.SpecName())
	}
}
