-- Live work: what an agent's checkout looks like now, and what its machine
-- costs.
--
-- A Run's live diff is its checkout against the commit it started from,
-- read by the orchestrator through lux's exec while the agent works (after
-- each edit it reports, every so often while it works, and once more
-- before dude stops its container). Only the latest is kept, one row per
-- Run: it is a view of now. The ledger's run.diff.updated events carry
-- only a summary of each (paths and counts), so hunks never fill the
-- ledger. It stays after the Run ends, as the last thing the checkout held.
CREATE TABLE run_diffs (
  run_id          text PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- The commit the diff is against (the first repository's, with several).
  base            text NOT NULL,
  -- [{ path, status, additions, deletions, hunks: [{ header, lines }] }],
  -- cut at 1,000 lines a file and 5,000 in all (the file says truncated).
  files           jsonb NOT NULL DEFAULT '[]',
  -- sha256 of what git printed, and of the base: a read identical to the
  -- last is dropped before it is parsed, even by an orchestrator that just
  -- started.
  checksum        text NOT NULL,
  -- Read just before dude stopped the container, rather than while it ran.
  final           boolean NOT NULL DEFAULT false,
  updated_at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE run_diffs ENABLE ROW LEVEL SECURITY;
ALTER TABLE run_diffs FORCE ROW LEVEL SECURITY;
CREATE POLICY run_diffs_isolation ON run_diffs
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
GRANT SELECT, INSERT, UPDATE ON run_diffs TO dude_app;

-- Machine time costs money as tokens do. A Run records the hourly rate of
-- the machine it ran on when it is submitted (the orchestrator's
-- DUDE_MACHINE_USD_PER_HOUR today; a host's own rate once lux reports
-- one), so changing the rate later does not rewrite what past work cost.
-- NULL: a Run from before, whose machine cost is not known.
ALTER TABLE runs ADD COLUMN machine_usd_per_hour numeric(10, 4);
ALTER TABLE cost_samples ADD COLUMN machine_usd numeric(12, 6) NOT NULL DEFAULT 0;

-- The metrics gain machine cost: a Run's time on a host (its active time —
-- a parked Run holds none) at its rate. cost_usd stays what the agents
-- reported for tokens, so existing readers keep their meaning.
DROP FUNCTION epic_metrics(text);
DROP FUNCTION task_metrics(text);
DROP FUNCTION run_metrics(text);

CREATE FUNCTION run_metrics(p_run text)
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
      r.agent_cost_usd, r.input_tokens, r.output_tokens, r.machine_usd_per_hour
    FROM r WHERE r.started_at IS NOT NULL
    UNION ALL
    SELECT 0, 0, r.agent_cost_usd, r.input_tokens, r.output_tokens, r.machine_usd_per_hour
    FROM r WHERE r.started_at IS NULL)
  SELECT active_seconds::double precision, parked_seconds::double precision,
         COALESCE(agent_cost_usd, 0)::double precision, input_tokens, output_tokens,
         (active_seconds / 3600 * COALESCE(machine_usd_per_hour, 0))::double precision
  FROM m
$$;

CREATE FUNCTION task_metrics(p_task text)
RETURNS TABLE (lead_seconds double precision, active_seconds double precision,
               human_wait_seconds double precision, review_seconds double precision,
               cost_usd double precision, input_tokens bigint, output_tokens bigint, runs bigint,
               machine_usd double precision)
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
    (SELECT count(*) FROM runs WHERE task_id = p_task),
    COALESCE((SELECT sum(machine_usd) FROM rm), 0)
  FROM t
$$;

CREATE FUNCTION epic_metrics(p_epic text)
RETURNS TABLE (tasks bigint, done bigint, lead_seconds_median double precision,
               active_seconds double precision, human_wait_seconds double precision,
               review_seconds double precision, cost_usd double precision,
               input_tokens bigint, output_tokens bigint, machine_usd double precision)
LANGUAGE sql STABLE AS $$
  WITH tm AS (
    SELECT t.status, m.* FROM tasks t CROSS JOIN LATERAL task_metrics(t.id) m WHERE t.epic_id = p_epic)
  SELECT count(*), count(*) FILTER (WHERE status = 'done'),
         percentile_cont(0.5) WITHIN GROUP (ORDER BY lead_seconds) FILTER (WHERE status = 'done'),
         COALESCE(sum(active_seconds), 0), COALESCE(sum(human_wait_seconds), 0),
         COALESCE(sum(review_seconds), 0), COALESCE(sum(cost_usd), 0),
         COALESCE(sum(input_tokens), 0)::bigint, COALESCE(sum(output_tokens), 0)::bigint,
         COALESCE(sum(machine_usd), 0)
  FROM tm
$$;
