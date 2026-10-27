-- 215_chat_request_failure_backfill.sql — the cause of each request that
-- failed before 214, from its reasoning. Its own file, so its own
-- transaction: it holds row locks only, not 214's ACCESS EXCLUSIVE.
-- Bounded to the 30 days chat retention keeps.

UPDATE chat_requests
  SET failure = left(CASE
      WHEN reasoning LIKE 'the routing call failed: %' OR reasoning LIKE 'dude could not act on it: %'
        OR reasoning LIKE '% did not answer after % tries' THEN reasoning
      ELSE COALESCE(substring(reasoning FROM '\(([^()]*)\)$'), reasoning) END, 2000)
  WHERE route = 'failed' AND reasoning <> '' AND created_at > now() - interval '30 days';
