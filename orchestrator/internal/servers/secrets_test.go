package servers

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// rowCounts counts the rows each SELECT answered over a pool's connections.
type rowCounts struct {
	mu      sync.Mutex
	largest int64
}

func (r *rowCounts) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (r *rowCounts) TraceQueryEnd(_ context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.CommandTag.Select() {
		r.largest = max(r.largest, d.CommandTag.RowsAffected())
	}
}

// A resume reads the values of the names its Run declared and no other:
// a project with 100 secrets of 32 KiB added since answers one row, and a
// declared name since removed is reported gone.
func TestAResumeReadsOnlyTheDeclaredSecretsValues(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, preview_secrets)
		VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'preview', ARRAY['SEED_KEY', 'REMOVED_KEY'])`, org)
	exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint) VALUES ('prj_'||$1, $1, 'SEED_KEY', 'current', 'rent')`, org)
	exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint)
		SELECT 'prj_'||$1, $1, 'ADDED_'||n, $2, 'xxxx' FROM generate_series(1, 100) n`, org, strings.Repeat("x", 32768))

	counts := &rowCounts{}
	cfg := app.Pool.Config()
	cfg.ConnConfig.Tracer = counts
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	p := &Previews{Service: &Service{DB: &db.DB{Pool: pool}}}
	secrets, gone, err := p.resumeSecrets(ctx, previewRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org})
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Name != "SEED_KEY" || secrets[0].Value != "current" || secrets[0].As != "env" {
		t.Errorf("secrets = %+v; want SEED_KEY's current value as env", secrets)
	}
	if !slices.Equal(gone, []string{"REMOVED_KEY"}) {
		t.Errorf("gone = %v; want REMOVED_KEY", gone)
	}
	if counts.largest > 1 {
		t.Errorf("a resume declaring one present secret read a %d-row result", counts.largest)
	}
}

// A preview given an organisation secret (migration 210) resumes with its
// current value, not "gone"; a recorded name the project now holds of its
// own resumes with the project's value.
func TestAResumeReadsTheOrganisationSecretsItWasGiven(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, preview_secrets)
		VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'preview', ARRAY['NPM_TOKEN', 'GH_TOKEN'])`, org)
	exec(`INSERT INTO organization_secrets (organization_id, name, value, hint, recipients) VALUES
		($1, 'NPM_TOKEN', 'org-npm', 'onpm', '{previews}'), ($1, 'GH_TOKEN', 'org-gh', 'o-gh', '{previews}')`, org)
	exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients)
		VALUES ('prj_'||$1, $1, 'GH_TOKEN', 'project-gh', 'p-gh', '{previews}')`, org)

	p := &Previews{Service: &Service{DB: app}}
	secrets, gone, err := p.resumeSecrets(ctx, previewRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range secrets {
		got[s.Name] = s.Value
	}
	if len(got) != 2 || got["NPM_TOKEN"] != "org-npm" || got["GH_TOKEN"] != "project-gh" {
		t.Errorf("secrets = %v; want NPM_TOKEN the organisation's, GH_TOKEN the project's", got)
	}
	if len(gone) != 0 {
		t.Errorf("gone = %v; want none", gone)
	}
}
