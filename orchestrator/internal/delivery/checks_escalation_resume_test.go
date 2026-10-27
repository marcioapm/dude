package delivery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// decisionWait is a conducted delivery wf_w of task wi_w waiting at step,
// its conductor run_cond live, and a fixer run_fix the conductor started
// itself, which the step does not wait on.
type decisionWait struct {
	t     *testing.T
	ctx   context.Context
	app   *db.DB
	owner *pgx.Conn
	org   string
	rt    *workflow.Runtime
	ref   RunRef
}

func newDecisionWait(t *testing.T, step, awaiting string, decision *Pending) decisionWait {
	t.Helper()
	app, owner := dbtest.Open(t)
	w := decisionWait{t: t, ctx: context.Background(), app: app, owner: owner, org: dbtest.Org(t, owner)}
	w.ref = RunRef{Org: w.org, ProjectID: "prj_w", TaskID: "wi_w", RunID: "run_cond"}
	st := State{TaskID: "wi_w", ProjectID: "prj_w", Attempt: 1, Decider: DeciderConductor, Policy: DefaultPolicy(),
		Decision: decision, GateRequired: decision != nil && decision.Point == PointBeforePR}
	raw, _ := json.Marshal(st)
	for _, q := range []string{
		`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_w',$1,'Acme','acme','ACME')`,
		`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_w',$1,'prj_w',1,'W','W','running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,kind,status) VALUES('run_cond',$1,'prj_w','wi_w',1,'conductor','agent','running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_fix',$1,'prj_w','wi_w',1,'fix','running')`,
	} {
		w.exec(q, w.org)
	}
	w.exec(`INSERT INTO workflow_runs(id,organization_id,workflow_type,idempotency_key,task_id,status,step,state,awaiting_signals)
		VALUES('wf_w',$1,$2,'w','wi_w','waiting',$3,$4,$5::jsonb)`, w.org, WorkflowType, step, raw, awaiting)
	w.rt = workflow.New(app, "wedge-poller", slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.rt.Register(Workflow(&Store{DB: app}, nil))
	return w
}

func (w decisionWait) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(w.ctx, q, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w decisionWait) in(fn func(tx pgx.Tx) error) error {
	return w.app.InOrg(w.ctx, w.org, fn)
}

// escalateFixer holds the conductor's fixer at the checks gate.
func (w decisionWait) escalateFixer() {
	w.t.Helper()
	if err := w.in(func(tx pgx.Tx) error {
		return EscalateChecksTx(w.ctx, tx, w.org, "wi_w", "run_fix", "verify was not backed", 2, nil)
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w decisionWait) decideEscalation(action, note string) {
	w.t.Helper()
	if err := w.in(func(tx pgx.Tx) error {
		return DecideEscalationTx(w.ctx, tx, w.org, "wi_w", EscalationDecision{Action: action, Note: note, ActorType: "person", ActorID: "per_owner"})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// settle advances the workflow until no step is runnable. After each step
// the delivery is never waiting at the conductor's decision on anything
// but its decision: there a conductor's take() signal would wait unread.
func (w decisionWait) settle() {
	w.t.Helper()
	for range 10 {
		n, err := w.rt.Tick(w.ctx, 1)
		if err != nil {
			w.t.Fatal(err)
		}
		if run, _ := w.row(); run.Step == "conductorDecision" && run.Status == "waiting" &&
			!slices.Contains(run.AwaitingSignals, SignalConductorDecision) {
			w.t.Fatalf("the delivery waits at the conductor's decision on %v", run.AwaitingSignals)
		}
		if n == 0 {
			return
		}
	}
	w.t.Fatal("the workflow kept running")
}

func (w decisionWait) row() (*workflow.Run, State) {
	w.t.Helper()
	run, err := w.rt.Get(w.ctx, w.org, "wf_w")
	if err != nil {
		w.t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(run.State, &st); err != nil {
		w.t.Fatal(err)
	}
	return run, st
}

func (w decisionWait) startReview() error {
	return w.in(func(tx pgx.Tx) error {
		_, err := ConductStartPhase(w.ctx, tx, w.ref, StartPhase{Phase: PhaseReview})
		return err
	})
}

// A checks escalation of a Run the conductor started itself, raised while
// the delivery waits at the conductor's decision, sends the delivery back
// to that decision once a person decides it: the conductor's next decision
// is carried out, and is not left unconsumed behind a wait for a phase Run
// that never finishes.
func TestADecidedChecksEscalationAtADecisionWaitsOnTheConductorAgain(t *testing.T) {
	for _, c := range []struct{ action, note string }{
		{"retry", ""},
		{ActionWaive, "The check needs hardware CI has."},
	} {
		t.Run(c.action, func(t *testing.T) {
			w := newDecisionWait(t, "conductorDecision", `["conductor.decision"]`,
				&Pending{Point: PointBeforePR, Policy: "openPullRequest"})
			w.escalateFixer()
			if run, st := w.row(); run.Step != "decide" || st.Escalation == nil || st.Escalation.At != "conductorDecision" {
				t.Fatalf("escalated at %s, escalation %+v", run.Step, st.Escalation)
			}
			w.decideEscalation(c.action, c.note)
			w.settle()
			run, _ := w.row()
			if run.Step != "conductorDecision" || run.Status != "waiting" || !slices.Contains(run.AwaitingSignals, SignalConductorDecision) {
				t.Fatalf("after the decision the delivery is %s/%s awaiting %v, want conductorDecision waiting on %s",
					run.Step, run.Status, run.AwaitingSignals, SignalConductorDecision)
			}
			w.conductorStartsAReview()
		})
	}
}

// A decision the conductor took just before a Run of its own was held at
// the checks gate is carried out once a person decides the escalation.
func TestADecisionTakenBeforeAChecksEscalationIsCarriedOutAfterIt(t *testing.T) {
	w := newDecisionWait(t, "conductorDecision", `["conductor.decision"]`,
		&Pending{Point: PointBeforePR, Policy: "openPullRequest"})
	if err := w.startReview(); err != nil {
		t.Fatalf("start_phase review: %v", err)
	}
	w.escalateFixer()
	w.settle()
	w.waits("decide", SignalHumanDecision)
	w.decideEscalation("retry", "")
	w.settle()
	run, st := w.row()
	if run.Step != "awaitReview" || st.Decision != nil {
		t.Fatalf("the decision taken before the escalation was not carried out: at %s/%s awaiting %v, decision %+v",
			run.Step, run.Status, run.AwaitingSignals, st.Decision)
	}
}

func TestAChecksEscalationRestoresEverySignalFromTheLockedWait(t *testing.T) {
	w := newDecisionWait(t, "conductorDecision", `["conductor.decision","human.feedback"]`,
		&Pending{Point: PointBeforePR, Policy: "openPullRequest"})
	w.escalateFixer()
	w.decideEscalation("retry", "")
	w.settle()
	w.waits("conductorDecision", SignalConductorDecision, SignalHumanFeedback)
	w.conductorStartsAReview()
}

// waits says the delivery waits at step on exactly the signals want, in any order.
func (w decisionWait) waits(step string, want ...string) {
	w.t.Helper()
	run, st := w.row()
	got := slices.Sorted(slices.Values(run.AwaitingSignals))
	if run.Step != step || run.Status != "waiting" || !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		w.t.Fatalf("the delivery is at %s/%s awaiting %v (escalation %+v), want %s waiting on %v",
			run.Step, run.Status, run.AwaitingSignals, st.Escalation, step, want)
	}
}

func (w decisionWait) taskStatus() string {
	w.t.Helper()
	var s string
	if err := w.owner.QueryRow(w.ctx, `SELECT status::text FROM tasks WHERE id = 'wi_w'`).Scan(&s); err != nil {
		w.t.Fatal(err)
	}
	return s
}

// A step that waits on no phase Run is waited at again on its own signals
// once a person decides a checks escalation raised there: the pull request
// wait on its pull requests and the person, the wait for a reader on their
// feedback, a gate Deliver holds on the person's answer and feedback.
func TestADecidedChecksEscalationResumesTheWaitItStopped(t *testing.T) {
	for _, c := range []struct {
		name, step, awaiting string
		state                func(*State)
		want                 []string
		status               string
	}{
		{"pull request wait", "awaitPullRequest", `["pr.feedback","human.decision","human.feedback","pr.ended"]`,
			func(st *State) { st.PullRequestIDs = []string{"pr_w"} },
			[]string{SignalPRFeedback, SignalHumanDecision, SignalHumanFeedback, SignalPullRequestEnded}, "review"},
		{"ready to read", "awaitRead", `["human.feedback"]`, func(*State) {}, []string{SignalHumanFeedback}, "review"},
		{"gate Deliver holds", "conductorDecision", `["conductor.decision","human.feedback"]`,
			func(st *State) {
				st.Decider, st.ChoiceGate, st.GateRequired = DeciderPolicy, true, true
				st.Decision = &Pending{Point: PointBeforePR, Policy: "openPullRequest"}
			},
			[]string{SignalConductorDecision, SignalHumanFeedback}, "awaiting_input"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, legacy := range []bool{false, true} {
				name := "saved signals"
				if legacy {
					name = "legacy row"
				}
				t.Run(name, func(t *testing.T) {
					w := newDecisionWait(t, c.step, c.awaiting, nil)
					if c.step == "conductorDecision" {
						// Held by the task's Ask me first, with no conductor.
						w.exec(`UPDATE tasks SET pull_request_choice = 'ask' WHERE id = 'wi_w'`)
						w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_cond'`)
					}
					_, st := w.row()
					c.state(&st)
					raw, _ := json.Marshal(st)
					w.exec(`UPDATE workflow_runs SET state = $1 WHERE id = 'wf_w'`, raw)
					w.escalateFixer()
					if legacy {
						w.exec(`UPDATE workflow_runs SET state = state #- '{escalation,atSignals}' WHERE id = 'wf_w'`)
					}
					w.decideEscalation("retry", "")
					w.settle()
					// awaitEnded adds pr.ended to waitForPR's three signals when a PR exists.
					w.waits(c.step, c.want...)
					if s := w.taskStatus(); s != c.status {
						t.Errorf("task %s, want %s", s, c.status)
					}
				})
			}
		})
	}
}

// A checks escalation of the Run a step waits on goes back to waiting on
// it, on phase.finished, with the Runs it waited on: the decision is
// carried out in one pass, which does not run the wait itself.
func TestADecidedChecksEscalationOfAnAwaitedRunWaitsOnItAgain(t *testing.T) {
	w := newDecisionWait(t, "awaitFix", `["phase.finished"]`, nil)
	w.exec(`UPDATE workflow_runs SET state = state || '{"pendingRunIds":["run_fix"]}' WHERE id = 'wf_w'`)
	w.escalateFixer()
	w.decideEscalation("retry", "")
	if _, err := w.rt.Tick(w.ctx, 1); err != nil {
		t.Fatal(err)
	}
	w.waits("awaitFix", SignalPhaseFinished)
	if _, st := w.row(); !slices.Equal(st.PendingRunIDs, []string{"run_fix"}) {
		t.Errorf("waits on %v, want run_fix", st.PendingRunIDs)
	}
}

// A checks escalation stored as every one before this change was, decided
// there: one at the conductor's decision waits on it again; one at a pull
// request fix's wait waits on its Run.
func TestAStoredChecksEscalationResumesWhereItStopped(t *testing.T) {
	for _, c := range []struct {
		at, pending string
		want        []string
	}{
		{"conductorDecision", `[]`, []string{SignalConductorDecision}},
		{"awaitPRFix", `["run_fix"]`, []string{SignalPhaseFinished}},
	} {
		t.Run(c.at, func(t *testing.T) {
			w := newDecisionWait(t, "decide", `["human.decision"]`, &Pending{Point: PointBeforePR, Policy: "openPullRequest"})
			w.exec(`UPDATE workflow_runs SET state = state || jsonb_build_object('pendingRunIds', $1::jsonb, 'escalation',
				jsonb_build_object('reason', 'checks_unverified', 'at', $2::text,
					'detail', '{"runId":"run_fix","message":"held","rounds":2}'::jsonb,
					'decided', '{"at":"2026-10-27T07:17:00Z","action":"retry","note":""}'::jsonb)) WHERE id = 'wf_w'`, c.pending, c.at)
			if err := w.in(func(tx pgx.Tx) error {
				return workflow.SignalTx(w.ctx, tx, w.org, "wf_w", SignalHumanDecision, nil, "")
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.rt.Tick(w.ctx, 1); err != nil {
				t.Fatal(err)
			}
			if c.at == "conductorDecision" {
				w.settle()
			}
			w.waits(c.at, c.want...)
			if c.at == "conductorDecision" {
				w.conductorStartsAReview()
			}
		})
	}
}

// conductorStartsAReview has the conductor take start_phase review and
// checks the workflow carried it out: the review round is created and the
// decision consumed, so a later decision is not refused as taken already.
func (w decisionWait) conductorStartsAReview() {
	t := w.t
	t.Helper()
	if err := w.startReview(); err != nil {
		t.Fatalf("start_phase review: %v", err)
	}
	w.settle()
	run, st := w.row()
	if run.Step != "awaitReview" || st.Decision != nil {
		t.Fatalf("the conductor's start_phase review was not carried out: at %s/%s awaiting %v, decision %+v",
			run.Step, run.Status, run.AwaitingSignals, st.Decision)
	}
	var reviews, unconsumed int
	if err := w.owner.QueryRow(w.ctx, `SELECT (SELECT count(*) FROM runs WHERE task_id = 'wi_w' AND phase = 'review'),
		(SELECT count(*) FROM workflow_signals WHERE workflow_run_id = 'wf_w' AND name = $1 AND consumed_at IS NULL)`,
		SignalConductorDecision).Scan(&reviews, &unconsumed); err != nil {
		t.Fatal(err)
	}
	if reviews == 0 || unconsumed != 0 {
		t.Errorf("%d review Runs created, %d conductor decisions unconsumed; want some, none", reviews, unconsumed)
	}
	if err := w.startReview(); err != nil && strings.Contains(err.Error(), "taken already") {
		t.Errorf("a later decision is refused: %v", err)
	}
}
