package delivery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
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

// heldTask is a task wi_h with an implementer run_held and a reviewer
// run_other, its delivery wf_h waiting on the implementer.
type heldTask struct {
	t     *testing.T
	ctx   context.Context
	app   *db.DB
	owner *pgx.Conn
	org   string
}

func newHeldTask(t *testing.T) heldTask {
	app, owner := dbtest.Open(t)
	h := heldTask{t: t, ctx: context.Background(), app: app, owner: owner, org: dbtest.Org(t, owner)}
	for _, q := range []string{
		`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_h',$1,'H','h','H')`,
		`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_h',$1,'prj_h',1,'H','H','running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_held',$1,'prj_h','wi_h',1,'implement','running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_other',$1,'prj_h','wi_h',1,'review','running')`,
		`INSERT INTO workflow_runs(id,organization_id,workflow_type,idempotency_key,task_id,status,step,state)
			VALUES('wf_h',$1,'` + WorkflowType + `','h','wi_h','waiting','awaitImplement','{"taskId":"wi_h","projectId":"prj_h","attempt":1}')`,
	} {
		h.exec(q, h.org)
	}
	return h
}

func (h heldTask) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.owner.Exec(h.ctx, q, args...); err != nil {
		h.t.Fatal(err)
	}
}

// escalate runs the checks gate's escalation for runID.
func (h heldTask) escalate(runID string) {
	h.t.Helper()
	if err := h.app.InOrg(h.ctx, h.org, func(tx pgx.Tx) error {
		return EscalateChecksTx(h.ctx, tx, h.org, "wi_h", runID, "held", 0)
	}); err != nil {
		h.t.Fatal(err)
	}
}

// decide marks the escalation decided, as a person's decision does.
func (h heldTask) decide() {
	h.t.Helper()
	h.exec(`UPDATE workflow_runs SET state = jsonb_set(state, '{escalation,decided}', '{"action":"waive"}') WHERE id = 'wf_h'`)
}

// A checks escalation is a person's ask of the held Run alone, until it is
// decided: another Run of the task (its conductor) is not asked anything.
func TestAChecksEscalationAsksAPersonOfTheHeldRunUntilDecided(t *testing.T) {
	h := newHeldTask(t)
	asked := func() (held, other bool) {
		t.Helper()
		if err := h.owner.QueryRow(h.ctx, `SELECT bool_or(r.id = 'run_held' AND `+PersonAsk+`), bool_or(r.id = 'run_other' AND `+PersonAsk+`)
			FROM runs r WHERE r.task_id = 'wi_h'`).Scan(&held, &other); err != nil {
			t.Fatal(err)
		}
		return held, other
	}
	h.escalate("run_held")
	if held, other := asked(); !held || other {
		t.Errorf("undecided: held Run asked %v, other Run asked %v; want true, false", held, other)
	}
	h.decide()
	if held, other := asked(); held || other {
		t.Errorf("decided: held Run asked %v, other Run asked %v; want neither", held, other)
	}
}

// A held Run judged unverified again while its escalation is open waits
// on the person again; a decided escalation, or one raised by another
// Run, sets no wait.
func TestAChecksEscalationRearmsTheWaitOfItsOwnUndecidedRunOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		decided bool
		again   string
		waiting bool
	}{
		{"undecided, same Run", false, "run_held", true},
		{"decided, same Run", true, "run_held", false},
		{"undecided, another Run", false, "run_other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHeldTask(t)
			h.escalate("run_held")
			if tc.decided {
				h.decide()
			}
			h.exec(`UPDATE runs SET waiting_since = NULL WHERE id = 'run_held'`)
			h.escalate(tc.again)
			var waiting bool
			if err := h.owner.QueryRow(h.ctx, `SELECT waiting_since IS NOT NULL FROM runs WHERE id = $1`, tc.again).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting != tc.waiting {
				t.Errorf("%s waiting %v, want %v", tc.again, waiting, tc.waiting)
			}
		})
	}
}

// An attempt waiver covers only the held report's reported failures: a
// not-run beside them is not carried, and a report whose only non-pass is
// not-run has nothing to carry and is refused, deciding nothing.
func TestAnAttemptWaiverCoversOnlyReportedFailures(t *testing.T) {
	for _, tc := range []struct {
		name, checks string
		covers       string
	}{
		{"a fail beside a not-run", `[{"command":"make test","result":"fail","reason":"TestClock"},{"command":"make e2e","result":"not-run","reason":"no browser"}]`,
			`[{"result": "fail", "command": "make test", "evidence": "TestClock"}]`},
		{"a not-run alone", `[{"command":"make e2e","result":"not-run","reason":"no browser"}]`, ""},
		// Not a list of verdicts: an error, never "nothing to carry".
		{"a malformed judgement", `{"command":"make test"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, owner := dbtest.Open(t)
			ctx := context.Background()
			org := dbtest.Org(t, owner)
			for _, q := range []string{
				`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_w',$1,'W','w','W')`,
				`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_w',$1,'prj_w',1,'W','W','running')`,
				`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_w',$1,'prj_w','wi_w',1,'implement','running')`,
				`INSERT INTO workflow_runs(id,organization_id,workflow_type,idempotency_key,task_id,status,step,state)
					VALUES('wf_w',$1,'` + WorkflowType + `','w','wi_w','waiting','awaitImplement','{"taskId":"wi_w","projectId":"prj_w","attempt":1}')`,
				`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload) VALUES
					('ev_w',$1,'run.checks.judged','run_w','wi_w','system','dude','test',
					jsonb_build_object('turn',0,'verified',false,'escalated',true,'message','held','checks','` + tc.checks + `'::jsonb))`,
			} {
				if _, err := owner.Exec(ctx, q, org); err != nil {
					t.Fatal(err)
				}
			}
			err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
				if err := EscalateChecksTx(ctx, tx, org, "wi_w", "run_w", "held", 0); err != nil {
					return err
				}
				return DecideEscalationTx(ctx, tx, org, "wi_w", EscalationDecision{Action: ActionWaive, Note: "Accepted.", Scope: ScopeAttempt,
					ActorType: "human", ActorID: "per_ana", Person: "per_ana"})
			})
			var covers string
			_ = owner.QueryRow(ctx, `SELECT COALESCE(max(payload->>'covers'), '') FROM events WHERE run_id = 'run_w' AND event_type = $1`,
				EvRunChecksDecided).Scan(&covers)
			if tc.name == "a malformed judgement" {
				if _, refused := err.(EscalationError); err == nil || refused || covers != "" {
					t.Fatalf("a malformed judgement: %v, covers %q", err, covers)
				}
				return
			}
			if tc.covers == "" {
				if e, ok := err.(EscalationError); !ok || e.Kind != "bad_request" || covers != "" {
					t.Fatalf("a not-run alone: %v, covers %q", err, covers)
				}
				return
			}
			if err != nil || covers != tc.covers {
				t.Fatalf("%v, covers %s", err, covers)
			}
		})
	}
}
