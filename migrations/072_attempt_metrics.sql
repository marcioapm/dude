-- One attempt's time and cost: task_metrics's figures over the agent Runs
-- of one attempt only, for the task page, which shows one attempt at a time.
--
-- An attempt runs from its first agent Run's creation until the next
-- attempt's first Run (when it was set aside by a start over), or, for the
-- latest, until the task finished, else now. The finish is the last
-- terminal status within the attempt: Resume and Try again keep the
-- attempt, so it may have stopped and been picked up before it finished,
-- and an earlier attempt's abort comes before this attempt began. Lead time
-- is that span; the time in review is the task's review spans clipped to
-- it; the waits on people are those on the attempt's Runs' questions and
-- requests.
CREATE FUNCTION attempt_metrics(p_task text, p_attempt integer)
RETURNS TABLE (lead_seconds double precision, active_seconds double precision,
               human_wait_seconds double precision, review_seconds double precision,
               cost_usd double precision, input_tokens bigint, output_tokens bigint, runs bigint,
               machine_usd double precision)
LANGUAGE sql STABLE AS $$
  WITH t AS (SELECT * FROM tasks WHERE id = p_task),
  mine AS (SELECT * FROM runs WHERE task_id = p_task AND kind = 'agent' AND attempt = p_attempt),
  began AS (SELECT min(created_at) AS at FROM mine),
  done_at AS (
    SELECT max(e.occurred_at) AS at FROM events e
    WHERE e.task_id = p_task AND e.event_type = 'task.status_changed'
      AND e.payload->>'status' IN ('done', 'aborted', 'failed')
      AND e.occurred_at >= (SELECT at FROM began)
      AND (SELECT status FROM t) IN ('done', 'aborted', 'failed')),
  win AS (
    SELECT (SELECT at FROM began) AS from_at,
           COALESCE((SELECT min(created_at) FROM runs WHERE task_id = p_task AND kind = 'agent' AND attempt > p_attempt),
                    (SELECT at FROM done_at), now()) AS to_at),
  rm AS (SELECT m.* FROM mine r CROSS JOIN LATERAL run_metrics(r.id) m),
  waits AS (
    SELECT EXTRACT(EPOCH FROM (COALESCE(q.answered_at, CASE WHEN q.status = 'open' THEN now() END,
                                        r.ended_at, q.asked_at) - q.asked_at)) AS s
    FROM questions q JOIN mine r ON r.id = q.run_id WHERE q.task_id = p_task
    UNION ALL
    SELECT EXTRACT(EPOCH FROM (COALESCE(q.decided_at, CASE WHEN q.status = 'pending' THEN now() END,
                                        r.ended_at, q.created_at) - q.created_at))
    FROM repository_requests q JOIN mine r ON r.id = q.run_id WHERE q.task_id = p_task),
  spans AS (
    SELECT e.payload->>'status' AS status, e.occurred_at AS from_at,
           COALESCE(lead(e.occurred_at) OVER (ORDER BY e.cursor), now()) AS to_at
    FROM events e WHERE e.task_id = p_task AND e.event_type = 'task.status_changed')
  SELECT
    COALESCE(EXTRACT(EPOCH FROM ((SELECT to_at FROM win) - (SELECT from_at FROM win))), 0),
    COALESCE((SELECT sum(active_seconds) FROM rm), 0),
    COALESCE((SELECT sum(s) FROM waits), 0),
    COALESCE((SELECT sum(GREATEST(0, EXTRACT(EPOCH FROM (LEAST(s.to_at, w.to_at) - GREATEST(s.from_at, w.from_at)))))
              FROM spans s, win w WHERE s.status IN ('review', 'ready_to_merge')), 0),
    COALESCE((SELECT sum(cost_usd) FROM rm), 0),
    COALESCE((SELECT sum(input_tokens) FROM rm), 0)::bigint,
    COALESCE((SELECT sum(output_tokens) FROM rm), 0)::bigint,
    (SELECT count(*) FROM mine),
    COALESCE((SELECT sum(machine_usd) FROM rm), 0)
  FROM t
$$;
