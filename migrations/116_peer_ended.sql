-- 116_peer_ended.sql — a wait on a peer also ends when the peer's task
-- ends without replying (docs/design/agent-coordination.md, "As built");
-- and the scripted agent's fake/peer is a test model a tier may request.
--
-- peer_waits.closed_by gains 'peer_ended'. Each check is added NOT VALID
-- and validated after, so its table is scanned under SHARE UPDATE
-- EXCLUSIVE only; the drop and add take its lock for the catalog change.

ALTER TABLE peer_waits DROP CONSTRAINT peer_waits_closed_by_check;
ALTER TABLE peer_waits ADD CONSTRAINT peer_waits_closed_by_check
  CHECK (closed_by IN ('reply', 'self', 'run_ended', 'peer_ended')) NOT VALID;
ALTER TABLE peer_waits VALIDATE CONSTRAINT peer_waits_closed_by_check;

-- fake/peer: an implementer that asks its epic's live peer and waits, or
-- answers each peer message it reads. The list keeps 113's fake/subagent
-- and fake/subagent-ask: this check replaces 113's whole.
ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/subagent', 'fake/subagent-ask',
                   'fake/peer')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]')) NOT VALID;
ALTER TABLE model_tiers VALIDATE CONSTRAINT model_tiers_model_check;
