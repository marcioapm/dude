-- A task aborted from a Run before 029's fixes finished with no status
-- change in the ledger, so its lead time ran on forever. Finished is
-- finished: with no event to say when, it ended when the task was last
-- changed.
CREATE OR REPLACE FUNCTION task_metrics(p_task text)
RETURNS TABLE (lead_seconds double precision, active_seconds double precision,
               human_wait_seconds double precision, review_seconds double precision,
               cost_usd double precision, input_tokens bigint, output_tokens bigint, runs bigint)
LANGUAGE sql STABLE AS $$
  WITH t AS (SELECT * FROM tasks WHERE id = p_task),
  done_at AS (
    SELECT COALESCE(min(e.occurred_at), (SELECT updated_at FROM t)) AS at FROM events e
    WHERE e.task_id = p_task AND e.event_type = 'task.status_changed'
      AND e.payload->>'status' IN ('done', 'aborted', 'failed')
    HAVING (SELECT status FROM t) IN ('done', 'aborted', 'failed')),
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
