package delivery

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A session started from Slack, linked to two projects each with a linked
// repository, one hidden from Slack: its projects, its repositories and the
// organisation's projects it is offered all leave the hidden one out; the
// same session not from chat has both.
func TestASessionsReadsLeaveOutAProjectHiddenFromItsChat(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_open', $1, 'Open', 'open', 'OPN'),
		('prj_sec', $1, 'Secret', 'sec', 'SEC'), ('prj_other', $1, 'Other', 'other', 'OTH')`, org)
	exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES
		('repo_open', $1, 'prj_open', 'api', 'https://github.com/acme/api.git', 'main'),
		('repo_sec', $1, 'prj_sec', 'vault', 'https://github.com/acme/vault.git', 'main')`, org)
	exec(`INSERT INTO sessions (id, organization_id, title, origin) VALUES ('ssn_f', $1, 'Nightly', '{"provider":"slack","conversationId":"C1"}')`, org)
	exec(`INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_f', 'prj_open', $1), ('ssn_f', 'prj_sec', $1)`, org)
	exec(`INSERT INTO session_repositories (session_id, repository_id, organization_id) VALUES ('ssn_f', 'repo_open', $1), ('ssn_f', 'repo_sec', $1)`, org)
	exec(`INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', 'prj_sec'), ($1, 'slack', 'prj_other')`, org)

	read := func() (projects, repos, offered []string) {
		t.Helper()
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			linked, err := SessionProjects(ctx, tx, "ssn_f")
			if err != nil {
				return err
			}
			for _, p := range linked {
				projects = append(projects, p.Key)
			}
			rs, err := SessionRepositories(ctx, tx, "ssn_f")
			if err != nil {
				return err
			}
			for _, r := range rs {
				repos = append(repos, r.Key+"/"+r.Name)
			}
			all, err := OrgProjects(ctx, tx, "ssn_f")
			for _, p := range all {
				offered = append(offered, p.Key)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return projects, repos, offered
	}
	projects, repos, offered := read()
	if !slices.Equal(projects, []string{"OPN"}) || !slices.Equal(repos, []string{"OPN/api"}) || !slices.Equal(offered, []string{"OPN"}) {
		t.Fatalf("from Slack: projects %v, repositories %v, offered %v", projects, repos, offered)
	}
	exec(`UPDATE sessions SET origin = NULL WHERE id = 'ssn_f'`)
	projects, repos, offered = read()
	if !slices.Equal(projects, []string{"OPN", "SEC"}) || !slices.Equal(repos, []string{"OPN/api", "SEC/vault"}) ||
		!slices.Equal(offered, []string{"OPN", "OTH", "SEC"}) {
		t.Fatalf("not from chat: projects %v, repositories %v, offered %v", projects, repos, offered)
	}
}
