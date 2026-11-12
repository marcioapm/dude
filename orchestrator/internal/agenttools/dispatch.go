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

func doRunWorkflow(ctx context.Context, _ Caller, checked dispatchChecked) (delivery.DispatchDone, error) {
	return conducted(delivery.DoDispatch(ctx, checked.DispatchCheck))
}

func recordRunWorkflow(ctx context.Context, tx pgx.Tx, _ Caller, checked dispatchChecked, done delivery.DispatchDone) (runWorkflowOut, error) {
	id, err := delivery.RecordDispatchTx(ctx, tx, checked.DispatchCheck, done)
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
