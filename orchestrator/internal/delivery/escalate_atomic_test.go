package delivery

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// An escalation's task status and its question.asked event commit
// together: the API derives `escalation` from that event, so a reader that
// sees awaiting_input without it shows a stopped task with no reason. The
// escalation event's insert is held on an advisory lock (a trigger of this
// test's own); while it waits, a reader must see neither or both.
func TestAnEscalationsStatusAndQuestionCommitTogether(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	const gate = 0x65736361 // "esca"
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_e', $1, 'prj_e', 1, 'T', 'g', 'running')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `CREATE FUNCTION test_hold_escalation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(`+strconv.Itoa(gate)+`); RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE TRIGGER test_hold_escalation BEFORE INSERT ON events FOR EACH ROW
		WHEN (NEW.event_type = 'question.asked' AND NEW.payload->>'kind' = 'escalation')
		EXECUTE FUNCTION test_hold_escalation()`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `SELECT pg_advisory_lock($1)`, gate); err != nil {
		t.Fatal(err)
	}

	w := &steps{s: &Store{DB: app}}
	sc := workflow.StepContext{WorkflowRunID: "wf_e", OrganizationID: org, Step: "awaitImplement"}
	st := &State{TaskID: "wi_e", ProjectID: "prj_e"}
	done := make(chan error, 1)
	go func() {
		_, err := w.escalate(ctx, sc, st, "implement_failed", map[string]any{"runId": "run_gone", "error": "no model"})
		done <- err
	}()

	// The escalation's insert is waiting on the gate.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		select {
		case err := <-done:
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

	if _, err := owner.Exec(ctx, `SELECT pg_advisory_unlock($1)`, gate); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if status, asked := seen(); status != "awaiting_input" || asked != 1 {
		t.Errorf("after the escalation: status %s with %d escalation events; want awaiting_input with one", status, asked)
	}
}
