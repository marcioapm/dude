package delivery

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A turn's nudge a stop failed unread (a turn retry, a move's or idle
// carry-on) was about the turn the stop ended: taking the Run back up
// does not give it again.
func TestATurnsNudgeIsNotRequeued(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_n', $1, 'P', 'p', 'N')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_n', $1, 'prj_n', 1, 'Fix', 'fix', 'running')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase) VALUES ('run_n', $1, 'prj_n', 'wi_n', 1, 'paused', 'fix')`,
		`INSERT INTO directives (id, organization_id, task_id, run_id, text, scope, failed_at, error) VALUES
			('dir_retry_n', $1, 'wi_n', 'run_n', 'carry on after the temporary error', 'turn', now(), '` + UnreadRunAborted + `'),
			('dir_brief_n', $1, 'wi_n', 'run_n', '## Review findings', 'run', now(), '` + UnreadRunAborted + `')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	var resent []string
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := RequeueUnreadTx(ctx, tx, []string{"run_n"}); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(array_agg(resends ORDER BY resends), '{}') FROM directives
			WHERE run_id = 'run_n' AND resends IS NOT NULL`).Scan(&resent)
	}); err != nil {
		t.Fatal(err)
	}
	if len(resent) != 1 || resent[0] != "dir_brief_n" {
		t.Fatalf("requeued copies of %v, want only the brief", resent)
	}
}
