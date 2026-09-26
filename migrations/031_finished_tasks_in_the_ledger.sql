-- A task a person aborted from a Run, before 029, finished with no status
-- change in the ledger: its lead time, and the review span it was in, ran
-- on forever. 030 ended them at updated_at, which moves with any later
-- edit. Instead the ledger gets the change it missed, once, dated when the
-- task's last Run ended (else when the task was last changed, now), and
-- the metrics read only the ledger again.
INSERT INTO events (id, organization_id, event_type, occurred_at, project_id, task_id,
                    actor_type, actor_id, source, correlation_id, payload)
SELECT 'evt_backfill_' || md5(t.id), t.organization_id, 'task.status_changed',
       COALESCE((SELECT max(r.ended_at) FROM runs r WHERE r.task_id = t.id), t.updated_at),
       t.project_id, t.id, 'system', 'migration', 'control-plane', t.id,
       jsonb_build_object('status', t.status::text, 'reason', 'recorded late: finished before its change was')
FROM tasks t
WHERE t.status IN ('done', 'aborted', 'failed')
  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.task_id = t.id AND e.event_type = 'task.status_changed'
                  AND e.payload->>'status' IN ('done', 'aborted', 'failed'));

CREATE OR REPLACE FUNCTION task_metrics(p_task text)
RETURNS TABLE (lead_seconds double precision, active_seconds double precision,
               human_wait_seconds double precision, review_seconds double precision,
               cost_usd double precision, input_tokens bigint, output_tokens bigint, runs bigint)
LANGUAGE sql STABLE AS $$
  WITH t AS (SELECT * FROM tasks WHERE id = p_task),
  done_at AS (
    SELECT min(e.occurred_at) AS at FROM events e
    WHERE e.task_id = p_task AND e.event_type = 'task.status_changed'
      AND e.payload->>'status' IN ('done', 'aborted', 'failed')
      AND (SELECT status FROM t) IN ('done', 'aborted', 'failed')),
  rm AS (SELECT m.* FROM runs r CROSS JOIN LATERAL run_metrics(r.id) m WHERE r.task_id = p_task),
  -- An ask left unanswered until its Run ended was waited on until then.
  waits AS (
    SELECT EXTRACT(EPOCH FROM (COALESCE(q.answered_at, CASE WHEN q.status = 'open' THEN now() END,
                                        r.ended_at, q.asked_at) - q.asked_at)) AS s
    FROM questions q LEFT JOIN runs r ON r.id = q.run_id WHERE q.task_id = p_task
    UNION ALL
    SELECT EXTRACT(EPOCH FROM (COALESCE(q.decided_at, CASE WHEN q.status = 'pending' THEN now() END,
                                        r.ended_at, q.created_at) - q.created_at))
    FROM repository_requests q LEFT JOIN runs r ON r.id = q.run_id WHERE q.task_id = p_task),
  -- Each status change, and when the next one came (or now).
  spans AS (
    SELECT e.payload->>'status' AS status, e.occurred_at AS from_at,
           COALESCE(lead(e.occurred_at) OVER (ORDER BY e.cursor), now()) AS to_at
    FROM events e WHERE e.task_id = p_task AND e.event_type = 'task.status_changed')
  SELECT
    EXTRACT(EPOCH FROM (COALESCE((SELECT at FROM done_at), now()) - (SELECT created_at FROM t))),
    COALESCE((SELECT sum(active_seconds) FROM rm), 0),
    COALESCE((SELECT sum(s) FROM waits), 0),
    COALESCE((SELECT sum(EXTRACT(EPOCH FROM (to_at - from_at))) FROM spans WHERE status IN ('review', 'ready_to_merge')), 0),
    COALESCE((SELECT sum(cost_usd) FROM rm), 0),
    COALESCE((SELECT sum(input_tokens) FROM rm), 0)::bigint,
    COALESCE((SELECT sum(output_tokens) FROM rm), 0)::bigint,
    (SELECT count(*) FROM runs WHERE task_id = p_task)
  FROM t
$$;
