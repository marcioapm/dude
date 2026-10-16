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
	"os/exec"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// committingImplementer makes the scripted implementer write content to
// path in its checkout and commit it, in place of its scripted commit.
func (w *world) committingImplementer(path, content string) {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit = nil
			b.CallTools = [][2]string{
				{"write", `{"path":"` + path + `","content":"` + content + `\n"}`},
				{"commit", `{"message":"Add config"}`},
			}
		}
		return b
	}
}

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
			for _, leak := range leaks(implToken) {
				if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload::text LIKE '%' || $2 || '%'`, task, leak); n != 0 {
					t.Errorf("%d of the task's events hold the value as %q", n, leak)
				}
			}
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
	w.committingImplementer("config/app.toml", "password = read from IMPL_TOKEN")
	task := w.task()
	w.deliver(task)
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
	var luxID string
	w.until("the implementer's base recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND base_shas <> '{}'::jsonb AND status = 'running' AND lux_run_id IS NOT NULL`, task).Scan(&luxID)
		return luxID != ""
	})
	w.lux.EndTurn(luxID)
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
			// The per-Run branch lux pushes to; the attempt's branch dude
			// makes itself, from the base.
			pushed := func() string {
				out, _ := exec.Command("git", "-C", w.gh.Repo, "for-each-ref", "--format=%(refname)", "refs/heads/dude/").Output()
				var runs []string
				for _, ref := range strings.Fields(string(out)) {
					if strings.Contains(ref, "/run-") {
						runs = append(runs, ref)
					}
				}
				return strings.Join(runs, " ")
			}
			w.until("the implementer failed or pushed", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'`, task).Scan(&runID)
				return runID != "" || pushed() != ""
			})
			if branch := pushed(); branch != "" {
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
				if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload::text LIKE '%' || $2 || '%'`, task, leak); n != 0 {
					t.Errorf("%d of the task's events hold the value as %q", n, leak)
				}
			}
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
	w.scriptedImplementer(writeCall("README.md", "configured"), commitCall("Configure"))
	task := w.task()
	w.deliver(task)
	var runID, luxID, base string
	w.until("the implementer's base recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, lux_run_id, base_shas->>'target' FROM runs WHERE task_id = $1
			AND phase = 'implement' AND base_shas ? 'target' AND status = 'running' AND lux_run_id IS NOT NULL`, task).Scan(&runID, &luxID, &base)
		return luxID != ""
	})
	mustExec(t, w.owner, `UPDATE runs SET base_shas = base_shas || jsonb_build_object('ghost', $2::text) WHERE id = $1`, runID, base)
	w.lux.EndTurn(luxID)
	w.until("the implementer failed or pushed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1 || w.luxRunFor(runID).Pushed
	})
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 || lr.Pushed {
		t.Fatalf("lux was asked to push %d times", len(lr.PushCredentials))
	}
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE id = $1`, runID).Scan(&reason)
	if !strings.Contains(reason, "cannot check the work for secrets") || !strings.Contains(reason, "ghost") {
		t.Errorf("the reason does not say the ghost checkout went unchecked: %q", reason)
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
