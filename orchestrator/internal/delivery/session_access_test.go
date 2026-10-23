package delivery_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Writing joins only an organisation session: a pending invitation to a
// private one stays pending, with no session.joined; one to an organisation
// session is accepted as it stood (read stays read), joined once; a
// handover's invitation is left to Accept.
func TestJoinByWritingJoinsOnlyAnOrganisationSession(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := owner.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	own, ana, eli := "per_own_"+org, "per_ana_"+org, "per_eli_"+org
	exec(`INSERT INTO people (id, organization_id, name) VALUES ($1, $4, 'Own'), ($2, $4, 'Ana'), ($3, $4, 'Eli')`, own, ana, eli, org)
	priv, open := "ssn_priv_"+org, "ssn_open_"+org
	exec(`INSERT INTO sessions (id, organization_id, title, access) VALUES ($1, $3, 'P', 'private'), ($2, $3, 'O', 'organisation')`, priv, open, org)
	exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ($1, $3, $4, 'owner', now()), ($2, $3, $4, 'owner', now())`, priv, open, own, org)
	exec(`INSERT INTO session_people (session_id, person_id, organization_id, role) VALUES ($1, $3, $4, 'read'), ($2, $3, $4, 'read')`,
		priv, open, ana, org)
	exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, becomes_owner, handover_keep)
		VALUES ($1, $2, $3, 'chat', true, 'chat')`, open, eli, org)

	join := func(session, person string) {
		t.Helper()
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			return delivery.JoinByWriting(ctx, tx, delivery.SessionRef(org, session), person, "human", person)
		}); err != nil {
			t.Fatal(err)
		}
	}
	join(priv, ana)
	if n := count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND accepted_at IS NULL`, priv, ana); n != 1 {
		t.Errorf("writing accepted a private session's pending invitation")
	}
	if n := count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.joined'`, priv); n != 0 {
		t.Errorf("session.joined in a private session: %d", n)
	}

	join(open, ana)
	join(open, ana)
	if n := count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND accepted_at IS NOT NULL AND role = 'read'`, open, ana); n != 1 {
		t.Errorf("the organisation session's pending read invitation was not accepted as it stood")
	}
	if n := count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.joined' AND payload->>'person' = $2`, open, ana); n != 1 {
		t.Errorf("session.joined for Ana: %d, want 1", n)
	}
	join(open, eli)
	if n := count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND accepted_at IS NULL AND becomes_owner`, open, eli); n != 1 {
		t.Errorf("writing accepted a handover's invitation")
	}
}
