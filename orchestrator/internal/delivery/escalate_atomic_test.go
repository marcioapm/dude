package delivery

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// escalatingTask seeds the running task wi_e and installs a BEFORE INSERT
// trigger whose PL/pgSQL body runs on the task's escalation event only.
func escalatingTask(ctx context.Context, t *testing.T, owner *pgx.Conn, org, body string) (*State, workflow.StepContext) {
	t.Helper()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_e', $1, 'prj_e', 1, 'T', 'g', 'running')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `CREATE FUNCTION test_on_escalation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN `+body+` END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE TRIGGER test_on_escalation BEFORE INSERT ON events FOR EACH ROW
		WHEN (NEW.event_type = 'question.asked' AND NEW.payload->>'kind' = 'escalation')
		EXECUTE FUNCTION test_on_escalation()`); err != nil {
		t.Fatal(err)
	}
	return &State{TaskID: "wi_e", ProjectID: "prj_e"},
		workflow.StepContext{WorkflowRunID: "wf_e", OrganizationID: org, Step: "awaitImplement"}
}

// An escalation's task status and its question.asked event commit
// together: the API derives `escalation` from that event, so a reader that
// sees awaiting_input without it shows a stopped task with no reason. The
// escalation event's insert is held on an advisory lock (a trigger of this
// test's own); while it waits, a reader must see neither or both.
//
// The gate samples only the moment before question.asked is inserted: it
// rejects a status committed ahead of its question, not a question
// committed ahead of its status.
func TestAnEscalationsStatusAndQuestionCommitTogether(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const gate = 0x65736361 // "esca"
	st, sc := escalatingTask(ctx, t, owner, org, `PERFORM pg_advisory_xact_lock(`+strconv.Itoa(gate)+`); RETURN NEW;`)
	if _, err := owner.Exec(ctx, `SELECT pg_advisory_lock($1)`, gate); err != nil {
		t.Fatal(err)
	}

	w := &steps{s: &Store{DB: app}}
	done := make(chan error, 1)
	go func() {
		_, err := w.escalate(ctx, sc, st, "implement_failed", map[string]any{"runId": "run_gone", "error": "no model"})
		done <- err
	}()
	released, joined := false, false
	var result error
	release := func() error {
		if released {
			return nil
		}
		released = true
		_, err := owner.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, gate)
		return err
	}
	// Bounded by ctx, which escalate's statements are cancelled by.
	join := func() error {
		if !joined {
			joined, result = true, <-done
		}
		return result
	}
	// Registered after dbtest.Open's, so it runs first: on any exit the worker
	// is unblocked and finished before owner, app and the database go away.
	t.Cleanup(func() {
		_ = release()
		_ = join()
	})

	// The escalation's insert is waiting on this test's gate.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE l.locktype = 'advisory' AND l.classid = 0 AND l.objid::bigint = $1 AND l.objsubid = 1
				AND NOT l.granted AND a.datname = current_database()`, gate).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		select {
		case err := <-done:
			joined, result = true, err
			t.Fatalf("escalate returned before its escalation was written: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the escalation's insert never reached the gate")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// One statement, one snapshot: the task's status and its escalation.
	seen := func() (status string, asked int) {
		if err := owner.QueryRow(ctx, `SELECT t.status::text, (SELECT count(*) FROM events e WHERE e.task_id = t.id
			AND e.event_type = 'question.asked' AND e.payload->>'kind' = 'escalation') FROM tasks t WHERE t.id = 'wi_e'`).
			Scan(&status, &asked); err != nil {
			t.Fatal(err)
		}
		return status, asked
	}
	if status, asked := seen(); status != "running" || asked != 0 {
		t.Errorf("mid-escalation a reader saw status %s with %d escalation events; want running with none", status, asked)
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := join(); err != nil {
		t.Fatal(err)
	}
	if status, asked := seen(); status != "awaiting_input" || asked != 1 {
		t.Errorf("after the escalation: status %s with %d escalation events; want awaiting_input with one", status, asked)
	}
}

// When the escalation's question.asked insert fails, the awaiting_input
// status written before it rolls back with it.
func TestAnEscalationWhoseQuestionFailsLeavesTheTaskAsItWas(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, sc := escalatingTask(ctx, t, owner, org, `RAISE EXCEPTION 'escalation refused';`)

	w := &steps{s: &Store{DB: app}}
	if _, err := w.escalate(ctx, sc, st, "implement_failed", map[string]any{"runId": "run_gone"}); err == nil {
		t.Fatal("escalate succeeded although its question.asked insert failed")
	}
	var status string
	var changed int
	if err := owner.QueryRow(ctx, `SELECT t.status::text, (SELECT count(*) FROM events e WHERE e.task_id = t.id
		AND e.event_type = 'task.status_changed') FROM tasks t WHERE t.id = 'wi_e'`).Scan(&status, &changed); err != nil {
		t.Fatal(err)
	}
	if status != "running" || changed != 0 {
		t.Errorf("after a failed escalation event: status %s with %d status changes; want running with none", status, changed)
	}
}
