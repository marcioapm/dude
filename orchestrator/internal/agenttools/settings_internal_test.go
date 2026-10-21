package agenttools

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// countingTx counts the statements run through it.
type countingTx struct {
	pgx.Tx
	n int
}

func (c *countingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n++
	return c.Tx.Query(ctx, sql, args...)
}

func (c *countingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n++
	return c.Tx.QueryRow(ctx, sql, args...)
}

func (c *countingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.n++
	return c.Tx.Exec(ctx, sql, args...)
}

// A brainstorm's settings read runs as many statements with three linked
// projects as with one: each table is read once for every project.
func TestSettingsStatementsDoNotGrowWithLinkedProjects(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
			('p1_'||$1, $1, 'One', 'p1_'||$1, 'ONE'), ('p2_'||$1, $1, 'Two', 'p2_'||$1, 'TWO'), ('p3_'||$1, $1, 'Three', 'p3_'||$1, 'THR')`,
		`INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('s1_'||$1, $1, NULL, NULL), ('s3_'||$1, $1, NULL, NULL)`,
		`INSERT INTO session_projects (session_id, project_id, organization_id) VALUES
			('s1_'||$1, 'p1_'||$1, $1), ('s3_'||$1, 'p1_'||$1, $1), ('s3_'||$1, 'p2_'||$1, $1), ('s3_'||$1, 'p3_'||$1, $1)`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	count := func(session string) int {
		t.Helper()
		var n, scopes int
		err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			c := &countingTx{Tx: tx}
			out, err := readSettings(ctx, c, Caller{Org: org, Role: "brainstorm", SessionID: session,
				grants: capabilities.Default("brainstorm")}, settingsIn{})
			n, scopes = c.n, len(out.Scopes)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]int{"s1_" + org: 1, "s3_" + org: 3}[session]; scopes != want {
			t.Errorf("%s: %d scopes, want %d", session, scopes, want)
		}
		return n
	}
	one, three := count("s1_"+org), count("s3_"+org)
	if one != three {
		t.Errorf("statements: %d with one linked project, %d with three", one, three)
	}
	t.Logf("a brainstorm's settings read with three linked projects: %d statements", three)
}
