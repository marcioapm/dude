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
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lostAtGate is a conducted task parked at the pull request gate on the
// conductor's ask_person, its conductor paused for the person, and lux then
// losing the conductor's Run. Returns the task, the lost conductor and the
// gate question.
func (w *world) lostAtGate() (task, old, question string) {
	w.t.Helper()
	// Waiting on the person, its turn over, and still followed: lux's 404
	// reaches the follower (the issue's sequence, phases.retryLater).
	w.syncer.ParkAfter = time.Hour
	task = w.task()
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	question = w.str(`SELECT id FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'open'`, task)
	old = w.str(`SELECT run_id FROM questions WHERE id = $1`, question)
	w.until("the conductor waiting on the person", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND waiting_since IS NOT NULL`, old) == 1
	})
	w.lux.ForgetRun(w.luxRunOf(old))
	return task, old, question
}

// replacementOf is the Run that replaced a lost one, "" for none.
func (w *world) replacementOf(old string) string {
	return w.str(`SELECT COALESCE(replaced_by, '') FROM runs WHERE id = $1`, old)
}

// untilLost pumps until lux's loss of old was found and old ended.
func (w *world) untilLost(old string) {
	w.t.Helper()
	w.until("the lost conductor to end", func() bool {
		return w.str(`SELECT status::text FROM runs WHERE id = $1`, old) == "failed"
	})
}

// 1. The issue's case: lux loses the conductor parked on the gate's
// question. The question stays open, on a placeholder conductor in the
// lost one's slot that costs nothing (no lux Run) until the person answers;
// the task still waits on them.
func TestExplicitResumeDoesNotSubmitAnUnansweredPlaceholder(t *testing.T) {
	w := conducting(t)
	_, old, q := w.lostAtGate()
	w.untilLost(old)
	next := w.replacementOf(old)
	submitted := len(w.lux.Runs())
	if status, out := w.call("/internal/runs/"+next+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, out)
	}
	for range 8 {
		w.pump()
	}
	if w.str(`SELECT status::text FROM questions WHERE id = $1`, q) != "open" || w.luxRunOf(next) != "" || len(w.lux.Runs()) != submitted {
		t.Fatalf("unanswered placeholder submitted: %s", w.describeRuns())
	}
	if c := w.str(`SELECT control::text FROM runs WHERE id = $1`, next); c != "none" {
		t.Fatalf("meaningless resume left due: %s", c)
	}
}

func TestRetryOnLiveConductorAndPlaceholderIsConflict(t *testing.T) {
	w := conducting(t)
	task, old, _ := w.lostAtGate()
	if status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{}); status != 409 {
		t.Fatalf("live retry: %d %v", status, out)
	}
	w.untilLost(old)
	for range 2 {
		if status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{}); status != 409 {
			t.Fatalf("stale retry after automatic replacement: %d %v", status, out)
		}
	}
}

func TestAConductorLostWhileParkedOnTheGateKeepsTheQuestionOpen(t *testing.T) {
	w := conducting(t)
	task, old, q := w.lostAtGate()
	submitted := len(w.lux.Runs())
	w.untilLost(old)
	if st := w.str(`SELECT status::text FROM questions WHERE id = $1`, q); st != "open" {
		t.Fatalf("the gate question is %s once lux lost its conductor, want open", st)
	}
	next := w.replacementOf(old)
	if next == "" || w.str(`SELECT run_id FROM questions WHERE id = $1`, q) != next {
		t.Fatalf("the question is on %s, want the replacement %q", w.str(`SELECT run_id FROM questions WHERE id = $1`, q), next)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person' AND lux_run_id IS NULL
		AND role = 'conductor'`, next); n != 1 {
		t.Errorf("the replacement is not a parked placeholder:\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_lost_at IS NOT NULL`, old); n != 1 {
		t.Error("the lost Run has no lux_lost_at")
	}
	for range 3 {
		w.pump()
	}
	if s := w.taskStatus(task); s != "awaiting_input" {
		t.Errorf("task %s, want awaiting_input", s)
	}
	if n := len(w.lux.Runs()); n != submitted {
		t.Errorf("%d lux Runs submitted before the person answered", n-submitted)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'questionId' = $3`,
		next, "question.rebound", q); n != 1 {
		t.Errorf("%d question.rebound events", n)
	}
}

// 2. The answer to it, through the question's own endpoint, reaches the
// replacement: started then, briefed on the lost Run, the answer its first
// message, and the gate opens a draft.
func TestTheAnswerToALostConductorsQuestionReachesItsReplacement(t *testing.T) {
	w := conducting(t)
	task, old, q := w.lostAtGate()
	const replaySentinel = "PREDECESSOR-REPLAY-404-unique-purple-otter"
	fullQuestion := w.str(`SELECT prompt FROM questions WHERE id = $1`, q)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := ledger.Append(context.Background(), tx, delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: old}.Event("agent.message", ledger.ActorAgent, map[string]any{"text": replaySentinel}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Use the real prompt renderer for the replacement; scripted harnesses
	// intentionally reduce a briefing to an echo, unsuitable for this check.
	w.onModel("conductor", "claude-opus-5-5")
	w.untilLost(old)
	if status, out := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Draft"}); status != 200 {
		t.Fatalf("answer Draft: %d %v", status, out)
	}
	next := w.replacementOf(old)
	if next == "" {
		t.Fatal("no replacement")
	}
	w.until("the replacement on lux", func() bool { return w.luxRunOf(next) != "" })
	spec := w.conductorSpecOf(task)
	var submitted lux.Spec
	if err := json.Unmarshal([]byte(spec), &submitted); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{old, "to answer:\n" + fmt.Sprintf("%q", fullQuestion), replaySentinel, "answer is your next message", "do not ask again"} {
		if !strings.Contains(submitted.Workload.Prompt, required) {
			t.Errorf("submitted prompt missing %q:\n%s", required, submitted.Workload.Prompt)
		}
	}
	prompt := w.str(`SELECT COALESCE(prompt, '') FROM runs WHERE id = $1`, next)
	if !strings.Contains(prompt, "lux lost your predecessor's Run") || !strings.Contains(prompt, "Open / Draft") {
		t.Errorf("the replacement's briefing does not say its predecessor was lost at the gate:\n%s", prompt)
	}
	w.until("the answer delivered", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL AND text LIKE '%Draft%'`, next) > 0
	})
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if !w.gh.Pulls()[0].Draft {
		t.Error("Draft opened a pull request ready for review")
	}
}

// 3. The answer in Chat answers the question on the placeholder; no new
// conductor is started with it.
func TestAChatAnswerToALostConductorsQuestionAnswersIt(t *testing.T) {
	w := conducting(t)
	task, old, q := w.lostAtGate()
	w.untilLost(old)
	status, out := w.chat(task, "Open")
	if status != 200 || out["questionId"] != q || out["created"] == true {
		t.Fatalf("Chat answer: %d %v", status, out)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 2 {
		t.Errorf("%d conductors, want the lost one and its replacement", n)
	}
}

// 4. The follower and the sweep both find the loss: one replacement, one
// question.rebound.
func TestALostConductorIsReplacedOnceAcrossFollowerAndSweep(t *testing.T) {
	w := conducting(t)
	w.syncer.ParkAfter = time.Millisecond
	task := w.task()
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	q := w.str(`SELECT id FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'open'`, task)
	old := w.str(`SELECT run_id FROM questions WHERE id = $1`, q)
	// Forgotten while it is still followed: before the park.
	w.lux.ForgetRun(w.luxRunOf(old))
	w.untilLost(old)
	for range 4 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor' AND replaced_by IS NULL`, task); n != 1 {
		t.Errorf("%d conductors not replaced, want 1:\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 2 {
		t.Errorf("%d conductors, want the lost one and one replacement", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE event_type = $1 AND payload->>'questionId' = $2`, "question.rebound", q); n != 1 {
		t.Errorf("%d question.rebound events, want 1", n)
	}
	if st := w.str(`SELECT status::text FROM questions WHERE id = $1`, q); st != "open" {
		t.Errorf("question %s", st)
	}
}

// 5. The person answered just before the loss was found: the answer goes
// to the replacement as its first message, its briefing naming the lost Run.
func TestHistoricalAnswerAndUnrelatedChatAreNotAnUnreadAnswer(t *testing.T) {
	w := conducting(t)
	w.syncer.ParkAfter = time.Hour
	task := w.task()
	old := w.talk(task)
	q, _ := w.must(task, "ask_person", `{"questions":[{"question":"Historical colour?","choices":[{"label":"Blue"},{"label":"Red"}]}]}`)["questionId"].(string)
	if status, body := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Blue"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("old answer heard", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NULL AND failed_at IS NULL`, old) == 0
	})
	if status, body := w.chat(task, "Unrelated new README work"); status >= 300 {
		t.Fatalf("chat: %d %v", status, body)
	}
	var next string
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		next, err = delivery.ReplaceLostConductorTx(context.Background(), tx, delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: old}, "probe")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("unrelated Chat treated as historical answer: %s", next)
	}
	w.lux.ForgetRun(w.luxRunOf(old))
	w.untilLost(old)
	if w.replacementOf(old) != "" {
		t.Fatal("historical answer took the lost-waiting replacement path")
	}
	w.until("unread Chat handed over normally", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND role='conductor' AND id<>$2`, task, old) == 1
	})
	if w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND id<>$2 AND prompt LIKE '%lux lost your predecessor%'`, task, old) != 0 {
		t.Fatal("normal handover has misleading lost-answer briefing")
	}
}

func TestAnAnswerCommittedAsTheConductorIsLostStartsTheReplacementWithIt(t *testing.T) {
	w := conducting(t)
	w.syncer.ParkAfter = time.Millisecond
	task := w.task()
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	q := w.str(`SELECT id FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'open'`, task)
	old := w.str(`SELECT run_id FROM questions WHERE id = $1`, q)
	w.untilParked(old)
	w.lux.ForgetRun(w.luxRunOf(old))
	// Answered on the old Run, still paused in dude.
	if status, out := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Draft"}); status != 200 {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.untilLost(old)
	next := w.replacementOf(old)
	if next == "" {
		t.Fatalf("no replacement:\n%s", w.describeRuns())
	}
	w.until("the replacement on lux", func() bool { return w.luxRunOf(next) != "" })
	briefed := w.str(`SELECT COALESCE((SELECT payload->>'text' FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor DESC LIMIT 1), '')`,
		next, delivery.EvConductorBriefed)
	if !strings.Contains(briefed, "lux lost your predecessor's Run ("+old+")") {
		t.Errorf("the replacement's briefing does not name the lost Run:\n%s", briefed)
	}
	w.until("the answer delivered to it", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL AND text LIKE '%Draft%'`, next) > 0
	})
}

// 6. A quiet conductor lost before its decision's deadline, waiting on the
// person it asked: its replacement is the latest conductor, not failed, so
// Deliver's defaults do not take the decision for a failed Run.
func TestALostQuietConductorDoesNotDefaultTheDecision(t *testing.T) {
	w := conducting(t)
	w.syncer.ParkAfter = time.Hour
	task := w.task()
	old := w.talk(task)
	w.implemented(task)
	w.carriedOut(task, "start_phase", `{"phase":"simplify"}`)
	w.agentsDone(task, "the simplifier's narrow review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.wokenWith(task, "review")
	w.until("genuinely heard wakes settled before loss", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND delivered_at IS NULL AND failed_at IS NULL`, old) == 0 &&
			w.count(`SELECT count(*) FROM conductor_wake_attempts a LEFT JOIN directives x ON x.id=a.directive_id
			 WHERE a.conductor_run_id=$1 AND a.failed_at IS NULL AND a.heard_at IS NULL
			 AND (a.directive_id IS NULL OR (x.delivered_at IS NULL AND x.failed_at IS NULL))`, old) == 0
	})
	if w.count(`SELECT count(*) FROM conductor_wake_attempts WHERE conductor_run_id=$1`, old) == 0 {
		t.Fatal("no real wake was heard")
	}
	w.must(task, "decide", `{"action":"ask_person","note":"Dismiss the finding?"}`)
	w.until("the conductor waiting on the person", func() bool {
		return w.count(`SELECT count(*) FROM questions WHERE run_id = $1 AND status = 'open' AND addressee = 'person'`, old) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND (waiting_since IS NOT NULL OR turn_done_at IS NOT NULL)`, old) == 1
	})
	// Quiet, its deadline an hour off.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(jsonb_set(jsonb_set(state, '{voice}', '"quiet"'),
			'{decision,decideBy}', to_jsonb(now() + interval '1 hour')), '{decision,since}', to_jsonb(now() - interval '1 minute'))
		WHERE task_id = $1 AND step = 'conductorDecision'`, task)
	w.lux.ForgetRun(w.luxRunOf(old))
	w.untilLost(old)
	next := w.replacementOf(old)
	if next == "" || w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND lux_run_id IS NULL`, next) != 1 {
		t.Fatalf("expected paused replacement before checking defaults: %s", w.describeRuns())
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, task)
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionDefaulted); n != 0 {
		t.Errorf("%d decisions defaulted after lux lost the conductor", n)
	}
	if w.decisionAt(task) != delivery.PointReviewed {
		t.Errorf("the review's decision was taken: %s %s", w.workflowState(task),
			w.str(`SELECT string_agg(event_type || ' ' || payload::text, E'\n' ORDER BY cursor) FROM events WHERE task_id = $1
				AND (event_type LIKE 'conductor.decision%' OR event_type LIKE 'chat.notice')`, task))
	}
}

// failedAsLuxLost fails conductor run as main did when lux lost it: no
// question.closed, its open questions cancelled by the status change.
func (w *world) failedAsLuxLost(run string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE runs SET status = 'failed', error = 'lux no longer has this Run', ended_at = now() WHERE id = $1`, run)
}

// failedOnceHeard waits until conductor run was delivered everything it was
// sent, then fails it as main did when lux lost it (failedAsLuxLost).
func (w *world) failedOnceHeard(run string) {
	w.t.Helper()
	w.until("all conductor input heard", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND delivered_at IS NULL AND failed_at IS NULL`, run) == 0
	})
	w.failedAsLuxLost(run)
}

// parkedAskingAtGate is a conducted task at the pull request gate whose
// conductor asked the person with decide (an ask_person) and was parked
// for them, having heard everything dude sent it. Returns the task, the
// conductor and the gate question.
//
// The conductor asks in a turn of its own, as a real one does: that turn
// ends with the question open, so it waits on the person (translate's
// idle) by construction, not only if a wake happens to land after the ask.
// It is parked only once its input is heard and no reason is pending: a
// note queued as the park's grace ends is held, unread, until the person
// answers (resumable; requestPause does not wait for it on a park for a
// person), which is not the state this sets up.
func (w *world) parkedAskingAtGate(decide string) (task, old, q string) {
	w.t.Helper()
	w.syncer.ParkAfter = time.Hour
	task = w.task()
	w.reachGate(task)
	old, _, _ = w.conductor(task)
	if status, out := w.chat(task, "Ask me at the gate.\n"+fakeagent.ConductorCallPrefix+"decide "+decide); status >= 300 {
		w.t.Fatalf("chat: %d %v", status, out)
	}
	w.until("the conductor waiting on its gate question, all it was sent heard", func() bool {
		q = w.str(`SELECT COALESCE((SELECT id FROM questions WHERE run_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'open'), '')`, old)
		return q != "" &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND waiting_since IS NOT NULL`, old) == 1 &&
			w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NULL AND failed_at IS NULL`, old) == 0 &&
			// A reason that only rides on another's note never wakes it.
			w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL AND NOT starts_with(key, $2)`,
				task, delivery.RideWakeKey("")) == 0
	})
	w.syncer.ParkAfter = time.Millisecond
	w.untilParked(old)
	return task, old, q
}

// failedAsOnMain is main's end state for the issue: the conductor failed
// with lux's 404, its gate question cancelled with no event, everything
// it was sent delivered. Returns the task, the failed conductor and the
// question.
func (w *world) failedAsOnMain() (task, old, q string) {
	w.t.Helper()
	return w.failedAsOnMainAsking(`{"action":"ask_person"}`)
}

// failedAsOnMainAsking is failedAsOnMain with the conductor's own decide
// ask_person, e.g. one that recommends another of the gate's choices.
func (w *world) failedAsOnMainAsking(decide string) (task, old, q string) {
	w.t.Helper()
	task, old, q = w.parkedAskingAtGate(decide)
	w.failedAsLuxLost(old)
	if st := w.str(`SELECT status::text FROM questions WHERE id = $1`, q); st != "cancelled" {
		w.t.Fatalf("the question is %s", st)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NULL AND failed_at IS NULL`, old); n != 0 {
		w.t.Fatalf("%d directives unread", n)
	}
	return task, old, q
}

// 7. Try again on a failed conductor with nothing unread starts a fresh
// conductor, and dude asks the lost gate question again on it at the same
// heads.
func TestTryAgainOnAFailedConductorWithNothingUnreadReasks(t *testing.T) {
	w := conducting(t)
	task, old, q := w.failedAsOnMain()
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND role = 'conductor' AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, next); n != 1 {
		t.Fatalf("no fresh conductor %q:\n%s", next, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'retries' = $2`, next, old); n != 1 {
		t.Error("its run.created does not name the Run it retries")
	}
	if n := w.count(`SELECT count(*) FROM questions q JOIN workflow_runs wf ON wf.task_id = q.task_id
		WHERE q.run_id = $1 AND q.status = 'open' AND q.id <> $2 AND q.prompt = (SELECT prompt FROM questions WHERE id = $2)
		AND q.pr_gate_heads = wf.state->'heads'`, next, q); n != 1 {
		t.Errorf("%d gate questions asked again on the fresh conductor, want 1", n)
	}
	prompt := w.str(`SELECT COALESCE(prompt, '') FROM runs WHERE id = $1`, next)
	if !strings.Contains(prompt, "failed (lux no longer has this Run) while it waited on") || !strings.Contains(prompt, "dude asked") {
		t.Errorf("the briefing does not say its predecessor failed waiting:\n%s", prompt)
	}
}

// 7b. The gate's heads moved since the lost question was asked: it is not
// asked again, and the briefing says why.
func TestTryAgainDoesNotReaskAGateQuestionAtOtherHeads(t *testing.T) {
	w := conducting(t)
	task, _, q := w.failedAsOnMain()
	mustExec(t, w.owner, `UPDATE questions SET pr_gate_heads = '{"elsewhere":"0000"}'::jsonb WHERE id = $1`, q)
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := w.count(`SELECT count(*) FROM questions WHERE run_id = $1`, next); n != 0 {
		t.Errorf("%d questions asked again at heads the work moved on from", n)
	}
	if prompt := w.str(`SELECT COALESCE(prompt, '') FROM runs WHERE id = $1`, next); !strings.Contains(prompt, "the work moved on since") {
		t.Errorf("the briefing does not say why it was not asked again:\n%s", prompt)
	}
}

// 8. /recover offers Try again for the failed conductor, naming the lost
// question.
func TestRecoverOffersTryAgainForAFailedConductor(t *testing.T) {
	w := conducting(t)
	task, old, q := w.failedAsOnMain()
	status, body := w.get("/internal/tasks/"+task+"/recover", w.org)
	if status != 200 || !strings.Contains(body, `"conductor":{`) || !strings.Contains(body, `"retry":true`) ||
		!strings.Contains(body, `"lostQuestion":{"id":"`+q+`"`) || !strings.Contains(body, `"runId":"`+old+`"`) {
		t.Fatalf("recover: %d %s", status, body)
	}
}

// 9. A lost conductor that cannot be replaced (a publish of its is moving
// the task branch) fails as before, but says so: question.closed {by:
// lost}, and the task no longer waits on the person.
func TestALostConductorThatCannotBeReplacedSaysSoAndFreesTheTask(t *testing.T) {
	w := conducting(t)
	task, old, q := w.lostAtGate()
	w.noPublishes = true
	mustExec(t, w.owner, `INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, attempt, status)
		SELECT 'cpub_moving', organization_id, task_id, id, 'req_moving', attempt, 'moving' FROM runs WHERE id = $1`, old)
	w.untilLost(old)
	if next := w.replacementOf(old); next != "" {
		t.Fatalf("replaced by %s while its publish moved", next)
	}
	if st := w.str(`SELECT status::text FROM questions WHERE id = $1`, q); st != "cancelled" {
		t.Errorf("question %s, want cancelled", st)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'questionId' = $3
		AND payload->>'by' = 'lost'`, old, delivery.EvQuestionClosed, q); n != 1 {
		t.Errorf("%d question.closed {by: lost}", n)
	}
	if s := w.taskStatus(task); s == "awaiting_input" {
		t.Errorf("the task still waits on the person for a question nobody will hear")
	}
}

// 9b. A conductor lux lost with no person's question open holds nothing to
// keep: it fails as before, and the next message starts a fresh one.
func TestALostConductorWithNoPersonQuestionFailsAsBefore(t *testing.T) {
	w := conducting(t)
	task := w.task()
	old := w.talk(task)
	w.lux.ForgetRun(w.luxRunOf(old))
	w.untilLost(old)
	if next := w.replacementOf(old); next != "" {
		t.Fatalf("replaced by %s with nothing a person was asked", next)
	}
	if e := w.str(`SELECT COALESCE(error, '') FROM runs WHERE id = $1`, old); e != "lux no longer has this Run" {
		t.Errorf("failed with %q", e)
	}
}

// Risk 1: a placeholder (paused for a person, no lux Run) reads as a parked
// conductor. Stopping the task ends it, with no lux call, and cancels its
// question; nothing sweeps it as a stall, a cost or a resume.
func TestLostConductorPreservesHeldRepositoryRequestUntilForwarding(t *testing.T) {
	w, task, _, request := heldRequest(t, true)
	w.syncer.ParkAfter = time.Hour
	w.syncer.HeldAskWait = time.Hour
	old, _, _ := w.conductor(task)
	w.must(task, "ask_person", `{"questions":[{"question":"Which direction?"}]}`)
	w.until("conductor done asking", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND turn_done_at IS NOT NULL`, old) == 1
	})
	w.lux.ForgetRun(w.luxRunOf(old))
	w.untilLost(old)
	if w.replacementOf(old) == "" {
		t.Fatal("no replacement")
	}
	for range 5 {
		w.pump()
	}
	if w.count(`SELECT count(*) FROM repository_requests WHERE id=$1 AND addressee='conductor' AND status='pending'`, request) != 1 || w.events(task, "repository.requested") != 0 {
		t.Fatal("conductor-held typed repository request silently became person's")
	}
	q := w.conductorQuestion(task)
	if status, body := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Please forward the repository request"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.must(task, "decide_repository_request", `{"id":"`+request+`","action":"forward"}`)
	if w.count(`SELECT count(*) FROM repository_requests WHERE id=$1 AND addressee='person'`, request) != 1 || w.events(task, "repository.requested") != 1 {
		t.Fatal("explicit forwarding did not transfer exactly once")
	}
}

func TestCloseAndArchiveEndPlaceholderWithoutSubmitting(t *testing.T) {
	for _, action := range []string{"close", "archive"} {
		t.Run(action, func(t *testing.T) {
			w := conducting(t)
			task, old, q := w.lostAtGate()
			w.untilLost(old)
			next := w.replacementOf(old)
			submitted := len(w.lux.Runs())
			if status, body := w.callAs(w.ownedBy(task, "Ana"), "/internal/tasks/"+task+"/"+action, map[string]any{"resolution": "wont_do"}); status != 200 {
				t.Fatalf("%s: %d %v", action, status, body)
			}
			for range 5 {
				w.pump()
			}
			if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status IN ('aborted','completed','failed') AND lux_run_id IS NULL`, next) != 1 || len(w.lux.Runs()) != submitted {
				t.Fatalf("placeholder submitted or live: %s", w.describeRuns())
			}
			if w.str(`SELECT status::text FROM questions WHERE id=$1`, q) != "cancelled" || w.taskStatus(task) == "awaiting_input" {
				t.Fatal("ended task still waiting on question")
			}
		})
	}
}

func TestRecoverDoesNotOfferRetryForLiveConductors(t *testing.T) {
	w := conducting(t)
	task := w.task()
	assertRecovery := func(run string) {
		t.Helper()
		status, raw := w.get("/internal/tasks/"+task+"/recover", w.org)
		var body map[string]any
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		if status != 200 {
			t.Fatalf("recover: %d %s", status, raw)
		}
		if run == "" {
			if body["conductor"] != nil {
				t.Fatalf("no conductor: %s", raw)
			}
			return
		}
		c := obj(body["conductor"])
		if c["runId"] != run || c["retry"] != false || c["lostQuestion"] != nil {
			t.Fatalf("live recovery: %s", raw)
		}
	}
	assertRecovery("")
	old := w.talk(task)
	assertRecovery(old)
	w.must(task, "ask_person", `{"questions":[{"question":"Continue?"}]}`)
	w.until("conductor waiting", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND (waiting_since IS NOT NULL OR turn_done_at IS NOT NULL)`, old) == 1
	})
	w.lux.ForgetRun(w.luxRunOf(old))
	w.untilLost(old)
	assertRecovery(w.replacementOf(old))
}

func TestNonLostFailureClassifiesAndReleasesItsQuestion(t *testing.T) {
	w := conducting(t)
	w.syncer.ParkAfter = time.Millisecond
	task := w.task()
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	q := w.conductorQuestion(task)
	old := w.str(`SELECT run_id FROM questions WHERE id=$1`, q)
	w.untilParked(old)
	if err := w.syncer.Lux.Cancel(context.Background(), w.luxRunOf(old)); err != nil {
		t.Fatal(err)
	}
	if status, body := w.call("/internal/runs/"+old+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.untilLost(old)
	if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='question.closed' AND payload->>'runId'=$1 AND payload->>'questionId'=$2 AND payload->>'by'='run_failed'`, old, q) != 1 {
		t.Fatal("no exact nonlost question.closed")
	}
	if w.str(`SELECT status::text FROM questions WHERE id=$1`, q) != "cancelled" || w.taskStatus(task) == "awaiting_input" {
		t.Fatal("failed question still blocks task")
	}
}

func TestStoppingATaskEndsItsPlaceholderConductor(t *testing.T) {
	w := conducting(t)
	task, old, q := w.lostAtGate()
	w.untilLost(old)
	next := w.replacementOf(old)
	if next == "" {
		t.Fatal("no placeholder")
	}
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_run_id IS NULL
		AND stall_reported_at IS NULL AND lux_cost_next_at IS NULL`, next); n != 1 {
		t.Fatalf("the placeholder was swept as a live Run:\n%s", w.describeRuns())
	}
	w.stop(w.ownedBy(task, "Ana"), task, map[string]any{})
	w.until("the placeholder ended", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('aborted', 'failed', 'completed')`, next) == 1
	})
	if st := w.str(`SELECT status::text FROM questions WHERE id = $1`, q); st != "cancelled" {
		t.Errorf("question %s after the stop", st)
	}
	if lr := w.luxRunOf(next); lr != "" {
		t.Errorf("the stopped placeholder was submitted to lux as %s", lr)
	}
}

// 4c. A Run already replaced as lost (lux_lost_at) is never replaced again,
// even should it read as live and waiting once more: the routine's own
// guard, not only the status the second caller re-reads.
func TestARunReplacedAsLostIsNotReplacedTwice(t *testing.T) {
	w := conducting(t)
	task, old, _ := w.lostAtGate()
	w.untilLost(old)
	next := w.replacementOf(old)
	if next == "" {
		t.Fatal("no replacement")
	}
	// The placeholder out of the way, the lost Run made to read as a live
	// parked conductor with a person's question open again.
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = $1`, next)
	mustExec(t, w.owner, `UPDATE runs SET status = 'paused', dude_pause = 'person', ended_at = NULL WHERE id = $1`, old)
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, addressee)
		VALUES ('q_again', $1, $2, $3, 'Again?', 'person')`, w.org, task, old)
	var again string
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		again, err = delivery.ReplaceLostConductorTx(context.Background(), tx,
			delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: old}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if again != "" {
		t.Errorf("replaced a second time, by %s", again)
	}
}
