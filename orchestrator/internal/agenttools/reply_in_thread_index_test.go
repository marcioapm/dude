package agenttools

import (
	"context"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// reply_in_thread's budget count, over a long session among many others'
// tool attempts: it counts only this turn's reply_in_thread attempts, and
// reads them along events_session_tool_attempts_idx, not every attempt of
// every session (events_task_tool_attempts_idx) or the session's whole
// history (events_session_cursor_idx).
func TestTheThreadReplyBudgetCountsTheTurnAlongTheSessionsAttemptsIndex(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ('org_r', 'R', 'r')`); err != nil {
		t.Fatal(err)
	}
	// 20,000 events of the long session (thoughts, tool calls, attempts of
	// other tools) over two days, and 20,000 attempts in 200 other sessions.
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, task_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_l' || n, 'org_r', (ARRAY['agent.thought', 'agent.tool.dude', 'agent.tool.attempted'])[n % 3 + 1], 'ses_long', NULL,
			now() - interval '2 days' + make_interval(secs => n * 8), 'agent', 'run_l', 'agent', jsonb_build_object('tool', 'chat_history')
		FROM generate_series(1, 20000) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, task_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_o' || n, 'org_r', 'agent.tool.attempted', 'ses_' || n % 200, 'tsk_' || n % 50, now() - make_interval(mins => n % 3000),
			'agent', 'run_o', 'agent', jsonb_build_object('tool', 'reply_in_thread')
		FROM generate_series(1, 20000) n`); err != nil {
		t.Fatal(err)
	}
	// The other sessions' and tasks' conversations: 20,000 messages.
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, run_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_m' || n, 'org_r', (ARRAY['chat.message', 'agent.message'])[n % 2 + 1], 'ses_' || n % 200, 'run_' || n % 400,
			now() - make_interval(mins => n % 3000), (ARRAY['person', 'agent'])[n % 2 + 1], 'per_a', 'orchestrator', '{"text":"words"}'
		FROM generate_series(1, 20000) n`); err != nil {
		t.Fatal(err)
	}
	// The turn: a person's message ten minutes ago, then two replies among
	// 3,000 of the agent's thoughts and tool calls; one reply before it, in
	// the last turn.
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, occurred_at, actor_type, actor_id, source, payload)
		VALUES ('evt_before', 'org_r', 'agent.tool.attempted', 'ses_long', now() - interval '20 minutes', 'agent', 'run_l', 'agent', '{"tool":"reply_in_thread"}'),
			('evt_asked', 'org_r', 'chat.message', 'ses_long', now() - interval '10 minutes', 'person', 'per_a', 'orchestrator', '{"text":"hi"}'),
			('evt_r1', 'org_r', 'agent.tool.attempted', 'ses_long', now() - interval '9 minutes', 'agent', 'run_l', 'agent', '{"tool":"reply_in_thread"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_t' || n, 'org_r', (ARRAY['agent.thought', 'agent.tool.dude'])[n % 2 + 1], 'ses_long', now() - interval '9 minutes' + make_interval(secs => n * 0.1),
			'agent', 'run_l', 'agent', '{"tool":"search_memory"}'
		FROM generate_series(1, 3000) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, occurred_at, actor_type, actor_id, source, payload)
		VALUES ('evt_r2', 'org_r', 'agent.tool.attempted', 'ses_long', now() - interval '1 minute', 'agent', 'run_l', 'agent', '{"tool":"reply_in_thread"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `ANALYZE events`); err != nil {
		t.Fatal(err)
	}
	reset := asApp(t, owner, "org_r")
	defer reset()
	var cursor int64
	var at time.Time
	var payload []byte
	if err := owner.QueryRow(ctx, latestInput, "ses_long").Scan(&cursor, &at, &payload); err != nil {
		t.Fatal(err)
	}
	var made int
	if err := owner.QueryRow(ctx, repliesThisTurn, "ses_long", at, cursor).Scan(&made); err != nil || made != 2 {
		t.Fatalf("this turn's replies: %d (%v), want 2", made, err)
	}
	if !planUses(t, owner, "events_session_tool_attempts_idx", repliesThisTurn, "ses_long", at, cursor) {
		t.Error("the budget count does not read events_session_tool_attempts_idx")
	}
	for _, other := range []string{"events_task_tool_attempts_idx", "events_pkey", "events_session_cursor_idx"} {
		if planUses(t, owner, other, repliesThisTurn, "ses_long", at, cursor) {
			t.Errorf("the budget count reads %s", other)
		}
	}
}
