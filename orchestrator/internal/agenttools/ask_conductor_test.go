package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func questionDelivery(t *testing.T, f *fixture, state string) {
	t.Helper()
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES ('wf_question',$1,$2,'task.delivery','question','waiting','implement',$3::jsonb)`, f.org, f.item, state)
}

func TestConductorMultiAnswerMCPHistory(t *testing.T) {
	f := setup(t)
	writer := f.run(t, "run_writer", "implementer", "running")
	conductor := f.run(t, "run_conductor", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_conductor'`)
	questionDelivery(t, f, `{"attempt":1,"decider":"conductor"}`)
	var ask struct{ QuestionID string }
	if status := f.postAs(t, writer, "ask_conductor", `{"questions":[{"header":"First","question":"Which first?"},{"header":"Second","question":"Which second?"}]}`, &ask); status != 200 {
		t.Fatal(status)
	}
	cs, err := f.connect(t, conductor)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "answer_question", Arguments: map[string]any{"id": ask.QuestionID, "answers": []map[string]any{{"text": "Alpha"}, {"text": "Beta"}}}})
	if err != nil || res.IsError {
		t.Fatalf("answer: %v %+v", err, res)
	}
	var stored string
	if err := f.owner.QueryRow(context.Background(), `SELECT answer FROM questions WHERE id=$1`, ask.QuestionID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var chat struct {
		Messages []struct{ From, Kind, Text string }
	}
	if status := f.postAs(t, conductor, "run_chat", `{"run":"run_writer"}`, &chat); status != 200 {
		t.Fatal(status)
	}
	if len(chat.Messages) != 1 || chat.Messages[0].From != "conductor" || chat.Messages[0].Kind != "answer" || chat.Messages[0].Text != stored || stored == "" {
		t.Fatalf("stored=%q history=%+v", stored, chat.Messages)
	}
}

func TestQuestionConductorModelAvailability(t *testing.T) {
	for _, tool := range []string{"ask_conductor", "ask_person"} {
		for _, mode := range []string{"chatty-unset", "chatty-deleted", "chatty-failed", "quiet-none", "live-removed"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				f := setup(t)
				writer := f.run(t, "run_writer", "implementer", "running")
				f.run(t, "run_conductor", "conductor", "running")
				mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_conductor'`)
				questionDelivery(t, f, `{"attempt":1,"decider":"conductor","voice":"chatty"}`)
				mustExec(t, f.owner, `UPDATE organizations SET default_agent_models='{}' WHERE id=$1`, f.org)
				mustExec(t, f.owner, `UPDATE projects SET agent_models='{}' WHERE id=$1`, f.project)
				if mode == "chatty-deleted" {
					mustExec(t, f.owner, `UPDATE projects SET agent_models='{"conductor":{"tier":"mtr_deleted"}}' WHERE id=$1`, f.project)
				}
				if mode == "quiet-none" {
					mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"voice":"quiet"}'`)
				}
				if mode != "live-removed" {
					mustExec(t, f.owner, `UPDATE runs SET status='completed',ended_at=now() WHERE id='run_conductor'`)
				}
				if mode == "chatty-failed" {
					mustExec(t, f.owner, `UPDATE runs SET status='failed' WHERE id='run_conductor'`)
				}
				var ask struct{ QuestionID string }
				if status := f.postAs(t, writer, tool, `{"question":"What now?"}`, &ask); status != 200 {
					t.Fatal(status)
				}
				var recipient, status string
				if err := f.owner.QueryRow(context.Background(), `SELECT q.addressee,t.status::text FROM questions q JOIN tasks t ON t.id=q.task_id WHERE q.id=$1`, ask.QuestionID).Scan(&recipient, &status); err != nil {
					t.Fatal(err)
				}
				wantRecipient, wantStatus := "person", "awaiting_input"
				if mode == "live-removed" {
					wantRecipient, wantStatus = "conductor", "running"
				}
				if recipient != wantRecipient || status != wantStatus {
					t.Fatalf("recipient=%s status=%s want %s/%s", recipient, status, wantRecipient, wantStatus)
				}
			})
		}
	}
}

func TestConductorQuestionFallbackAndInvalidAnswers(t *testing.T) {
	for _, mode := range []string{"policy", "no-model", "failed", "halted", "stopped", "stale", "null-task", "wrong-task", "person", "closed"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			writer := f.run(t, "run_writer", "implementer", "running")
			conductor := f.run(t, "run_conductor", "conductor", "running")
			mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_conductor'`)
			questionDelivery(t, f, `{"attempt":1,"decider":"conductor"}`)
			switch mode {
			case "policy":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"decider":"policy"}'`)
			case "no-model":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"voice":"quiet"}'`)
				mustExec(t, f.owner, `UPDATE runs SET status='completed',ended_at=now() WHERE id='run_conductor'`)
			case "failed":
				mustExec(t, f.owner, `UPDATE runs SET status='failed',ended_at=now() WHERE id='run_conductor'`)
			}
			var ask struct{ QuestionID string }
			if status := f.postAs(t, writer, "ask_conductor", `{"question":"What next?"}`, &ask); status != 200 {
				t.Fatalf("ask=%d", status)
			}
			if mode == "policy" || mode == "no-model" || mode == "failed" {
				var recipient, status string
				if err := f.owner.QueryRow(context.Background(), `SELECT q.addressee,t.status::text FROM questions q JOIN tasks t ON t.id=q.task_id WHERE q.id=$1`, ask.QuestionID).Scan(&recipient, &status); err != nil {
					t.Fatal(err)
				}
				if recipient != "person" || status != "awaiting_input" {
					t.Fatalf("fallback %s %s", recipient, status)
				}
				return
			}
			switch mode {
			case "halted":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"halt":{}}'`)
			case "stopped":
				mustExec(t, f.owner, `UPDATE tasks SET status='aborted' WHERE id=$1`, f.item)
			case "stale":
				mustExec(t, f.owner, `UPDATE runs SET attempt=2 WHERE id='run_conductor'`)
			case "null-task":
				mustExec(t, f.owner, `UPDATE questions SET task_id=NULL WHERE id=$1`, ask.QuestionID)
			case "wrong-task":
				mustExec(t, f.owner, `INSERT INTO tasks(id,organization_id,project_id,number,title,status) VALUES('wi_other',$1,$2,2,'Other','running')`, f.org, f.project)
				mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_other',$1,'wi_other','task.delivery','other','waiting','implement','{"attempt":1,"decider":"conductor"}')`, f.org)
				conductor = f.runOn(t, "run_other_conductor", "wi_other", "implement", "conductor", "running")
				mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_other_conductor'`)
			case "person":
				mustExec(t, f.owner, `UPDATE questions SET addressee='person' WHERE id=$1`, ask.QuestionID)
			case "closed":
				mustExec(t, f.owner, `UPDATE questions SET status='cancelled' WHERE id=$1`, ask.QuestionID)
			}
			if status, _ := f.post(t, conductor, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"Proceed"}`, ask.QuestionID)); status != 422 {
				t.Fatalf("invalid answer=%d", status)
			}
			if mode == "wrong-task" {
				var open bool
				var n int
				if err := f.owner.QueryRow(context.Background(), `SELECT status='open',(SELECT count(*) FROM directives WHERE run_id='run_writer') FROM questions WHERE id=$1`, ask.QuestionID).Scan(&open, &n); err != nil {
					t.Fatal(err)
				}
				if !open || n != 0 {
					t.Fatalf("open=%v directives=%d", open, n)
				}
			}
		})
	}
}

func TestConductorQuestionConcurrentAnswerOneWins(t *testing.T) {
	f := setup(t)
	writer := f.run(t, "run_writer", "implementer", "running")
	conductor := f.run(t, "run_conductor", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_conductor'`)
	questionDelivery(t, f, `{"attempt":1,"decider":"conductor"}`)
	var ask struct{ QuestionID string }
	if status := f.postAs(t, writer, "ask_conductor", `{"question":"What next?"}`, &ask); status != 200 {
		t.Fatal(status)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, _ := f.post(t, conductor, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"Proceed"}`, ask.QuestionID))
			results <- status
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for status := range results {
		if status == 200 {
			wins++
		} else if status != 422 {
			t.Errorf("status=%d", status)
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
	var n int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM directives WHERE run_id='run_writer'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("directives=%d", n)
	}
}

func TestQuestionGrantsWithdrawnOnEverySurface(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		return capabilities.Default(a.Role).Without(capabilities.AskConductor, capabilities.Conduct), nil
	})
	f := setupWith(t, srv)
	for _, role := range []string{"implementer", "conductor"} {
		token := f.run(t, "run_"+role, role, "running")
		names := []string{"ask_conductor", "ask_person"}
		if role == "conductor" {
			names = []string{"answer_question"}
		}
		cs, err := f.connect(t, token)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if _, ok := f.getTools(t, token)[name]; ok {
				t.Errorf("HTTP lists %s", name)
			}
			for _, tool := range listed.Tools {
				if tool.Name == name {
					t.Errorf("MCP lists %s", name)
				}
			}
			if status, _ := f.post(t, token, name, `{}`); status != 404 {
				t.Errorf("HTTP %s=%d", name, status)
			}
			if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}}); err == nil {
				t.Errorf("MCP admitted %s", name)
			}
		}
		cs.Close()
	}
	// The caller's settings view uses the same resolved grants as dispatch.
	mustExec(t, f.owner, `UPDATE runs SET status='completed' WHERE id='run_conductor'`)
	token := f.run(t, "run_settings", "conductor", "running")
	var out map[string]any
	if status := f.postAs(t, token, "settings", `{"section":"capabilities"}`, &out); status != 200 {
		t.Fatal(status)
	}
	raw, _ := json.Marshal(out)
	var got struct {
		Capabilities []struct {
			Yours   bool
			Granted []struct{ Key capabilities.Key }
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, role := range got.Capabilities {
		if role.Yours {
			for _, g := range role.Granted {
				if g.Key == capabilities.Conduct {
					t.Fatal("settings granted withdrawn conduct")
				}
			}
		}
	}
}

func TestWriterAsksConductorAndReceivesOneAnswer(t *testing.T) {
	for _, tool := range []string{"ask_conductor", "ask_person"} {
		t.Run(tool, func(t *testing.T) {
			f := setup(t)
			writer := f.run(t, "run_writer", "implementer", "running")
			conductor := f.run(t, "run_conductor", "conductor", "running")
			mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_conductor'`)
			mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
    VALUES ('wf_question',$1,$2,'task.delivery','question','waiting','implement',jsonb_build_object('attempt',1,'taskId',$2::text,'projectId',$3::text,'decider','conductor'))`, f.org, f.item, f.project)
			var ask struct{ QuestionID string }
			if status := f.postAs(t, writer, tool, `{"question":"Which behavior should I implement?"}`, &ask); status != 200 {
				t.Fatalf("ask status=%d", status)
			}
			var addressed, taskStatus string
			var wakes, human int
			err := f.owner.QueryRow(context.Background(), `SELECT q.addressee,t.status::text,(SELECT count(*) FROM conductor_wakes WHERE key='question:'||q.id),(SELECT count(*) FROM events WHERE event_type='question.asked') FROM questions q JOIN tasks t ON t.id=q.task_id WHERE q.id=$1`, ask.QuestionID).Scan(&addressed, &taskStatus, &wakes, &human)
			if err != nil {
				t.Fatal(err)
			}
			if addressed != "conductor" || taskStatus != "running" || wakes != 1 || human != 0 {
				t.Fatalf("route=%s status=%s wakes=%d human=%d", addressed, taskStatus, wakes, human)
			}
			mustExec(t, f.owner, `UPDATE runs SET status='paused',dude_pause='person' WHERE id='run_writer'`)
			body := fmt.Sprintf(`{"id":%q,"answer":"Use the documented behavior."}`, ask.QuestionID)
			if status, _ := f.post(t, conductor, "answer_question", body); status != 200 {
				t.Fatalf("answer=%d", status)
			}
			if status, _ := f.post(t, conductor, "answer_question", body); status != 422 {
				t.Fatalf("double answer=%d", status)
			}
			var by string
			var directives int
			if err := f.owner.QueryRow(context.Background(), `SELECT answered_by_run,(SELECT count(*) FROM directives WHERE run_id='run_writer') FROM questions WHERE id=$1 AND status='answered'`, ask.QuestionID).Scan(&by, &directives); err != nil {
				t.Fatal(err)
			}
			if by != "run_conductor" || directives != 1 {
				t.Fatalf("by=%s directives=%d", by, directives)
			}
			// The shared section-model reader is used by fresh specs and resumed
			// rounds. An owner override remains human regardless of addressee.
			mustExec(t, f.owner, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,phase,role,ended_at) VALUES('run_prior',$1,$2,$3,1,'completed','review','reviewer',now()-interval '1 hour')`, f.org, f.project, f.item)
			mustExec(t, f.owner, `INSERT INTO questions(id,organization_id,task_id,prompt,status,answer,answered_at,addressee) VALUES('q_owner',$1,$2,'Owner choice?','answered','Keep owner choice',now(),'conductor')`, f.org, f.item)
			for _, predecessor := range []string{"", "run_prior"} {
				if err := f.app.InOrg(context.Background(), f.org, func(tx pgx.Tx) error {
					decisions, err := delivery.PeopleDecisionsTx(context.Background(), tx, f.item, predecessor)
					if err == nil && (len(decisions) != 1 || decisions[0].Answer != "Keep owner choice") {
						t.Errorf("human decisions (%s)=%+v", predecessor, decisions)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			var chat struct {
				Messages []struct{ From, Kind, Text string }
			}
			if status := f.postAs(t, conductor, "run_chat", `{"run":"run_writer"}`, &chat); status != 200 {
				t.Fatal(status)
			}
			if len(chat.Messages) != 1 || chat.Messages[0].From != "conductor" || chat.Messages[0].Kind != "answer" || chat.Messages[0].Text != "Use the documented behavior." {
				t.Fatalf("answer history=%+v", chat.Messages)
			}
		})
	}
}
