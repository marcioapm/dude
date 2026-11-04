package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

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
