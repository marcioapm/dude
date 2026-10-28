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
	other := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, s := range []struct{ id, org, title, by string }{
		{"ssn_untitled", org, "", ""}, {"ssn_agent", org, "Metering", "agent"}, {"ssn_person", org, "Dry run", "person"},
		{"ssn_other_org", other, "Theirs", "person"},
	} {
		if _, err := owner.Exec(ctx, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''))`,
			s.id, s.org, s.title, s.by); err != nil {
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
	// A missing or other-organisation session has no visible row, so defaults stay.
	for session, want := range map[string]bool{"ssn_untitled": true, "ssn_agent": true, "ssn_person": false,
		"ssn_missing": true, "ssn_other_org": true} {
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

// reply_in_thread is a brainstorm's only when a chat thread started its
// session: a session from the web, or one whose origin names no
// conversation, resolves without it, and keeps chat_history.
func TestOnlyAChatBornSessionsBrainstormRepliesInItsThread(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, s := range []struct{ id, origin string }{
		{"ssn_chat", `{"provider":"slack","workspaceId":"T1","conversationId":"C1","threadId":"1.1"}`},
		{"ssn_web", ""}, {"ssn_partial", `{"provider":"slack"}`},
	} {
		if _, err := owner.Exec(ctx, `INSERT INTO sessions (id, organization_id, title, titled_by, origin) VALUES ($1, $2, 'Berlin', 'agent', NULLIF($3, '')::jsonb)`,
			s.id, org, s.origin); err != nil {
			t.Fatal(err)
		}
	}
	for session, want := range map[string]bool{"ssn_chat": true, "ssn_web": false, "ssn_partial": false, "ssn_missing": false} {
		var g capabilities.Grants
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
			g, err = capabilities.Resolve(ctx, tx, capabilities.Agent{Role: "brainstorm", Org: org, Run: "run_" + session, Session: session})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if g.Tool("reply_in_thread") != want || !g.Tool("chat_history") {
			t.Errorf("%s: reply_in_thread %v, want %v; chat_history %v", session, g.Tool("reply_in_thread"), want, g.Tool("chat_history"))
		}
	}
	for role := range capabilities.Defaults {
		if capabilities.Default(role).Tool("reply_in_thread") {
			t.Errorf("%q's defaults, read with no session, reply in a thread", role)
		}
	}
}
