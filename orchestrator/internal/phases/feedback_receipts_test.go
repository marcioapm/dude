package phases

import (
	"context"
	"testing"
)

// A person's item steered to a Run whose directive fails unread, the Run
// still working: the item is open with no Run or directive on it, so the
// next steer or fixer that is given it records it afresh.
func TestAFailedSteerLetsGoOfThePeoplesFeedbackItCarried(t *testing.T) {
	w := newReceiptWorld(t)
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE runs SET status = 'running', phase = 'fix', role = 'implementer', kind = 'agent' WHERE id = 'run_'||$1`,
		`INSERT INTO people (id, organization_id, name) VALUES ('per_'||$1, $1, 'Ana')`,
		`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
			VALUES ('fbt_'||$1, $1, 'wi_'||$1, 1, 'per_'||$1, 'k')`,
		`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor,
				status, submitted_at, delivered_run_id, directive_id)
			VALUES ('fbi_'||$1, $1, 'wi_'||$1, 1, 'fbt_'||$1, 'per_'||$1, 'Rename it', 'general', '{"kind":"general"}', 'open', now(),
				'run_'||$1, 'dir_1')`,
	} {
		if _, err := w.owner.Exec(ctx, q, w.org); err != nil {
			t.Fatal(err)
		}
	}
	w.receive(failedFirst("dir_1", "the agent's input queue is full"))
	if failed, _ := w.failure(); !failed {
		t.Fatal("the directive did not fail")
	}
	var status string
	var cleared bool
	if err := w.owner.QueryRow(ctx, `SELECT status, directive_id IS NULL AND delivered_run_id IS NULL FROM feedback_items
		WHERE id = 'fbi_'||$1`, w.org).Scan(&status, &cleared); err != nil {
		t.Fatal(err)
	}
	if status != "open" || !cleared {
		t.Errorf("after the steer failed: %s, run and directive cleared %v", status, cleared)
	}
	var running string
	_ = w.owner.QueryRow(ctx, `SELECT status::text FROM runs WHERE id = 'run_'||$1`, w.org).Scan(&running)
	if running != "running" {
		t.Errorf("the Run is %s: the test is of a directive failing while it works", running)
	}
}
