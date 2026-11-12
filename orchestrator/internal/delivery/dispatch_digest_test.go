package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A wait's digest is stored clipped to its bound. Only the workflow
// outcomes whose whole item is in the digest as stored are delivered by
// it; an item cut from it reached no one and stays owed, for the
// supervisor to wake for.
func TestAnItemCutFromTheDigestIsStillOwed(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_d', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_d', $1, 'prj_d', 1, 'T')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_d', $1, 'prj_d', 'target', 'https://github.com/acme/target.git')`,
		`INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, state, outcome, reason)
			SELECT 'wfd_' || n, $1, 'wi_d', 'repo_d', 'ci.yml', 'main', 'unknown', 'unknown', repeat('r', 900) FROM generate_series(1, 3) n`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		var ev waitEval
		for _, id := range []string{"wfd_1", "wfd_2", "wfd_3"} {
			it, err := workflowItemTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if n := len([]rune(it.Workflow.Line)); n > workflowItemChars {
				t.Errorf("%s's item is %d runes", id, n)
			}
			ev.items = append(ev.items, it)
		}
		full := renderWaitDigest(ev, "cwt_d", false, false, nil, "")
		// Stored clipped inside the third item: it was not told.
		told := clip(full, strings.Index(full, "(wfd_3)")+20)
		return deliveredByWaitTx(ctx, tx, "cwt_d", told, ev)
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := owner.Query(ctx, `SELECT id || ' ' || COALESCE(woken_outcome, 'owed') FROM workflow_dispatches WHERE task_id = 'wi_d' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ", ") != "wfd_1 unknown, wfd_2 unknown, wfd_3 owed" {
		t.Fatalf("delivered %v", got)
	}
}
