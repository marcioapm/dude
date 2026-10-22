package orchestrator_test

// A repository's setup script that fails is not tried again by itself: the
// same script on the same checkout fails the same way. A phase Run it fails
// escalates setup_failed to a person; a conductor it fails is not replaced
// by another, however many reasons arrive to wake one, until a person tries
// again.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// failingSetup is a script that fails as the abs repository's did on
// 2026-10-22: writing a hook into dude's own, root-owned, hooks directory.
const failingSetup = "echo \"ln: failed to create symbolic link '/usr/local/share/dude/git-hooks/pre-commit': Permission denied\" >&2\nexit 1\n"

func (w *world) setupScript(script string) {
	w.t.Helper()
	var v any
	if script != "" {
		v = script
	}
	mustExec(w.t, w.owner, `UPDATE repositories SET setup_script = $2 WHERE id = $1`, w.repoID, v)
}

// escalation is the task's latest escalation: its reason and detail.
func (w *world) escalation(task string) (reason string, detail map[string]any) {
	w.t.Helper()
	var p struct {
		Reason string         `json:"reason"`
		Detail map[string]any `json:"detail"`
	}
	_ = w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'kind' = 'escalation' ORDER BY cursor DESC LIMIT 1`, task).Scan(&p)
	return p.Reason, p.Detail
}

func TestASetupScriptThatFailsAnImplementerEscalatesOnce(t *testing.T) {
	w := newWorld(t)
	w.setupScript(failingSetup)
	wi := w.task()
	w.deliver(wi)
	w.until("the escalation", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	reason, detail := w.escalation(wi)
	if reason != delivery.ReasonSetupFailed {
		t.Fatalf("escalated %q, want %s (%v)", reason, delivery.ReasonSetupFailed, detail)
	}
	if detail["repository"] != "target" || detail["exitCode"] != float64(1) ||
		!strings.Contains(detail["output"].(string), "Permission denied") || detail["runId"] == "" {
		t.Errorf("detail %v", detail)
	}
	for range 10 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi); n != 1 {
		t.Errorf("%d Runs after the setup failure, want the one implementer", n)
	}

	// The script fixed, a person's retry implements again.
	w.setupScript("")
	status, out := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry"})
	if status != 200 {
		t.Fatalf("retry: %d %v", status, out)
	}
	w.until("a second implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi) == 2
	})
}

// The incident on 2026-10-22: a woken conductor a setup script failed was
// replaced every sweep, 21 times in 9 minutes on one task. Now the one that
// failed is the last, whatever wakes it or what it held unread; a person's
// Try again starts the next, once the script is fixed.
func TestAConductorASetupScriptFailedIsNotReplacedUntilAPersonTriesAgain(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	// The conductor between turns: gone with its container, as a parked one
	// a wake would resume, then a new one is started for the next reason.
	w.setupScript(failingSetup)
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE task_id = $1 AND role = 'conductor'`, task)
	ctx := context.Background()
	wake := func(key string) {
		t.Helper()
		if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			_, err := delivery.RecordWakeTx(ctx, tx, w.org, task, "decision", key, "a decision is yours: "+key)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = now() - interval '1 minute' WHERE task_id = $1`, task)
	}
	conductors := func() int {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task)
	}
	wake("first")
	w.until("the woken conductor to fail its setup", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor' AND status = 'failed'`, task) == 1
	})
	failed := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'setup_failed'`, task)
	if failed != 1 {
		t.Errorf("%d setup_failed notices in Chat, want 1", failed)
	}
	// A message the failed one never read, and more reasons: none starts another.
	var failedID string
	_ = w.owner.QueryRow(ctx, `SELECT id FROM runs WHERE task_id = $1 AND role = 'conductor' AND status = 'failed'`, task).Scan(&failedID)
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, run_id, text, scope) VALUES ('dir_unread', $1, $2, 'and this', 'run')`,
		w.org, failedID)
	wake("second")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w.pump()
		time.Sleep(untilPoll)
	}
	if n := conductors(); n != 2 {
		t.Fatalf("%d conductors after the setup failure and more wakes, want the talk's and the failed one\n%s", n, w.describeRuns())
	}

	// Fixed, and tried again: a fresh conductor, naming the one it retries,
	// told what was waiting.
	w.setupScript("")
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'retries' = $2`, next, failedID); n != 1 {
		t.Errorf("the new conductor does not name the one it retries")
	}
	w.until("the new conductor to hear the wakes and the message", func() bool {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, task) == 0 &&
			w.count(`SELECT count(*) FROM directives WHERE id = 'dir_unread' AND failed_at IS NOT NULL`) == 1
	})
	if n := conductors(); n != 3 {
		t.Errorf("%d conductors, want 3", n)
	}
}
