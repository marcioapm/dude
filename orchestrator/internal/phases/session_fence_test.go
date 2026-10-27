package phases

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A repository linked to a chat-born session's agent that its lux Run does
// not hold yet is brought by a resume (repoApproved), unless its project is
// hidden from the session's chat: then nothing is brought and the agent is
// not paused for it.
func TestAHiddenProjectsRepositoryIsNotBroughtToASessionsAgent(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_sec', $1, 'Secret', 'sec', 'SEC')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ('repo_sec', $1, 'prj_sec', 'vault', 'https://github.com/acme/vault.git', 'main')`,
		`INSERT INTO sessions (id, organization_id, title, origin) VALUES ('ssn_r', $1, 'R', '{"provider":"slack","conversationId":"C1"}')`,
		`INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_r', 'prj_sec', $1)`,
		`INSERT INTO session_repositories (session_id, repository_id, organization_id) VALUES ('ssn_r', 'repo_sec', $1)`,
		`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, lux_run_id, lux_state)
			VALUES ('run_r', $1, 'ssn_r', 1, 'running', 'agent', 'brainstorm', 'lrun_r', 'running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	missing := func() bool {
		t.Helper()
		var b bool
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT `+repoApproved+` FROM runs r WHERE r.id = 'run_r'`).Scan(&b)
		}); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if !missing() {
		t.Fatal("a linked repository not checked out is not brought")
	}
	if _, err := owner.Exec(ctx, `INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', 'prj_sec')`, org); err != nil {
		t.Fatal(err)
	}
	if missing() {
		t.Fatal("a hidden project's repository is brought to the session's agent")
	}
}
