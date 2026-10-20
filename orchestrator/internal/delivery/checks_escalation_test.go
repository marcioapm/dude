package delivery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

func TestChecksEscalationSurvivesLeasedWaitTransition(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_checks',$1,'Checks','checks','CK')`,
		`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_checks',$1,'prj_checks',1,'Checks','Checks','running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_checks',$1,'prj_checks','wi_checks',1,'implement','running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	st := &State{TaskID: "wi_checks", ProjectID: "prj_checks", PendingRunIDs: []string{"run_checks"}, Attempt: 1}
	raw, _ := json.Marshal(st)
	if _, err := owner.Exec(ctx, `INSERT INTO workflow_runs(id,organization_id,workflow_type,idempotency_key,task_id,status,step,state) VALUES('wf_checks',$1,$2,'checks','wi_checks','running','awaitImplement',$3)`, org, WorkflowType, raw); err != nil {
		t.Fatal(err)
	}
	rt := workflow.New(app, "checks-poller", slog.New(slog.NewTextHandler(io.Discard, nil)))
	def := Workflow(&Store{DB: app}, nil)
	def.Steps["awaitImplement"] = func(_ context.Context, sc workflow.StepContext) (workflow.Result, error) {
		old, err := load(sc)
		if err != nil {
			return workflow.Result{}, err
		}
		return park("awaitImplement", old, old.PendingRunIDs), nil
	}
	calls := 0
	def.BeforeCommit = func(id, step, next string) {
		calls++
		var leased bool
		if err := owner.QueryRow(ctx, `SELECT locked_by='checks-poller' AND locked_until>now() FROM workflow_runs WHERE id=$1`, id).Scan(&leased); err != nil {
			t.Fatal(err)
		}
		if !leased {
			t.Fatal("test did not intercept a leased transition")
		}
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			return EscalateChecksTx(ctx, tx, org, "wi_checks", "run_checks", "verify was not backed", 2)
		}); err != nil {
			t.Fatal(err)
		}
	}
	rt.Register(def)
	if _, err := rt.Tick(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("intercepted %d transitions", calls)
	}
	got, err := rt.Get(ctx, org, "wf_checks")
	if err != nil {
		t.Fatal(err)
	}
	var after State
	if err := json.Unmarshal(got.State, &after); err != nil {
		t.Fatal(err)
	}
	if got.Step != "decide" || got.Status != "waiting" || after.Escalation == nil || after.Escalation.Reason != ReasonChecksUnverified || after.Escalation.At != "awaitImplement" {
		t.Fatalf("transition lost escalation: %+v state %+v", got, after)
	}
	var status string
	if err := owner.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id='wi_checks'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "awaiting_input" {
		t.Fatalf("task status %s", status)
	}
}
