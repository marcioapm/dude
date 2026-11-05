package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The owner must wait at Chat, not while holding the question/Run that the
// real conductor endpoint will need. A held task row makes that ordering
// observable through pg_blocking_pids, without scheduling sleeps.
func TestOwnerAndConductorAnswerSharePrelock(t *testing.T) {
	s := newSteering(t, conducting(t))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var spec string
	for _, run := range s.lux.Runs() {
		if run.ID == s.luxRunOf(s.implementer) {
			spec = string(run.Spec)
		}
	}
	status, body := s.callTool(s.syncer.Agent.ToolsURL, spec, "ask_conductor", `{"question":"Which behavior?"}`)
	if status != 200 {
		t.Fatalf("ask %d %s", status, body)
	}
	var ask struct{ QuestionID string }
	if err := json.Unmarshal([]byte(body), &ask); err != nil {
		t.Fatal(err)
	}
	holder, err := pgx.Connect(ctx, s.owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(context.Background())
	hold, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if err := delivery.LockTaskTx(ctx, hold, s.task); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := hold.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		body   any
		err    error
	}
	ownerDone := make(chan result, 1)
	go func() {
		status, body, err := s.post(ctx, "/internal/questions/"+ask.QuestionID+"/answer", map[string]any{"text": "Owner answer"})
		ownerDone <- result{status, body, err}
	}()
	ownerPID := s.blockedBy(ctx, pid, "owner answer prelock")
	// While waiting for the task prelock, the owner must hold neither the
	// question nor writer Run. NOWAIT is an ordering assertion, not a retry.
	if _, err := hold.Exec(ctx, `SELECT id FROM questions WHERE id=$1 FOR UPDATE NOWAIT`, ask.QuestionID); err != nil {
		t.Fatalf("owner locked question before task: %v", err)
	}
	if _, err := hold.Exec(ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE NOWAIT`, s.implementer); err != nil {
		t.Fatalf("owner locked Run before task: %v", err)
	}
	conductorDone := make(chan result, 1)
	go func() {
		status, body := s.callTool(s.syncer.Agent.ToolsURL, s.spec, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"Conductor answer"}`, ask.QuestionID))
		conductorDone <- result{status, body, nil}
	}()
	s.blockedBy(ctx, ownerPID, "conductor serialized behind owner")
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []chan result{ownerDone, conductorDone} {
		select {
		case got := <-ch:
			want := 200
			if ch == conductorDone {
				want = 422
			}
			if got.err != nil || got.status != want {
				t.Fatalf("answer %+v want %d", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id=$1`, s.implementer); n != 1 {
		t.Fatalf("directives=%d", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='answered' AND answered_by_run IS NULL AND answer='Owner answer'`, ask.QuestionID); n != 1 {
		t.Fatal("owner attribution lost")
	}
}

func TestConductorAnswerIsNotHumanAuthorityInFreshSpec(t *testing.T) {
	for _, mode := range []string{"fresh", "resumed"} {
		t.Run(mode, func(t *testing.T) {
			s := newSteering(t, conducting(t))
			var spec string
			for _, r := range s.lux.Runs() {
				if r.ID == s.luxRunOf(s.implementer) {
					spec = string(r.Spec)
				}
			}
			ask := func(question string) string {
				t.Helper()
				status, body := s.callTool(s.syncer.Agent.ToolsURL, spec, "ask_conductor", fmt.Sprintf(`{"question":%q}`, question))
				if status != 200 {
					t.Fatalf("ask %d %s", status, body)
				}
				var q struct{ QuestionID string }
				if err := json.Unmarshal([]byte(body), &q); err != nil {
					t.Fatal(err)
				}
				return q.QuestionID
			}
			qid := ask("Conductor technical choice?")
			if status, body := s.callTool(s.syncer.Agent.ToolsURL, s.spec, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"Technical guidance only"}`, qid)); status != 200 {
				t.Fatalf("answer %d %s", status, body)
			}
			ownerQ := ask("Owner binding choice?")
			if status, body := s.call("/internal/questions/"+ownerQ+"/answer", map[string]any{"text": "Binding owner answer"}); status != 200 {
				t.Fatalf("owner %d %v", status, body)
			}
			var prompt string
			if mode == "fresh" {
				s.onModel("implementer", "llm-authority")
				out := s.must(s.task, "restart_run", fmt.Sprintf(`{"run":%q,"note":"fresh authority check"}`, s.implementer))
				fresh, _ := out["run"].(string)
				s.until("fresh writer submitted", func() bool { return s.promptOf(fresh) != "" })
				prompt = s.promptOf(fresh)
			} else {
				// End the original round before both answers and create a real
				// continuation directive from the production resumed-round reader.
				s.lux.FinishTools(s.luxRunOf(s.implementer))
				mustExec(t, s.owner, `UPDATE runs SET status='completed',ended_at=now()-interval '1 hour',keep=true,kept_until=now()+interval '1 day',base_refs='{"api":"aaa"}',heads='{"api":{"sha":"bbb"}}' WHERE id=$1`, s.implementer)
				mustExec(t, s.owner, `UPDATE runs SET artifacts_due_at=NULL WHERE id=$1`, s.implementer)
				var tier string
				if err := s.owner.QueryRow(context.Background(), `SELECT id FROM model_tiers WHERE organization_id=$1 AND model='fake/scripted' LIMIT 1`, s.org).Scan(&tier); err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.owner, `UPDATE projects SET agent_models=jsonb_build_object('implementer',jsonb_build_object('tier',$2::text)) WHERE id=$1`, s.project, tier)
				mustExec(t, s.owner, `UPDATE runs SET model='fake/scripted',model_tier=(SELECT name FROM model_tiers WHERE id=$2),harness='opencode' WHERE id=$1`, s.implementer, tier)
				id, err := (&delivery.Store{DB: s.app}).CreatePhaseRun(context.Background(), s.org, delivery.PhaseRun{TaskID: s.task, Phase: delivery.PhaseFix, Key: "authority-resume", Attempt: 1, BaseRefs: map[string]string{"api": "bbb"}, Continue: &delivery.Continue{Predecessor: s.implementer}})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id=$1`, id).Scan(&prompt); err != nil {
					t.Fatal(err)
				}
			}
			// Check the actual people section as a whole, not mere string absence.
			marker := "Decided by people during this work (part of the task; do not ask again):"
			start := strings.Index(prompt, marker)
			if start < 0 {
				t.Fatalf("missing owner decision: %s", prompt)
			}
			section := strings.SplitN(prompt[start:], "\n\n", 2)[0]
			want := marker + "\n- Q: Owner binding choice?\n  A: Binding owner answer"
			if section != want {
				t.Fatalf("people section=%q want %q", section, want)
			}
		})
	}
}

// Real HTTP tools, delivery/syncer, DB and fake lux: a writer parks for its
// conductor, then receives exactly one answer on the same Run.
func TestWriterQuestionConductorAnswerResumesParkedRun(t *testing.T) {
	s := newSteering(t, conducting(t))
	s.syncer.ParkAfter = 100 * time.Millisecond
	var spec string
	for _, run := range s.lux.Runs() {
		if run.ID == s.luxRunOf(s.implementer) {
			spec = string(run.Spec)
		}
	}
	status, body := s.callTool(s.syncer.Agent.ToolsURL, spec, "ask_conductor", `{"question":"Which behavior should I implement?"}`)
	if status != 200 {
		t.Fatalf("ask %d %s", status, body)
	}
	var ask struct{ QuestionID string }
	if err := json.Unmarshal([]byte(body), &ask); err != nil {
		t.Fatal(err)
	}
	s.lux.FinishTools(s.luxRunOf(s.implementer))
	s.lux.EndTurn(s.luxRunOf(s.implementer))
	s.until("writer parked with conductor", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, s.implementer) == 1
	})
	if s.taskStatus(s.task) == "awaiting_input" {
		t.Fatal("writer question reached Needs you")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked'`, s.task); n != 0 {
		t.Fatalf("human asks=%d", n)
	}
	if n := s.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND key='question:'||$2`, s.task, ask.QuestionID); n != 1 {
		t.Fatalf("wakes=%d", n)
	}
	status, body = s.callTool(s.syncer.Agent.ToolsURL, s.spec, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"Use the documented behavior."}`, ask.QuestionID))
	if status != 200 {
		t.Fatalf("answer %d %s", status, body)
	}
	s.until("writer resumed and received conductor answer", func() bool {
		for _, run := range s.lux.Runs() {
			if run.ID == s.luxRunOf(s.implementer) {
				for _, input := range run.Inputs {
					if strings.Contains(input, "Use the documented behavior.") {
						return true
					}
				}
			}
		}
		return false
	})
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id=$1`, s.implementer); n != 1 {
		t.Fatalf("directives=%d", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND answered_by_run=$2 AND status='answered'`, ask.QuestionID, s.conductor); n != 1 {
		t.Fatal("answer attribution missing")
	}
}
