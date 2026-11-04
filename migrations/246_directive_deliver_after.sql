-- 244_directive_deliver_after.sql — a directive held until a time: a turn
-- retried after a temporary model error (phases retryTurn) is told to carry
-- on once its back-off is over. Only that directive waits: the Run's own
-- back-off (runs.next_attempt_at) also holds a person's pause, a child's
-- abort and every other sweep action, so the retry's wait is kept here.
-- NULL, every directive before this one and any other kind: sent as soon
-- as the Run can take it.
-- (242 and 243 are taken by feat/cf-chokepoint.)
ALTER TABLE directives ADD COLUMN deliver_after timestamptz;
