-- 181_org_sessions.sql — sessions open to the whole organisation, sessions
-- owned by the organisation's admins after their owner left, and
-- @-mentions in a session.
--
-- access: private (its accepted members) or organisation (every active
-- member of its organisation may open it and chat). One way only: the
-- trigger refuses organisation → private, whoever writes the row.
--
-- owned_by_admins: the owner was removed from the organisation while the
-- session was open to it or shared. The departed owner's row stays (one
-- owner, migration 086); while this is set every active admin acts as owner,
-- until one takes it over.

ALTER TABLE sessions
  ADD COLUMN access text NOT NULL DEFAULT 'private' CHECK (access IN ('private', 'organisation')),
  ADD COLUMN owned_by_admins boolean NOT NULL DEFAULT false;

CREATE FUNCTION sessions_access_one_way() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.access = 'organisation' AND NEW.access <> 'organisation' THEN
    RAISE EXCEPTION 'session % is open to its organisation; that cannot be undone', OLD.id
      USING ERRCODE = 'check_violation', CONSTRAINT = 'sessions_access_one_way';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER sessions_access_one_way BEFORE UPDATE OF access ON sessions
  FOR EACH ROW EXECUTE FUNCTION sessions_access_one_way();

-- The "Open to everyone" list reads an organisation's open sessions.
CREATE INDEX sessions_org_access_idx ON sessions (organization_id) WHERE access = 'organisation';

-- The role a person has in a session. Every reader of a session checks it
-- (the orchestrator's routes, the ledger's visibility, artifacts, locate),
-- so the organisation rules live here once. Nobody who is not an active
-- member of the session's organisation has one, whatever rows they left:
--   * an active admin is the owner while the session is owned by admins;
--   * an accepted member has their role, `read` lifted to `chat` in an
--     organisation session;
--   * any other active member of an organisation session's organisation
--     can chat (writing makes them a member: the orchestrator's sessionChat).
CREATE OR REPLACE FUNCTION session_role(p_session text, p_person text) RETURNS text
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT CASE
    WHEN me.id IS NULL THEN NULL
    WHEN s.owned_by_admins AND me.role = 'admin' THEN 'owner'
    WHEN sp.role IS NOT NULL THEN CASE WHEN s.access = 'organisation' AND sp.role = 'read' THEN 'chat' ELSE sp.role END
    WHEN s.access = 'organisation' THEN 'chat'
  END
  FROM sessions s
  LEFT JOIN session_people sp ON sp.session_id = s.id AND sp.person_id = p_person AND sp.accepted_at IS NOT NULL
  LEFT JOIN people me ON me.id = p_person AND me.organization_id = s.organization_id AND me.removed_at IS NULL
  WHERE s.id = p_session
$$;

-- An @-mention of a person in a session's message. It is seen once their
-- page says it has the session open after it was made (the open
-- heartbeat); until then it waits on them ("Waiting on you") whenever they
-- do not have the session open. So one made while they had it open shows
-- only if they leave before their page next says so.
CREATE TABLE session_mentions (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  session_id      text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  person_id       text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  by_person       text REFERENCES people(id) ON DELETE SET NULL,
  -- The message's words, as the row quotes them.
  text            text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  seen_at         timestamptz
);
CREATE INDEX session_mentions_waiting_idx ON session_mentions (person_id, created_at) WHERE seen_at IS NULL;
CREATE INDEX session_mentions_session_idx ON session_mentions (session_id, person_id) WHERE seen_at IS NULL;
ALTER TABLE session_mentions ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_mentions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON session_mentions
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON session_mentions TO dude_app;
-- The push loop reads whether a mention is still unseen.
GRANT SELECT ON session_mentions TO dude_sweeper;

-- When someone who is not one of an organisation session's people last had
-- it open (the web's heartbeat; members keep theirs on session_people), so
-- a mention of them while they are looking sends no push.
CREATE TABLE session_viewers (
  session_id      text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  person_id       text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  open_at         timestamptz,
  PRIMARY KEY (session_id, person_id)
);
ALTER TABLE session_viewers ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_viewers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON session_viewers
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON session_viewers TO dude_app;

-- The notifier also tells a person they were mentioned (session.mentioned).
DROP INDEX IF EXISTS events_asks_idx;
CREATE INDEX events_asks_idx ON events (cursor)
  WHERE event_type IN ('question.asked', 'repository.requested', 'task.ready_to_merge', 'run.stalled', 'session.mentioned');
