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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func questionDelivery(t *testing.T, f *fixture, state string) {
	t.Helper()
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES ('wf_question',$1,$2,'task.delivery','question','waiting','implement',$3::jsonb)`, f.org, f.item, state)
}

func TestConductorQuestionFallbackAndInvalidAnswers(t *testing.T) {
	for _, mode := range []string{"policy", "no-model", "failed", "halted", "stopped", "stale", "wrong-task", "person", "closed"} {
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
			case "wrong-task":
				mustExec(t, f.owner, `UPDATE questions SET task_id=NULL WHERE id=$1`, ask.QuestionID)
			case "person":
				mustExec(t, f.owner, `UPDATE questions SET addressee='person' WHERE id=$1`, ask.QuestionID)
			case "closed":
				mustExec(t, f.owner, `UPDATE questions SET status='cancelled' WHERE id=$1`, ask.QuestionID)
			}
			if status, _ := f.post(t, conductor, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"Proceed"}`, ask.QuestionID)); status != 422 {
				t.Fatalf("invalid answer=%d", status)
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
		})
	}
}
