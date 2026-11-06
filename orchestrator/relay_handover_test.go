package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// writerAsks has the steering world's implementer ask its conductor.
func (s *steering) writerAsks(question string) string {
	s.t.Helper()
	var spec string
	for _, run := range s.lux.Runs() {
		if run.ID == s.luxRunOf(s.implementer) {
			spec = string(run.Spec)
		}
	}
	status, body := s.callTool(s.syncer.Agent.ToolsURL, spec, "ask_conductor", fmt.Sprintf(`{"question":%q}`, question))
	if status != 200 {
		s.t.Fatalf("ask %d %s", status, body)
	}
	var ask struct{ QuestionID string }
	if err := json.Unmarshal([]byte(body), &ask); err != nil {
		s.t.Fatal(err)
	}
	return ask.QuestionID
}

// relays has the conductor relay writer question qid to the person.
func (s *steering) relays(qid string) string {
	s.t.Helper()
	status, body := s.callTool(s.syncer.Agent.ToolsURL, s.spec, "ask_person", fmt.Sprintf(`{"question":"The implementer asks: A or B?","relay":%q}`, qid))
	if status != 200 {
		s.t.Fatalf("relay %d %s", status, body)
	}
	var out struct{ QuestionID string }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		s.t.Fatal(err)
	}
	return out.QuestionID
}

func (s *steering) inputsWith(run, text string) int {
	n := 0
	for _, r := range s.lux.Runs() {
		if r.ID == s.luxRunOf(run) {
			for _, input := range r.Inputs {
				if strings.Contains(input, text) {
					n++
				}
			}
		}
	}
	return n
}

// One owner answer to the conductor's relay answers the writer's question
// too, as the owner's: the writer is told once, the conductor once, and the
// answer is a person's decision for the writers after.
func TestRelayAnswerForwardsToWriter(t *testing.T) {
	s := newSteering(t, conducting(t))
	s.syncer.ParkAfter = 100 * time.Millisecond
	qid := s.writerAsks("Which API shape?")
	s.lux.FinishTools(s.luxRunOf(s.implementer))
	s.lux.EndTurn(s.luxRunOf(s.implementer))
	s.until("writer parked with conductor", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, s.implementer) == 1
	})
	relay := s.relays(qid)
	if s.taskStatus(s.task) != "awaiting_input" {
		t.Fatalf("relay: task %s", s.taskStatus(s.task))
	}
	status, out := s.call("/internal/questions/"+relay+"/answer", map[string]any{"text": "Shape B, owner's call"})
	if status != 200 {
		t.Fatalf("owner answer %d %v", status, out)
	}
	if r, _ := out["relay"].(map[string]any); r["forwarded"] != true || r["questionId"] != qid {
		t.Fatalf("relay outcome %v", out["relay"])
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id IN ($1,$2) AND status='answered'`, qid, relay); n != 2 {
		t.Fatalf("answered=%d", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND answered_by_run IS NULL AND answer='Shape B, owner''s call'`, qid); n != 1 {
		t.Fatal("writer's question not answered as the person's")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type='question.answered' AND payload->>'questionId'=$1 AND payload->>'by'='person' AND actor_type<>'agent'`, qid); n != 1 {
		t.Fatalf("writer question.answered by person=%d", n)
	}
	s.until("writer resumed with the owner's answer", func() bool { return s.inputsWith(s.implementer, "Shape B, owner's call") >= 1 })
	s.until("conductor told the owner's answer", func() bool { return s.inputsWith(s.conductor, "Shape B, owner's call") >= 1 })
	for range 5 {
		s.pump()
	}
	if n := s.inputsWith(s.implementer, "Shape B, owner's call"); n != 1 {
		t.Fatalf("writer told %d times", n)
	}
	if n := s.inputsWith(s.conductor, "Shape B, owner's call"); n != 1 {
		t.Fatalf("conductor told %d times", n)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id=$1`, s.implementer); n != 1 {
		t.Fatalf("writer directives=%d", n)
	}
	if !strings.Contains(s.directiveText(s.implementer), "It is their decision") {
		t.Fatalf("writer directive not marked the person's: %q", s.directiveText(s.implementer))
	}
	if n := s.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked'`, s.task); n != 1 {
		t.Fatalf("question.asked=%d (the relay only)", n)
	}
	if s.taskStatus(s.task) == "awaiting_input" {
		t.Fatal("task still waits on the person")
	}
}

func (s *steering) directiveText(run string) string {
	var text string
	_ = s.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id=$1 ORDER BY created_at LIMIT 1`, run).Scan(&text)
	return text
}

// The writer's question settled before the person answers the relay: the
// relay is withdrawn (conductor first) or its answer forwards nothing
// (owner answered the writer directly), and nothing is answered twice.
func TestRelayRaceOrders(t *testing.T) {
	for _, order := range []string{"conductor-first", "owner-on-writer-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			s := newSteering(t, conducting(t))
			qid := s.writerAsks("Which API shape?")
			relay := s.relays(qid)
			switch order {
			case "conductor-first":
				if status, body := s.callTool(s.syncer.Agent.ToolsURL, s.spec, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"A, by the docs"}`, qid)); status != 200 {
					t.Fatalf("conductor %d %s", status, body)
				}
				if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='cancelled'`, relay); n != 1 {
					t.Fatal("relay left open")
				}
				if status, _ := s.call("/internal/questions/"+relay+"/answer", map[string]any{"text": "B"}); status != 409 {
					t.Fatalf("owner on a withdrawn relay: %d", status)
				}
			case "owner-on-writer-first":
				if status, out := s.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "A, owner direct"}); status != 200 {
					t.Fatalf("owner direct %d %v", status, out)
				}
				if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='cancelled'`, relay); n != 1 {
					t.Fatal("relay left open after the owner answered the writer")
				}
			case "concurrent":
				var wg sync.WaitGroup
				var ownerStatus, condStatus int
				start := make(chan struct{})
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					ownerStatus, _ = s.call("/internal/questions/"+relay+"/answer", map[string]any{"text": "B"})
				}()
				go func() {
					defer wg.Done()
					<-start
					condStatus, _ = s.callTool(s.syncer.Agent.ToolsURL, s.spec, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"A"}`, qid))
				}()
				close(start)
				wg.Wait()
				// Serialized by the shared prelock: one wins the writer's
				// question; the other is a refusal or a no-op on it.
				if ownerStatus == 200 && condStatus == 200 {
					t.Fatal("both answered")
				}
				if ownerStatus != 200 && condStatus != 200 {
					t.Fatalf("neither answered: owner=%d conductor=%d", ownerStatus, condStatus)
				}
			}
			if n := s.count(`SELECT count(*) FROM directives WHERE run_id=$1`, s.implementer); n != 1 {
				t.Fatalf("writer directives=%d", n)
			}
			if n := s.count(`SELECT count(*) FROM events WHERE event_type='question.answered' AND payload->>'questionId'=$1`, qid); n != 1 {
				t.Fatalf("writer answered %d times", n)
			}
			if n := s.count(`SELECT count(*) FROM questions WHERE task_id=$1 AND status='open'`, s.task); n != 0 {
				t.Fatalf("open questions=%d", n)
			}
		})
	}
}

// A writer question parked past the (shortened) wait is handed to the
// person by the sweep: once, however often it runs, and pushed once.
func TestConductorQuestionHandsOverAtDeadline(t *testing.T) {
	s := newSteering(t, conducting(t))
	s.syncer.ConductorAnswerWait = 300 * time.Millisecond
	s.syncer.ParkAfter = 100 * time.Millisecond
	qid := s.writerAsks("Which API shape?")
	s.lux.FinishTools(s.luxRunOf(s.implementer))
	s.lux.EndTurn(s.luxRunOf(s.implementer))
	s.until("handed over", func() bool {
		return s.count(`SELECT count(*) FROM questions WHERE id=$1 AND addressee='person'`, qid) == 1
	})
	for range 5 {
		s.pump()
	}
	if n := s.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked' AND payload->>'handedOverFrom'='conductor'`, s.task); n != 1 {
		t.Fatalf("question.asked=%d", n)
	}
	if s.taskStatus(s.task) != "awaiting_input" {
		t.Fatalf("task %s", s.taskStatus(s.task))
	}
	if n := s.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND key='base-moved:handover:'||$2`, s.task, qid); n != 1 {
		t.Fatalf("conductor news=%d", n)
	}
	// The person answers: the writer resumes with it, the task goes on.
	if status, out := s.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "B, after handover"}); status != 200 {
		t.Fatalf("answer %d %v", status, out)
	}
	s.until("writer told", func() bool { return s.inputsWith(s.implementer, "B, after handover") == 1 })
	if s.taskStatus(s.task) == "awaiting_input" {
		t.Fatal("task still awaiting")
	}
}

// The task's conductor failing after the ask hands the question over at
// once, without the wait.
func TestHandsOverWhenConductorFailed(t *testing.T) {
	s := newSteering(t, conducting(t))
	s.syncer.ConductorAnswerWait = time.Hour
	qid := s.writerAsks("Which API shape?")
	for range 3 {
		s.pump()
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND addressee='conductor'`, qid); n != 1 {
		t.Fatal("handed over before the wait with a live conductor")
	}
	mustExec(t, s.owner, `UPDATE runs SET status='failed', ended_at=now() WHERE id=$1`, s.conductor)
	s.until("handed over", func() bool {
		return s.count(`SELECT count(*) FROM questions WHERE id=$1 AND addressee='person' AND status='open'`, qid) == 1
	})
	for range 3 {
		s.pump()
	}
	if n := s.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked' AND payload->>'handedOverBecause'='its conductor failed'`, s.task); n != 1 {
		t.Fatalf("question.asked=%d", n)
	}
}

// idleParkedWorld is a conducted task whose implementer hangs mid-turn
// and is parked as idle (A12), its conductor woken: the task and the Run.
func idleParkedWorld(t *testing.T, w *world) (string, string) {
	t.Helper()
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	var implementer string
	w.until("the implementer to run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND status = 'running' AND lux_state = 'running'`, task).Scan(&implementer)
		return implementer != ""
	})
	w.syncer.IdleAfter = 300 * time.Millisecond
	w.until("the implementer to be parked as idle", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND dude_pause='idle'`, implementer) == 1
	})
	w.syncer.IdleAfter = time.Hour
	return task, implementer
}

func (w *world) inputsTo(run, text string) int {
	n := 0
	for _, r := range w.lux.Runs() {
		if r.ID == w.luxRunOf(run) {
			for _, input := range append(append([]string{}, r.Inputs...), r.ResumeInputs...) {
				if strings.Contains(input, text) {
					n++
				}
			}
		}
	}
	return n
}

// A12: a conducted task's phase agent that goes quiet is parked, and its
// conductor decides — the task does not wait on a person. Its steer is
// what takes the Run up again: the Run resumes and hears it, once.
func TestIdleParkWakesConductorWhenConducted(t *testing.T) {
	w := conducting(t)
	task, implementer := idleParkedWorld(t, w)
	for range 3 {
		w.pump()
	}
	if got := w.taskStatus(task); got == "awaiting_input" {
		t.Fatal("a conducted idle park asked the person")
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND kind='stalled' AND key LIKE 'stalled:idle:'||$2||':%'
		AND line LIKE '%your steer resumes it%' AND line NOT LIKE '%Leave it%'`, task, implementer); n != 1 {
		t.Fatalf("stalled wakes=%d", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked'`, task); n != 0 {
		t.Fatalf("question.asked=%d", n)
	}
	w.must(task, "steer", fmt.Sprintf(`{"run":%q,"text":"Carry on: write the handler first."}`, implementer))
	w.until("the steered Run to resume", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running'`, implementer) == 1
	})
	w.until("the steer delivered", func() bool { return w.inputsTo(implementer, "write the handler first") >= 1 })
	for range 5 {
		w.pump()
	}
	if n := w.inputsTo(implementer, "write the handler first"); n != 1 {
		t.Fatalf("steer heard %d times", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.unparked'`, implementer); n != 1 {
		t.Fatalf("run.unparked=%d", n)
	}
	if got := w.taskStatus(task); got == "awaiting_input" {
		t.Fatalf("task %s", got)
	}
}

// An idle park its conductor leaves (no steer, no restart) is not a
// silent stall: past the wait it is handed to the person, who sees it in
// Needs you; their Resume takes it up and puts the task back.
func TestALeftIdleParkIsHandedToThePerson(t *testing.T) {
	w := conducting(t)
	w.syncer.HeldAskWait = 500 * time.Millisecond
	task, implementer := idleParkedWorld(t, w)
	w.until("the task to wait on the person", func() bool { return w.taskStatus(task) == "awaiting_input" })
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.idle_handed_over'`, implementer); n != 1 {
		t.Fatalf("handed over %d times", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND dude_pause='idle'`, implementer); n != 1 {
		t.Fatal("the Run is no longer parked")
	}
	if status, out := w.call("/internal/runs/"+implementer+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume %d %v", status, out)
	}
	w.until("the Run resumed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running'`, implementer) == 1
	})
	w.until("the task back", func() bool { return w.taskStatus(task) != "awaiting_input" })
	// Put back by the resume itself, to the status the hand-over kept.
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='task.status_changed'
		AND payload->>'reason'='a person resumed the agent'`, task); n != 1 {
		t.Fatalf("restored by the resume %d times", n)
	}
}

// S9: a conducted idle park whose conductor is not available (a quiet
// delivery with no conductor model) waits on the person at once, and
// wakes no conductor.
func TestIdleParkWithNoConductorModelAsksThePerson(t *testing.T) {
	w := conducting(t)
	w.syncer.HeldAskWait = time.Hour
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	var implementer string
	w.until("the implementer to run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND status = 'running' AND lux_state = 'running'`, task).Scan(&implementer)
		return implementer != ""
	})
	// Quiet, its conductor gone, and no model to run another on.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"voice":"quiet"}' WHERE task_id = $1`, task)
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE task_id = $1 AND role = 'conductor'`, task)
	mustExec(t, w.owner, `UPDATE organizations SET default_agent_models = '{}'`)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = '{}'`)
	w.syncer.IdleAfter = 300 * time.Millisecond
	w.until("the implementer to be parked as idle", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND dude_pause='idle'`, implementer) == 1
	})
	w.syncer.IdleAfter = time.Hour
	if got := w.taskStatus(task); got != "awaiting_input" {
		t.Fatalf("task %s", got)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND key LIKE 'stalled:idle:%'`, task); n != 0 {
		t.Fatalf("stalled wakes=%d", n)
	}
}

// The writer's Run ended (its question cancelled with it) while the relay
// was open: the person's answer closes the relay and forwards nothing,
// saying so.
func TestRelayAnswerAfterWriterQuestionClosedForwardsNothing(t *testing.T) {
	s := newSteering(t, conducting(t))
	qid := s.writerAsks("Which API shape?")
	relay := s.relays(qid)
	mustExec(t, s.owner, `UPDATE questions SET status='cancelled' WHERE id=$1`, qid)
	status, out := s.call("/internal/questions/"+relay+"/answer", map[string]any{"text": "B"})
	if status != 200 {
		t.Fatalf("owner %d %v", status, out)
	}
	if r, _ := out["relay"].(map[string]any); r["forwarded"] != false || r["status"] != "cancelled" {
		t.Fatalf("relay outcome %v", out["relay"])
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='cancelled'`, qid); n != 1 {
		t.Fatal("a closed writer question was answered")
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id=$1`, s.implementer); n != 0 {
		t.Fatalf("writer directives=%d", n)
	}
}

// An image alone answers the conductor's relay too: the writer's question
// is answered (in words saying so), not refused with a 500 that rolls the
// person's answer back. A refusal of the relayed answer is a 422.
func TestAnImageOnlyRelayAnswerIsForwarded(t *testing.T) {
	s := newSteering(t, conducting(t))
	b := s.withImages()
	qid := s.writerAsks("Which layout?")
	relay := s.relays(qid)
	s.upload(b, "att_layout", s.task, "layout.png", screenshot)
	status, out := s.call("/internal/questions/"+relay+"/answer", map[string]any{"text": "", "attachmentIds": []string{"att_layout"}})
	if status != 200 {
		t.Fatalf("image-only relay answer %d %v", status, out)
	}
	if r, _ := out["relay"].(map[string]any); r["forwarded"] != true {
		t.Fatalf("relay outcome %v", out["relay"])
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id IN ($1,$2) AND status='answered'`, qid, relay); n != 2 {
		t.Fatalf("answered=%d", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND answer LIKE '%answered with an image%'`, qid); n != 1 {
		t.Fatal("the writer's answer does not say it was an image")
	}
}

// relayLost is a writer's question W relayed by the conductor as R, the
// writer parked on it, all the conductor's input heard, and then the
// conductor's Run failing as lux losing it does (R cancelled with it). The
// person's id is set on R as whom it is for. Returns W, R and the person.
func (s *steering) relayLost() (qid, relay, person string) {
	s.t.Helper()
	s.syncer.ConductorAnswerWait = time.Hour
	s.syncer.ParkAfter = 100 * time.Millisecond
	qid = s.writerAsks("Which API shape?")
	s.lux.FinishTools(s.luxRunOf(s.implementer))
	s.lux.EndTurn(s.luxRunOf(s.implementer))
	s.until("writer parked with conductor", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, s.implementer) == 1
	})
	relay = s.relays(qid)
	person = s.str(`SELECT person_id FROM api_keys WHERE id=$1`, s.person("Ana"))
	mustExec(s.t, s.owner, `UPDATE questions SET to_person=$2 WHERE id=$1`, relay, person)
	s.until("all conductor input heard", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND delivered_at IS NULL AND failed_at IS NULL`, s.conductor) == 0
	})
	mustExec(s.t, s.owner, `UPDATE runs SET status='failed', error='lux no longer has this Run', ended_at=now() WHERE id=$1`, s.conductor)
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='cancelled'`, relay); n != 1 {
		s.t.Fatal("the relay was not cancelled with its conductor")
	}
	return qid, relay, person
}

// Try again before any sweep: the writer's question is still the
// conductor's, so the lost relay is asked again as a relay, for the same
// person; its answer reaches the writer, once.
func TestTryAgainReasksALostRelayThatStillRelays(t *testing.T) {
	s := newSteering(t, conducting(t))
	qid, relay, person := s.relayLost()
	status, out := s.call("/internal/tasks/"+s.task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	fresh := s.str(`SELECT COALESCE((SELECT id FROM questions WHERE run_id=$1 AND status='open' AND addressee='person'), '')`, next)
	if fresh == "" || fresh == relay {
		t.Fatalf("no fresh question: %q", fresh)
	}
	if got := s.str(`SELECT COALESCE(relays, '') FROM questions WHERE id=$1`, fresh); got != qid {
		t.Fatalf("the fresh question relays %q, want %q", got, qid)
	}
	if got := s.str(`SELECT COALESCE(to_person, '') FROM questions WHERE id=$1`, fresh); got != person {
		t.Fatalf("the fresh question is for %q, want %q", got, person)
	}
	status, out = s.call("/internal/questions/"+fresh+"/answer", map[string]any{"text": "Shape B, after the retry"})
	if status != 200 {
		t.Fatalf("answer %d %v", status, out)
	}
	if r, _ := out["relay"].(map[string]any); r["forwarded"] != true || r["questionId"] != qid {
		t.Fatalf("relay outcome %v", out["relay"])
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='answered'`, qid); n != 1 {
		t.Fatal("the writer's question was not answered")
	}
	s.until("writer told", func() bool { return s.inputsWith(s.implementer, "Shape B, after the retry") >= 1 })
	for range 5 {
		s.pump()
	}
	if n := s.inputsWith(s.implementer, "Shape B, after the retry"); n != 1 {
		t.Fatalf("writer told %d times", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type='question.answered' AND payload->>'questionId'=$1`, qid); n != 1 {
		t.Fatalf("writer answered %d times", n)
	}
}

// Try again after the sweeps handed the writer's question itself to the
// person (its conductor failed): it is not relayed again, so the person
// has it once, and their answer reaches the writer once.
func TestTryAgainDoesNotRelayAWriterQuestionHandedToThePerson(t *testing.T) {
	s := newSteering(t, conducting(t))
	qid, _, _ := s.relayLost()
	for range 4 {
		s.pump()
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='open' AND addressee='person'`, qid); n != 1 {
		t.Fatal("the sweeps did not hand the writer's question to the person")
	}
	status, out := s.call("/internal/tasks/"+s.task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := s.count(`SELECT count(*) FROM questions WHERE task_id=$1 AND status='open' AND addressee='person' AND (id=$2 OR relays=$2)`, s.task, qid); n != 1 {
		t.Fatalf("the person has %d open questions for the writer's", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE run_id=$1`, next); n != 0 {
		t.Fatalf("%d questions asked again on the fresh conductor", n)
	}
	if prompt := s.str(`SELECT COALESCE(prompt, '') FROM runs WHERE id=$1`, next); !strings.Contains(prompt, "the writer's question was handed to the person since") {
		t.Errorf("the briefing does not say why it was not asked again:\n%s", prompt)
	}
	if status, out := s.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "Shape B, handed over"}); status != 200 {
		t.Fatalf("answer %d %v", status, out)
	}
	s.until("writer told", func() bool { return s.inputsWith(s.implementer, "Shape B, handed over") >= 1 })
	for range 5 {
		s.pump()
	}
	if n := s.inputsWith(s.implementer, "Shape B, handed over"); n != 1 {
		t.Fatalf("writer told %d times", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE task_id=$1 AND status='open' AND addressee='person'`, s.task); n != 0 {
		t.Fatalf("%d person questions still open", n)
	}
}

// notReasked checks Try again after a lost relay asked nothing: no question
// on the fresh conductor, none open, and the briefing saying why; with
// running, the task not waiting on a moot question, now or after the
// sweeps.
func (s *steering) notReasked(qid, why string, running bool) {
	s.t.Helper()
	status, out := s.call("/internal/tasks/"+s.task+"/retry", map[string]any{})
	if status != 201 {
		s.t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := s.count(`SELECT count(*) FROM questions WHERE run_id=$1`, next); n != 0 {
		s.t.Fatalf("%d questions asked again on the fresh conductor", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE task_id=$1 AND status='open' AND addressee='person'`, s.task); n != 0 {
		s.t.Fatalf("%d person questions open after Try again", n)
	}
	if prompt := s.str(`SELECT COALESCE(prompt, '') FROM runs WHERE id=$1`, next); !strings.Contains(prompt, why) {
		s.t.Errorf("the briefing does not say %q:\n%s", why, prompt)
	}
	if st := s.taskStatus(s.task); running && st == "awaiting_input" {
		s.t.Fatalf("task %s after Try again", st)
	}
	for range 4 {
		s.pump()
	}
	if st := s.taskStatus(s.task); running && st == "awaiting_input" {
		s.t.Fatalf("task %s on a moot question after the sweeps", st)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE task_id=$1 AND status='open'`, s.task); n != 0 {
		s.t.Fatalf("%d questions open after the sweeps", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type='question.answered' AND payload->>'questionId'=$1`, qid); n > 1 {
		s.t.Fatalf("writer answered %d times", n)
	}
}

// The sweeps hand the writer's question to the person and they answer it,
// then Try again: it is settled, so nothing is asked again.
func TestTryAgainDoesNotReaskARelayAnsweredAfterTheHandOver(t *testing.T) {
	s := newSteering(t, conducting(t))
	qid, _, _ := s.relayLost()
	for range 4 {
		s.pump()
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='open' AND addressee='person'`, qid); n != 1 {
		t.Fatal("the sweeps did not hand the writer's question to the person")
	}
	if status, out := s.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "Shape B, handed over"}); status != 200 {
		t.Fatalf("answer %d %v", status, out)
	}
	if st := s.taskStatus(s.task); st == "awaiting_input" {
		t.Fatalf("task %s once the person answered", st)
	}
	s.notReasked(qid, "the writer's question was settled since", true)
	s.until("writer told", func() bool { return s.inputsWith(s.implementer, "Shape B, handed over") >= 1 })
	if n := s.inputsWith(s.implementer, "Shape B, handed over"); n != 1 {
		t.Fatalf("writer told %d times", n)
	}
}

// The person restarts the writer's Run (it ends, its question cancelled
// with it and the wait its relay raised ended), then Try again: there is
// no one to relay to, so nothing is asked again, and the task is not left
// waiting on a moot question.
func TestTryAgainDoesNotReaskARelayWhoseWriterEnded(t *testing.T) {
	s := newSteering(t, conducting(t))
	qid, _, _ := s.relayLost()
	if status, out := s.call("/internal/runs/"+s.implementer+"/restart", map[string]any{"note": "start again"}); status != 200 {
		t.Fatalf("restart the writer: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='cancelled'`, qid); n != 1 {
		t.Fatal("the writer's question was not cancelled with its Run")
	}
	s.notReasked(qid, "the writer's question was settled since", true)
}

// Another relay of the writer's question is open already (the person has
// it), then Try again: it is not asked a second time.
func TestTryAgainDoesNotReaskARelayAlreadyAsked(t *testing.T) {
	s := newSteering(t, conducting(t))
	qid, relay, _ := s.relayLost()
	mustExec(t, s.owner, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, items, addressee, status, relays, asked_at)
		SELECT 'qst_other_relay', organization_id, task_id, run_id, prompt, items, addressee, 'open', relays, asked_at - interval '1 second'
		FROM questions WHERE id=$1`, relay)
	status, out := s.call("/internal/tasks/"+s.task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := s.count(`SELECT count(*) FROM questions WHERE run_id=$1`, next); n != 0 {
		t.Fatalf("%d questions asked again on the fresh conductor", n)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE relays=$1 AND status='open'`, qid); n != 1 {
		t.Fatalf("%d open relays of the writer's question", n)
	}
	if prompt := s.str(`SELECT COALESCE(prompt, '') FROM runs WHERE id=$1`, next); !strings.Contains(prompt, "the writer's question is already asked") {
		t.Errorf("the briefing does not say why it was not asked again:\n%s", prompt)
	}
}

// The writer's Run is no longer the task's current attempt's, its question
// still open with the conductor, then Try again: the writer is gone, so
// nothing is asked again, and the question is not taken for the person's.
func TestTryAgainDoesNotReaskARelayOfAnEarlierAttempt(t *testing.T) {
	s := newSteering(t, conducting(t))
	qid, _, _ := s.relayLost()
	mustExec(t, s.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{attempt}', to_jsonb(task_attempt(task_id) + 1))
		WHERE task_id = $1`, s.task)
	if n := s.count(`SELECT count(*) FROM questions WHERE id=$1 AND status='open' AND addressee='conductor'`, qid); n != 1 {
		t.Fatal("the writer's question is not still open with the conductor")
	}
	status, out := s.call("/internal/tasks/"+s.task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := s.count(`SELECT count(*) FROM questions WHERE run_id=$1`, next); n != 0 {
		t.Fatalf("%d questions asked again on the fresh conductor", n)
	}
	if prompt := s.str(`SELECT COALESCE(prompt, '') FROM runs WHERE id=$1`, next); !strings.Contains(prompt, "the writer's question was settled since") {
		t.Errorf("the briefing does not say why it was not asked again:\n%s", prompt)
	}
}
