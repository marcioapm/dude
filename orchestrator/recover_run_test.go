package orchestrator_test

// The conductor's recover_run, through the orchestrator's real code: the
// tool called with the conductor's token, the recovery worker reading the
// fake lux's checkpoint of the dead Run, a publisher Run cloned from it,
// the push landing on the fake GitHub's repository, and the wake.

import (
	"context"
	"encoding/json"
	"maps"
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

// hangingWriter is a conducted task whose implementer is at work, hanging,
// its checkout started. Returns the task, the implementer and its lux Run.
func (w *world) hangingWriter() (task, run, luxID string) {
	w.t.Helper()
	w.hangingWriters()
	task = w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer at work, its checkout started", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs WHERE task_id = $1
			AND phase = 'implement' AND status = 'running' AND lux_state = 'running' AND base_shas ? 'target'`, task).Scan(&run, &luxID)
		return luxID != ""
	})
	return task, run, luxID
}

// timedOut has lux stop the implementer at its time limit: failed, not
// kept, its lux Run terminated.
func (w *world) timedOut(run, luxID string) {
	w.t.Helper()
	w.lux.Timeout(luxID)
	w.until("the implementer failed at its limit and its lux Run terminated", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed' AND NOT keep AND lux_stop_reason = 'cancel'`, run) == 1
	})
}

// deadWriter is a conducted task whose implementer committed content to
// path in its checkout and was then stopped by lux at its time limit.
// Returns the task, its implementer and the commit, which nothing
// published.
func (w *world) deadWriter(path, content string) (task, run, commit string) {
	w.t.Helper()
	task, run, luxID := w.hangingWriter()
	work, err := w.lux.Checkout(luxID, "target")
	if err != nil {
		w.t.Fatal(err)
	}
	commit = w.commit(work, path, content)
	w.timedOut(run, luxID)
	return task, run, commit
}

// commit commits content to path in the checkout, as the agent would, and
// returns the commit.
func (w *world) commit(work, path, content string) string {
	w.t.Helper()
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
	return strings.TrimSpace(string(out))
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

// pushesOf is the set of request ids the publisher's pushes were asked
// under, as lux reported them (git.push): one push asked twice under the
// same id is the same push, whatever each reported.
func (w *world) pushesOf(luxID string) map[string]bool {
	ids := map[string]bool{}
	frames, err := w.syncer.Lux.Events(context.Background(), luxID, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, f := range frames {
		var p lux.PushResult
		if f.EventType != "git.push" || json.Unmarshal(f.EventData, &p) != nil {
			continue
		}
		ids[p.RequestID] = true
	}
	return ids
}

func (w *world) recoveryStatus(id string) string {
	return w.str(`SELECT status FROM run_recoveries WHERE id = $1`, id)
}

// recoveryDue has the recovery's next attempt due now.
func (w *world) recoveryDue(id string) {
	mustExec(w.t, w.owner, `UPDATE run_recoveries SET next_attempt_at = NULL WHERE id = $1`, id)
}

// pastGiveUp has the recovery's step started past its give-up
// (recoveryStartGiveUp, ten minutes), and due now.
func (w *world) pastGiveUp(id string) {
	mustExec(w.t, w.owner, `UPDATE run_recoveries SET stepped_at = now() - interval '11 minutes', next_attempt_at = NULL WHERE id = $1`, id)
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
	task, run, luxID := w.hangingWriter()
	w.timedOut(run, luxID)
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

	// Clean commits, the secret in the branch's name: refused as well, the
	// branch named where it was found, nothing on GitHub.
	t.Run("in the branch name", func(t *testing.T) {
		w := conducting(t)
		const value = "BranchSecret_0123456789"
		w.grant("BRANCH_TOKEN", value, "implementer")
		task, run, _ := w.deadWriter("WIP.md", "half of it\n")
		branch := "dude/" + task + "/recovered-" + value
		id, _ := w.must(task, "recover_run", `{"run":"`+run+`","branch":"`+branch+`"}`)["recoveryId"].(string)
		w.until("the recovery refused", func() bool { return w.recoveryStatus(id) == "refused" })
		if got := w.branchAt(branch); got != "" {
			t.Errorf("the recovered branch reached GitHub at %s", got)
		}
		if pub := w.publisherOf(id); pub == nil || len(pub.PushCredentials) != 0 {
			t.Errorf("lux was asked to push the publisher: %v", pub)
		}
		if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
			AND payload->'secrets' = '["BRANCH_TOKEN"]' AND payload->'files' = '["the branch name"]' AND payload->>'recoveryId' = $2`, run, id); n != 1 {
			t.Errorf("%d run.push.refused naming the secret and the branch name", n)
		}
	})
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
		w.recoveryDue(id)
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
	if got := w.pushesOf(pub.ID); !maps.Equal(got, map[string]bool{id: true}) {
		t.Errorf("the publisher was asked to push under %v, want only the recovery's id %s", slices.Sorted(maps.Keys(got)), id)
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
	task, run, _ := w.deadWriter("WIP.md", "half of it\n")
	ana := w.ownedBy(task, "Ana")
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.noRecoveries = false
	ctx := context.Background()
	// Its publisher asked of lux, then the task closed.
	w.recoveryDue(id)
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
}

// A writer lux stopped once (a move: its checkpoint taken then) went on
// and committed more. The conductor restarts it and asks for its recovery
// at once, before lux has ended it: the recovery waits for lux to end the
// Run, and pushes its latest commit, never the earlier checkpoint's.
func TestRecoverRunWaitsForLuxToEndTheRunBeforeReadingItsCheckpoints(t *testing.T) {
	w := conducting(t)
	task, run, luxID := w.hangingWriter()
	work, err := w.lux.Checkout(luxID, "target")
	if err != nil {
		t.Fatal(err)
	}
	early := w.commit(work, "WIP.md", "half of it\n")
	w.lux.Move(luxID, "preempt")
	w.until("the implementer back at work after its move", func() bool { return w.lux.State(luxID) == "running" })
	if work, err = w.lux.Checkout(luxID, "target"); err != nil {
		t.Fatal(err)
	}
	latest := w.commit(work, "WIP.md", "all of it\n")
	// Both asked with nothing swept between: the syncer has not yet told lux.
	spec := w.conductorSpecOf(task)
	w.noRecoveries = true
	if status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "restart_run", `{"run":"`+run+`","note":"again"}`); status != 200 {
		t.Fatalf("restart_run: %d %s", status, body)
	}
	status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "recover_run", `{"run":"`+run+`"}`)
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	id, _ := out["recoveryId"].(string)
	if status != 200 || id == "" {
		t.Fatalf("recover_run: %d %s", status, body)
	}
	if _, err := w.syncer.SettleRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.noRecoveries = false
	w.until("the recovery ended", func() bool { s := w.recoveryStatus(id); return s == "done" || s == "failed" })
	branch := "dude/" + task + "/recovered-" + run
	if got := w.branchAt(branch); got != latest {
		which := "neither"
		if got == early {
			which = "the earlier checkpoint's"
		}
		t.Errorf("%s is at %q (%s), want the Run's latest commit %s; recovery %s: %s", branch, got, which, latest,
			w.recoveryStatus(id), w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id))
	}
}

// A task a person stopped keeps its recovered branches, however many: they
// never hold up the release of a closed task's, which goes in the same pass.
func TestAStoppedTasksRecoveriesDoNotHoldUpAClosedTasksBranch(t *testing.T) {
	w := conducting(t)
	stopped, deadRun, _ := w.deadWriter("WIP.md", "half of it\n")
	ana := w.ownedBy(stopped, "Ana")
	conductor, _, _ := w.conductor(stopped)
	w.stop(ana, stopped, map[string]any{"note": "hold on"})
	w.until("the conductor to be kept", func() bool { return w.keptRun(conductor) })
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'halt'`, stopped); n != 1 {
		t.Fatalf("the stopped task's delivery is not halted (%d)", n)
	}
	// More ended recoveries on it than one pass releases, all older.
	mustExec(t, w.owner, `INSERT INTO run_recoveries (id, organization_id, task_id, run_id, branch, status, ended_at)
		SELECT 'rcv_held_'||g, $1, $2, $3, 'dude/'||$2||'/recovered-held-'||g, 'failed', now() - interval '1 day'
		FROM generate_series(1, 101) g`, w.org, stopped, deadRun)
	closed, run, commit := w.deadWriter("WIP.md", "half of it\n")
	id, _ := w.must(closed, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	branch := "dude/" + closed + "/recovered-" + run
	w.until("the recovery done", func() bool { return w.recoveryStatus(id) == "done" })
	if got := w.branchAt(branch); got != commit {
		t.Fatalf("%s is at %q", branch, got)
	}
	bob := w.ownedBy(closed, "Bob")
	if status, body := w.callAs(bob, "/internal/tasks/"+closed+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	if _, err := w.syncer.RetireCompleted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.branchAt(branch); got != "" {
		t.Errorf("the closed task's recovered branch is still at %s after a pass", got)
	}
	if n := w.count(`SELECT count(*) FROM run_recoveries WHERE task_id = $1 AND branch_released_at IS NOT NULL`, stopped); n != 0 {
		t.Errorf("%d of the stopped task's recovered branches released", n)
	}
}

// A worker whose publisher lux took, and which died before it recorded it:
// the next pass, finding the recovery still requested, ends it (lux has
// lost the dead Run meanwhile). The publisher it never recorded is found
// by its idempotency key, cancelled, and its cost read with the recovery.
func TestARecoveryEndedBeforeItRecordedItsPublisherCancelsIt(t *testing.T) {
	w := conducting(t)
	task, run, _ := w.deadWriter("WIP.md", "half of it\n")
	deadLux := w.luxRunOf(run)
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	ctx := context.Background()
	mustExec(t, w.owner, `CREATE FUNCTION lose_submitted() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'the worker died'; END $$`)
	mustExec(t, w.owner, `CREATE TRIGGER lose_submitted BEFORE UPDATE ON run_recoveries FOR EACH ROW
		WHEN (NEW.status = 'submitted') EXECUTE FUNCTION lose_submitted()`)
	if _, err := w.syncer.SettleRecoveries(ctx); err != nil {
		t.Fatal(err)
	}
	pub := w.publisherOf(id)
	if pub == nil || w.recoveryStatus(id) != "requested" {
		t.Fatalf("after the lost update: publisher %v, recovery %s", pub, w.recoveryStatus(id))
	}
	mustExec(t, w.owner, `DROP TRIGGER lose_submitted ON run_recoveries`)
	w.lux.ForgetRun(deadLux)
	w.recoveryDue(id)
	if _, err := w.syncer.SettleRecoveries(ctx); err != nil {
		t.Fatal(err)
	}
	if s := w.recoveryStatus(id); s != "failed" {
		t.Fatalf("the recovery is %s", s)
	}
	if n := w.luxCalls(pub.ID, "cancel"); n == 0 {
		t.Errorf("the publisher %s was never cancelled", pub.ID)
	}
	if got := w.str(`SELECT COALESCE(publisher_lux_run_id, '') FROM run_recoveries WHERE id = $1`, id); got != pub.ID {
		t.Errorf("the recovery's publisher is %q, want %s: its cost is never read", got, pub.ID)
	}
	for _, r := range w.lux.Runs() {
		if r != nil && r.ID != pub.ID && submitted(t, r).Labels["dude.recovery"] == id {
			t.Errorf("a second publisher %s was submitted", r.ID)
		}
	}
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

// A dead Run whose control nothing will clear (finish marked it completed
// after a machine move asked for a pause): once the recovery has waited
// past its give-up, it reads lux's checkpoints anyway, pushes, and wakes
// the conductor, rather than waiting on lux for ever.
func TestARecoveryStopsWaitingOnARunsControlAfterItsGiveUp(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	mustExec(t, w.owner, `UPDATE runs SET control = 'pause_graceful', control_requested_at = now(), status = 'completed' WHERE id = $1`, run)
	w.pastGiveUp(id)
	w.until("the recovery done", func() bool { return w.recoveryStatus(id) == "done" })
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != commit {
		t.Errorf("the recovered branch is at %q, want %s", got, commit)
	}
	w.until("the conductor woken", func() bool { return w.recoveredWakes(task) == 1 })
}

// A recovery that failed once before it ever asked lux for a publisher
// (a transient error reading lux, say) and then finds nothing committed
// ends with no publisher: none is submitted to be cancelled, none recorded.
func TestARecoveryThatFailedBeforeItsSubmitEndsWithNoPublisher(t *testing.T) {
	w := conducting(t)
	task, run, luxID := w.hangingWriter()
	w.timedOut(run, luxID)
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	// What a pass that failed on a transient error leaves: a failure counted.
	mustExec(t, w.owner, `UPDATE run_recoveries SET failures = 1 WHERE id = $1`, id)
	w.noRecoveries = false
	w.until("the recovery failed", func() bool { return w.recoveryStatus(id) == "failed" })
	if why := w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id); !strings.Contains(why, "nothing committed") {
		t.Errorf("failed saying %q", why)
	}
	if pub := w.publisherOf(id); pub != nil {
		t.Errorf("a publisher %s was submitted for nothing", pub.ID)
	}
	if got := w.str(`SELECT COALESCE(publisher_lux_run_id, '') FROM run_recoveries WHERE id = $1`, id); got != "" {
		t.Errorf("a publisher %s is recorded", got)
	}
}

// deadRunLux is lux as dude finds it about the dead Run, the fake answering
// every other Run: it says the Run is still running, or refuses its Get
// with refusal the first refusals times (every time when negative). With
// checkpoints it refuses the checkpoint read that follows a refused Get
// too, once, or every one when it always refuses.
type deadRunLux struct {
	lux.Client
	dead        string
	running     bool
	refusal     *lux.Error
	refusals    int
	checkpoints bool
	// gets counts the dead Run's Gets; refused is whether the last was.
	gets    int
	refused bool
}

func (c *deadRunLux) Get(ctx context.Context, runID string) (lux.Run, error) {
	if runID != c.dead {
		return c.Client.Get(ctx, runID)
	}
	c.gets++
	c.refused = c.refusal != nil && (c.refusals < 0 || c.gets <= c.refusals)
	if c.refused {
		return lux.Run{}, c.refusal
	}
	run, err := c.Client.Get(ctx, runID)
	if err == nil && c.running {
		run.State = "running"
	}
	return run, err
}

func (c *deadRunLux) Checkpoints(ctx context.Context, runID string) ([]lux.Checkpoint, error) {
	if runID == c.dead && c.checkpoints && (c.refusals < 0 || c.refused) {
		c.refused = false
		return nil, c.refusal
	}
	return c.Client.Checkpoints(ctx, runID)
}

// A dead Run lux has still not ended once the recovery has waited past its
// give-up: its checkpoints are those of its last stop, not of its end, so
// the recovery ends failed, saying so, and pushes nothing.
func TestARecoveryPastItsGiveUpOnARunLuxStillRunsEndsUnpushed(t *testing.T) {
	w := conducting(t)
	task, run, _ := w.deadWriter("WIP.md", "half of it\n")
	w.syncer.Lux = &deadRunLux{Client: w.syncer.Lux, dead: w.luxRunOf(run), running: true}
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.pastGiveUp(id)
	w.noRecoveries = false
	w.until("the recovery ended", func() bool { s := w.recoveryStatus(id); return s != "requested" && s != "" })
	if s := w.recoveryStatus(id); s != "failed" {
		t.Fatalf("the recovery is %s", s)
	}
	if why := w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id); !strings.Contains(why, "lux never ended the Run") {
		t.Errorf("failed saying %q", why)
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("the recovered branch was pushed, at %s", got)
	}
	if pub := w.publisherOf(id); pub != nil {
		t.Errorf("a publisher %s was submitted", pub.ID)
	}
}

// lux refusing dude once (a 401 on the dead Run) is not lux having lost
// it: the recovery is tried again and pushes the Run's commits.
func TestARecoveryRefusedOnceByLuxIsRetried(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	l := &deadRunLux{Client: w.syncer.Lux, dead: w.luxRunOf(run), refusals: 1, checkpoints: true,
		refusal: &lux.Error{Status: 401, Code: "unauthorized", Message: "bad key"}}
	w.syncer.Lux = l
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	if _, err := w.syncer.SettleRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.gets == 0 {
		t.Fatal("lux was never asked for the dead Run")
	}
	if s := w.recoveryStatus(id); s != "requested" {
		t.Fatalf("after the refused pass the recovery is %s", s)
	}
	w.recoveryDue(id)
	w.noRecoveries = false
	w.until("the recovery ended", func() bool { s := w.recoveryStatus(id); return s == "done" || s == "failed" || s == "refused" })
	if s := w.recoveryStatus(id); s != "done" {
		t.Fatalf("the recovery is %s: %s", s, w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id))
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != commit {
		t.Errorf("the recovered branch is at %q, want %s", got, commit)
	}
}

// lux refusing dude for good on the dead Run does not keep the recovery
// requested for ever: past its give-up it ends failed, saying so, pushes
// nothing, and wakes the conductor.
func TestARecoveryLuxKeepsRefusingEndsAtItsGiveUp(t *testing.T) {
	w := conducting(t)
	task, run, _ := w.deadWriter("WIP.md", "half of it\n")
	w.syncer.Lux = &deadRunLux{Client: w.syncer.Lux, dead: w.luxRunOf(run), refusals: -1, checkpoints: true,
		refusal: &lux.Error{Status: 403, Code: "forbidden", Message: "key does not cover the Run"}}
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.pastGiveUp(id)
	if _, err := w.syncer.SettleRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := w.recoveryStatus(id); s != "failed" {
		t.Fatalf("the recovery is %s", s)
	}
	if why := w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id); !strings.Contains(why, "lux would not say whether it ended the Run") {
		t.Errorf("failed saying %q", why)
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("the recovered branch was pushed, at %s", got)
	}
	if pub := w.publisherOf(id); pub != nil {
		t.Errorf("a publisher %s was submitted", pub.ID)
	}
	w.noRecoveries = false
	w.until("the conductor woken", func() bool { return w.recoveredWakes(task) == 1 })
}

// A transient lux error past the give-up is not lux refusing to say
// whether it ended the Run: the recovery is tried again and pushes.
func TestARecoveryPastItsGiveUpRetriesATransientLuxError(t *testing.T) {
	w := conducting(t)
	task, run, commit := w.deadWriter("WIP.md", "half of it\n")
	l := &deadRunLux{Client: w.syncer.Lux, dead: w.luxRunOf(run), refusals: 1,
		refusal: &lux.Error{Status: 503, Code: "unavailable", Message: "lux is down for a moment"}}
	w.syncer.Lux = l
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.pastGiveUp(id)
	if _, err := w.syncer.SettleRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.gets == 0 {
		t.Fatal("lux was never asked for the dead Run")
	}
	if s := w.recoveryStatus(id); s != "requested" {
		t.Fatalf("after the 503 the recovery is %s: %s", s, w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id))
	}
	w.recoveryDue(id)
	w.noRecoveries = false
	w.until("the recovery ended", func() bool { s := w.recoveryStatus(id); return s == "done" || s == "failed" || s == "refused" })
	if s := w.recoveryStatus(id); s != "done" {
		t.Fatalf("the recovery is %s: %s", s, w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id))
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != commit {
		t.Errorf("the recovered branch is at %q, want %s", got, commit)
	}
}

// A dead Run lux has not yet ended, its control cleared, before the
// recovery's give-up: the recovery waits for lux to end it, submitting no
// publisher and pushing nothing.
func TestARecoveryWaitsWhileLuxStillRunsTheRun(t *testing.T) {
	w := conducting(t)
	task, run, _ := w.deadWriter("WIP.md", "half of it\n")
	l := &deadRunLux{Client: w.syncer.Lux, dead: w.luxRunOf(run), running: true}
	w.syncer.Lux = l
	w.noRecoveries = true
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	mustExec(t, w.owner, `UPDATE runs SET control = 'none' WHERE id = $1`, run)
	mustExec(t, w.owner, `UPDATE run_recoveries SET stepped_at = now(), next_attempt_at = NULL WHERE id = $1`, id)
	if _, err := w.syncer.SettleRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.gets == 0 {
		t.Fatal("lux was never asked for the dead Run")
	}
	if s := w.recoveryStatus(id); s != "requested" {
		t.Fatalf("the recovery is %s: %s", s, w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id))
	}
	if pub := w.publisherOf(id); pub != nil {
		t.Errorf("a publisher %s was submitted", pub.ID)
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("the recovered branch was pushed, at %s", got)
	}
}
