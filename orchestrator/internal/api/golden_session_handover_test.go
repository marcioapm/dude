package api

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

var updateGoldens = flag.Bool("update", false, "rewrite delivery/agents/testdata/*.golden.md from the current code")

// What a session's brainstorm agent is told when its session changes
// owner, for each way the owner before stays. The golden lives with the
// other agent goldens, in delivery/agents/testdata.
func TestGoldenSessionHandover(t *testing.T) {
	ctx := context.Background()
	if *updateGoldens {
		if err := dbtest.Reachable(ctx); err != nil {
			t.Fatalf("-update needs the test database: %v", err)
		}
	}
	var b strings.Builder
	for _, keep := range []string{delivery.SessionChat, delivery.SessionRead, "leave"} {
		app, owner := dbtest.Open(t)
		org := dbtest.Org(t, owner)
		for _, q := range []string{
			`INSERT INTO people (id, organization_id, name) VALUES ('per_ana', $1, 'Ana'), ('per_bo', $1, 'Bo')`,
			`INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ses_h', $1, 'Plan', 'person')`,
			`INSERT INTO session_people (session_id, person_id, organization_id, role, invited_at, accepted_at)
				VALUES ('ses_h', 'per_ana', $1, 'owner', now(), now()), ('ses_h', 'per_bo', $1, 'chat', now(), now())`,
			`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, base_refs)
				VALUES ('run_h', $1, 'ses_h', 1, 'running', 'agent', 'brainstorm', '{}')`,
		} {
			if _, err := owner.Exec(ctx, q, org); err != nil {
				t.Fatal(err)
			}
		}
		var told string
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			by := principal{Actor: "per_ana", Person: "per_ana", ActorType: "human"}
			if err := makeOwner(ctx, tx, org, "ses_h", "per_ana", "per_bo", keep, by); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT text FROM directives WHERE run_id = 'run_h'`).Scan(&told)
		}); err != nil {
			t.Fatal(err)
		}
		b.WriteString("## keep-" + keep + "\n\n" + told + "\n\n")
	}
	path := filepath.Join("..", "delivery", "agents", "testdata", "session-handover.golden.md")
	if *updateGoldens {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if b.String() != string(want) {
		t.Errorf("session-handover differs from %s:\n%s", path, b.String())
	}
}
