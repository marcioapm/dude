package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

func TestPendingEscalationTransitionPreservesCommittedChanges(t *testing.T) {
	for _, expired := range []bool{false, true} {
		for _, actor := range []string{"owner", "conductor", "ask_person"} {
			t.Run(fmt.Sprintf("expired=%v/%s", expired, actor), func(t *testing.T) {
				w := newGoldenWorld(t)
				ctx := context.Background()
				w.exec(t, `INSERT INTO task_people(task_id,person_id,organization_id,position) VALUES('wi_g','per_ana',$1,0)`)
				w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind) VALUES('run_cond1',$1,'prj_g','wi_g',1,'running','conductor','agent')`)
				at := time.Now().UTC()
				if expired {
					at = at.Add(-time.Hour)
				}
				w.delivery(t, "decide", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","decider":"conductor","escalation":{"reason":"implement_failed","step":"implement","holder":"conductor","raisedAt":%q}}`, at.Format(time.RFC3339Nano)))
				w.exec(t, `UPDATE workflow_runs SET wake_at=now() WHERE id='wfr_g'`)
				rt := workflow.New(w.app, "checkpoint-test", slog.New(slog.NewTextHandler(io.Discard, nil)))
				def := Workflow(&Store{DB: w.app}, nil)
				calls := 0
				def.BeforeCommit = func(id, step, next string) {
					calls++
					w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
						switch actor {
						case "owner":
							return "", DecideEscalationTx(ctx, tx, w.org, "wi_g", EscalationDecision{Action: "stop", Person: "per_ana", ActorType: "person", ActorID: "per_ana"})
						case "conductor":
							_, err := ConductDecideEscalation(ctx, tx, RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond1"}, "stop", "Stop this attempt.")
							return "", err
						default:
							_, _, err := EscalationQuestion(ctx, tx, RunRef{Org: w.org, TaskID: "wi_g", RunID: "run_cond1"}, []QuestionItem{{Question: "Stop this attempt?", Choices: []Choice{{Label: "Stop", Recommended: true}, {Label: "Retry"}}}}, []string{"stop", "retry"})
							return "", err
						}
					})
				}
				rt.Register(def)
				if _, err := rt.Tick(ctx, 1); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatalf("hook calls=%d", calls)
				}
				got, err := rt.Get(ctx, w.org, "wfr_g")
				if err != nil {
					t.Fatal(err)
				}
				var after State
				if err := json.Unmarshal(got.State, &after); err != nil {
					t.Fatal(err)
				}
				if actor == "ask_person" {
					if after.Escalation.HeldBy() != HolderPerson {
						t.Fatalf("lost holder/wake: %+v %+v", after.Escalation, got)
					}
				} else if after.Escalation.Decided == nil || after.Escalation.Decided.Action != "stop" {
					t.Fatalf("committed %s stop erased: %s", actor, got.State)
				}
				var wake *time.Time
				if err := w.owner.QueryRow(ctx, `SELECT wake_at FROM workflow_runs WHERE id='wfr_g'`).Scan(&wake); err != nil {
					t.Fatal(err)
				}
				if wake != nil {
					t.Fatalf("stale deadline restored: %v", wake)
				}
				def.BeforeCommit = nil
				if _, err := rt.Tick(ctx, 1); err != nil {
					t.Fatal(err)
				}
				var decisions, questions, asks int
				if err := w.owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM events WHERE task_id='wi_g' AND event_type='task.decided'), (SELECT count(*) FROM questions WHERE task_id='wi_g'), (SELECT count(*) FROM events WHERE task_id='wi_g' AND event_type='question.asked')`).Scan(&decisions, &questions, &asks); err != nil {
					t.Fatal(err)
				}
				wantDecisions, wantQuestions := 1, 0
				if actor == "ask_person" && !expired {
					wantDecisions, wantQuestions = 0, 0
				}
				if actor == "ask_person" && expired {
					wantDecisions, wantQuestions = 0, 0
				}
				wantAsks := 0
				if expired {
					wantAsks = 1
				}
				if asks != wantAsks {
					t.Fatalf("extra person asks=%d want=%d", asks, wantAsks)
				}
				if actor != "ask_person" {
					var status string
					if err := w.owner.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id='wi_g'`).Scan(&status); err != nil {
						t.Fatal(err)
					}
					if status != "aborted" {
						t.Fatalf("stop not carried out: %s", status)
					}
				}
				if decisions != wantDecisions || questions != wantQuestions {
					t.Fatalf("duplicate/orphan effects: decisions=%d questions=%d", decisions, questions)
				}
			})
		}
	}
}

func TestFailedEscalationWakeBatchFairness(t *testing.T) {
	w := newGoldenWorld(t)
	ctx := context.Background()
	w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind) VALUES('run_cond1',$1,'prj_g','wi_g',1,'failed','conductor','agent')`)
	w.delivery(t, "decide", `{"taskId":"wi_g","projectId":"prj_g","decider":"conductor","escalation":{"reason":"implement_failed","holder":"conductor","step":"implement","raisedAt":"2026-01-01T00:00:00Z"}}`)
	w.exec(t, `UPDATE workflow_runs SET wake_at=now()+interval '1 hour' WHERE id='wfr_g'`)
	w.exec(t, `INSERT INTO conductor_wakes(id,organization_id,task_id,kind,key,line,created_at) VALUES('cw_g',$1,'wi_g','escalation','test','Decision waits',now()-interval '1 hour')`)
	eligible := func() bool {
		var yes bool
		if err := w.owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conductor_wakes c WHERE c.task_id='wi_g' AND `+Wakeable+`)`, 0).Scan(&yes); err != nil {
			t.Fatal(err)
		}
		return yes
	}
	if !eligible() {
		t.Fatal("conductor handover not eligible")
	}
	for range 2 {
		w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			return WakeConductorTx(ctx, tx, w.org, "wi_g", 0)
		})
	}
	var scheduled time.Time
	if err := w.owner.QueryRow(ctx, `SELECT wake_at FROM workflow_runs WHERE id='wfr_g'`).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if eligible() {
		t.Fatal("already scheduled handover remains eligible")
	}
	w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		return WakeConductorTx(ctx, tx, w.org, "wi_g", 0)
	})
	var again time.Time
	if err := w.owner.QueryRow(ctx, `SELECT wake_at FROM workflow_runs WHERE id='wfr_g'`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if !again.Equal(scheduled) {
		t.Fatal("due handover wake rewritten")
	}
	var raw []byte
	if err := w.owner.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id='wfr_g'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	s := &steps{s: &Store{DB: w.app}}
	if _, err := s.decide(ctx, workflow.StepContext{OrganizationID: w.org, WorkflowRunID: "wfr_g", Step: "decide", State: raw}); err != nil {
		t.Fatal(err)
	}
	if eligible() {
		t.Error("person handover remains eligible")
	}
	w.exec(t, `INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status)
		SELECT 'wi_block'||n,$1,'prj_g',100+n,'Blocked','Blocked','awaiting_input' FROM generate_series(1,199) n`)
	w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind)
		SELECT 'run_block'||n,$1,'prj_g','wi_block'||n,1,'failed','conductor','agent' FROM generate_series(1,199) n`)
	w.exec(t, `INSERT INTO workflow_runs(id,organization_id,workflow_type,idempotency_key,task_id,status,step,state)
		SELECT 'wf_block'||n,$1,'task.delivery','block'||n,'wi_block'||n,'waiting','decide',jsonb_build_object('taskId','wi_block'||n,'decider','conductor','escalation','{"reason":"implement_failed","holder":"person"}'::jsonb) FROM generate_series(1,199) n`)
	w.exec(t, `INSERT INTO conductor_wakes(id,organization_id,task_id,kind,key,line,created_at)
		SELECT 'cw_block'||n,$1,'wi_block'||n,'escalation','test','Decision waits',now()-interval '1 hour' FROM generate_series(1,199) n`)
	w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind,turn_done_at) VALUES('run_healthy',$1,'prj_g','wi_p1',1,'running','conductor','agent',now())`)
	w.exec(t, `INSERT INTO conductor_wakes(id,organization_id,task_id,kind,key,line,created_at) VALUES('cw_healthy',$1,'wi_p1','escalation','test','Healthy news',now()-interval '1 minute')`)
	for sweep := 0; sweep < 2; sweep++ {
		rows, err := w.owner.Query(ctx, `SELECT c.task_id FROM conductor_wakes c WHERE `+Wakeable+` GROUP BY c.task_id ORDER BY min(c.created_at) LIMIT 200`, 0)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		healthy := false
		for _, id := range ids {
			if id == "wi_p1" {
				healthy = true
			}
			w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) { return WakeConductorTx(ctx, tx, w.org, id, 0) })
		}
		if sweep == 0 && !healthy {
			t.Errorf("full batch starved healthy task: %d selected", len(ids))
		}
		if sweep == 1 && len(ids) != 0 {
			t.Errorf("settled healthy wake still selected or blocked batch: %d", len(ids))
		}
	}
	var pending, asked int
	if err := w.owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM conductor_wakes WHERE delivered_at IS NULL AND task_id<>'wi_p1'),(SELECT count(*) FROM events WHERE task_id='wi_g' AND event_type='question.asked')`).Scan(&pending, &asked); err != nil {
		t.Fatal(err)
	}
	if pending != 200 || asked != 1 {
		t.Fatalf("lost news or duplicate handover: pending=%d asked=%d", pending, asked)
	}
	w.exec(t, `UPDATE runs SET status='running',turn_done_at=now() WHERE id='run_cond1'`)
	if !eligible() {
		t.Fatal("recovered conductor not eligible")
	}
}

func TestFailedEscalationBriefingWakesHandoverInsteadOfRestarting(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind,ended_at)
		VALUES('run_cond1',$1,'prj_g','wi_g',1,'failed','conductor','agent',now())`)
	w.delivery(t, "decide", `{"taskId":"wi_g","projectId":"prj_g","decider":"conductor","escalation":{"reason":"implement_failed","holder":"conductor","raisedAt":"2026-01-01T00:00:00Z","step":"implement"}}`)
	w.exec(t, `UPDATE workflow_runs SET wake_at=now()+interval '10 minutes' WHERE id='wfr_g'`)
	for range 2 {
		w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			return WakeConductorTx(ctx, tx, w.org, "wi_g", 0)
		})
	}
	var due bool
	var conductors int
	if err := w.owner.QueryRow(context.Background(), `SELECT wake_at<=now(),
		(SELECT count(*) FROM runs WHERE task_id='wi_g' AND role='conductor')
		FROM workflow_runs WHERE id='wfr_g'`).Scan(&due, &conductors); err != nil {
		t.Fatal(err)
	}
	if !due || conductors != 1 {
		t.Fatalf("failed briefing: due=%v conductors=%d", due, conductors)
	}
}

func TestConductorFirstEscalationRoutes(t *testing.T) {
	reasons := []string{"implement_failed", "fix_failed", "test_failed", "pr_fix_failed", "review_failed", "no_changes", "stuck", "exhausted", ReasonNotConverging, ReasonTestFailedCriteria, ReasonTestBlocked, ReasonChecksUnverified, "pull_request_conflict", "ci_stuck", ReasonPullRequestMoved, ReasonStoppedMidPromotion, "pull_request_closed", "pr_loop_exhausted", "source_gone", "source_changed", ReasonChoiceUnresolved, ReasonConductorUnavailable}
	for _, mode := range []string{"available", "policy", "no_model", "failed"} {
		for _, reason := range reasons {
			t.Run(mode+"/"+reason, func(t *testing.T) {
				w := newGoldenWorld(t)
				decider, voice := DeciderConductor, ""
				if mode == "policy" {
					decider = DeciderPolicy
				}
				if mode == "no_model" {
					voice = VoiceQuiet
				}
				w.delivery(t, "awaitFix", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":%q,"voice":%q}`, decider, voice))
				if mode == "failed" {
					w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind) VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'failed', 'conductor', 'agent')`)
				}
				st := &State{TaskID: "wi_g", ProjectID: "prj_g", Attempt: 1, Decider: decider, Voice: voice}
				s := &steps{s: &Store{DB: w.app}}
				res, err := s.escalate(context.Background(), workflow.StepContext{OrganizationID: w.org, WorkflowRunID: "wfr_g", Step: "awaitFix"}, st, reason, map[string]any{"message": "original failure", "rounds": 7})
				if err != nil {
					t.Fatal(err)
				}
				conductor := mode == "available" && reason != ReasonConductorUnavailable
				wantHolder, wantStatus, wantAsked, wantWake := HolderPerson, "awaiting_input", 1, 0
				if conductor {
					wantHolder, wantStatus, wantAsked, wantWake = HolderConductor, "running", 0, 1
				} else if decider == DeciderConductor {
					wantWake = 1
				}
				if st.Escalation.HeldBy() != wantHolder {
					t.Errorf("holder=%s want %s", st.Escalation.HeldBy(), wantHolder)
				}
				if conductor && res.SleepUntil.IsZero() {
					t.Error("missing conductor deadline")
				}
				var status string
				var asked, wakes, open int
				if err := w.owner.QueryRow(context.Background(), `SELECT status::text, (SELECT count(*) FROM events WHERE task_id=t.id AND event_type='question.asked'), (SELECT count(*) FROM conductor_wakes WHERE task_id=t.id), (SELECT count(*) FROM questions WHERE task_id=t.id AND status='open') FROM tasks t WHERE id='wi_g'`).Scan(&status, &asked, &wakes, &open); err != nil {
					t.Fatal(err)
				}
				if status != wantStatus || asked != wantAsked || wakes != wantWake || open != 0 {
					t.Errorf("status=%s asked=%d wakes=%d open=%d; want %s/%d/%d/0", status, asked, wakes, open, wantStatus, wantAsked, wantWake)
				}
				if conductor {
					var payload struct {
						Reason, Holder string
						Detail         map[string]any
					}
					var raw []byte
					if err := w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE task_id='wi_g' AND event_type='escalation.raised'`).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Fatal(err)
					}
					if payload.Reason != reason || payload.Holder != HolderConductor || payload.Detail["message"] != "original failure" {
						t.Fatalf("lost details: %s", raw)
					}
				}
			})
		}
	}
}

func TestConductorDecidesWithoutOwnerAnswer(t *testing.T) {
	for _, c := range []struct{ reason, action string }{{"implement_failed", "retry"}, {"implement_failed", "stop"}, {"pull_request_conflict", "wait"}, {"stuck", "retry"}, {"exhausted", "retry"}, {ReasonNotConverging, "retry"}, {"stuck", "accept"}, {ReasonNotConverging, "amend"}, {"review_failed", "accept"}, {"pr_loop_exhausted", "retry"}} {
		t.Run(c.reason+"/"+c.action, func(t *testing.T) {
			w := newGoldenWorld(t)
			w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind) VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
			w.delivery(t, "decide", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor","escalation":{"reason":%q,"holder":"conductor","step":"fix"}}`, c.reason))
			// Two earlier explicit retries are not a human-only boundary.
			w.exec(t, `INSERT INTO events (id,organization_id,event_type,task_id,actor_type,actor_id,source,payload) VALUES ('ev_retry1',$1,'task.decided','wi_g','agent','run_cond1','test',jsonb_build_object('reason',$2::text,'action','retry','by','conductor')), ('ev_retry2',$1,'task.decided','wi_g','agent','run_cond1','test',jsonb_build_object('reason',$2::text,'action','retry','by','conductor'))`, c.reason)
			err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				_, err := ConductDecideEscalation(context.Background(), tx, RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond1"}, c.action, "Split the parser from the fix.")
				return err
			})
			if PersonOnly(c.reason, c.action) {
				if err == nil {
					t.Fatal("person-only decision accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var by, note, said string
			if err := w.owner.QueryRow(context.Background(), `SELECT payload->>'by',payload->>'note' FROM events WHERE task_id='wi_g' AND event_type='task.decided' ORDER BY cursor DESC LIMIT 1`).Scan(&by, &note); err != nil {
				t.Fatal(err)
			}
			if err := w.owner.QueryRow(context.Background(), `SELECT state->'escalation'->'decided'->>'said' FROM workflow_runs WHERE id='wfr_g'`).Scan(&said); err != nil {
				t.Fatal(err)
			}
			var personRecords int
			if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM questions WHERE task_id='wi_g'`).Scan(&personRecords); err != nil || personRecords != 0 {
				t.Fatalf("fabricated person decision records=%d err=%v", personRecords, err)
			}
			if by != "conductor" || note != "Split the parser from the fix." || said != "Your conductor said:\nSplit the parser from the fix." {
				t.Fatalf("audit by=%s note=%q said=%q", by, note, said)
			}
		})
	}
}

func TestRetryingEscalationDeadlineRechecksAndHandsOverOnce(t *testing.T) {
	for _, terminal := range []string{"completed", "paused", "failed", "failed_early", "abort", "no_model", "no_model_early", "retry_failed", "stale_count"} {
		t.Run(terminal, func(t *testing.T) {
			w := newGoldenWorld(t)
			ctx := context.Background()
			w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind,harness_state)
				VALUES('run_cond1',$1,'prj_g','wi_g',1,'running','conductor','agent','{"turnRetries":1}')`)
			w.exec(t, `INSERT INTO directives(id,organization_id,task_id,run_id,text,scope,deliver_after)
				VALUES('dir_retry',$1,'wi_g','run_cond1','Retry temporary failure','turn',now()+interval '1 hour')`)
			at := time.Now().UTC().Add(-ConductorAnswerWait - time.Minute)
			w.delivery(t, "decide", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","decider":"conductor","escalation":{"reason":"implement_failed","step":"implement","holder":"conductor","raisedAt":%q}}`, at.Format(time.RFC3339Nano)))
			rt := workflow.New(w.app, "retry-deadline-test", slog.New(slog.NewTextHandler(io.Discard, nil)))
			rt.Register(Workflow(&Store{DB: w.app}, nil))
			check := func(held bool) {
				t.Helper()
				w.exec(t, `UPDATE workflow_runs SET wake_at=now() WHERE id='wfr_g'`)
				before := time.Now().UTC()
				if _, err := rt.Tick(ctx, 1); err != nil {
					t.Fatal(err)
				}
				var holder, status string
				var wake *time.Time
				var asked int
				if err := w.owner.QueryRow(ctx, `SELECT state->'escalation'->>'holder',wake_at,
					(SELECT status::text FROM tasks WHERE id='wi_g'),
					(SELECT count(*) FROM events WHERE task_id='wi_g' AND event_type='question.asked')
					FROM workflow_runs WHERE id='wfr_g'`).Scan(&holder, &wake, &status, &asked); err != nil {
					t.Fatal(err)
				}
				if held {
					if holder != HolderConductor || status != "running" || asked != 0 {
						t.Fatalf("retry handed to person: holder=%s status=%s asked=%d", holder, status, asked)
					}
					if wake == nil || wake.Before(before.Add(29*time.Second)) || wake.After(time.Now().Add(31*time.Second)) {
						t.Fatalf("retry wake=%v; want bounded 30-second recheck", wake)
					}
				} else if holder != HolderPerson || status != "awaiting_input" || asked != 1 || wake != nil {
					t.Fatalf("handover holder=%s status=%s asked=%d wake=%v", holder, status, asked, wake)
				}
			}
			check(true) // queued backoff beyond the original deadline
			w.exec(t, `UPDATE directives SET sent_at=now() WHERE id='dir_retry'`)
			check(true) // sent, before the harness's busy notification
			w.exec(t, `UPDATE runs SET agent_busy_at=now(),turn_done_at=NULL WHERE id='run_cond1'`)
			w.exec(t, `UPDATE directives SET delivered_at=now() WHERE id='dir_retry'`)
			check(true) // delivered and busy still legitimately retrying
			if terminal == "failed_early" || terminal == "no_model_early" {
				w.exec(t, `UPDATE workflow_runs SET state=jsonb_set(state,'{escalation,raisedAt}',to_jsonb(now())) WHERE id='wfr_g'`)
			}
			switch terminal {
			case "completed":
				w.exec(t, `UPDATE runs SET harness_state='{}',turn_done_at=now(),agent_busy_at=NULL WHERE id='run_cond1'`)
			case "paused":
				w.exec(t, `UPDATE runs SET status='paused' WHERE id='run_cond1'`)
			case "failed", "failed_early":
				w.exec(t, `UPDATE runs SET status='failed',ended_at=now() WHERE id='run_cond1'`)
			case "abort":
				w.exec(t, `UPDATE runs SET control='abort' WHERE id='run_cond1'`)
			case "no_model", "no_model_early":
				w.exec(t, `UPDATE workflow_runs SET state=state||'{"voice":"quiet"}' WHERE id='wfr_g'`)
			case "retry_failed":
				w.exec(t, `UPDATE directives SET failed_at=now() WHERE id='dir_retry'`)
			case "stale_count":
				w.exec(t, `DELETE FROM directives WHERE id='dir_retry'`)
			}
			check(false)
			check(false)
		})
	}
}

func TestConductorEscalationDeadlineHandsOverOnce(t *testing.T) {
	for _, mode := range []string{"deadline", "failed", "no_model", "pending"} {
		t.Run(mode, func(t *testing.T) {
			w := newGoldenWorld(t)
			at := time.Now().Add(-time.Hour).UTC()
			if mode != "deadline" {
				at = time.Now().Add(-time.Second).UTC()
			}
			if mode == "failed" {
				w.exec(t, `INSERT INTO runs (id,organization_id,project_id,task_id,attempt,status,role,kind,ended_at) VALUES ('run_cond1',$1,'prj_g','wi_g',1,'failed','conductor','agent',now())`)
			}
			st := &State{TaskID: "wi_g", ProjectID: "prj_g", Decider: DeciderConductor, Escalation: &Escalation{Reason: "stuck", Step: "fix", Holder: HolderConductor, RaisedAt: &at, Detail: map[string]any{"message": "kept"}}}
			if mode == "no_model" {
				st.Voice = VoiceQuiet
			}
			raw, _ := json.Marshal(st)
			w.delivery(t, "decide", string(raw))
			s := &steps{s: &Store{DB: w.app}}
			for range 2 {
				res, err := s.decide(context.Background(), workflow.StepContext{OrganizationID: w.org, WorkflowRunID: "wfr_g", Step: "decide", State: raw})
				if err != nil {
					t.Fatal(err)
				}
				st = res.State.(*State)
				raw, _ = json.Marshal(st)
				if mode == "pending" {
					if st.Escalation.HeldBy() != HolderConductor || !res.SleepUntil.Equal(at.Add(ConductorAnswerWait)) {
						t.Fatalf("pending wait lost: %+v", res)
					}
					continue
				}
				if st.Escalation.HeldBy() != HolderPerson {
					t.Fatal("not handed over")
				}
			}
			var n int
			want := 1
			if mode == "pending" {
				want = 0
			}
			if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE task_id='wi_g' AND event_type='question.asked'`).Scan(&n); err != nil || n != want {
				t.Fatalf("handover count=%d err=%v", n, err)
			}
		})
	}
}

func TestConductorChecksEscalationAndRecoverableStop(t *testing.T) {
	for _, action := range []string{"retry", "stop"} {
		t.Run(action, func(t *testing.T) {
			h := newHeldTask(t)
			h.exec(`UPDATE workflow_runs SET state=state||'{"decider":"conductor"}' WHERE id='wf_h'`)
			h.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind) VALUES('run_cond1',$1,'prj_h','wi_h',1,'running','conductor','agent')`, h.org)
			h.escalate("run_held")
			h.escalate("run_held")
			var holder, status string
			var wakes, asked int
			var deadline bool
			if err := h.owner.QueryRow(h.ctx, `SELECT state->'escalation'->>'holder',wake_at IS NOT NULL,(SELECT status::text FROM tasks WHERE id='wi_h'),(SELECT count(*) FROM conductor_wakes WHERE task_id='wi_h'),(SELECT count(*) FROM events WHERE task_id='wi_h' AND event_type='question.asked') FROM workflow_runs WHERE id='wf_h'`).Scan(&holder, &deadline, &status, &wakes, &asked); err != nil {
				t.Fatal(err)
			}
			if holder != HolderConductor || !deadline || status != "running" || wakes != 1 || asked != 0 {
				t.Fatalf("route=%s/%v/%s/%d/%d", holder, deadline, status, wakes, asked)
			}
			if err := h.app.InOrg(h.ctx, h.org, func(tx pgx.Tx) error {
				_, err := ConductDecideEscalation(h.ctx, tx, RunRef{Org: h.org, ProjectID: "prj_h", TaskID: "wi_h", RunID: "run_cond1"}, action, "Verify the baseline before rerunning.")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := h.owner.QueryRow(h.ctx, `SELECT state FROM workflow_runs WHERE id='wf_h'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			s := &steps{s: &Store{DB: h.app}}
			res, err := s.decide(h.ctx, workflow.StepContext{OrganizationID: h.org, WorkflowRunID: "wf_h", Step: "decide", State: raw})
			if err != nil {
				t.Fatal(err)
			}
			if action == "stop" {
				if res.State.(*State).Stopped == nil {
					t.Fatal("checks stop not recoverable")
				}
			} else if res.Next != "awaitImplement" {
				t.Fatalf("retry next=%s", res.Next)
			}
		})
	}
}

func TestConductorResumeWithoutOwnerAnswer(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind,phase,keep,kept_until) VALUES ('run_cond1',$1,'prj_g','wi_g',1,'running','conductor','agent',NULL,false,NULL),('run_failed',$1,'prj_g','wi_g',1,'failed','implementer','agent','implement',true,now()+interval '1 day')`)
	w.exec(t, `UPDATE runs SET lux_run_id='lux_failed',lux_stop_reason='kept' WHERE id='run_failed'`)
	w.delivery(t, "decide", `{"taskId":"wi_g","projectId":"prj_g","decider":"conductor","escalation":{"reason":"implement_failed","holder":"conductor","at":"awaitImplement","step":"implement","detail":{"runId":"run_failed"}}}`)
	w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		_, err := ConductDecideEscalation(ctx, tx, RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond1"}, "resume", "Continue from the checkpoint.")
		return "", err
	})
	var status string
	if err := w.owner.QueryRow(context.Background(), `SELECT status::text FROM runs WHERE id='run_failed'`).Scan(&status); err != nil || status != "paused" {
		t.Fatalf("resumed status=%s err=%v", status, err)
	}
}

func TestEscalationQuestionRequiresOneRecommendation(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			w := newGoldenWorld(t)
			w.exec(t, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind) VALUES('run_cond1',$1,'prj_g','wi_g',1,'running','conductor','agent')`)
			w.delivery(t, "decide", `{"taskId":"wi_g","projectId":"prj_g","decider":"conductor","escalation":{"reason":"review_failed","holder":"conductor","step":"review"}}`)
			err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				_, _, err := EscalationQuestion(context.Background(), tx, RunRef{Org: w.org, TaskID: "wi_g", RunID: "run_cond1"}, []QuestionItem{{Question: "Accept the unreviewed lens?", Choices: []Choice{{Label: "Accept", Recommended: count > 0}, {Label: "Stop", Recommended: count > 1}}}}, []string{"accept", "stop"})
				return err
			})
			if (err == nil) != (count == 1) {
				t.Fatalf("recommendations=%d err=%v", count, err)
			}
		})
	}
}

func TestOwnerOverridesEitherEscalationHolder(t *testing.T) {
	for _, holder := range []string{HolderConductor, HolderPerson} {
		t.Run(holder, func(t *testing.T) {
			w := newGoldenWorld(t)
			w.exec(t, `INSERT INTO task_people(task_id,person_id,organization_id,position) VALUES ('wi_g','per_ana',$1,0)`)
			w.delivery(t, "decide", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","decider":"conductor","escalation":{"reason":"review_failed","holder":%q,"step":"review"}}`, holder))
			if holder == HolderPerson {
				w.exec(t, `UPDATE tasks SET status='awaiting_input' WHERE id='wi_g'`)
			}
			w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
				return "", DecideEscalationTx(ctx, tx, w.org, "wi_g", EscalationDecision{Action: "accept", Note: "Owner accepts this lens unreviewed.", Person: "per_ana", ActorType: "person", ActorID: "per_ana"})
			})
			var action string
			if err := w.owner.QueryRow(context.Background(), `SELECT state->'escalation'->'decided'->>'action' FROM workflow_runs WHERE id='wfr_g'`).Scan(&action); err != nil || action != "accept" {
				t.Fatalf("override=%s err=%v", action, err)
			}
		})
	}
}
