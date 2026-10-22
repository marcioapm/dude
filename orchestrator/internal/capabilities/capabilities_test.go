package capabilities_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// A brainstorm whose session a member named resolves without name_session,
// and keeps the rest of its grants; an untitled session or one the agent
// named still has it. A task's Run, which has no session, is unaffected.
func TestABrainstormLosesNameSessionOnceAMemberNamedItsSession(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, s := range []struct{ id, title, by string }{
		{"ssn_untitled", "", ""}, {"ssn_agent", "Metering", "agent"}, {"ssn_person", "Dry run", "person"},
	} {
		if _, err := owner.Exec(ctx, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''))`,
			s.id, org, s.title, s.by); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(a capabilities.Agent) (g capabilities.Grants) {
		t.Helper()
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
			g, err = capabilities.Resolve(ctx, tx, a)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return g
	}
	for session, want := range map[string]bool{"ssn_untitled": true, "ssn_agent": true, "ssn_person": false} {
		g := resolve(capabilities.Agent{Role: "brainstorm", Org: org, Run: "run_" + session, Session: session})
		if g.Tool("name_session") != want {
			t.Errorf("%s: name_session granted %v, want %v", session, g.Tool("name_session"), want)
		}
		for _, kept := range []string{"propose", "relate", "ask_person", "refresh_checkout"} {
			if !g.Tool(kept) {
				t.Errorf("%s: %s taken away with name_session", session, kept)
			}
		}
	}
	if g := resolve(capabilities.Agent{Role: "conductor", Org: org, Run: "run_c"}); !g.Tool("start_phase") || g.Tool("name_session") {
		t.Errorf("a conductor's grants changed: start_phase %v, name_session %v", g.Tool("start_phase"), g.Tool("name_session"))
	}
}
