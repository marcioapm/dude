-- Time and cost, from what is already recorded (plan §18.4): per Run, per
-- task and per epic. Nothing new is stored; these read the ledger and the
-- Runs' own timings, so every screen and the API agree.
--
-- For a task:
--   lead time      created → done (or now)
--   agent active   the sum of its Runs' running time (started → ended, or
--                  now), less the time they were parked waiting on a person
--   human wait     questions asked → answered, repository requests made →
--                  decided (or now, while open)
--   review time    in the review or ready-to-merge status, from the
--                  ledger's status changes
--   cost, tokens   the Runs' totals, as the agents reported them
--
-- Intervals are returned as seconds (double precision): the API and the
-- app speak milliseconds and numbers, not Postgres intervals.

CREATE FUNCTION run_metrics(p_run text)
RETURNS TABLE (active_seconds double precision, parked_seconds double precision,
               cost_usd double precision, input_tokens bigint, output_tokens bigint)
LANGUAGE sql STABLE AS $$
  WITH r AS (SELECT * FROM runs WHERE id = p_run),
  -- Parked spans: from each run.parked to the run.unparked after it (or now,
  -- while it is parked).
  parks AS (
    SELECT p.occurred_at AS from_at,
           COALESCE((SELECT min(u.occurred_at) FROM events u
                     WHERE u.run_id = p.run_id AND u.event_type = 'run.unparked' AND u.cursor > p.cursor),
                    (SELECT ended_at FROM r), now()) AS to_at
    FROM events p WHERE p.run_id = p_run AND p.event_type = 'run.parked')
  SELECT
    GREATEST(0, EXTRACT(EPOCH FROM (COALESCE(r.ended_at, now()) - r.started_at))
                - COALESCE((SELECT sum(EXTRACT(EPOCH FROM (to_at - from_at))) FROM parks), 0)),
    COALESCE((SELECT sum(EXTRACT(EPOCH FROM (to_at - from_at))) FROM parks), 0),
    COALESCE(r.agent_cost_usd, 0)::double precision,
    r.input_tokens, r.output_tokens
  FROM r WHERE r.started_at IS NOT NULL
  UNION ALL
  SELECT 0, 0, COALESCE(r.agent_cost_usd, 0)::double precision, r.input_tokens, r.output_tokens
  FROM r WHERE r.started_at IS NULL
$$;

CREATE FUNCTION task_metrics(p_task text)
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
  waits AS (
    SELECT EXTRACT(EPOCH FROM (COALESCE(answered_at, CASE WHEN status = 'open' THEN now() END, asked_at) - asked_at)) AS s
    FROM questions WHERE task_id = p_task
    UNION ALL
    SELECT EXTRACT(EPOCH FROM (COALESCE(decided_at, CASE WHEN status = 'pending' THEN now() END, created_at) - created_at))
    FROM repository_requests WHERE task_id = p_task),
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

-- An epic: its tasks' totals, and how long its finished ones took.
CREATE FUNCTION epic_metrics(p_epic text)
RETURNS TABLE (tasks bigint, done bigint, lead_seconds_median double precision,
               active_seconds double precision, human_wait_seconds double precision,
               review_seconds double precision, cost_usd double precision,
               input_tokens bigint, output_tokens bigint)
LANGUAGE sql STABLE AS $$
  WITH tm AS (
    SELECT t.status, m.* FROM tasks t CROSS JOIN LATERAL task_metrics(t.id) m WHERE t.epic_id = p_epic)
  SELECT count(*), count(*) FILTER (WHERE status = 'done'),
         percentile_cont(0.5) WITHIN GROUP (ORDER BY lead_seconds) FILTER (WHERE status = 'done'),
         COALESCE(sum(active_seconds), 0), COALESCE(sum(human_wait_seconds), 0),
         COALESCE(sum(review_seconds), 0), COALESCE(sum(cost_usd), 0),
         COALESCE(sum(input_tokens), 0)::bigint, COALESCE(sum(output_tokens), 0)::bigint
  FROM tm
$$;
