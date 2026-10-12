-- 112_peer_ended.sql — a wait on a peer also ends when the peer's task
-- ends without replying (docs/design/agent-coordination.md, "As built").
--
-- peer_waits.closed_by gains 'peer_ended'. The new check is added NOT
-- VALID and validated after, so the table is scanned under SHARE UPDATE
-- EXCLUSIVE only; the drop and add take its lock for the catalog change.

ALTER TABLE peer_waits DROP CONSTRAINT peer_waits_closed_by_check;
ALTER TABLE peer_waits ADD CONSTRAINT peer_waits_closed_by_check
  CHECK (closed_by IN ('reply', 'self', 'run_ended', 'peer_ended')) NOT VALID;
ALTER TABLE peer_waits VALIDATE CONSTRAINT peer_waits_closed_by_check;
