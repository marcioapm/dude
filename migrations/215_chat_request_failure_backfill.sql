-- 215_chat_request_failure_backfill.sql — the cause of each request that
-- failed before 214, from its reasoning. Its own file, so its own
-- transaction: it holds row locks only, not 214's ACCESS EXCLUSIVE.
-- Bounded to the 30 days chat retention keeps.
--
-- Only causes whose writer scrubbed them are copied: giving up (the whole
-- reasoning), the provider's refusal of the reply, and the database's
-- refusal of what was made (the clause appended last, pgx's own
-- "(SQLSTATE …)" nested in it). A failed routing call's error was stored
-- unredacted, so those rows keep no failure. Anything else stays NULL
-- rather than copy the model's words.

UPDATE chat_requests
  SET failure = left(CASE
      WHEN reasoning LIKE 'the routing call failed: %' THEN NULL
      WHEN reasoning LIKE 'dude could not act on it: %' OR reasoning ~ '^[A-Za-z]+ did not answer after [0-9]+ tries$' THEN reasoning
      ELSE COALESCE(substring(reasoning FROM '^.* \((its reply was not posted: .*)\)$'),
        substring(reasoning FROM '^.* \(([A-Z]+: .* \(SQLSTATE [0-9A-Z]{5}\))\)$')) END, 2000)
  WHERE route = 'failed' AND reasoning <> '' AND created_at > now() - interval '30 days';
