package orchestrator_test

// No secret in a push: an implementer given a project secret commits its
// value. dude reads the checkout before asking lux to push: nothing
// reaches GitHub, the Run fails kept, and the task's owner is told the
// secret's name and the file, never the value. An implementer committing
// nothing secret pushes as before.

import (
	"context"
	"encoding/base64"
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
			w.committingImplementer("config/app.toml", content)
			task := w.task()
			w.deliver(task)
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
