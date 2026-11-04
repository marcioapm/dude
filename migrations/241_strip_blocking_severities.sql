-- 241_strip_blocking_severities.sql — every delivery has the one fix bar
-- now (blocking, high, medium, and a reviewer's low marked trivial): the
-- retired `blockingSeverities` delivery key is dropped from the policies
-- organisations and projects stored, so nothing reads or shows it again.
-- Every other key is left as it is; a task's frozen policy keeps it, and
-- the orchestrator ignores it there.

UPDATE organizations SET delivery_policy = delivery_policy - 'blockingSeverities'
  WHERE delivery_policy ? 'blockingSeverities';
UPDATE projects SET delivery_policy = delivery_policy - 'blockingSeverities'
  WHERE delivery_policy ? 'blockingSeverities';
