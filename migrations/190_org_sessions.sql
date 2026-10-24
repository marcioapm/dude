-- 190_org_sessions.sql — sessions open to the whole organisation, sessions
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
-- An admin's Sessions list reads the sessions its admins own.
CREATE INDEX sessions_owned_by_admins_idx ON sessions (organization_id) WHERE owned_by_admins;

-- A session row's person as the organisation stands them now: member,
-- admin, or none (removed, or not of the row's organisation). Kept by the
-- triggers below, so session_role's common case reads this row alone. An
-- insert takes the person's row FOR SHARE, so it waits for a removal or a
-- role change in flight and reads its outcome (the people trigger cannot
-- see a row not committed yet).
ALTER TABLE session_people ADD COLUMN standing text NOT NULL DEFAULT 'member'
  CHECK (standing IN ('member', 'admin', 'none'));
CREATE FUNCTION person_standing(p_person text, p_org text) RETURNS text LANGUAGE sql STABLE AS $$
  SELECT COALESCE((SELECT CASE WHEN p.removed_at IS NOT NULL OR p.organization_id <> p_org THEN 'none'
                               WHEN p.role = 'admin' THEN 'admin' ELSE 'member' END
                   FROM people p WHERE p.id = p_person), 'none')
$$;
UPDATE session_people sp SET standing = person_standing(sp.person_id, sp.organization_id)
  WHERE person_standing(sp.person_id, sp.organization_id) <> 'member';
CREATE FUNCTION session_people_standing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM 1 FROM people WHERE id = NEW.person_id FOR SHARE;
  NEW.standing := person_standing(NEW.person_id, NEW.organization_id);
  RETURN NEW;
END $$;
CREATE TRIGGER session_people_standing BEFORE INSERT OR UPDATE OF person_id, organization_id ON session_people
  FOR EACH ROW EXECUTE FUNCTION session_people_standing();
CREATE FUNCTION people_session_standing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE session_people SET standing = person_standing(NEW.id, organization_id)
    WHERE person_id = NEW.id AND standing IS DISTINCT FROM person_standing(NEW.id, organization_id);
  RETURN NULL;
END $$;
CREATE TRIGGER people_session_standing AFTER UPDATE OF removed_at, role, organization_id ON people
  FOR EACH ROW WHEN (OLD.removed_at IS DISTINCT FROM NEW.removed_at OR OLD.role IS DISTINCT FROM NEW.role
    OR OLD.organization_id IS DISTINCT FROM NEW.organization_id)
  EXECUTE FUNCTION people_session_standing();

-- The role a person has in a session. Every reader of a session checks it
-- (the orchestrator's routes, the ledger's visibility, artifacts, locate),
-- so the organisation rules live here once. Nobody who is not an active
-- member of the session's organisation has one, whatever rows they left:
--   * an active admin is the owner while the session is owned by admins;
--   * an accepted member has their role, `read` lifted to `chat` in an
--     organisation session;
--   * any other active member of an organisation session's organisation
--     can chat (writing makes them a member: the orchestrator's sessionChat).
-- An owner row, or a non-admin's chat row, decides alone: one probe of the
-- person's own row (session_people_pkey), whose standing mirrors people
-- (above). Anything else reads the session and the person. PL/pgSQL so the
-- second read is skipped, not planned around, when the first decides.
CREATE OR REPLACE FUNCTION session_role(p_session text, p_person text) RETURNS text
LANGUAGE plpgsql STABLE PARALLEL SAFE AS $$
DECLARE
  mine text;
  standing text;
  r text;
BEGIN
  SELECT sp.role, sp.standing INTO mine, standing FROM session_people sp
  WHERE sp.session_id = p_session AND sp.person_id = p_person AND sp.accepted_at IS NOT NULL;
  IF (mine = 'owner' AND standing <> 'none') OR (mine = 'chat' AND standing = 'member') THEN
    RETURN mine;
  END IF;
  SELECT CASE
      WHEN s.owned_by_admins AND me.role = 'admin' THEN 'owner'
      WHEN mine IS NOT NULL THEN CASE WHEN s.access = 'organisation' AND mine = 'read' THEN 'chat' ELSE mine END
      WHEN s.access = 'organisation' THEN 'chat'
    END INTO r
  FROM sessions s JOIN people me ON me.id = p_person AND me.organization_id = s.organization_id AND me.removed_at IS NULL
  WHERE s.id = p_session;
  RETURN r;
END $$;

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

-- The notifier's session sweep (a mention; a question to the owner of a
-- session its admins own, which goes to each admin) keeps a watermark and
-- a sent set of its own beside the asks' (push_config.after_cursor,
-- push_sent, migration 024): the asks' watermark is bounded only by asks
-- not sent yet, so it could pass a mention not sent yet. NULL until the
-- sweep first moves it: it starts at the asks' watermark.
ALTER TABLE push_config ADD COLUMN session_after_cursor bigint;
CREATE TABLE push_session_sent (cursor bigint PRIMARY KEY);
GRANT SELECT, INSERT, DELETE ON push_session_sent TO dude_sweeper;
