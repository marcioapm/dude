package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// conductorWorld is a world whose project puts the conductor on a tier
// requesting the scripted agent (model is "fake/scripted" unless given).
func conductorWorld(t *testing.T, model ...string) *world {
	w := newWorld(t)
	m := "fake/scripted"
	if len(model) > 0 {
		m = model[0]
	}
	w.onModel("conductor", m)
	return w
}

// delivered is a task delivered to its pull request, and its branch then.
func (w *world) delivered() (task string, log []string) {
	w.t.Helper()
	task = w.task()
	w.deliver(task)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	return task, w.gh.Log(delivery.BranchFor(task, 1))
}

// head is the task's branch's commit on the fake GitHub.
func (w *world) head(task string) string {
	w.t.Helper()
	out, err := exec.Command("git", "-C", w.gh.Repo, "rev-parse", delivery.BranchFor(task, 1)).Output()
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (w *world) chat(task, text string) (int, map[string]any) {
	w.t.Helper()
	return w.call("/internal/tasks/"+task+"/chat", map[string]any{"text": text})
}

// conductor is the task's conductor Run's id, status and dude_pause.
func (w *world) conductor(task string) (id, status, pause string) {
	w.t.Helper()
	_ = w.owner.QueryRow(context.Background(), `SELECT id, status::text, COALESCE(dude_pause, '') FROM runs
		WHERE task_id = $1 AND role = 'conductor' ORDER BY created_at DESC LIMIT 1`, task).Scan(&id, &status, &pause)
	return id, status, pause
}

// said is what a Run's agent said, each message whole, in order.
func (w *world) said(runID string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload->>'text' FROM events
		WHERE run_id = $1 AND event_type = 'agent.message' ORDER BY cursor`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) conductorSpec() *lux.Spec {
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.role"] == "conductor" {
			return &spec
		}
	}
	return nil
}

// phaseRuns is what the task's workflow made: how many phase Runs, and
// the workflow's step and status.
func (w *world) workflowState(task string) string {
	var n int
	var wf string
	_ = w.owner.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM runs WHERE task_id = $1 AND phase IS NOT NULL),
		(SELECT step || ' ' || status::text FROM workflow_runs WHERE task_id = $1)`, task).Scan(&n, &wf)
	return fmt.Sprintf("%d phase runs, workflow %s, task %s", n, wf, w.taskStatus(task))
}

// The first message in a delivered task's Chat starts its conductor: a Run
// with no phase, role conductor, at the task's head (its branch, writable),
// briefed by
// dude with the message; it answers. A second message reaches the same
// conductor as its next input. The task, its workflow and its branch are
// as they were.
func TestTheFirstMessageStartsTheConductorAndTheNextReachesIt(t *testing.T) {
	w := conductorWorld(t)
	task, log := w.delivered()
	before := w.workflowState(task)
	head := w.head(task)

	status, out := w.chat(task, "why is the max backoff 8s?")
	if status != 201 || out["created"] != true {
		t.Fatalf("first message: %d %v", status, out)
	}
	runID, _ := out["runId"].(string)
	var phase *string
	var role, prompt string
	var baseRefs map[string]string
	if err := w.owner.QueryRow(context.Background(), `SELECT phase::text, role::text, prompt, base_refs FROM runs WHERE id = $1`, runID).
		Scan(&phase, &role, &prompt, &baseRefs); err != nil {
		t.Fatal(err)
	}
	if phase != nil || role != "conductor" {
		t.Fatalf("the conductor: phase %v role %q", phase, role)
	}
	if baseRefs["target"] != head {
		t.Errorf("starts from %v, want the task's head %s", baseRefs, head)
	}
	if !strings.Contains(prompt, "## The task\n\n") || !strings.HasSuffix(prompt, "why is the max backoff 8s?") {
		t.Errorf("briefing:\n%s", prompt)
	}
	w.until("the conductor to answer", func() bool { return len(w.said(runID)) == 1 })
	first := w.said(runID)[0]
	key := "P-1 · " + task
	if !strings.Contains(first, key) || !strings.Contains(first, "why is the max backoff 8s?") {
		t.Errorf("answer %q does not quote the briefing's task line %q and the message", first, key)
	}

	// Its checkout: writable, on the task branch by name (lux's fast-forward
	// never switches branches), pushed only to a branch of its own.
	spec := w.conductorSpec()
	if spec == nil || spec.Git == nil || spec.Git.Push == nil || spec.Git.Push.Branch != "dude/"+task+"/run-"+runID {
		t.Fatalf("the conductor's spec: %+v", spec)
	}
	for _, r := range spec.Git.Repositories {
		if r.Push != nil || r.Ref != delivery.BranchFor(task, 1) {
			t.Errorf("repository %s: push %v at %s, want writable on the task branch", r.Name, r.Push, r.Ref)
		}
	}

	status, out = w.chat(task, "and does it retry POSTs?")
	if status != 200 || out["runId"] != runID || out["created"] != false {
		t.Fatalf("second message: %d %v, want the same conductor %s", status, out, runID)
	}
	w.until("the second answer", func() bool { return len(w.said(runID)) == 2 })
	if second := w.said(runID)[1]; !strings.Contains(second, "and does it retry POSTs?") || !strings.Contains(second, key) {
		t.Errorf("second answer %q", second)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message'`, runID); n != 2 {
		t.Errorf("%d chat.message events, want 2", n)
	}

	if after := w.workflowState(task); after != before {
		t.Errorf("the task moved: %s, then %s", before, after)
	}
	if got := w.gh.Log(delivery.BranchFor(task, 1)); strings.Join(got, "|") != strings.Join(log, "|") {
		t.Errorf("the branch moved: %v, then %v", log, got)
	}
}

// After its turn a conductor stays warm for its period, then dude parks
// it (dude_pause conductor), quietly: the task does not move. A message
// resumes it, timed with cause conductor, and it answers that message.
func TestAConductorParksAfterItsWarmPeriodAndAMessageResumesIt(t *testing.T) {
	w := conductorWorld(t)
	w.syncer.ConductorWarm = 400 * time.Millisecond
	task, log := w.delivered()
	before := w.workflowState(task)

	_, out := w.chat(task, "what changed?")
	runID, _ := out["runId"].(string)
	w.until("the answer", func() bool { return len(w.said(runID)) == 1 })
	// Warm: still running a moment after its answer.
	w.pump()
	if _, status, _ := w.conductor(task); status != "running" {
		t.Errorf("right after its answer the conductor is %s, want running (warm)", status)
	}
	w.until("the conductor to be parked", func() bool {
		_, status, pause := w.conductor(task)
		return status == "paused" && pause == "conductor"
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked' AND payload->>'reason' = 'conductor'`, runID); n != 1 {
		t.Errorf("%d run.parked for the conductor, want 1", n)
	}
	if after := w.workflowState(task); after != before {
		t.Errorf("parking moved the task: %s, then %s", before, after)
	}

	w.syncer.ConductorWarm = time.Hour
	status, out := w.chat(task, "and the tests?")
	if status != 200 || out["runId"] != runID {
		t.Fatalf("message to the parked conductor: %d %v", status, out)
	}
	var woken time.Time
	if err := w.owner.QueryRow(context.Background(), `SELECT created_at FROM directives WHERE id = $1`, out["directiveId"]).Scan(&woken); err != nil {
		t.Fatal(err)
	}
	w.oneTimedResume(runID, "conductor", woken, false)
	w.until("its answer to the message that resumed it", func() bool { return len(w.said(runID)) == 2 })
	if got := w.said(runID)[1]; !strings.Contains(got, "and the tests?") {
		t.Errorf("answer after the resume: %q", got)
	}
	if after := w.workflowState(task); after != before {
		t.Errorf("the task moved: %s, then %s", before, after)
	}
	if got := w.gh.Log(delivery.BranchFor(task, 1)); strings.Join(got, "|") != strings.Join(log, "|") {
		t.Errorf("the branch moved: %v, then %v", log, got)
	}
}

// Two people writing at once reach one conductor: the handler takes one
// message per task at a time, and the database holds one live conductor per task.
func TestOneConductorPerTaskWhoeverWritesAtOnce(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	const n = 8
	var wg sync.WaitGroup
	statuses := make([]int, n)
	runs := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, out := w.chat(task, fmt.Sprintf("message %d", i))
			statuses[i] = status
			runs[i], _ = out["runId"].(string)
		}()
	}
	wg.Wait()
	created := 0
	for i, s := range statuses {
		if s == 201 {
			created++
		} else if s != 200 {
			t.Errorf("message %d: %d", i, s)
		}
		if runs[i] != runs[0] {
			t.Errorf("message %d reached %s, message 0 %s", i, runs[i], runs[0])
		}
	}
	if created != 1 {
		t.Errorf("%d messages created a conductor, want 1", created)
	}
	if c := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); c != 1 {
		t.Errorf("%d conductors, want 1", c)
	}
	if c := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, runs[0]); c != n-1 {
		t.Errorf("%d messages queued for it, want %d (the first is its briefing)", c, n-1)
	}
	// The database refuses a second live one however it is made.
	if _, err := w.owner.Exec(context.Background(), `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role)
		VALUES ('run_second', $1, $2, $3, 1, 'conductor')`, w.org, w.project, task); err == nil ||
		!strings.Contains(err.Error(), "runs_live_conductor_idx") {
		t.Errorf("a second live conductor: %v", err)
	}
}

// A conductor never publishes and never moves the task's branch: its spec
// names no push and no repository it may push, its turn's end is never
// collected as a phase's is (so a push result — however it came — is
// never fast-forwarded), and whatever it commits stays in its checkout.
func TestAConductorNeverPublishesOrFastForwards(t *testing.T) {
	w := conductorWorld(t)
	task, log := w.delivered()
	// An agent that commits anyway.
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.role"] == "conductor" {
			b.Commit = map[string]string{"SNEAKY.md": "a conductor's edit\n"}
			b.Message = "A conductor's edit"
		}
		return b
	}
	_, out := w.chat(task, "please just fix it")
	runID, _ := out["runId"].(string)
	w.until("the answer", func() bool { return len(w.said(runID)) == 1 })
	// A push result as if lux had pushed it, and a branch to push to.
	head := w.head(task)
	mustExec(t, w.owner, `UPDATE runs SET push_branch = 'dude/x/run-y', push_request_id = 'push-y',
		push_result = jsonb_build_object('results', jsonb_build_array(jsonb_build_object('repo', 'target', 'branch', 'dude/x/run-y',
			'commit', $2::text, 'status', 'pushed'))) WHERE id = $1`, runID, head)
	for range 5 {
		w.pump()
	}
	var status string
	var heads string
	if err := w.owner.QueryRow(context.Background(), `SELECT status::text, heads::text FROM runs WHERE id = $1`, runID).Scan(&status, &heads); err != nil {
		t.Fatal(err)
	}
	if status != "running" || heads != "{}" {
		t.Errorf("the conductor was collected as a phase: status %s heads %s", status, heads)
	}
	if got := w.gh.Log(delivery.BranchFor(task, 1)); strings.Join(got, "|") != strings.Join(log, "|") {
		t.Errorf("the branch moved: %v, then %v", log, got)
	}
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.role"] == "conductor" && r.Pushed {
			t.Errorf("lux pushed the conductor's commit")
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'git.commit_created'`, runID); n != 0 {
		t.Errorf("%d commits recorded for the conductor", n)
	}
}

// luxRunOf is the lux Run a dude Run runs as.
func (w *world) luxRunOf(runID string) string {
	w.t.Helper()
	var id string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(lux_run_id, '') FROM runs WHERE id = $1`, runID).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// stopped stops a conductor's container on its own, as a dead host would,
// and waits, without sweeping, for its follower to record what lux said.
func (w *world) stopped(runID string) {
	w.t.Helper()
	w.lux.Crash(w.luxRunOf(runID))
	deadline := time.Now().Add(10 * time.Second)
	for w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'failed'`, runID) == 0 {
		if time.Now().After(deadline) {
			w.t.Fatalf("lux's stop of %s was never recorded:\n%s", runID, w.describeRuns())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A message Chat took for a conductor whose container then stopped, before
// the conductor read it, is not lost when the sweep ends that conductor:
// it goes to the next one, which answers it, and the first says where it went.
func TestAMessageForAConductorThatStoppedReachesTheNext(t *testing.T) {
	w := conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	task, _ := w.delivered()
	_, out := w.chat(task, "what changed?")
	first, _ := out["runId"].(string)
	w.until("the answer", func() bool { return len(w.said(first)) == 1 })

	status, out := w.chat(task, "and the tests?")
	if status != 200 || out["runId"] != first {
		t.Fatalf("the message: %d %v, want it queued for %s", status, out, first)
	}
	queued, _ := out["directiveId"].(string)
	w.stopped(first)

	w.until("a conductor to answer the message", func() bool {
		next, _, _ := w.conductor(task)
		if next == first {
			return false
		}
		said := w.said(next)
		return len(said) == 1 && strings.Contains(said[0], "and the tests?")
	})
	next, _, _ := w.conductor(task)
	var prompt string
	if err := w.owner.QueryRow(context.Background(), `SELECT prompt FROM runs WHERE id = $1`, next).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(prompt, "and the tests?") {
		t.Errorf("the next conductor's briefing does not end with the message:\n%s", prompt)
	}
	if _, st, _ := w.conductor(task); st == "completed" || st == "failed" {
		t.Errorf("the next conductor is %s", st)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, first); n != 1 {
		t.Errorf("the stopped conductor is not completed:\n%s", w.describeRuns())
	}
	// The first conductor's copy is settled, saying where it went.
	var failed string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->>'error', '') FROM events
		WHERE run_id = $1 AND event_type = 'run.directive.failed' AND payload->>'directiveId' = $2`, first, queued).Scan(&failed); err != nil {
		t.Fatalf("no failed delivery recorded for the stopped conductor's copy: %v", err)
	}
	if !strings.Contains(failed, next) {
		t.Errorf("the failed delivery says %q, not that %s has it", failed, next)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NULL AND failed_at IS NULL`, first); n != 0 {
		t.Errorf("%d messages left queued for the stopped conductor", n)
	}
}

// Chat never queues for a conductor whose container has stopped: the
// message starts the next conductor at once, which answers it.
func TestChatStartsTheNextConductorWhenTheLastHasStopped(t *testing.T) {
	w := conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	task, _ := w.delivered()
	_, out := w.chat(task, "what changed?")
	first, _ := out["runId"].(string)
	w.until("the answer", func() bool { return len(w.said(first)) == 1 })
	w.stopped(first)

	status, out := w.chat(task, "and the tests?")
	next, _ := out["runId"].(string)
	if status != 201 || out["created"] != true || next == first {
		t.Fatalf("a message after the stop: %d %v, want a new conductor", status, out)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, first); n != 0 {
		t.Errorf("%d messages queued for the stopped conductor", n)
	}
	w.until("the next conductor's answer", func() bool {
		said := w.said(next)
		return len(said) == 1 && strings.Contains(said[0], "and the tests?")
	})
}

// A conductor that fails mid-turn with a message queued for it hands the
// message to the next conductor; one a person aborted fails it, saying so.
func TestAMessageForAConductorThatFailedOrWasAbortedIsSettled(t *testing.T) {
	w := conductorWorld(t, fakeagent.HangModel)
	task := w.task()
	_, out := w.chat(task, "what changed?")
	first, _ := out["runId"].(string)
	w.until("the conductor to work", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, first) == 1
	})
	_, out = w.chat(task, "and the tests?")
	queued, _ := out["directiveId"].(string)
	w.lux.Crash(w.luxRunOf(first))
	w.until("the message to reach the next conductor", func() bool {
		next, _, _ := w.conductor(task)
		return next != first && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND prompt LIKE '%and the tests?'`, next) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, first); n != 1 {
		t.Errorf("the crashed conductor:\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NOT NULL`, queued); n != 1 {
		t.Errorf("the crashed conductor's copy is not settled")
	}

	second, _, _ := w.conductor(task)
	w.until("the second conductor to work", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, second) == 1
	})
	_, out = w.chat(task, "one more")
	queued, _ = out["directiveId"].(string)
	if status, _ := w.call("/internal/runs/"+second+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d", status)
	}
	w.until("the aborted conductor's message to be settled", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NOT NULL`, queued) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.failed'
		AND payload->>'directiveId' = $2 AND payload->>'error' LIKE '%was stopped%'`, second, queued); n != 1 {
		t.Errorf("no failed delivery saying the conductor was stopped")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 2 {
		t.Errorf("%d conductors after the abort, want 2: an abort starts none", n)
	}
}

// endedConductor is a delivered task whose conductor answered a message
// and then stopped, and was ended with nothing unread; and that conductor.
func (w *world) endedConductor() (task, ended string) {
	w.t.Helper()
	w.syncer.ConductorWarm = time.Hour
	task, _ = w.delivered()
	_, out := w.chat(task, "what changed?")
	ended, _ = out["runId"].(string)
	w.until("the answer", func() bool { return len(w.said(ended)) == 1 })
	w.stopped(ended)
	w.until("the stopped conductor to be ended", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, ended) == 1
	})
	return task, ended
}

// unread is a message by actor, with images, that a person sent conductor
// runID and it never read, as Chat or a steer records it: its directive,
// its images, and its event. Returns the directive.
func (w *world) unread(runID, actor, text string, images ...string) string {
	w.t.Helper()
	ctx := context.Background()
	var id string
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		ref := delivery.RunRef{Org: w.org, ProjectID: w.project, RunID: runID}
		if err := tx.QueryRow(ctx, `SELECT task_id FROM runs WHERE id = $1`, runID).Scan(&ref.TaskID); err != nil {
			return err
		}
		var err error
		if id, _, err = delivery.QueueDirective(ctx, tx, ref, delivery.Directive{Text: text, Scope: "run"}); err != nil {
			return err
		}
		attached, err := delivery.Attach(ctx, tx, ref.TaskID, id, images)
		if err != nil {
			return err
		}
		writer := delivery.Writer{ActorType: "human", ActorID: actor}
		if len(attached) == 0 {
			return delivery.ChatEvent(ctx, tx, ref, writer, map[string]any{"text": text, "directiveId": id})
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{Type: "run.steered", OrganizationID: w.org, ProjectID: w.project,
			TaskID: ref.TaskID, RunID: runID, ActorType: writer.ActorType, ActorID: actor, Source: ledger.SourceOrchestrator,
			CorrelationID: ref.TaskID, Payload: map[string]any{"directiveId": id, "text": text, "attachments": attached}})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// handedTo is the conductor an ended one's unread directive went to, ""
// for none, and the error its copy failed with.
func (w *world) handedTo(directive string) (next, why string) {
	w.t.Helper()
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->>'nextRunId', ''), payload->>'error' FROM events
		WHERE event_type = 'run.directive.failed' AND payload->>'directiveId' = $1`, directive).Scan(&next, &why)
	return next, why
}

func (w *world) nextConductor(task string) string {
	w.t.Helper()
	_, out := w.chat(task, "hello again")
	next, _ := out["runId"].(string)
	w.until("the next conductor's answer", func() bool { return len(w.said(next)) == 1 })
	return next
}

// A message handed on to a conductor a person paused asks for it back, as
// a message in Chat does: it is resumed, and answers the message.
func TestAMessageHandedToAPausedConductorResumesIt(t *testing.T) {
	w := conductorWorld(t)
	task, ended := w.endedConductor()
	next := w.nextConductor(task)
	if status, body := w.call("/internal/runs/"+next+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	w.until("the pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause IS NULL`, next) == 1
	})

	queued := w.unread(ended, "", "and the tests?")
	w.until("the paused conductor to answer the message handed to it", func() bool {
		said := w.said(next)
		return len(said) == 2 && strings.Contains(said[1], "and the tests?")
	})
	if to, why := w.handedTo(queued); to != next {
		t.Errorf("the ended conductor's copy failed with %q, next %q, want %s", why, to, next)
	}
}

// A message with images handed on to the task's live conductor reaches it
// with its images, the same bytes; a text-only one after it too, in order.
func TestAMessageWithImagesHandedToTheLiveConductorKeepsThem(t *testing.T) {
	w := conductorWorld(t)
	b := w.withImages()
	task, ended := w.endedConductor()
	next := w.nextConductor(task)

	w.upload(b, "att_layout_"+w.org, task, "layout.png", screenshot)
	image := w.unread(ended, "", "Explain this screenshot", "att_layout_"+w.org)
	words := w.unread(ended, "", "and the tests?")
	w.until("both messages answered", func() bool { return len(w.said(next)) == 3 })
	said := w.said(next)
	if !strings.Contains(said[1], "Explain this screenshot") || !strings.Contains(said[2], "and the tests?") {
		t.Errorf("answered out of order: %q", said[1:])
	}
	var carried string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'directiveId' FROM events WHERE run_id = $1
		AND event_type = 'chat.message' AND payload->>'text' = 'Explain this screenshot'`, next).Scan(&carried)
	luxNext := w.luxRunOf(next)
	got := w.lux.Attachments(luxNext)[carried]
	if len(got) != 1 || got[0].Name != "layout.png" || got[0].ContentType != "image/png" || !bytes.Equal(got[0].Data, screenshot) {
		t.Fatalf("the live conductor got %+v with the message (directive %s)", got, carried)
	}
	if bodies := w.lux.InputBodies(luxNext, carried); len(bodies) != 1 || !strings.Contains(bodies[0], "Explain this screenshot") {
		t.Errorf("/input for the handed-on message got %q", bodies)
	}
	for _, d := range []string{image, words} {
		if to, why := w.handedTo(d); to != next {
			t.Errorf("%s failed with %q, next %q, want %s", d, why, to, next)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message'
		AND payload->'attachments'->0->>'id' = $2`, next, "att_layout_"+w.org); n != 1 {
		t.Errorf("the next conductor's Chat does not show the image")
	}
}

// Two retries of a message with images that failed, both unread by a
// conductor that stopped, share its images: handed on to the live
// conductor, each copy carries them, in its delivery and in its Chat.
func TestRetriesSharingImagesHandedOnBothKeepThem(t *testing.T) {
	w := conductorWorld(t)
	b := w.withImages()
	task, ended := w.endedConductor()
	next := w.nextConductor(task)
	w.upload(b, "att_layout_"+w.org, task, "layout.png", screenshot)

	// The failed original and its two retries, recorded together so the
	// hand-over sees both retries at once.
	const text = "Explain this screenshot"
	ctx := context.Background()
	var retries []string
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		ref := delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: ended}
		original, _, err := delivery.QueueDirective(ctx, tx, ref, delivery.Directive{Text: text, Scope: "run"})
		if err != nil {
			return err
		}
		if _, err := delivery.Attach(ctx, tx, task, original, []string{"att_layout_" + w.org}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE directives SET failed_at = now(), error = 'lux refused it' WHERE id = $1`, original); err != nil {
			return err
		}
		for range 2 {
			id, _, err := delivery.QueueDirective(ctx, tx, ref, delivery.Directive{Text: text, Scope: "run", Supersedes: original})
			if err != nil {
				return err
			}
			if err := delivery.ChatEvent(ctx, tx, ref, delivery.Writer{ActorType: "human"},
				map[string]any{"text": text, "directiveId": id}); err != nil {
				return err
			}
			retries = append(retries, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	w.until("both retries answered by the live conductor", func() bool { return len(w.said(next)) == 3 })
	for _, d := range retries {
		if to, why := w.handedTo(d); to != next {
			t.Errorf("%s failed with %q, next %q, want %s", d, why, to, next)
		}
	}
	rows, err := w.owner.Query(ctx, `SELECT payload->>'directiveId', COALESCE(payload->'attachments'->0->>'id', '') FROM events
		WHERE run_id = $1 AND event_type = 'chat.message' AND payload->>'text' = $2 ORDER BY cursor`, next, text)
	if err != nil {
		t.Fatal(err)
	}
	type copied struct{ Directive, Image string }
	copies, err := pgx.CollectRows(rows, pgx.RowToStructByPos[copied])
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 2 {
		t.Fatalf("the live conductor's Chat has %d copies, want 2", len(copies))
	}
	luxNext := w.luxRunOf(next)
	for i, c := range copies {
		if c.Image != "att_layout_"+w.org {
			t.Errorf("copy %d's Chat message shows image %q", i+1, c.Image)
		}
		got := w.lux.Attachments(luxNext)[c.Directive]
		if len(got) != 1 || got[0].Name != "layout.png" || got[0].ContentType != "image/png" || !bytes.Equal(got[0].Data, screenshot) {
			t.Errorf("copy %d (directive %s) reached the live conductor with %+v", i+1, c.Directive, got)
		}
	}
}

// With no live conductor, the first message handed on would be a new
// conductor's briefing, which carries no images: a message with images
// fails, saying so, and goes nowhere; the text-only one after it starts
// the next conductor.
func TestAMessageWithImagesIsNotABriefing(t *testing.T) {
	w := conductorWorld(t)
	b := w.withImages()
	task, ended := w.endedConductor()
	w.upload(b, "att_layout_"+w.org, task, "layout.png", screenshot)
	image := w.unread(ended, "", "Explain this screenshot", "att_layout_"+w.org)
	words := w.unread(ended, "", "and the tests?")
	var next string
	w.until("the text-only message to start the next conductor", func() bool {
		next, _, _ = w.conductor(task)
		return next != ended && len(w.said(next)) == 1
	})
	if to, why := w.handedTo(image); to != "" || why != "the conductor stopped before reading it; its images were not passed on; send it again" {
		t.Errorf("the message with images: next %q, %q", to, why)
	}
	if to, _ := w.handedTo(words); to != next {
		t.Errorf("the text-only message went to %q, want %s", to, next)
	}
	if said := w.said(next)[0]; strings.Contains(said, "Explain this screenshot") || !strings.Contains(said, "and the tests?") {
		t.Errorf("the next conductor answered %q", said)
	}
}

// Two people's unread messages, handed on to a new conductor, keep their
// order and their writers: the first is its briefing's message, the
// second queued for it; it answers both.
func TestTwoMessagesHandedOnKeepTheirOrderAndWriters(t *testing.T) {
	w := conductorWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	task, ended := w.endedConductor()
	first := w.unread(ended, ana, "Ana: and the tests?")
	second := w.unread(ended, bo, "Bo: and the docs?")
	var next string
	w.until("both answered by the next conductor", func() bool {
		next, _, _ = w.conductor(task)
		return next != ended && len(w.said(next)) == 2
	})
	rows, err := w.owner.Query(context.Background(), `SELECT actor_id, payload->>'text' FROM events
		WHERE run_id = $1 AND event_type = 'chat.message' ORDER BY cursor`, next)
	if err != nil {
		t.Fatal(err)
	}
	type message struct{ Actor, Text string }
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[message])
	if err != nil {
		t.Fatal(err)
	}
	want := []message{{ana, "Ana: and the tests?"}, {bo, "Bo: and the docs?"}}
	if !slices.Equal(got, want) {
		t.Errorf("the next conductor's Chat: %v, want %v", got, want)
	}
	if said := w.said(next); !strings.Contains(said[1], "Bo: and the docs?") {
		t.Errorf("its second answer %q", said[1])
	}
	for _, d := range []string{first, second} {
		if to, why := w.handedTo(d); to != next {
			t.Errorf("%s failed with %q, next %q, want %s", d, why, to, next)
		}
	}
}

// Aborting a conductor during live delivery stops it alone and keeps
// nothing: the phase Run and the workflow go on. Aborting the phase Run
// stops delivery, and leaves the live conductor alone.
func TestAbortingAConductorOrAPhaseLeavesTheOtherAlone(t *testing.T) {
	w := conductorWorld(t)
	w.syncer.ConductorWarm = time.Hour
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.role"] == "conductor" {
			return scripted(spec)
		}
		return fakelux.Behaviour{Hang: true}
	}
	task := w.task()
	w.deliver(task)
	var implementer string
	w.until("the implementer to run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND status = 'running' AND lux_state = 'running'`, task).Scan(&implementer)
		return implementer != ""
	})
	// state is what an abort may change: each Run's status, keep and lux
	// end, the workflow's status and the task's.
	state := func(runID string) string {
		var s string
		_ = w.owner.QueryRow(context.Background(), `SELECT status::text || ' keep=' || keep::text
			|| ' stop=' || COALESCE(lux_stop_reason, '') || ' kept=' || (kept_until IS NOT NULL)::text FROM runs WHERE id = $1`, runID).Scan(&s)
		return s
	}
	workflow := func() string {
		var s string
		_ = w.owner.QueryRow(context.Background(), `SELECT status::text FROM workflow_runs WHERE task_id = $1`, task).Scan(&s)
		return s + " task " + w.taskStatus(task)
	}

	_, out := w.chat(task, "what changed?")
	first, _ := out["runId"].(string)
	w.until("the conductor's answer", func() bool { return len(w.said(first)) == 1 })
	implBefore, wfBefore := state(implementer), workflow()
	if status, body := w.call("/internal/runs/"+first+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort the conductor: %d %v", status, body)
	}
	w.until("the aborted conductor's lux Run to be ended", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason IS NOT NULL`, first) == 1
	})
	for range 3 {
		w.pump()
	}
	if got := state(first); got != "aborted keep=false stop=cancel kept=false" {
		t.Errorf("the aborted conductor: %s, want cancelled and never kept", got)
	}
	if got := state(implementer); got != implBefore {
		t.Errorf("aborting the conductor moved the implementer: %s, then %s", implBefore, got)
	}
	if got := workflow(); got != wfBefore {
		t.Errorf("aborting the conductor moved delivery: %s, then %s", wfBefore, got)
	}

	_, out = w.chat(task, "and now?")
	second, _ := out["runId"].(string)
	w.until("the next conductor's answer", func() bool { return len(w.said(second)) == 1 })
	if status, body := w.call("/internal/runs/"+implementer+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort the implementer: %d %v", status, body)
	}
	w.until("the implementer to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, implementer) == 1
	})
	if got := state(second); got != "running keep=false stop= kept=false" {
		t.Errorf("aborting the implementer touched the live conductor: %s", got)
	}
	if status, out := w.chat(task, "still there?"); status != 200 || out["runId"] != second {
		t.Errorf("a message after the phase abort: %d %v, want it for %s", status, out, second)
	}
}

// A conductor's question is the usual question: the task waits on the
// person, and the answer in Chat puts it back where it was — a conductor
// changes nothing about the task.
func TestAConductorsQuestionIsAnsweredInChat(t *testing.T) {
	w := conductorWorld(t, fakeagent.AskModel)
	w.withTools()
	task, _ := w.delivered()
	was := w.taskStatus(task)
	_, out := w.chat(task, "can you change it?")
	runID, _ := out["runId"].(string)
	w.until("its question", func() bool { return w.taskStatus(task) == "awaiting_input" })
	status, out := w.chat(task, "Yes")
	if status != 200 || out["questionId"] == nil {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if got := w.taskStatus(task); got != was {
		t.Errorf("after the answer the task is %s, want %s again", got, was)
	}
	w.until("its answer", func() bool { return len(w.said(runID)) == 2 })
	if got := w.said(runID)[1]; !strings.Contains(got, "Make it a follow-up task?") {
		t.Errorf("after the answer it said %q", got)
	}
}

// A conductor quiet mid-turn is nudged and then parked as any agent is,
// but its task stays as it was, parked and resumed: a conductor changes
// nothing about the task.
func TestAnIdleConductorsParkLeavesTheTaskAlone(t *testing.T) {
	w := conductorWorld(t)
	w.syncer.IdleAfter = 300 * time.Millisecond
	task, _ := w.delivered()
	// Silent even after the nudge: its turn is taken and it says nothing.
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	was := w.taskStatus(task)
	_, out := w.chat(task, "what changed?")
	runID, _ := out["runId"].(string)
	w.until("the conductor to be parked as idle", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'idle'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.idle_nudged'`, runID); n != 1 {
		t.Errorf("%d nudges before the park, want 1", n)
	}
	if got := w.taskStatus(task); got != was {
		t.Errorf("parking an idle conductor moved the task: %s, then %s", was, got)
	}
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the resume", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
	if got := w.taskStatus(task); got != was {
		t.Errorf("resuming the conductor moved the task: %s, then %s", was, got)
	}
}

// conductorQuestion is the open question of the task's conductor.
func (w *world) conductorQuestion(task string) string {
	var id string
	_ = w.owner.QueryRow(context.Background(), `SELECT q.id FROM questions q JOIN runs r ON r.id = q.run_id
		WHERE q.task_id = $1 AND r.role = 'conductor' AND q.status = 'open'`, task).Scan(&id)
	return id
}

// A conductor's question asked while the task already waits on another
// agent's question moved nothing, so its answer moves nothing: the task
// still waits on the implementer's.
func TestAnsweringAConductorLeavesAnotherAgentsQuestionWaiting(t *testing.T) {
	w := conductorWorld(t, fakeagent.AskModel)
	w.withTools()
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.role"] {
		case "conductor":
			return scripted(spec)
		case "implementer":
			return fakelux.Behaviour{Ask: fakeagent.Question, Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Hang: true}
	}
	task := w.task()
	w.deliver(task)
	w.until("the implementer's question", func() bool { return w.taskStatus(task) == "awaiting_input" })
	implementers := w.questionID(task)

	w.chat(task, "can you change it?")
	w.until("the conductor's question", func() bool { return w.conductorQuestion(task) != "" })
	if status, out := w.chat(task, "No"); status != 200 || out["questionId"] == nil {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if got := w.taskStatus(task); got != "awaiting_input" {
		t.Errorf("after the conductor's answer the task is %s, want awaiting_input: the implementer still asks", got)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'open'`, implementers); n != 1 {
		t.Errorf("the implementer's question is no longer open")
	}
}

// A conductor's question put the task in awaiting_input from review; then
// delivery moved it on and a newer wait took it back to awaiting_input.
// That wait is not the conductor's to end: its answer leaves the task
// waiting. Answered while its own wait still holds, the task goes back.
func TestAnsweringAConductorRestoresOnlyTheWaitItRaised(t *testing.T) {
	w := conductorWorld(t, fakeagent.AskModel)
	w.withTools()
	task, _ := w.delivered()
	was := w.taskStatus(task)
	w.chat(task, "can you change it?")
	w.until("its question", func() bool { return w.conductorQuestion(task) != "" && w.taskStatus(task) == "awaiting_input" })

	// Delivery moves on, and a newer wait (an escalation) is raised.
	move := func(from, to, why string) {
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, task, from, to, why)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	move("awaiting_input", "running", "delivery went on")
	move("running", "awaiting_input", "an escalation")
	if status, out := w.chat(task, "No"); status != 200 || out["questionId"] == nil {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if got := w.taskStatus(task); got != "awaiting_input" {
		t.Errorf("the conductor's answer ended a newer wait: the task is %s", got)
	}

	// Its own wait, still current: the answer puts the task back.
	other := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET status = $2 WHERE id = $1`, other, was)
	w.chat(other, "can you change it?")
	w.until("its question", func() bool { return w.conductorQuestion(other) != "" && w.taskStatus(other) == "awaiting_input" })
	if status, out := w.chat(other, "No"); status != 200 || out["questionId"] == nil {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if got := w.taskStatus(other); got != was {
		t.Errorf("after the answer the task is %s, want %s again", got, was)
	}
}

// luxSpecOf is the lux spec of the task's Run in phase.
func (w *world) luxSpecOf(phase string) string {
	w.t.Helper()
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.phase"] == phase {
			return string(r.Spec)
		}
	}
	w.t.Fatalf("no lux Run in phase %s", phase)
	return ""
}

// pausePhase is a person pausing the Run through the API; the pause is
// pending until the syncer has lux stop it.
func (w *world) pausePhase(runID string) {
	w.t.Helper()
	if status, body := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		w.t.Fatalf("pause: %d %v", status, body)
	}
}

// personallyPaused says whether a person's pause of the Run took effect.
func (w *world) personallyPaused(runID string) bool {
	return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause IS NULL AND control = 'none'`, runID) == 1
}

// resumePhase is a person resuming the Run through the API, until it runs.
func (w *world) resumePhase(runID string) {
	w.t.Helper()
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		w.t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the Run to run again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
}

// The conductor's question raised the task's current wait, and something
// else then came to wait on a person without moving the task. Answering
// the conductor leaves the task waiting; settling that blocker, the last,
// puts it back to the status before the conductor's wait.
func TestAConductorsWaitEndsWhenItsLastBlockerSettles(t *testing.T) {
	anotherAgentsQuestion := func(w *world, task, implementer string) func() {
		var qid string
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			var err error
			qid, err = delivery.AskTx(context.Background(), tx,
				delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: implementer}, "Which file?", nil)
			return err
		}); err != nil {
			w.t.Fatal(err)
		}
		return func() {
			if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "a.md"}); status != 200 {
				w.t.Fatalf("answer the implementer: %d %v", status, body)
			}
		}
	}
	for _, c := range []struct {
		name string
		// from is the status the task is moved to before the conductor's
		// question; "" leaves it running.
		from string
		// block makes the task wait on a person for something else, and
		// returns how a person settles it.
		block func(w *world, task, implementer string) (settle func())
	}{
		{"another agent's question", "", anotherAgentsQuestion},
		{"another agent's question, from review", "review", anotherAgentsQuestion},
		{"a blocking repository request", "", func(w *world, task, implementer string) func() {
			mustExec(w.t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
				VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
			status, body := w.callTool(w.syncer.Agent.ToolsURL, w.luxSpecOf("implement"), "request_repository",
				`{"repository":"web","reason":"the client","wait":true}`)
			var req struct{ RequestID string }
			_ = json.Unmarshal([]byte(body), &req)
			if status != 200 || req.RequestID == "" {
				w.t.Fatalf("request_repository: %d %s", status, body)
			}
			return func() {
				if status, body := w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": false}); status != 200 {
					w.t.Fatalf("deny: %d %v", status, body)
				}
			}
		}},
		{"an escalation", "", func(w *world, task, implementer string) func() {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{escalation}',
				'{"reason":"stuck","step":"fix"}') WHERE task_id = $1`, task)
			return func() {
				if status, body := w.call("/internal/tasks/"+task+"/decide", map[string]any{"action": "stop"}); status != 200 {
					w.t.Fatalf("decide: %d %v", status, body)
				}
			}
		}},
		{"an idle-parked phase agent", "", func(w *world, task, implementer string) func() {
			w.syncer.IdleAfter = 300 * time.Millisecond
			w.until("the implementer to be parked as idle", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'idle'`, implementer) == 1
			})
			w.syncer.IdleAfter = time.Hour
			return func() {
				if status, body := w.call("/internal/runs/"+implementer+"/resume", map[string]any{}); status != 200 {
					w.t.Fatalf("resume: %d %v", status, body)
				}
				w.until("the implementer to run again", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, implementer) == 1
				})
			}
		}},
		{"a phase agent a person paused", "", func(w *world, task, implementer string) func() {
			w.pausePhase(implementer)
			w.until("the implementer to be paused", func() bool { return w.personallyPaused(implementer) })
			return func() { w.resumePhase(implementer) }
		}},
		{"a person's pause still pending", "", func(w *world, task, implementer string) func() {
			w.pausePhase(implementer)
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'
				AND control = 'pause_graceful' AND dude_pause IS NULL`, implementer); n != 1 {
				w.t.Fatalf("the pause is not pending")
			}
			return func() {
				w.until("the implementer to be paused", func() bool { return w.personallyPaused(implementer) })
				if got := w.taskStatus(task); got != "awaiting_input" {
					w.t.Fatalf("the pause taking effect ended the wait: the task is %s", got)
				}
				w.resumePhase(implementer)
			}
		}},
		{"a person's resume still pending", "", func(w *world, task, implementer string) func() {
			w.pausePhase(implementer)
			w.until("the implementer to be paused", func() bool { return w.personallyPaused(implementer) })
			if status, body := w.call("/internal/runs/"+implementer+"/resume", map[string]any{}); status != 200 {
				w.t.Fatalf("resume: %d %v", status, body)
			}
			return func() {
				w.until("the implementer to run again", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, implementer) == 1
				})
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := conductorWorld(t, fakeagent.AskModel)
			w.withTools()
			w.syncer.IdleAfter = time.Hour
			scripted := w.lux.Decide
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				if labels, _ := spec["labels"].(map[string]any); labels["dude.role"] == "conductor" {
					return scripted(spec)
				}
				return fakelux.Behaviour{Hang: true}
			}
			task := w.task()
			w.deliver(task)
			var implementer string
			w.until("the implementer to run", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'
					AND status = 'running' AND lux_state = 'running'`, task).Scan(&implementer)
				return implementer != ""
			})
			if c.from != "" {
				if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
					_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, task, "running", c.from, "the work is in "+c.from)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			was := w.taskStatus(task)
			if c.from != "" && was != c.from {
				t.Fatalf("the task is %s, want %s", was, c.from)
			}
			w.chat(task, "can you change it?")
			w.until("the conductor's question", func() bool {
				return w.conductorQuestion(task) != "" && w.taskStatus(task) == "awaiting_input"
			})

			settle := c.block(w, task, implementer)
			if got := w.taskStatus(task); got != "awaiting_input" {
				t.Fatalf("the blocker moved the task: %s", got)
			}
			if status, out := w.chat(task, "No"); status != 200 || out["questionId"] == nil {
				t.Fatalf("the answer: %d %v", status, out)
			}
			if got := w.taskStatus(task); got != "awaiting_input" {
				t.Fatalf("answering the conductor ended the wait while %s still waits on a person: the task is %s", c.name, got)
			}
			settle()
			if got := w.taskStatus(task); got != was {
				t.Errorf("once %s was settled the task is %s, want %s again", c.name, got, was)
			}
		})
	}
}
