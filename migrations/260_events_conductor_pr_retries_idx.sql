-- dude:no-transaction
-- The notifier also tells the owner of a conductor's retry of
-- pr_loop_exhausted. It reads those in an arm of their own beside the
-- stops (247), so that each arm's predicate implies its own partial index;
-- 247's is left as it is. Match the arm's literal predicate. Concurrently,
-- a file of its own, IF NOT EXISTS, as 247.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_conductor_pr_retries_idx ON events (cursor)
  WHERE event_type = 'task.decided'
    AND payload->>'by' = 'conductor'
    AND payload->>'action' = 'retry'
    AND payload->>'reason' = 'pr_loop_exhausted';
