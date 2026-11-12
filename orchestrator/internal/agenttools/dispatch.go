package agenttools

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// dispatchesPerRun bounds the workflows one conductor Run dispatches.
const dispatchesPerRun = 10

type runWorkflowIn struct {
	Repository string            `json:"repository,omitempty"`
	Workflow   string            `json:"workflow"`
	Ref        string            `json:"ref,omitempty"`
	Inputs     map[string]string `json:"inputs,omitempty"`
	QuestionID string            `json:"questionId,omitempty"`
}

type runWorkflowOut struct {
	DispatchID string `json:"dispatchId"`
	Repository string `json:"repository"`
	Workflow   string `json:"workflow"`
	Ref        string `json:"ref"`
	Next       string `json:"next"`
}

var runWorkflowResult = delivery.MustText("results/run-workflow")

type dispatchChecked struct {
	delivery.DispatchCheck
}

func checkRunWorkflow(ctx context.Context, tx pgx.Tx, c Caller, in runWorkflowIn) (dispatchChecked, error) {
	gh, err := githubOf(c)
	if err != nil {
		return dispatchChecked{}, err
	}
	checked, err := conducted(delivery.CheckDispatchTx(ctx, tx, c.run(), gh, delivery.DispatchAsk{Repo: in.Repository,
		Workflow: in.Workflow, Ref: in.Ref, Inputs: in.Inputs, QuestionID: in.QuestionID}))
	return dispatchChecked{checked}, err
}

// doRunWorkflow dispatches outside any transaction. When it fails, the row
// the check reserved is ended in a transaction of its own
// (delivery.EndDispatchTx), even when the call was cut off: dude knows
// what happened, and the row does not wait for the supervisor.
func doRunWorkflow(ctx context.Context, c Caller, checked dispatchChecked) (delivery.DispatchDone, error) {
	done, err := delivery.DoDispatch(ctx, checked.DispatchCheck)
	if err == nil {
		return done, nil
	}
	ctx = context.WithoutCancel(ctx)
	answer := err
	if c.env.db != nil {
		if terr := c.env.db.InOrg(ctx, c.Org, func(tx pgx.Tx) (terr error) {
			answer, terr = delivery.EndDispatchTx(ctx, tx, checked.DispatchCheck, done, err)
			return terr
		}); terr != nil {
			return done, terr
		}
	}
	return conducted(done, answer)
}

func recordRunWorkflow(ctx context.Context, tx pgx.Tx, c Caller, checked dispatchChecked, done delivery.DispatchDone) (runWorkflowOut, error) {
	id, err := delivery.RecordDispatchTx(ctx, tx, c.Org, checked.DispatchCheck, done)
	if err != nil {
		return runWorkflowOut{}, err
	}
	repo, workflow, ref := checked.Dispatched()
	return runWorkflowOut{DispatchID: id, Repository: repo, Workflow: workflow, Ref: ref, Next: runWorkflowResult.Render(nil)}, nil
}

var dispatchTools = []tool{
	around(define("run_workflow", checkRunWorkflow).before(forgeFor).limit(dispatchesPerRun).countingAttempts(), doRunWorkflow,
		recordRunWorkflow),
}
