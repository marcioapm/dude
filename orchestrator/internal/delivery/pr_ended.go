package delivery

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A pull request merged or closed on GitHub ends the delivery from whatever
// step it is at once a pull request exists. Every step that parks while the
// delivery has one also waits on pr.ended (awaitEnded), and a step woken by
// it first ends the loop when the recorded states say it is over (endable).
// awaitPullRequest reads the end from pr.feedback, as before.

// endable wraps a delivery step: woken by pr.ended, it ends the loop
// (heldPullRequestEnded) instead of running, when that end is over.
func (w *steps) endable(step workflow.Step) workflow.Step {
	return func(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
		if !prEnded(sc) {
			return step(ctx, sc)
		}
		st, err := load(sc)
		if err != nil {
			return workflow.Result{}, err
		}
		if e := st.Escalation; e != nil && e.Reason == "pull_request_closed" {
			// The person is asked, or has just decided, about these ends:
			// only a further one, which changes what they may choose, asks again.
			states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
			if err != nil {
				return workflow.Result{}, err
			}
			if e.counts() == countStates(states) {
				return step(ctx, sc)
			}
		}
		res, ended, err := w.heldPullRequestEnded(ctx, sc, st)
		if err != nil || ended {
			return res, err
		}
		return step(ctx, sc)
	}
}

// prEnded says whether the signals carry pr.ended.
func prEnded(sc workflow.StepContext) bool {
	return slices.ContainsFunc(sc.Signals, func(s workflow.Signal) bool { return s.Name == SignalPullRequestEnded })
}

// awaitEnded is the definition's Recheck: recheck's, then pr.ended added to
// any signals a step parks on while the delivery has a pull request, the
// conductor's decision recheck parks on included.
func (w *steps) awaitEnded(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, res workflow.Result) (workflow.Result, bool, error) {
	next, changed, err := w.recheck(ctx, tx, sc, res)
	if err != nil {
		return next, changed, err
	}
	if !changed {
		next = res
	}
	if len(next.AwaitSignals) == 0 || slices.Contains(next.AwaitSignals, SignalPullRequestEnded) {
		return next, changed, nil
	}
	st, ok := next.State.(*State)
	if !ok {
		// A result that keeps the state: the one the step claimed.
		if st, err = load(sc); err != nil {
			return next, changed, err
		}
	}
	if len(st.PullRequestIDs) == 0 {
		return next, changed, nil
	}
	next.AwaitSignals = append(slices.Clip(next.AwaitSignals), SignalPullRequestEnded)
	return next, true, nil
}

type prCounts struct{ open, merged, closed int }

func countStates(states []PullRequestState) prCounts {
	var c prCounts
	for _, p := range states {
		switch p.State {
		case forge.StateMerged:
			c.merged++
		case forge.StateClosed:
			c.closed++
		default:
			c.open++
		}
	}
	return c
}

// counts reads a pull_request_closed escalation's detail as pullRequestEnded wrote it.
func (e *Escalation) counts() prCounts {
	open, _ := e.detail("open")
	merged, _ := e.detail("merged")
	closed, _ := e.detail("closed")
	return prCounts{int(open), int(merged), int(closed)}
}

// heldPullRequestEnded ends the delivery (endLoop) when its pull requests'
// recorded states say the loop is over: none open, or one closed (a
// person's decision). Merged ones beside open ones are not an end: the rest
// are still to be reviewed.
func (w *steps) heldPullRequestEnded(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, bool, error) {
	if len(st.PullRequestIDs) == 0 {
		return workflow.Result{}, false, nil
	}
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return workflow.Result{}, false, err
	}
	if c := countStates(states); c.open > 0 && c.closed == 0 {
		return workflow.Result{}, false, nil
	}
	return w.endLoop(ctx, sc, st, states)
}

// endedBeforeOpening ends the delivery (endLoop) when none of its pull
// requests is open as recorded, whether or not pr.ended was ever sent: no
// pull request is opened and no fix promoted past the loop's end. A mixed
// end a person chose to wait on still has one open, so it is not asked again.
func (w *steps) endedBeforeOpening(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, bool, error) {
	if len(st.PullRequestIDs) == 0 {
		return workflow.Result{}, false, nil
	}
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil || countStates(states).open > 0 {
		return workflow.Result{}, false, err
	}
	res, _, err := w.endLoop(ctx, sc, st, states)
	return res, true, err
}

// endLoop ends the pull request loop wherever the delivery stands: the
// task's live phase Runs aborted (a candidate abandoned with them), a
// decision pending for the conductor dropped, an escalation waiting on a
// person withdrawn; then the outcome is pullRequestEnded's.
func (w *steps) endLoop(ctx context.Context, sc workflow.StepContext, st *State, states []PullRequestState) (workflow.Result, bool, error) {
	c := countStates(states)
	why, action := "the pull request was merged", "done"
	if c.closed > 0 {
		why, action = "the pull request was closed", "stop"
	}
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		// The delivery's row and the task's before the Runs, as Stop takes
		// them; the escalation is read under that lock, so a person's
		// decision committed meanwhile is not withdrawn after it.
		_, d, err := LockEscalationTx(ctx, tx, st.TaskID)
		if err != nil {
			return err
		}
		if _, err := StopPhaseRunsTx(ctx, tx, sc.OrganizationID, st.ProjectID, st.TaskID, "", why, false); err != nil {
			return err
		}
		if st.Decision != nil {
			// Not yet told, the conductor is not asked for a decision nobody waits on.
			if _, err := tx.Exec(ctx, `UPDATE conductor_wakes SET delivered_at = now()
				WHERE task_id = $1 AND key = $2 AND delivered_at IS NULL`,
				st.TaskID, fmt.Sprintf("%s:decision:%d", sc.WorkflowRunID, st.Decisions)); err != nil {
				return err
			}
		}
		if d != nil && d.WorkflowID == sc.WorkflowRunID && d.State.Escalation != nil && d.State.Escalation.Decided == nil {
			return withdrawEscalationTx(ctx, tx, sc, st, d.State.Escalation, action, why)
		}
		return nil
	}); err != nil {
		return workflow.Result{}, true, err
	}
	if st.Decision != nil {
		// Each decision's wake is its own: a later one must not reuse this key.
		st.Decision = nil
		st.Decisions++
	}
	st.Escalation, st.Directed, st.Candidate, st.Integration = nil, nil, nil, nil
	st.PendingRunIDs, st.ReviewRunIDs, st.PRFeedback, st.QuestionsHeld = nil, nil, nil, false
	res, finished, err := w.pullRequestEnded(ctx, sc, st)
	if res.State == nil {
		// Ended here, the delivery keeps nothing it was waiting on.
		res.State = st
	}
	return res, finished, err
}

// withdrawEscalationTx records that the escalation waits on nobody now, as
// ChecksFinishedTx records one the checks settled: decided by the system
// (task.decided, which ends the banner), the conductor's questions about it
// closed, a failed Run kept to resume let go.
func withdrawEscalationTx(ctx context.Context, tx pgx.Tx, sc workflow.StepContext, st *State, e *Escalation, action, why string) error {
	if err := closeEscalationQuestionsTx(ctx, tx, sc.OrganizationID, st.ProjectID, st.TaskID,
		EscalationKey(sc.WorkflowRunID, st.Escalations),
		EscalationDecision{Action: action, ActorType: ledger.ActorSystem, ActorID: "forge"}); err != nil {
		return err
	}
	if err := ReleaseKeptTx(ctx, tx, st.TaskID); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvTaskDecided, OrganizationID: sc.OrganizationID, ProjectID: st.ProjectID,
		TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "forge", Source: ledger.SourceOrchestrator,
		CorrelationID: st.TaskID, Payload: map[string]any{"reason": e.Reason, "action": action,
			"note": "Withdrawn: " + why + " on GitHub, which ends the delivery."}})
	return err
}
