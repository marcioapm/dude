package agenttools

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Migration 234 indexes a task's git.commit_created events: on a task with
// thousands of other events, I-3's probe of whether dude pushed a pull
// request's head (delivery.unreviewedPRPushesTx) and a conductor publish's
// once-per-repository check (phases' publish) read
// events_task_commit_created_idx, not every event of the task.
func TestACommitCreatedProbeReadsItsIndex(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "234")
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ('org_p', 'P', 'p')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		SELECT 'evt_noise_' || n, 'org_p', (ARRAY['agent.tool.dude', 'agent.thought', 'agent.message'])[n % 3 + 1],
			'tsk_busy', 'agent', 'run_x', 'agent', jsonb_build_object('text', 'x')
		FROM generate_series(1, 20000) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		SELECT 'evt_push_' || n, 'org_p', 'git.commit_created', 'tsk_busy', 'agent', 'run_x', 'orchestrator',
			jsonb_build_object('repo', 'target', 'headSha', 'sha' || n, 'publishId', 'pub_' || n)
		FROM generate_series(1, 5) n`); err != nil {
		t.Fatal(err)
	}
	apply()
	if _, err := owner.Exec(ctx, `ANALYZE events`); err != nil {
		t.Fatal(err)
	}
	reset := asApp(t, owner, "org_p")
	defer reset()
	pushed := `SELECT EXISTS (SELECT 1 FROM events e WHERE e.task_id = $1 AND e.event_type = $2
		AND e.payload->>'repo' = $3 AND e.payload->>'headSha' = $4)`
	var ok bool
	if err := owner.QueryRow(ctx, pushed, "tsk_busy", "git.commit_created", "target", "sha3").Scan(&ok); err != nil || !ok {
		t.Fatalf("the probe found %v (%v), want the push", ok, err)
	}
	if !planUses(t, owner, "events_task_commit_created_idx", pushed, "tsk_busy", "git.commit_created", "target", "sha3") {
		t.Error("I-3's pushed-by-dude probe does not read events_task_commit_created_idx")
	}
	said := `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->>'publishId' = $3 AND payload->>'repo' = $4)`
	if !planUses(t, owner, "events_task_commit_created_idx", said, "tsk_busy", "git.commit_created", "pub_2", "target") {
		t.Error("a publish's once-per-repository check does not read events_task_commit_created_idx")
	}
}
