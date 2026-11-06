package orchestrator_test

import (
	"context"
	"testing"
)

// Migration 250: a question on a Run marked lost by lux (lux_lost_at, set
// in the same UPDATE as its status) stays open through the transaction, to
// be moved to the Run replacing it; whatever is still open on it at commit
// is cancelled (the deferred cancel_lost_questions). A Run failed without
// it cancels at once, as 015 always did.
func TestALostRunsQuestionIsCancelledAtCommitUnlessMoved(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	task := w.task()
	run := func(id, status string) {
		mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role)
			VALUES ($1, $2, $3, $4, 1, $5, 'agent', 'conductor')`, id, w.org, w.project, task, status)
	}
	ask := func(id, runID string) {
		mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt) VALUES ($1, $2, $3, $4, 'Open?')`,
			id, w.org, task, runID)
	}
	status := func(q string) string { return w.str(`SELECT status::text || '/' || run_id FROM questions WHERE id = $1`, q) }

	t.Run("lost, not moved: cancelled at commit", func(t *testing.T) {
		run("run_lost_a", "paused")
		ask("q_a", "run_lost_a")
		tx, err := w.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', lux_lost_at = now() WHERE id = 'run_lost_a'`); err != nil {
			t.Fatal(err)
		}
		var inTx string
		if err := tx.QueryRow(ctx, `SELECT status::text FROM questions WHERE id = 'q_a'`).Scan(&inTx); err != nil {
			t.Fatal(err)
		}
		if inTx != "open" {
			t.Errorf("inside the transaction the question is %s, want open until commit", inTx)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if got := status("q_a"); got != "cancelled/run_lost_a" {
			t.Errorf("after commit %s, want cancelled", got)
		}
	})
	t.Run("lost and moved in the same transaction: open on the replacement", func(t *testing.T) {
		run("run_lost_b", "paused")
		ask("q_b", "run_lost_b")
		tx, err := w.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`UPDATE runs SET status = 'failed', lux_lost_at = now() WHERE id = 'run_lost_b'`,
			`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role)
				SELECT 'run_next_b', organization_id, project_id, task_id, 1, 'paused', 'agent', 'conductor' FROM runs WHERE id = 'run_lost_b'`,
			`UPDATE questions SET run_id = 'run_next_b' WHERE run_id = 'run_lost_b' AND status = 'open'`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if got := status("q_b"); got != "open/run_next_b" {
			t.Errorf("after commit %s, want open on the replacement", got)
		}
	})
	t.Run("failed, not lost: cancelled at once", func(t *testing.T) {
		mustExec(t, w.owner, `UPDATE runs SET status = 'completed' WHERE id = 'run_next_b'`)
		run("run_failed_c", "paused")
		ask("q_c", "run_failed_c")
		tx, err := w.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed' WHERE id = 'run_failed_c'`); err != nil {
			t.Fatal(err)
		}
		var inTx string
		if err := tx.QueryRow(ctx, `SELECT status::text FROM questions WHERE id = 'q_c'`).Scan(&inTx); err != nil {
			t.Fatal(err)
		}
		if inTx != "cancelled" {
			t.Errorf("inside the transaction the question is %s, want cancelled at once", inTx)
		}
	})
}
