-- One rule for what a Run cost, so every screen agrees.
--
-- Model: lux's AI cost once lux has reported one (its LLM proxy meters
-- every token at list price), else what the agent's harness reported.
-- Never the two added: they price the same tokens.
-- Machine: lux's compute cost once reported, else dude's estimate, the
-- Run's active time at the rate it recorded (050).
--
-- A function of the row, so a list (the board) takes it without reading
-- each Run's events as run_metrics does.
CREATE FUNCTION run_model_usd(r runs) RETURNS numeric
LANGUAGE sql IMMUTABLE AS $$
  SELECT COALESCE(r.lux_ai_usd, r.agent_cost_usd, 0)
$$;

-- Same columns as 050's, so task_metrics and epic_metrics, which sum it,
-- follow without change.
CREATE OR REPLACE FUNCTION run_metrics(p_run text)
RETURNS TABLE (active_seconds double precision, parked_seconds double precision,
               cost_usd double precision, input_tokens bigint, output_tokens bigint,
               machine_usd double precision)
LANGUAGE sql STABLE AS $$
  WITH r AS (SELECT * FROM runs WHERE id = p_run),
  parks AS (
    SELECT p.occurred_at AS from_at,
           COALESCE((SELECT min(u.occurred_at) FROM events u
                     WHERE u.run_id = p.run_id AND u.event_type = 'run.unparked' AND u.cursor > p.cursor),
                    (SELECT ended_at FROM r), now()) AS to_at
    FROM events p WHERE p.run_id = p_run AND p.event_type = 'run.parked'),
  m AS (
    SELECT
      GREATEST(0, EXTRACT(EPOCH FROM (COALESCE(r.ended_at, now()) - r.started_at))
                  - COALESCE((SELECT sum(EXTRACT(EPOCH FROM (to_at - from_at))) FROM parks), 0)) AS active_seconds,
      COALESCE((SELECT sum(EXTRACT(EPOCH FROM (to_at - from_at))) FROM parks), 0) AS parked_seconds,
      run_model_usd(r) AS model_usd, r.lux_compute_usd, r.input_tokens, r.output_tokens, r.machine_usd_per_hour
    FROM r WHERE r.started_at IS NOT NULL
    UNION ALL
    SELECT 0, 0, run_model_usd(r), r.lux_compute_usd, r.input_tokens, r.output_tokens, r.machine_usd_per_hour
    FROM r WHERE r.started_at IS NULL)
  SELECT active_seconds::double precision, parked_seconds::double precision,
         model_usd::double precision, input_tokens, output_tokens,
         COALESCE(lux_compute_usd, active_seconds / 3600 * COALESCE(machine_usd_per_hour, 0))::double precision
  FROM m
$$;
