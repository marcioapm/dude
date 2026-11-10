-- 261_pull_request_ready_gate.sql — the last ready gate computed per pull
-- request.
--
-- ready_gate: {head, at, ready, items}: the gate (delivery.ReadyGate) as
-- weighReadiness, or the conductor's pull_requests tool, last judged it,
-- at which head and when. pull_requests serves it while the head it was
-- judged at is still the one recorded, and reads GitHub again only when it
-- is not, so listing a task's pull requests does not re-read every gate.
-- NULL for every existing pull request: none judged yet.
ALTER TABLE pull_requests ADD COLUMN ready_gate jsonb;
