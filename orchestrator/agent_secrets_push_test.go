package orchestrator_test

// No secret in a push: an implementer given a project secret commits its
// value. dude reads the checkout before asking lux to push: nothing
// reaches GitHub, the Run fails kept, and the task's owner is told the
// secret's name and the file, never the value. An implementer committing
// nothing secret pushes as before.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func TestAnImplementerCommittingASecretIsRefusedBeforeThePush(t *testing.T) {
	for name, content := range map[string]string{
		"raw":    "password = " + implToken,
		"base64": "password_b64 = " + base64.StdEncoding.EncodeToString([]byte(implToken)),
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.grant("IMPL_TOKEN", implToken, "implementer")
			w.scriptedImplementer(writeCall("config/app.toml", content), commitCall("Add config"))
			task := w.task()
			w.deliver(task)
			w.finishOnceBased(task)
			var runID string
			w.until("the implementer failed", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'`, task).Scan(&runID)
				return runID != ""
			})
			w.until("the task escalated", func() bool {
				return w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND status = 'awaiting_input'`, task) == 1
			})
			if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 {
				t.Errorf("lux was asked to push %d times", len(lr.PushCredentials))
			}
			out, _ := exec.Command("git", "-C", w.gh.Repo, "for-each-ref", "--format=%(refname)").Output()
			if strings.Contains(string(out), "run-") {
				t.Errorf("a per-Run branch reached GitHub: %s", out)
			}
			var reason string
			_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE id = $1`, runID).Scan(&reason)
			if !strings.Contains(reason, "IMPL_TOKEN in config/app.toml") {
				t.Errorf("the reason does not name the secret and file: %q", reason)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
				AND payload->'secrets' = '["IMPL_TOKEN"]' AND payload->'files' = '["config/app.toml"]'`, runID); n != 1 {
				t.Errorf("%d run.push.refused naming IMPL_TOKEN and the file", n)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND keep`, runID); n != 1 {
				t.Error("the Run is not kept for its owner to resume")
			}
			w.assertTaskHoldsNo(task, implToken)
			var escalation string
			_ = w.owner.QueryRow(context.Background(), `SELECT state->'escalation'->>'reason' FROM workflow_runs WHERE task_id = $1`, task).Scan(&escalation)
			if escalation != "implement_failed" {
				t.Errorf("escalation %q", escalation)
			}
			// Not retried by itself: the Run stays failed, no new implementer.
			w.pump()
			if n := w.phaseRuns(task, delivery.PhaseImplement); n != 1 {
				t.Errorf("%d implementers; the refusal was retried", n)
			}
		})
	}
}

// Work holding no secret is pushed as before, the scan costing one read.
func TestWorkWithoutASecretIsPushed(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.scriptedImplementer(writeCall("config/app.toml", "password = read from IMPL_TOKEN"), commitCall("Add config"))
	task := w.task()
	w.deliver(task)
	w.finishOnceBased(task)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}

// The scripted fake/commit-secrets implementer, as the E2E suite drives it:
// it commits the env secrets its Run declares, and the push is refused.
func TestTheScriptedSecretCommitterIsRefused(t *testing.T) {
	w := newWorld(t)
	w.onModel("implementer", fakeagent.CommitSecretsModel)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	task := w.task()
	w.deliver(task)
	w.until("the implementer failed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'
			AND error LIKE '%IMPL_TOKEN in `+fakeagent.SecretsFile+`%'`, task) == 1
	})
}

// scriptedImplementer makes the scripted implementer do calls in its
// checkout (fakeagent.Local*) in place of its scripted commit, then hang
// in its turn (finishOnceBased ends it).
func (w *world) scriptedImplementer(calls ...[2]string) {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit = nil
			b.CallTools = calls
			b.Hang = true
		}
		return b
	}
}

// finishOnceBased ends the task's implementer's turn once dude has
// recorded where its checkout started: lux reports that after the agent's
// own records, and a turn ending first is refused for the missing base
// rather than for what the test put in its history.
func (w *world) finishOnceBased(task string) {
	w.t.Helper()
	_, luxID := w.basedImplementer(task)
	w.lux.EndTurn(luxID)
}

// basedImplementer waits until dude has recorded where the task's running
// implementer's checkout started; its Run and lux Run.
func (w *world) basedImplementer(task string) (runID, luxID string) {
	w.t.Helper()
	w.until("the implementer's base recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, lux_run_id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND base_shas <> '{}'::jsonb AND status = 'running' AND lux_run_id IS NOT NULL`, task).Scan(&runID, &luxID)
		return luxID != ""
	})
	return runID, luxID
}

// runBranchesOnGitHub are the per-Run branches lux pushed to, space
// separated; the attempt's branch dude makes itself, from the base, is
// not one.
func (w *world) runBranchesOnGitHub() string {
	out, _ := exec.Command("git", "-C", w.gh.Repo, "for-each-ref", "--format=%(refname)", "refs/heads/dude/").Output()
	var runs []string
	for _, ref := range strings.Fields(string(out)) {
		if strings.Contains(ref, "/run-") {
			runs = append(runs, ref)
		}
	}
	return strings.Join(runs, " ")
}

// assertTaskHoldsNo checks that no event of the task holds value, raw or
// encoded.
func (w *world) assertTaskHoldsNo(task, value string) {
	w.t.Helper()
	for _, l := range leaks(value) {
		if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload::text LIKE '%' || $2 || '%'`, task, l); n != 0 {
			w.t.Errorf("%d of the task's events hold the value as %q", n, l)
		}
	}
}

func jsonArg(key, value string) string {
	b, _ := json.Marshal(map[string]string{key: value})
	return string(b)
}

func writeCall(path, content string) [2]string {
	b, _ := json.Marshal(map[string]string{"path": path, "content": content + "\n"})
	return [2]string{fakeagent.LocalWrite, string(b)}
}

func commitCall(message string) [2]string {
	return [2]string{fakeagent.LocalCommit, jsonArg("message", message)}
}

func gitCall(args ...string) [2]string {
	b, _ := json.Marshal(map[string][]string{"args": args})
	return [2]string{fakeagent.LocalGit, string(b)}
}

// Everything lux would send GitHub is checked, not only the net change: a
// secret committed and deleted again, in a commit's message, author or
// committer, or added by a merge, still refuses the push, naming the
// secret and where, never the value.
func TestASecretAnywhereInThePushedHistoryIsRefused(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte(implToken))
	for name, c := range map[string]struct {
		calls [][2]string
		where string
	}{
		"committed then deleted": {[][2]string{
			writeCall("config/app.toml", "password = "+implToken), commitCall("Add config"),
			gitCall("rm", "-q", "config/app.toml"), writeCall("README.md", "reads IMPL_TOKEN from the environment"), commitCall("Drop config"),
		}, "IMPL_TOKEN in config/app.toml"},
		"in a commit message": {[][2]string{
			writeCall("README.md", "configured"), commitCall("Configure with " + implToken),
		}, "IMPL_TOKEN in commit message of "},
		"base64 in a commit message": {[][2]string{
			writeCall("README.md", "configured"), commitCall("Configure\n\nkey: " + b64),
		}, "IMPL_TOKEN in commit message of "},
		"in a commit's author": {[][2]string{
			writeCall("README.md", "configured"), gitCall("add", "-A"),
			gitCall("-c", "user.email="+implToken+"@example.test", "commit", "-q", "-m", "Configure"),
		}, "IMPL_TOKEN in commit author of "},
		// The author is the clean one the checkout's git is configured with.
		"in a commit's committer only": {[][2]string{
			writeCall("README.md", "configured"), gitCall("add", "-A"),
			gitCall("-c", "committer.email="+implToken+"@example.test", "commit", "-q", "-m", "Configure"),
		}, "IMPL_TOKEN in commit committer of "},
		// The merge's resolution adds the value, which neither parent has,
		// and a later commit takes it out: only the merge's own diff (git
		// log -m) shows it, not any single-parent commit or the net change.
		"added by a merge": {[][2]string{
			writeCall("config.txt", "mode = base"), commitCall("Base config"),
			gitCall("checkout", "-q", "-b", "side"),
			writeCall("config.txt", "mode = side"), commitCall("Side config"),
			gitCall("checkout", "-q", "-"),
			writeCall("config.txt", "mode = main"), commitCall("Main config"),
			gitCall("merge", "-q", "--no-edit", "side"),
			writeCall("config.txt", "password = "+implToken), gitCall("add", "-A"), gitCall("commit", "-q", "--no-edit"),
			writeCall("config.txt", "mode = merged"), commitCall("Read the password from the environment"),
		}, "IMPL_TOKEN in config.txt"},
		// A branch merged in adds the value and takes it out again before
		// the merge.
		"added on a merged-in branch": {[][2]string{
			gitCall("checkout", "-q", "-b", "side"),
			writeCall("side.txt", "password = "+implToken), commitCall("Side config"),
			writeCall("side.txt", "password from the environment"), commitCall("Side reads the environment"),
			gitCall("checkout", "-q", "-"),
			writeCall("main.txt", "main"), commitCall("Main"),
			gitCall("merge", "-q", "--no-edit", "side"),
		}, "IMPL_TOKEN in side.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.grant("IMPL_TOKEN", implToken, "implementer")
			w.scriptedImplementer(c.calls...)
			task := w.task()
			w.deliver(task)
			w.finishOnceBased(task)
			var runID string
			w.until("the implementer failed or pushed", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'`, task).Scan(&runID)
				return runID != "" || w.runBranchesOnGitHub() != ""
			})
			if branch := w.runBranchesOnGitHub(); branch != "" {
				t.Fatalf("the work reached GitHub as %s", branch)
			}
			if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 {
				t.Errorf("lux was asked to push %d times", len(lr.PushCredentials))
			}
			var reason string
			_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE id = $1`, runID).Scan(&reason)
			if !strings.Contains(reason, c.where) {
				t.Errorf("the reason does not say %q: %q", c.where, reason)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
				AND payload->'secrets' = '["IMPL_TOKEN"]'`, runID); n != 1 {
				t.Errorf("%d run.push.refused naming IMPL_TOKEN", n)
			}
			for _, leak := range leaks(implToken) {
				if strings.Contains(reason, leak) {
					t.Errorf("the reason holds the value as %q", leak)
				}
			}
			w.assertTaskHoldsNo(task, implToken)
			w.pump()
			if n := w.phaseRuns(task, delivery.PhaseImplement); n != 1 {
				t.Errorf("%d implementers; the refusal was retried", n)
			}
		})
	}
}

// A history dude cannot read is not pushed unchecked: a repository the
// Run's recorded bases name, with no checkout where lux keeps it, refuses
// the push rather than being left out of the scan.
func TestAMissingCheckoutRefusesThePush(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	reason := w.ghostRepositoryPushRefused()
	if !strings.Contains(reason, "cannot check the work for secrets") || !strings.Contains(reason, "ghost") {
		t.Errorf("the reason does not say the ghost checkout went unchecked: %q", reason)
	}
}

// ghostRepositoryPushRefused delivers a task whose implementer commits,
// adds "ghost", a repository with no checkout, to its recorded bases once
// target's is recorded, and ends its turn; it requires that the Run fails
// without lux being asked to push, and returns the Run's error.
func (w *world) ghostRepositoryPushRefused() string {
	w.t.Helper()
	w.scriptedImplementer(writeCall("README.md", "configured"), commitCall("Configure"))
	task := w.task()
	w.deliver(task)
	var runID, luxID, base string
	w.until("the implementer's base recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, lux_run_id, base_shas->>'target' FROM runs WHERE task_id = $1
			AND phase = 'implement' AND base_shas ? 'target' AND status = 'running' AND lux_run_id IS NOT NULL`, task).Scan(&runID, &luxID, &base)
		return luxID != ""
	})
	mustExec(w.t, w.owner, `UPDATE runs SET base_shas = base_shas || jsonb_build_object('ghost', $2::text) WHERE id = $1`, runID, base)
	w.lux.EndTurn(luxID)
	w.until("the implementer failed or pushed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1 || w.luxRunFor(runID).Pushed
	})
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 || lr.Pushed {
		w.t.Fatalf("lux was asked to push %d times", len(lr.PushCredentials))
	}
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE id = $1`, runID).Scan(&reason)
	return reason
}

// skipsUnreadHistory is a container whose history script leaves out a
// repository it cannot read and still exits 0, as a script that skipped a
// missing checkout did: only dude's own check for each section remains.
type skipsUnreadHistory struct{ lux.Client }

func (c skipsUnreadHistory) Exec(ctx context.Context, runID string, command []string) (lux.ExecResult, error) {
	res, err := c.Client.Exec(ctx, runID, command)
	if err == nil && len(command) > 3 && command[3] == "dude-history" && res.ExitCode == 4 {
		res.ExitCode, res.Stderr = 0, nil
	}
	return res, err
}

// The history read exits 0 but has no section for a repository the Run's
// recorded bases name: the push is refused as unchecked, never made.
func TestAHistoryMissingARepositorysSectionRefusesThePush(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.syncer.Lux = skipsUnreadHistory{w.syncer.Lux}
	reason := w.ghostRepositoryPushRefused()
	if want := "cannot check the work for secrets before pushing it: no history was read for ghost"; !strings.Contains(reason, want) {
		t.Errorf("reason %q, want it to say %q", reason, want)
	}
}

// A history of several clean commits, one deleting what another added, is
// pushed.
func TestACleanMultiCommitHistoryIsPushed(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.scriptedImplementer(
		writeCall("config/app.toml", "password = read from IMPL_TOKEN"), commitCall("Add config"),
		writeCall("notes.txt", "scratch"), commitCall("Notes"),
		gitCall("rm", "-q", "notes.txt"), commitCall("Drop notes\n\nThe token comes from the environment."),
	)
	task := w.task()
	w.deliver(task)
	w.finishOnceBased(task)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.push.refused'`, task); n != 0 {
		t.Errorf("%d push refusals of clean work", n)
	}
}

// An implementer given a secret commits, then fetches a base that moved by
// more than the scan reads (sync_base's fetch, recorded as the Run's
// trusted base) and merges it: what the base brought is GitHub's already
// and is not read, nor the merge against its first parent, so the push
// goes ahead; with the fetch's record gone, the whole history is read and
// the push is refused as unchecked.
func TestABaseMergedUnderItsTrustedFetchIsNotScanned(t *testing.T) {
	for name, trusted := range map[string]bool{"trusted": true, "untrusted": false} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.grant("IMPL_TOKEN", implToken, "implementer")
			w.scriptedImplementer()
			task := w.task()
			w.deliver(task)
			runID, luxID := w.basedImplementer(task)
			work, err := w.lux.Checkout(luxID, "target")
			if err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) string {
				return gitOut(t, work, append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
			}
			if err := os.WriteFile(filepath.Join(work, "task.txt"), []byte("task\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git("add", "task.txt")
			git("commit", "-q", "-m", "Task work")
			line := strings.Repeat("b", 99) + "\n"
			w.commitFileIn(w.gh.Repo, "main", "vendor.txt", strings.Repeat(line, (8<<20)/len(line)), "Vendor a large file")
			w.fetchBase(runID)
			if !trusted {
				mustExec(t, w.owner, `UPDATE runs SET synced_bases = '{}' WHERE id = $1`, runID)
			}
			git("merge", "-q", "--no-edit", "lux/main")
			if parents := strings.Fields(git("log", "-1", "--format=%P")); len(parents) != 2 {
				t.Fatalf("HEAD has parents %v, want a merge", parents)
			}
			w.lux.EndTurn(luxID)
			w.until("the implementer failed or pushed", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('failed', 'completed')`, runID) == 1
			})
			var reason, status string
			_ = w.owner.QueryRow(context.Background(), `SELECT status::text, COALESCE(error, '') FROM runs WHERE id = $1`, runID).Scan(&status, &reason)
			switch {
			case trusted && (status != "completed" || !w.luxRunFor(runID).Pushed):
				t.Fatalf("under the trusted base: %s, pushed %v: %s", status, w.luxRunFor(runID).Pushed, reason)
			case !trusted && !strings.Contains(reason, "more than dude reads"):
				t.Fatalf("without the trusted base: %q, want the history over the limit", reason)
			}
		})
	}
}

// The ref lux fetched the base into is the agent's to move: one it points
// at its own commits after merging the trusted base, a secret committed
// and deleted again among them, leaves those commits scanned. Only the
// commit lux reported is left out.
func TestASecretBehindAMovedFetchRefIsStillRefused(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.scriptedImplementer()
	task := w.task()
	w.deliver(task)
	runID, luxID := w.basedImplementer(task)
	work, err := w.lux.Checkout(luxID, "target")
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		return gitOut(t, work, append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	}
	w.commitFileIn(w.gh.Repo, "main", "base.txt", "base\n", "Base moved")
	w.fetchBase(runID)
	git("merge", "-q", "--no-edit", "lux/main")
	if err := os.WriteFile(filepath.Join(work, "config.toml"), []byte("password = "+implToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "config.toml")
	git("commit", "-q", "-m", "Configure")
	git("rm", "-q", "config.toml")
	git("commit", "-q", "-m", "Read the password from the environment")
	git("update-ref", "refs/remotes/lux/main", "HEAD")
	w.lux.EndTurn(luxID)
	w.until("the implementer failed or pushed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('failed', 'completed')`, runID) == 1
	})
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 || lr.Pushed {
		t.Fatalf("lux was asked to push %d times", len(lr.PushCredentials))
	}
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(error, '') FROM runs WHERE id = $1`, runID).Scan(&reason)
	if !strings.Contains(reason, "IMPL_TOKEN in config.toml") {
		t.Fatalf("reason %q, want IMPL_TOKEN in config.toml", reason)
	}
}
