package orchestrator_test

// Every way an escalation is decided, racing another: each takes the task's
// delivery, then its row (delivery.LockEscalationTx), so exactly one decision
// lands, and none deadlocks. Each race is driven deterministically: one side holds
// its locks, the other is started and seen waiting on a lock, then the first
// is released.

import (
	"context"
	"encoding/json"
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

// held runs fn in a transaction of the app's and holds it open, fn done,
// until release is called; release returns the transaction's error.
func (w *world) held(fn func(tx pgx.Tx) error) (release func() error) {
	w.t.Helper()
	ctx := context.Background()
	done, proceed, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		result <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			close(done)
			<-proceed
			return nil
		})
	}()
	select {
	case <-done:
	case err := <-result:
		w.t.Fatalf("the held transaction: %v", err)
	}
	return func() error {
		close(proceed)
		return <-result
	}
}

// The banner, started while the conductor's decision is uncommitted, waits
// for it and then finds the escalation decided: 409, and the conductor's
// decision stands.
func TestTheBannerRacingTheConductorsDecisionIsRefused(t *testing.T) {
	w, task, ana, ref := handedOver(t)
	release := w.held(func(tx pgx.Tx) error {
		_, err := delivery.ConductDecideEscalation(context.Background(), tx, ref, "retry", "")
		return err
	})
	banner := make(chan int, 1)
	go func() {
		status, _ := w.callAs(ana, "/internal/tasks/"+task+"/decide", map[string]any{"action": "stop"})
		banner <- status
	}()
	w.waiters(1)
	if err := release(); err != nil {
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

// The owner changes while the conductor's decision waits for the task: Bo,
// owner when it gets the lock, never answered, so the decision is refused,
// not credited to Ana.
func TestTheOwnerChangingDuringTheConductorsDecisionRefusesIt(t *testing.T) {
	w, task, _, ref := handedOver(t)
	bo := w.person("Bo")
	ctx := context.Background()
	// As the control plane changes a task's people: its row locked first.
	holder := w.second()
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tasks WHERE id = $1 FOR UPDATE`, task); err != nil {
		t.Fatal(err)
	}
	decided := make(chan error, 1)
	go func() {
		decided <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			_, err := delivery.ConductDecideEscalation(ctx, tx, ref, "retry", "")
			return err
		})
	}()
	w.waiters(1)
	for _, sql := range []string{`DELETE FROM task_people WHERE task_id = $1`,
		`INSERT INTO task_people (task_id, person_id, organization_id, position)
			SELECT $1, person_id, organization_id, 0 FROM api_keys WHERE id = $2`} {
		args := []any{task}
		if strings.Contains(sql, "$2") {
			args = append(args, bo)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-decided
	if err == nil || !strings.Contains(err.Error(), "not the task's owner") {
		t.Errorf("the conductor's decision after the owner changed: %v, want refused as not the owner's answer", err)
	}
	if d := w.decided(task); d != nil {
		t.Errorf("decided on a former owner's answer: %v", d)
	}
}

// ask_person while the banner's decision is uncommitted waits for it, and
// is then refused saying the escalation was decided: no question is left
// open on an escalation that is over.
func TestAQuestionRacingTheBannerIsRefused(t *testing.T) {
	w, task := stuck(t)
	spec := w.conductorSpecOf(task)
	release := w.held(func(tx pgx.Tx) error {
		return delivery.DecideEscalationTx(context.Background(), tx, w.org, task,
			delivery.EscalationDecision{Action: "accept", ActorType: "human", ActorID: "banner"})
	})
	type result struct {
		status int
		body   string
	}
	asked := make(chan result, 1)
	go func() {
		status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "ask_person", escalationQuestion)
		asked <- result{status, body}
	}()
	w.waiters(1)
	if err := release(); err != nil {
		t.Fatalf("the banner: %v", err)
	}
	r := <-asked
	var out map[string]any
	_ = json.Unmarshal([]byte(r.body), &out)
	if msg, _ := out["error"].(string); r.status != 422 || !strings.Contains(msg, "was just decided by a person on the banner") {
		t.Errorf("ask_person racing the banner: %d %s, want refused as decided on the banner", r.status, r.body)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND escalation IS NOT NULL AND status = 'open'`, task); n != 0 {
		t.Errorf("%d escalation questions left open after the banner decided", n)
	}
}
