package delivery

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

func TestEscalationStatusAndQuestionShareTransaction(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet', 'running')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	w := &steps{s: &Store{DB: app}}
	st := &State{TaskID: "wi_e", ProjectID: "prj_e"}
	sc := workflow.StepContext{WorkflowRunID: "wf_e", OrganizationID: org, Step: "pr_wait"}
	if _, err := w.escalate(ctx, sc, st, "pull_request_conflict", map[string]any{"repo": "target", "number": 1}); err != nil {
		t.Fatal(err)
	}
	var status, task, asked string
	// xmin identifies the writing transaction; this checks atomicity, not race timing.
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT status::text, xmin::text FROM tasks WHERE id = $1`, st.TaskID).Scan(&status, &task); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT xmin::text FROM events WHERE task_id = $1 AND event_type = $2`, st.TaskID, EvQuestionAsked).Scan(&asked)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "awaiting_input" {
		t.Errorf("task status %s, want awaiting_input", status)
	}
	if task != asked {
		t.Errorf("awaiting_input committed in transaction %s, its question.asked in %s", task, asked)
	}
}
