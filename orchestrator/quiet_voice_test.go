package orchestrator_test

// The quiet voice (step 3b): a conductor that decides a delivery quietly
// takes no turn at its start, and its pull request opens by Deliver's own
// rules, through the orchestrator's real code: the API, the workflow, the
// syncer, and the fake lux and GitHub.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// startQuiet starts the task's delivery decided by its conductor, quietly,
// as the world's deliver starts one Deliver decides.
func (w *world) startQuiet(task string) string {
	w.t.Helper()
	ctx := context.Background()
	var st delivery.State
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		if err := delivery.NameOnlyRepository(ctx, tx, task); err != nil {
			return err
		}
		var err error
		st, err = delivery.NewState(ctx, tx, task, w.project, scriptedPolicy(), 1)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	st.Decider, st.HadConductor, st.Voice = delivery.DeciderConductor, true, delivery.VoiceQuiet
	id, _, err := w.runtime.Start(ctx, workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: w.org, IdempotencyKey: "delivery:" + task, TaskID: task, Input: st,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// quietly makes the task's live delivery quiet under its conductor, as a
// hand-back writes it, before its next step.
func (w *world) quietly(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = state || '{"decider":"conductor","hadConductor":true,"voice":"quiet"}'::jsonb
		WHERE task_id = $1`, task)
}

// startedQuietly says the task's implementer started with no conductor
// turn: no reason to wake one recorded, no conductor Run, and no planning
// with one.
func (w *world) startedQuietly(task string) {
	w.t.Helper()
	w.until("the implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, task) == 1
	})
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != 0 {
		w.t.Errorf("%d reasons to wake the conductor recorded at a quiet start", n)
	}
	if id, _, _ := w.conductor(task); id != "" {
		w.t.Errorf("a conductor Run %s at a quiet start", id)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionAwaited); n != 0 {
		w.t.Errorf("%d decisions awaited at a quiet start", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
		AND payload->>'reason' = 'planning with the conductor'`, task); n != 0 {
		w.t.Errorf("a quiet start said it plans with the conductor")
	}
}

// A quiet conductor takes no turn at the start: the implementer starts at
// once, as under Deliver, with no reason to wake anyone.
func TestAQuietStartRunsTheImplementerWithNoWake(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	w.startQuiet(task)
	w.startedQuietly(task)
}

// Work to continue, taken over quietly, starts its implementer at once
// too: the start is routed through the same point (adopt), whose wake
// would be settled, and so heard by nobody.
func TestAQuietContinueAdoptedStartRunsTheImplementerWithNoWake(t *testing.T) {
	w := conductorWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/half", "main", "Half done")
	wi := w.adopted("pr", n, "feature/half", "main", delivery.ModeContinue)
	w.startQuiet(wi)
	w.startedQuietly(wi)
}
