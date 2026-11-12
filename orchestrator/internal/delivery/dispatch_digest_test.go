package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
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

// failedDispatch is a task with one dispatch GitHub refused (422): failed,
// no outcome, its error stored. Nothing ran.
func failedDispatch(t *testing.T) (*db.DB, string) {
	t.Helper()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_f', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_f', $1, 'prj_f', 1, 'T')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_f', $1, 'prj_f', 'target', 'https://github.com/acme/target.git')`,
		`INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, state, reason, last_error)
			VALUES ('wfd_f', $1, 'wi_f', 'repo_f', 'deploy.yml', 'main', 'failed', 'GitHub refused the dispatch',
				'github 422: Unexpected inputs provided: ["env"]')`,
	} {
		if _, err := owner.Exec(context.Background(), q, org); err != nil {
			t.Fatal(err)
		}
	}
	return app, org
}

// M1: wait_for {workflowEnded} on a dispatch GitHub refused is refused,
// saying GitHub refused it, with its error, and that nothing ran: there
// is nothing to wait for, and a wait on it could never hold.
func TestAWaitOnARefusedDispatchIsRefused(t *testing.T) {
	app, org := failedDispatch(t)
	err := app.InOrg(context.Background(), org, func(tx pgx.Tx) error {
		_, err := normalizeWaitTx(context.Background(), tx, "wi_f", []WaitCondition{{WorkflowEnded: "wfd_f"}})
		return err
	})
	if !IsRefusal(err) {
		t.Fatalf("a wait on a refused dispatch: %v", err)
	}
	for _, want := range []string{"GitHub refused dispatch wfd_f", `github 422: Unexpected inputs provided: ["env"]`, "nothing ran"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}

// M1: a refused dispatch, read as a wait's item (a wait taken before it
// failed, or one naming several), is terminal and says GitHub refused it,
// with its error, and that nothing ran; never "has not answered yet".
func TestARefusedDispatchIsTerminalInAWait(t *testing.T) {
	app, org := failedDispatch(t)
	if err := app.InOrg(context.Background(), org, func(tx pgx.Tx) error {
		it, err := workflowItemTx(context.Background(), tx, "wfd_f")
		if err != nil {
			return err
		}
		if it.rank == rankWaiting {
			t.Errorf("a refused dispatch is waited on")
		}
		want := `workflow deploy.yml (wfd_f): GitHub refused it: github 422: Unexpected inputs provided: ["env"]; nothing ran`
		if it.Workflow.Line != want {
			t.Errorf("its item:\n got %q\nwant %q", it.Workflow.Line, want)
		}
		ev, err := evaluateConditionsTx(context.Background(), tx, []waitCond{{Kind: condWorkflowEnded, Dispatch: "wfd_f"}}, true)
		if err != nil {
			return err
		}
		if !ev.holds {
			t.Errorf("a wait on a refused dispatch does not hold")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
