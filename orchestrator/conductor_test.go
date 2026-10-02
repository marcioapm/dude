package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// conductorWorld is a world whose project configures the conductor with
// the scripted agent (model is "fake/scripted" unless given).
func conductorWorld(t *testing.T, model ...string) *world {
	w := newWorld(t)
	m := "fake/scripted"
	if len(model) > 0 {
		m = model[0]
	}
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || jsonb_build_object('conductor', jsonb_build_object('model', $2::text))
		WHERE id = $1`, w.project, m)
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
// with no phase, role conductor, at the task's head, read-only, briefed by
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

	// Read-only, by its spec: nothing pushed, nothing pushable.
	spec := w.conductorSpec()
	if spec == nil || spec.Git == nil || spec.Git.Push != nil {
		t.Fatalf("the conductor's spec pushes: %+v", spec)
	}
	for _, r := range spec.Git.Repositories {
		if r.Push == nil || *r.Push || r.Ref != head {
			t.Errorf("repository %s: push %v at %s, want read-only at %s", r.Name, r.Push, r.Ref, head)
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
