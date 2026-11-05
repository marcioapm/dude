package orchestrator_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

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
