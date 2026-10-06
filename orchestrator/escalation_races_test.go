package orchestrator_test

// Every way an escalation is decided, racing another: each takes the task's
// row first and reads the delivery after it, so exactly one decision lands,
// and none deadlocks. Each race is driven deterministically: one side holds
// its locks, the other is started and seen waiting on a lock, then the first
// is released.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// waiters waits until n backends of the world's database wait on a lock.
func (w *world) waiters(n int) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var got int
		if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&got); err != nil {
			w.t.Fatal(err)
		}
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for %d lock waiters", n)
}

// second is another owner-role connection to the world's database.
func (w *world) second() *pgx.Conn {
	w.t.Helper()
	c, err := pgx.Connect(context.Background(), w.owner.Config().ConnString())
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

// handedOver is a stuck delivery whose conductor asked about the escalation
// and whose owner, Ana, answered in her own words: decide_escalation may
// decide it. Returns Ana's key and the conductor's ref.
func handedOver(t *testing.T) (*world, string, string, delivery.RunRef) {
	w, task := stuck(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", escalationQuestion)
	if status, out := w.answerAs(ana, task, "Your call."); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	conductor, _, _ := w.conductor(task)
	return w, task, ana, delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: conductor}
}

// decisions is how many task.decided events the task has.
func (w *world) decisions(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, task)
}

// The banner, started while the conductor's decision is uncommitted, waits
// for it and then finds the escalation decided: 409, and the conductor's
// decision stands.
func TestTheBannerRacingTheConductorsDecisionIsRefused(t *testing.T) {
	w, task, ana, ref := handedOver(t)
	ctx := context.Background()
	held, release, decided := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		decided <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			if _, err := delivery.ConductDecideEscalation(ctx, tx, ref, "retry", ""); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-decided:
		t.Fatalf("the conductor's decision: %v", err)
	}
	banner := make(chan int, 1)
	go func() {
		status, _ := w.callAs(ana, "/internal/tasks/"+task+"/decide", map[string]any{"action": "stop"})
		banner <- status
	}()
	w.waiters(1)
	close(release)
	if err := <-decided; err != nil {
		t.Fatalf("the conductor's decision: %v", err)
	}
	if status := <-banner; status != 409 {
		t.Errorf("the racing banner: %d, want 409", status)
	}
	if d := w.decided(task); d["action"] != "retry" || d["conductor"] != ref.RunID {
		t.Errorf("the decision is %v, want the conductor's retry", d)
	}
	if n := w.decisions(task); n != 1 {
		t.Errorf("%d task.decided events, want 1", n)
	}
}

// The owner's choice and the banner at once, the delivery's row held while
// both start: neither deadlocks; one decides, the other is refused.
func TestAnAnswerAndTheBannerAtOnceDecideOnce(t *testing.T) {
	w, task := stuck(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", escalationQuestion)
	q := w.conductorQuestion(task)
	ctx := context.Background()
	holder := w.second()
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM workflow_runs WHERE task_id = $1 FOR UPDATE`, task); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		out    map[string]any
	}
	answer, banner := make(chan result, 1), make(chan result, 1)
	go func() {
		status, out := w.callAs(ana, "/internal/questions/"+q+"/answer", map[string]any{"text": "Retry as proposed"})
		answer <- result{status, out}
	}()
	w.waiters(1)
	go func() {
		status, out := w.callAs(ana, "/internal/tasks/"+task+"/decide", map[string]any{"action": "stop"})
		banner <- result{status, out}
	}()
	w.waiters(2)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := <-answer, <-banner
	for _, r := range []result{a, b} {
		if msg, _ := r.out["error"].(string); strings.Contains(msg, "deadlock") || r.status >= 500 {
			t.Errorf("%d %v", r.status, r.out)
		}
	}
	if !(a.status == 200 && b.status == 409 || a.status == 409 && b.status == 200) {
		t.Errorf("the answer %d %v, the banner %d %v: want one decided, one refused", a.status, a.out, b.status, b.out)
	}
	if n := w.decisions(task); n != 1 {
		t.Errorf("%d task.decided events, want 1", n)
	}
}
