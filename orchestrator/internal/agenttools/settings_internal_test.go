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
	if _, err := owner.Exec(ctx, `INSERT INTO model_tiers(id,organization_id,name,model,position) SELECT 'mtr_count_'||i,$1,'Count '||i,'m',i FROM generate_series(1,50) i`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO project_servers(project_id,organization_id,name,port,command) SELECT p, $1, 'srv'||i, 3000+i, 'run' FROM unnest(ARRAY['p1_'||$1,'p2_'||$1,'p3_'||$1]) p, generate_series(1,20) i`, org); err != nil {
		t.Fatal(err)
	}
	if many := count("s3_" + org); many != three {
		t.Errorf("statements: %d with catalogue and recipe data, %d before", many, three)
	}
}

// The images section reads a page in as many statements with one image as
// with fifty, each with a published version, a pending build and a parent.
func TestSettingsImagesStatementsDoNotGrowWithImages(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	count := func() (int, int) {
		t.Helper()
		var n, images int
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			c := &countingTx{Tx: tx}
			out, err := readSettings(ctx, c, Caller{Org: org, Role: "conductor", grants: capabilities.Default("conductor")},
				settingsIn{Section: "images", Limit: 50})
			n, images = c.n, len(out.Images.Images)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n, images
	}
	seed := func(from, to int) {
		t.Helper()
		for _, q := range []string{
			`INSERT INTO images (id, organization_id, name) SELECT 'img_'||i||'_'||$1, $1, 'i'||i FROM generate_series($2::int, $3::int) i`,
			`INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state, user_ref) SELECT
				'v1_'||i||'_'||$1, $1, 'img_'||i||'_'||$1, 1, 'FROM x', 'published', 'r' FROM generate_series($2::int, $3::int) i`,
			`INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state) SELECT
				'v2_'||i||'_'||$1, $1, 'img_'||i||'_'||$1, 2, 'FROM x', 'queued' FROM generate_series($2::int, $3::int) i`,
			`UPDATE images SET published_version_id = 'v1_'||i||'_'||$1 FROM generate_series($2::int, $3::int) i
				WHERE id = 'img_'||i||'_'||$1`,
			`INSERT INTO image_version_parents (organization_id, version_id, parent_image_id) SELECT
				$1, 'v2_'||i||'_'||$1, 'img_1_'||$1 FROM generate_series(greatest($2::int, 2), $3::int) i`,
		} {
			if _, err := owner.Exec(ctx, q, org, from, to); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(1, 1)
	one, n1 := count()
	seed(2, 50)
	fifty, n50 := count()
	if n1 != 1 || n50 != 50 || one != fifty {
		t.Errorf("statements: %d for %d image, %d for %d", one, n1, fifty, n50)
	}
	t.Logf("images section: %d statements for 1 and for 50 images", fifty)
}
