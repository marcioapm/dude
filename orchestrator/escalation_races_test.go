package orchestrator_test

// Every way an escalation is decided, racing another: each takes the task's
// delivery, then its row (delivery.LockEscalationTx), so exactly one decision
// lands, and none deadlocks. Each race is driven deterministically: one side holds
// its locks, the other is started and seen waiting on a lock, then the first
// is released.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// deadlocked says err is PostgreSQL's deadlock victim (SQLSTATE 40P01).
func deadlocked(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "40P01"
}

// A task ending holds the delivery (SetTaskStatusTx locks it before its
// UPDATE tasks) while a message arrives in Chat: Chat waits on the delivery
// holding nothing the ending needs, the ending commits, and Chat then goes
// on. Neither is a deadlock victim; Chat is no 500.
func TestChatWhileTheTaskEndsWaitsForIt(t *testing.T) {
	w, task := stuck(t)
	ctx := context.Background()
	tx, err := w.second().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := delivery.LoadDelivery(ctx, tx, task); err != nil {
		t.Fatal(err)
	}
	chat := make(chan int, 1)
	go func() {
		status, _ := w.chat(task, "Please explain the situation.")
		chat <- status
	}()
	w.waiters(1)
	if _, err := delivery.SetTaskStatusTx(ctx, tx, w.org, w.project, task, "", "failed", "a person failed it"); err != nil {
		t.Fatalf("the task ending: %v (deadlock: %v)", err, deadlocked(err))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if status := <-chat; status != 200 && status != 409 {
		t.Errorf("Chat while the task ended: %d, want 200 or 409", status)
	}
}

// The conductor's ask_person at a decision point holds the Chat lock and
// the delivery (parked) and then moves the task (AskTx), while a banner
// request arrives: the banner waits on the delivery holding nothing the ask
// needs, the question is asked, and the banner, finding no escalation, is
// refused with 409. Neither is a deadlock victim.
func TestTheBannerWhileTheConductorAsksWaitsForIt(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.until("the first decision", func() bool { return w.decisionAt(task) == delivery.PointStart })
	run, _, _ := w.conductor(task)
	ref := delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: run}
	ctx := context.Background()
	tx, err := w.second().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// ConductDecide's prelude (parked): the Chat lock, then the delivery.
	if err := delivery.LockChat(ctx, tx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.LoadDelivery(ctx, tx, task); err != nil {
		t.Fatal(err)
	}
	banner := make(chan int, 1)
	go func() {
		status, _ := w.call("/internal/tasks/"+task+"/decide", map[string]any{"action": "stop"})
		banner <- status
	}()
	w.waiters(1)
	if _, err := delivery.ConductDecide(ctx, tx, ref, "ask_person", "What should we implement?"); err != nil {
		t.Fatalf("the conductor's ask: %v (deadlock: %v)", err, deadlocked(err))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if status := <-banner; status != 409 {
		t.Errorf("the banner while the conductor asked: %d, want 409", status)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND run_id = $2 AND status = 'open'`, task, run); n != 1 {
		t.Errorf("%d open questions of the conductor, want 1", n)
	}
}

// The owner changes while the owner's choice waits for the task: Ana's
// answer, started while an ownership change holds the task's row, waits
// before checking who owns it, then finds Bo: 403, and nothing decided.
func TestTheOwnerChangingDuringAnAnswerRefusesIt(t *testing.T) {
	w, task := stuck(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", escalationQuestion)
	q := w.conductorQuestion(task)
	ctx := context.Background()
	// As the control plane changes a task's people: its row locked first.
	tx, err := w.second().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tasks WHERE id = $1 FOR UPDATE`, task); err != nil {
		t.Fatal(err)
	}
	answered := make(chan int, 1)
	go func() {
		status, _ := w.callAs(ana, "/internal/questions/"+q+"/answer", map[string]any{"text": "Retry as proposed"})
		answered <- status
	}()
	w.waiters(1)
	if _, err := tx.Exec(ctx, `DELETE FROM task_people WHERE task_id = $1`, task); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		SELECT $1, person_id, organization_id, 0 FROM api_keys WHERE id = $2`, task, bo); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if status := <-answered; status != 403 {
		t.Errorf("the former owner's answer: %d, want 403", status)
	}
	if d := w.decided(task); d != nil {
		t.Errorf("decided on a former owner's answer: %v", d)
	}
}

// removal is the control plane removing a person (people.ts removePerson
// and passOnTasks), its statements one by one in tx, uncommitted: the
// person's row, their keys and memberships, then each task they owned
// passed to its next person.
func (w *world) removal(tx pgx.Tx, person string) error {
	ctx := context.Background()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, w.org); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT role FROM people WHERE id = $1 AND removed_at IS NULL FOR UPDATE`, person); err != nil {
		return err
	}
	for _, sql := range []string{`UPDATE people SET removed_at = now() WHERE id = $1`,
		`UPDATE api_keys SET revoked_at = now() WHERE person_id = $1 AND revoked_at IS NULL`,
		`DELETE FROM push_subscriptions WHERE person_id = $1`} {
		if _, err := tx.Exec(ctx, sql, person); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT tp.task_id FROM task_people tp WHERE tp.person_id = $1 AND NOT EXISTS (
			SELECT 1 FROM task_people earlier JOIN people p ON p.id = earlier.person_id
			WHERE earlier.task_id = tp.task_id AND p.removed_at IS NULL
			  AND (earlier.position, earlier.person_id) < (tp.position, tp.person_id))`, person)
	if err != nil {
		return err
	}
	owned, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_people WHERE person_id = $1`, person); err != nil {
		return err
	}
	for _, task := range owned {
		var next, key *string
		err := tx.QueryRow(ctx, `SELECT tp.person_id FROM task_people tp JOIN people p ON p.id = tp.person_id
			WHERE tp.task_id = $1 AND p.removed_at IS NULL ORDER BY tp.position, tp.person_id LIMIT 1`, task).Scan(&next)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if next != nil {
			err := tx.QueryRow(ctx, `SELECT k.id FROM api_keys k JOIN people p ON p.id = k.person_id
				WHERE k.person_id = $1 AND k.revoked_at IS NULL AND p.removed_at IS NULL
				ORDER BY k.last_used_at DESC NULLS LAST, k.created_at DESC LIMIT 1`, *next).Scan(&key)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET owner_key_id = $2 WHERE id = $1`, task, key); err != nil {
			return err
		}
	}
	return nil
}

// handedOverWithBo is handedOver with Bo following the task, its owner once
// Ana is removed. Returns Ana's person id.
func handedOverWithBo(t *testing.T) (*world, string, string, delivery.RunRef) {
	w, task, ana, ref := handedOver(t)
	bo := w.person("Bo")
	mustExec(t, w.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		SELECT $1, person_id, organization_id, 1 FROM api_keys WHERE id = $2`, task, bo)
	var person string
	if err := w.owner.QueryRow(context.Background(), `SELECT person_id FROM api_keys WHERE id = $1`, ana).Scan(&person); err != nil {
		t.Fatal(err)
	}
	return w, task, person, ref
}

// Ana, the owner whose answer the conductor decides on, is removed while
// it decides. Its decision holding the task's row, the removal goes on up to
// passing the task on, waits there, and completes once the decision commits
// on the owner it read: the decision, then the removal.
func TestRemovingTheOwnerDuringTheConductorsDecisionWaitsForIt(t *testing.T) {
	w, task, ana, ref := handedOverWithBo(t)
	release := w.held(func(tx pgx.Tx) error {
		_, err := delivery.ConductDecideEscalation(context.Background(), tx, ref, "retry", "")
		return err
	})
	ctx := context.Background()
	tx, err := w.second().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	removed := make(chan error, 1)
	go func() { removed <- w.removal(tx, ana) }()
	w.waiters(1)
	if err := release(); err != nil {
		t.Fatalf("the conductor's decision: %v", err)
	}
	if err := <-removed; err != nil {
		t.Fatalf("the removal: %v (deadlock: %v)", err, deadlocked(err))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if d := w.decided(task); d["action"] != "retry" || d["conductor"] != ref.RunID {
		t.Errorf("the decision is %v, want the conductor's retry", d)
	}
	if n := w.count(`SELECT count(*) FROM people WHERE id = $1 AND removed_at IS NOT NULL`, ana); n != 1 {
		t.Error("Ana was not removed")
	}
}

// Ana is removed, uncommitted, before the conductor decides on her answer:
// the decision waits for the task's row the removal changed, then finds Bo
// its owner, who never answered: refused, nothing decided.
func TestTheConductorsDecisionAfterTheOwnersRemovalIsRefused(t *testing.T) {
	w, task, ana, ref := handedOverWithBo(t)
	ctx := context.Background()
	tx, err := w.second().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := w.removal(tx, ana); err != nil {
		t.Fatalf("the removal: %v", err)
	}
	decided := make(chan error, 1)
	go func() {
		decided <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			_, err := delivery.ConductDecideEscalation(ctx, tx, ref, "retry", "")
			return err
		})
	}()
	w.waiters(1)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-decided; err == nil || !strings.Contains(err.Error(), "not the task's owner") {
		t.Errorf("the conductor's decision after the owner was removed: %v, want refused as not the owner's answer", err)
	}
	if d := w.decided(task); d != nil {
		t.Errorf("decided on a removed owner's answer: %v", d)
	}
}

// The conductor's publish rechecks itself (Chat lock, the conductor's Run,
// then the delivery) while the owner answers the conductor's question:
// the answer waits on the Chat lock holding nothing, and goes on once the
// publish's transaction ends. Neither is a deadlock victim.
func TestAnAnswerWhileTheConductorsPublishRechecksWaitsForIt(t *testing.T) {
	w, task := stuck(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", escalationQuestion)
	q := w.conductorQuestion(task)
	run, _, _ := w.conductor(task)
	ctx := context.Background()
	tx, err := w.second().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// publishEligibleTx's first locks: the Chat lock, then the Run.
	if err := delivery.LockChat(ctx, tx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR NO KEY UPDATE`, run); err != nil {
		t.Fatal(err)
	}
	answered := make(chan int, 1)
	go func() {
		status, _ := w.callAs(ana, "/internal/questions/"+q+"/answer", map[string]any{"text": "Your call."})
		answered <- status
	}()
	w.waiters(1)
	if _, err := delivery.RecheckMovingTx(ctx, tx, delivery.PublishOf{TaskID: task, RunID: run}); err != nil {
		t.Fatalf("the publish's recheck: %v (deadlock: %v)", err, deadlocked(err))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if status := <-answered; status != 200 {
		t.Errorf("the owner's answer while the publish rechecked: %d, want 200", status)
	}
}
