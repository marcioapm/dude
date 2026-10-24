-- 192_session_owner_sweep.sql — what follows a session's owner leaving
-- the organisation or coming back, done by the orchestrator from the
-- ledger rather than called by the backend after its commit: each
-- `session.owner_left` / `session.owner_returned` tells the session's live
-- agent once (if the session is still in that state), and each
-- `person.removed` withdraws every open question put to that person by
-- name. The sweep keeps a watermark and the events done above it, as the
-- notifier does for asks (push_config, push_sent): cursors are handed out
-- at insert and seen at commit, so the watermark moves only past events
-- old enough that no transaction can still be committing one.

-- One row. Starts at the present: nothing recorded before is replayed.
CREATE TABLE session_owner_sweep (
  id           boolean PRIMARY KEY DEFAULT true CHECK (id),
  after_cursor bigint NOT NULL
);
INSERT INTO session_owner_sweep (after_cursor) SELECT COALESCE(max(cursor), 0) FROM events;
GRANT SELECT, UPDATE ON session_owner_sweep TO dude_sweeper;

-- The events done above the watermark. Written in the organisation's own
-- transaction, the one that tells the agent, so a change is told once.
CREATE TABLE session_owner_done (
  cursor          bigint PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE
);
ALTER TABLE session_owner_done ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_owner_done FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON session_owner_done
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT ON session_owner_done TO dude_app;
GRANT SELECT, DELETE ON session_owner_done TO dude_sweeper;
