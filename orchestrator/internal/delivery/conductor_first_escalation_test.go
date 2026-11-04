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

func TestConductorFirstEscalationRoutes(t *testing.T) {
 reasons := []string{"implement_failed", "fix_failed", "test_failed", "pr_fix_failed", "review_failed", "no_changes", "stuck", "exhausted", ReasonNotConverging, ReasonTestFailedCriteria, ReasonTestBlocked, ReasonChecksUnverified, "pull_request_conflict", "ci_stuck", ReasonPullRequestMoved, ReasonStoppedMidPromotion, "pull_request_closed", "pr_loop_exhausted", "source_gone", "source_changed", ReasonChoiceUnresolved, ReasonConductorUnavailable}
 for _, mode := range []string{"available", "policy", "no_model", "failed"} {
  for _, reason := range reasons {
   t.Run(mode+"/"+reason, func(t *testing.T) {
    w := newGoldenWorld(t)
    decider, voice := DeciderConductor, ""
    if mode == "policy" { decider = DeciderPolicy }
    if mode == "no_model" { voice = VoiceQuiet }
    w.delivery(t, "awaitFix", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":%q,"voice":%q}`, decider, voice))
    if mode == "failed" { w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind) VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'failed', 'conductor', 'agent')`) }
    st := &State{TaskID:"wi_g", ProjectID:"prj_g", Attempt:1, Decider:decider, Voice:voice}
    s := &steps{s:&Store{DB:w.app}}
    res, err := s.escalate(context.Background(), workflow.StepContext{OrganizationID:w.org, WorkflowRunID:"wfr_g", Step:"awaitFix"}, st, reason, map[string]any{"message":"original failure", "rounds":7})
    if err != nil { t.Fatal(err) }
    conductor := mode == "available" && reason != ReasonConductorUnavailable
    wantHolder, wantStatus, wantAsked, wantWake := HolderPerson, "awaiting_input", 1, 0
    if conductor { wantHolder, wantStatus, wantAsked, wantWake = HolderConductor, "running", 0, 1 } else if decider == DeciderConductor { wantWake = 1 }
    if st.Escalation.HeldBy() != wantHolder { t.Errorf("holder=%s want %s", st.Escalation.HeldBy(), wantHolder) }
    if conductor && res.SleepUntil.IsZero() { t.Error("missing conductor deadline") }
    var status string
    var asked, wakes, open int
    if err := w.owner.QueryRow(context.Background(), `SELECT status::text, (SELECT count(*) FROM events WHERE task_id=t.id AND event_type='question.asked'), (SELECT count(*) FROM conductor_wakes WHERE task_id=t.id), (SELECT count(*) FROM questions WHERE task_id=t.id AND status='open') FROM tasks t WHERE id='wi_g'`).Scan(&status,&asked,&wakes,&open); err != nil {t.Fatal(err)}
    if status != wantStatus || asked != wantAsked || wakes != wantWake || open != 0 { t.Errorf("status=%s asked=%d wakes=%d open=%d; want %s/%d/%d/0",status,asked,wakes,open,wantStatus,wantAsked,wantWake) }
    if conductor { var payload struct {Reason,Holder string; Detail map[string]any}; var raw []byte; if err := w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE task_id='wi_g' AND event_type='escalation.raised'`).Scan(&raw); err != nil {t.Fatal(err)}; if err:=json.Unmarshal(raw,&payload);err!=nil{t.Fatal(err)};if payload.Reason!=reason || payload.Holder!=HolderConductor || payload.Detail["message"]!="original failure" {t.Fatalf("lost details: %s",raw)} }
   })
  }
 }
}

func TestConductorDecidesWithoutOwnerAnswer(t *testing.T) {
 for _, c := range []struct{reason,action string}{{"implement_failed","retry"},{"implement_failed","stop"},{"pull_request_conflict","wait"},{"stuck","retry"},{"exhausted","retry"},{ReasonNotConverging,"retry"},{"stuck","accept"},{ReasonNotConverging,"amend"},{"review_failed","accept"},{"pr_loop_exhausted","retry"}} {
  t.Run(c.reason+"/"+c.action,func(t *testing.T){
   w:=newGoldenWorld(t)
   w.exec(t,`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind) VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
   w.delivery(t,"decide",fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor","escalation":{"reason":%q,"holder":"conductor","step":"fix"}}`,c.reason))
   // Two earlier explicit retries are not a human-only boundary.
   w.exec(t,`INSERT INTO events (id,organization_id,event_type,task_id,actor_type,actor_id,source,payload) VALUES ('ev_retry1',$1,'task.decided','wi_g','agent','run_cond1','test',jsonb_build_object('reason',$2::text,'action','retry','by','conductor')), ('ev_retry2',$1,'task.decided','wi_g','agent','run_cond1','test',jsonb_build_object('reason',$2::text,'action','retry','by','conductor'))`,c.reason)
   err:=w.app.InOrg(context.Background(),w.org,func(tx pgx.Tx)error{_,err:=ConductDecideEscalation(context.Background(),tx,RunRef{Org:w.org,ProjectID:"prj_g",TaskID:"wi_g",RunID:"run_cond1"},c.action,"Split the parser from the fix.");return err})
   if PersonOnly(c.reason,c.action) {if err==nil{t.Fatal("person-only decision accepted")};return}
   if err!=nil{t.Fatal(err)}
   var by,note,said string
   if err:=w.owner.QueryRow(context.Background(),`SELECT payload->>'by',payload->>'note' FROM events WHERE task_id='wi_g' AND event_type='task.decided' ORDER BY cursor DESC LIMIT 1`).Scan(&by,&note);err!=nil{t.Fatal(err)}
   if err:=w.owner.QueryRow(context.Background(),`SELECT state->'escalation'->'decided'->>'said' FROM workflow_runs WHERE id='wfr_g'`).Scan(&said);err!=nil{t.Fatal(err)}
   if by!="conductor" || note!="Split the parser from the fix." || said!="Your conductor said:\nSplit the parser from the fix." {t.Fatalf("audit by=%s note=%q said=%q",by,note,said)}
  })
 }
}

func TestConductorEscalationDeadlineHandsOverOnce(t *testing.T){
 w:=newGoldenWorld(t)
 at:=time.Now().Add(-time.Hour).UTC()
 st:=&State{TaskID:"wi_g",ProjectID:"prj_g",Decider:DeciderConductor,Escalation:&Escalation{Reason:"stuck",Step:"fix",Holder:HolderConductor,RaisedAt:&at,Detail:map[string]any{"message":"kept"}}}
 raw,_:=json.Marshal(st)
 w.delivery(t,"decide",string(raw))
 s:=&steps{s:&Store{DB:w.app}}
 for range 2 {
  res,err:=s.decide(context.Background(),workflow.StepContext{OrganizationID:w.org,WorkflowRunID:"wfr_g",Step:"decide",State:raw});if err!=nil{t.Fatal(err)}
  st=res.State.(*State);raw,_=json.Marshal(st)
  if st.Escalation.HeldBy()!=HolderPerson{t.Fatal("not handed over")}
 }
 var n int
 if err:=w.owner.QueryRow(context.Background(),`SELECT count(*) FROM events WHERE task_id='wi_g' AND event_type='question.asked'`).Scan(&n);err!=nil||n!=1{t.Fatalf("handover count=%d err=%v",n,err)}
}
