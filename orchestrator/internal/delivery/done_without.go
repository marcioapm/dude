package delivery

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Done without a pull request: the owner's answer to Deliver's hold before
// the pull request that the work is finished with none. The branch stays on
// GitHub in each repository, no pull request is opened, the task is Done,
// and the delivery ends with nothing left live.

// EvDoneWithoutPullRequest is the owner's Done without a pull request.
// Payload: {branch, repositories, heads}.
const EvDoneWithoutPullRequest = "task.done_without_pull_request"

// FeedbackNotHandledText is why finishing a task (Mark done, Done without
// a pull request) is refused while n of the people's feedback items are open.
func FeedbackNotHandledText(n int) string {
	return fmt.Sprintf("%d feedback items from people are not handled yet", n)
}

// DoneWithoutPullRequestTx ends a delivery held before its pull request as
// Done, its branch kept: refused unless the task's latest delivery is
// Deliver's hold before its first pull request, or once the attempt has a
// pull request on record. Who may (the owner) is the caller's to check.
func DoneWithoutPullRequestTx(ctx context.Context, tx pgx.Tx, org, taskID string, actor Actor) error {
	stopped, err := StopAllTx(ctx, tx, org, taskID, "done without a pull request", false, func(locked Stopped) error {
		d := locked.Delivery
		if d == nil || !d.Live() || !d.AtGate() || d.State.conducted() {
			return lifecycleRefuse("not_held", "the task is not held before its pull request")
		}
		opened, err := PullRequestOpenedTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if opened || len(d.State.PullRequestIDs) > 0 {
			return lifecycleRefuse("pull_request_open", PullRequestOpenText)
		}
		// As Mark done: people's feedback still open is not dropped by finishing.
		if n, err := BlockingFeedbackTx(ctx, tx, taskID, 0); err != nil {
			return err
		} else if n > 0 {
			return lifecycleRefuse("feedback_open", "%s", FeedbackNotHandledText(n))
		}
		return nil
	})
	if err != nil {
		return err
	}
	d := stopped.Delivery
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET status = 'completed', wake_at = NULL, awaiting_signals = '[]'::jsonb,
		locked_by = NULL, locked_until = NULL WHERE id = $1`, d.WorkflowID); err != nil {
		return err
	}
	// The gate's question, if one was asked on the conductor, is no longer one.
	if _, err := tx.Exec(ctx, `UPDATE questions SET status = 'cancelled' WHERE task_id = $1 AND pr_gate_heads IS NOT NULL
		AND status = 'open'`, taskID); err != nil {
		return err
	}
	if err := ReleaseKeptTx(ctx, tx, taskID); err != nil {
		return err
	}
	if err := EndWakesTx(ctx, tx, taskID); err != nil {
		return err
	}
	repos, err := TaskRepositories(ctx, tx, taskID)
	if err != nil {
		return err
	}
	var names []string
	for _, r := range repos {
		if d.State.Heads[r.Name] != "" {
			names = append(names, r.Name)
		}
	}
	if _, err := ledger.Append(ctx, tx, actor.event(org, stopped.ProjectID, taskID, EvDoneWithoutPullRequest,
		map[string]any{"branch": d.State.Branch, "repositories": names, "heads": nonNilMap(d.State.Heads)})); err != nil {
		return err
	}
	reason := "done without a pull request"
	if actor.Name != "" {
		reason += " by " + firstName(actor.Name)
	}
	moved, err := SetTaskStatusTx(ActingPerson(ctx, actor.Person), tx, org, stopped.ProjectID, taskID, "", "done", reason)
	if err != nil {
		return err
	}
	if !moved {
		return fmt.Errorf("task %s did not move to done", taskID)
	}
	_, err = tx.Exec(ctx, `UPDATE tasks SET resolution = 'nothing_to_merge' WHERE id = $1`, taskID)
	return err
}
