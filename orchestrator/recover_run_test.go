package orchestrator_test

// The conductor's recover_run, through the orchestrator's real code: the
// tool called with the conductor's token, the recovery worker reading the
// fake lux's checkpoint of the dead Run, a publisher Run cloned from it,
// the push landing on the fake GitHub's repository, and the wake.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// hangingWriters has the task's implementers hang at work, so a test can
// commit in the checkout as the agent would and then end the Run.
func (w *world) hangingWriters() {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit, b.Hang, b.NeverWake = nil, true, true
		}
		return b
	}
}

// deadWriter is a conducted task whose implementer committed content to
// path in its checkout and was then stopped by lux at its time limit:
// failed, not kept, its lux Run terminated. Returns the task, its
// implementer and the commit, which nothing published.
func (w *world) deadWriter(path, content string) (task, run, commit string) {
	w.t.Helper()
	w.hangingWriters()
	task = w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	var luxID string
	w.until("the implementer at work, its checkout started", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs WHERE task_id = $1
			AND phase = 'implement' AND status = 'running' AND lux_state = 'running' AND base_shas ? 'target'`, task).Scan(&run, &luxID)
		return luxID != ""
	})
	work, err := w.lux.Checkout(luxID, "target")
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(work, path)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, path), []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=agent", "-c", "user.email=a@x", "commit", "-q", "-m", "work in progress"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			w.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	out, _ := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	commit = strings.TrimSpace(string(out))
	w.lux.Timeout(luxID)
	w.until("the implementer failed at its limit and its lux Run terminated", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed' AND NOT keep AND lux_stop_reason = 'cancel'`, run) == 1
	})
	return task, run, commit
}

// branchAt is where branch is in the fake GitHub's repository, "" when it
// is not there.
func (w *world) branchAt(branch string) string {
	out, err := exec.Command("git", "-C", w.gh.Repo, "rev-parse", "-q", "--verify", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// publisherOf is the fake lux's publisher Run of a recovery.
func (w *world) publisherOf(recovery string) *fakelux.Run {
	for _, r := range w.lux.Runs() {
		if submitted(w.t, r).Labels["dude.recovery"] == recovery {
			return r
		}
	}
	return nil
}

// pushesOf counts the publisher's git.push results that moved a branch.
func (w *world) pushesOf(luxID string) int {
	n := 0
	frames, err := w.syncer.Lux.Events(context.Background(), luxID, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, f := range frames {
		var p lux.PushResult
		if f.EventType != "git.push" || json.Unmarshal(f.EventData, &p) != nil {
			continue
		}
		for _, r := range p.Results {
			if r.Status == "pushed" {
				n++
			}
		}
	}
	return n
}

func (w *world) recoveryStatus(id string) string {
	return w.str(`SELECT status FROM run_recoveries WHERE id = $1`, id)
}

func (w *world) recoveredWakes(task string) int {
	return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'recovered'`, task)
}

// An implementer lux stopped at its time limit had committed work it never
// published. recover_run pushes that commit, from lux's checkpoint, to the
// Run's recovered branch: the ledger says so on the dead Run, the
// conductor is woken once, the publisher is terminated, and the task
// branch has not moved.
func TestRecoverRunPushesATimedOutWritersCommitsToItsOwnBranch(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	taskBranch := w.branchAt("dude/" + task + "/attempt-1")
	out := w.must(task, "recover_run", `{"run":"`+run+`","note":"its work is not lost"}`)
	branch := "dude/" + task + "/recovered-" + run
	id, _ := out["recoveryId"].(string)
	if !strings.HasPrefix(id, "rcv_") || out["run"] != run || out["branch"] != branch || out["status"] != "requested" || out["next"] == "" {
		t.Fatalf("recover_run answered %v", out)
	}
	w.until("the recovery done", func() bool { return w.recoveryStatus(id) == "done" })
	if got := w.branchAt(branch); got != commit {
		t.Errorf("%s is at %q, want the dead Run's commit %s", branch, got, commit)
	}
	if got := w.branchAt("dude/" + task + "/attempt-1"); got != taskBranch {
		t.Errorf("the task branch moved: %q → %q", taskBranch, got)
	}
	var payload map[string]any
	if err := w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = 'run.recovered'`, run).
		Scan(&payload); err != nil {
		t.Fatal(err)
	}
	results, _ := payload["results"].([]any)
	first, _ := results[0].(map[string]any)
	if payload["recoveryId"] != id || payload["branch"] != branch || payload["status"] != "done" || len(results) != 1 ||
		first["repo"] != "target" || first["status"] != "pushed" || first["commit"] != commit {
		t.Errorf("run.recovered %v", payload)
	}
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'recovered'
		AND key = $2`, task, "recover:"+id).Scan(&line); err != nil {
		t.Fatalf("no recovered wake: %v", err)
	}
	if !strings.Contains(line, branch) || !strings.Contains(line, commit[:12]) {
		t.Errorf("the wake says %q", line)
	}
	pub := w.publisherOf(id)
	if pub == nil {
		t.Fatal("no publisher Run")
	}
	spec := submitted(t, pub)
	if spec.Workload.Adapter != "generic" || spec.ResumePolicy != "never" || spec.Git.Push.Branch != branch ||
		len(spec.Git.Repositories) != 1 || spec.Git.Repositories[0].From == nil || spec.Git.Repositories[0].From.Repo != "target" {
		t.Errorf("the publisher's spec: %+v", spec)
	}
	for _, s := range spec.Secrets {
		if s.As != "none" {
			t.Errorf("the publisher's secret %s is %q to the container", s.Name, s.As)
		}
	}
	w.until("the publisher terminated", func() bool { return w.luxCalls(pub.ID, "cancel") > 0 })
	for range 3 {
		w.pump()
	}
	if n := w.recoveredWakes(task); n != 1 {
		t.Errorf("%d recovered wakes", n)
	}
}

// A Run that committed nothing beyond where it started has nothing to
// recover: the recovery fails saying so, nothing is pushed, and the
// conductor is told.
func TestRecoverRunReportsNothingWhenTheRunCommittedNothing(t *testing.T) {
	w := conducting(t)
	w.hangingWriters()
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	var run, luxID string
	w.until("the implementer at work", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs WHERE task_id = $1
			AND phase = 'implement' AND lux_state = 'running' AND base_shas ? 'target'`, task).Scan(&run, &luxID)
		return luxID != ""
	})
	w.lux.Timeout(luxID)
	w.until("the implementer failed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed' AND lux_stop_reason = 'cancel'`, run) == 1
	})
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.until("the recovery failed", func() bool { return w.recoveryStatus(id) == "failed" })
	if why := w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id); !strings.Contains(why, "nothing committed") {
		t.Errorf("failed saying %q", why)
	}
	if w.publisherOf(id) != nil {
		t.Error("a publisher was started for nothing")
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("a branch was pushed: %s", got)
	}
	if line := w.str(`SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'recovered'`, task); !strings.Contains(line, "nothing committed") {
		t.Errorf("the wake says %q", line)
	}
}

// The dead Run was given a project secret and committed its value: the
// recovery reads its commits before any push, finds the secret, and is
// refused: nothing reaches GitHub, the dead Run's ledger names the secret
// and file (never the value), and the conductor is told.
func TestRecoverRunScansForSecretsBeforePushing(t *testing.T) {
	w := conducting(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	task, run, _ := w.deadWriter("config/app.toml", "password = "+implToken+"\n")
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.until("the recovery refused", func() bool { return w.recoveryStatus(id) == "refused" })
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("the recovered branch reached GitHub at %s", got)
	}
	if pub := w.publisherOf(id); pub == nil || len(pub.PushCredentials) != 0 {
		t.Errorf("lux was asked to push the publisher: %v", pub)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
		AND payload->'secrets' = '["IMPL_TOKEN"]' AND payload->'files' = '["config/app.toml"]' AND payload->>'recoveryId' = $2`, run, id); n != 1 {
		t.Errorf("%d run.push.refused naming the secret and file", n)
	}
	if line := w.str(`SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'recovered'`, task); !strings.Contains(line, "IMPL_TOKEN") {
		t.Errorf("the wake says %q", line)
	}
	w.assertTaskHoldsNo(task, implToken)
}

// A worker that asked lux to push and died before it recorded that: its
// claim lapses, the next worker asks again under the same request id, and
// the branch is pushed once, the conductor woken once.
func TestRecoverRunIsIdempotentAndSurvivesAWorkerCrash(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	ctx := context.Background()
	settle := func() {
		mustExec(t, w.owner, `UPDATE run_recoveries SET next_attempt_at = NULL WHERE id = $1`, id)
		if _, err := w.syncer.SettleRecoveries(ctx); err != nil {
			t.Fatal(err)
		}
	}
	settle()
	if s := w.recoveryStatus(id); s != "submitted" {
		t.Fatalf("after the first pass: %s", s)
	}
	pub := w.publisherOf(id)
	w.until("the publisher running", func() bool { return w.lux.State(pub.ID) == "running" })
	// The worker that died: it claimed the recovery, lux took its push (held,
	// unreported), and nothing was recorded; its claim then lapsed.
	w.lux.HoldPushes = true
	mustExec(t, w.owner, `UPDATE run_recoveries SET claim_token = 'dead', claimed_until = now() - interval '1 second' WHERE id = $1`, id)
	if err := w.syncer.Lux.Push(ctx, pub.ID, id, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	if s := w.recoveryStatus(id); s != "asked" {
		t.Fatalf("after the second worker: %s", s)
	}
	w.lux.ReleasePushes()
	w.until("the recovery done", func() bool {
		settle()
		return w.recoveryStatus(id) == "done"
	})
	for range 3 {
		settle()
		w.pump()
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != commit {
		t.Errorf("the branch is at %q, want %s", got, commit)
	}
	if n := w.pushesOf(pub.ID); n != 1 {
		t.Errorf("the branch was pushed %d times", n)
	}
	if n := w.recoveredWakes(task); n != 1 {
		t.Errorf("%d recovered wakes", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.recovered'`, run); n != 1 {
		t.Errorf("%d run.recovered", n)
	}
	// Asked again: the same recovery, done; nothing more is pushed.
	again := w.must(task, "recover_run", `{"run":"`+run+`"}`)
	if again["recoveryId"] != id || again["status"] != "done" {
		t.Errorf("asked again: %v", again)
	}
}

// A person's Stop holds the recovery's wake until the task is picked up.
func TestARecoveryOfAStoppedTaskWakesOnlyAfterThePickUp(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	ana := w.ownedBy(task, "Ana")
	conductor, _, _ := w.conductor(task)
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.stop(ana, task, map[string]any{"note": "hold on"})
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	w.noRecoveries = false
	w.until("the recovery done", func() bool { return w.recoveryStatus(id) == "done" })
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != commit {
		t.Errorf("the branch is at %q", got)
	}
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'recovered' AND delivered_at IS NULL`, task); n != 1 {
		t.Fatalf("%d recovered reasons held", n)
	}
	if got := w.conductorRuns(task); len(got) != 1 {
		t.Fatalf("a recovery on a stopped task started a conductor: %v", got)
	}
	// Its implementer is not kept (lux stopped it at its limit): the task is
	// tried again, under a conductor that hears the reason.
	if got, _ := w.options(task); !slices.Contains(got, "retry") {
		t.Fatalf("the stopped task offers %v", got)
	}
	w.pickUp(ana, task, map[string]any{"action": "retry"})
	w.until("the reason delivered", func() bool {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'recovered' AND delivered_at IS NOT NULL`, task) == 1
	})
}

// A recovery asked for before the task was closed still pushes: the work
// is kept; nobody is woken, as nobody is left to tell.
func TestARecoveryOfATaskThatEndsStillPushesAndWakesNobody(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	ana := w.ownedBy(task, "Ana")
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.noRecoveries = false
	ctx := context.Background()
	// Its publisher asked of lux, then the task closed.
	mustExec(t, w.owner, `UPDATE run_recoveries SET next_attempt_at = NULL WHERE id = $1`, id)
	if _, err := w.syncer.SettleRecoveries(ctx); err != nil {
		t.Fatal(err)
	}
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	w.until("the recovery done", func() bool { return w.recoveryStatus(id) == "done" })
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.recovered'`, run); n != 1 {
		t.Errorf("%d run.recovered", n)
	}
	if n := w.recoveredWakes(task); n != 0 {
		t.Errorf("%d recovered wakes for a closed task", n)
	}
	_ = commit
}

// The recovered branch is the task's, as its per-Run branches are: kept
// while a Stop waits on a pick-up, deleted from GitHub once the task ends.
func TestARecoveredBranchIsDeletedWithTheTasksPerRunBranches(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	ana := w.ownedBy(task, "Ana")
	conductor, _, _ := w.conductor(task)
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	branch := "dude/" + task + "/recovered-" + run
	w.until("the recovery done", func() bool { return w.recoveryStatus(id) == "done" })
	if got := w.branchAt(branch); got != commit {
		t.Fatalf("%s is at %q", branch, got)
	}
	w.stop(ana, task, map[string]any{"note": "hold on"})
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	for range 3 {
		w.pump()
	}
	if got := w.branchAt(branch); got != commit {
		t.Fatalf("a Stop waiting on its pick-up deleted the recovered branch: %q", got)
	}
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	w.until("the recovered branch deleted", func() bool { return w.branchAt(branch) == "" })
	if n := w.count(`SELECT count(*) FROM run_recoveries WHERE id = $1 AND branch_released_at IS NOT NULL`, id); n != 1 {
		t.Errorf("the recovery does not record its branch released")
	}
}
