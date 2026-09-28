-- People (docs/design/calmer-build.md, "People"): a person is one of an
-- organization's members, with a role, a face and many keys. Until now a
-- person was an API key, by its name; a key now acts *for* a person, and a
-- person with two keys (a laptop, a script) is still one person — who owns
-- a task, who answers for it, who is online.

CREATE TABLE people (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name            text NOT NULL,
  -- Null for the people made from keys that came before people did.
  email           citext,
  role            text NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
  -- An https URL, or a small image as a data: URL. Served to browsers
  -- under photo_token (see person_photo), so the list endpoints carry a
  -- short URL rather than the image.
  photo_url       text,
  photo_token     text,
  -- Touched at most once a minute by any request of theirs: presence,
  -- and what they had open then ("TEXT-14"), as their browser named it.
  last_seen_at    timestamptz,
  last_seen_where text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  -- Removed people keep their row, so what they did still has a name.
  removed_at      timestamptz
);
CREATE UNIQUE INDEX people_email_idx ON people (organization_id, email) WHERE email IS NOT NULL AND removed_at IS NULL;
CREATE INDEX people_org_idx ON people (organization_id) WHERE removed_at IS NULL;

ALTER TABLE people ENABLE ROW LEVEL SECURITY;
ALTER TABLE people FORCE ROW LEVEL SECURITY;
CREATE POLICY people_isolation ON people
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
GRANT SELECT, INSERT, UPDATE ON people TO dude_app;

-- A person as every API response names one (PersonRef): a photo kept
-- here is served by the backend under its token, so lists carry a short
-- URL and never the image; online is seen in the last five minutes.
CREATE FUNCTION person_ref(p people) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('id', p.id, 'name', p.name,
    'photoUrl', CASE WHEN p.photo_url LIKE 'data:%' THEN '/v1/people/' || p.id || '/photo?t=' || p.photo_token
                     ELSE p.photo_url END,
    'online', p.removed_at IS NULL AND COALESCE(p.last_seen_at > now() - interval '5 minutes', false))
$$;

ALTER TABLE api_keys ADD COLUMN person_id text REFERENCES people(id) ON DELETE CASCADE;
CREATE INDEX api_keys_person_idx ON api_keys (person_id) WHERE person_id IS NOT NULL;

-- One person per user key that exists, named as the key was; ids made
-- here, as newId would ("per_" and something unique). The first key of
-- each organization is its admin: someone must be able to invite.
INSERT INTO people (id, organization_id, name, role, last_seen_at, created_at)
SELECT 'per_' || substr(md5(k.id), 1, 24), k.organization_id, k.name,
       CASE WHEN k.id = (SELECT f.id FROM api_keys f WHERE f.organization_id = k.organization_id
                         AND f.kind = 'user' ORDER BY f.created_at, f.id LIMIT 1) THEN 'admin' ELSE 'member' END,
       k.last_used_at, k.created_at
FROM api_keys k WHERE k.kind = 'user';
UPDATE api_keys k SET person_id = 'per_' || substr(md5(k.id), 1, 24) WHERE k.kind = 'user';
-- A person whose only key was revoked had already left.
UPDATE people p SET removed_at = now()
WHERE NOT EXISTS (SELECT 1 FROM api_keys k WHERE k.person_id = p.id AND k.revoked_at IS NULL);

-- A user key made without a person (by an operator provisioning the
-- first key, or anything written before people existed) is a person of
-- its own, named as the key is — the organization's admin if it has none.
CREATE FUNCTION api_keys_person() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE pid text := 'per_' || substr(md5(NEW.id), 1, 24);
BEGIN
  IF NEW.person_id IS NOT NULL OR NEW.kind <> 'user' THEN RETURN NEW; END IF;
  INSERT INTO people (id, organization_id, name, role)
  VALUES (pid, NEW.organization_id, NEW.name,
          CASE WHEN EXISTS (SELECT 1 FROM people WHERE organization_id = NEW.organization_id
                            AND role = 'admin' AND removed_at IS NULL) THEN 'member' ELSE 'admin' END);
  NEW.person_id := pid;
  RETURN NEW;
END $$;
CREATE TRIGGER api_keys_person BEFORE INSERT ON api_keys FOR EACH ROW EXECUTE FUNCTION api_keys_person();

-- Authentication learns whose key it is.
DROP FUNCTION lookup_api_key(text);
CREATE FUNCTION lookup_api_key(p_key_hash text)
RETURNS TABLE (id text, organization_id text, name text, kind text, person_id text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT k.id, k.organization_id, k.name, k.kind, k.person_id
  FROM api_keys k
  WHERE k.key_hash = p_key_hash AND k.revoked_at IS NULL
  LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lookup_api_key(text) TO dude_app;

-- A person's photo for an <img>, which sends no credentials: found by
-- the person and the token only a signed-in reader was given.
CREATE FUNCTION person_photo(p_id text, p_token text) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT photo_url FROM people WHERE id = p_id AND photo_token = p_token AND removed_at IS NULL
$$;
REVOKE ALL ON FUNCTION person_photo(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION person_photo(text, text) TO dude_app;

-- The people on a task, in order: the first is its owner — who is told
-- when it waits on someone and who answers for it. The owner stays
-- mirrored in tasks.owner_key_id (one of their keys) for the
-- orchestrator, which compares people through their keys.
CREATE TABLE task_people (
  task_id         text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  person_id       text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  position        integer NOT NULL,
  added_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (task_id, person_id)
);
CREATE INDEX task_people_person_idx ON task_people (person_id);

ALTER TABLE task_people ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_people FORCE ROW LEVEL SECURITY;
CREATE POLICY task_people_isolation ON task_people
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
GRANT SELECT, INSERT, UPDATE, DELETE ON task_people TO dude_app;

INSERT INTO task_people (task_id, person_id, organization_id, position, added_at)
SELECT t.id, k.person_id, t.organization_id, 0, t.created_at
FROM tasks t JOIN api_keys k ON k.id = t.owner_key_id WHERE k.person_id IS NOT NULL;

-- Whoever writes tasks.owner_key_id — the backend, the orchestrator
-- making a task an agent found, a test — puts that key's person first on
-- the task; whoever was first stays on it, second.
CREATE FUNCTION tasks_owner_person() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE pid text;
BEGIN
  SELECT person_id INTO pid FROM api_keys WHERE id = NEW.owner_key_id;
  IF pid IS NULL OR EXISTS (SELECT 1 FROM task_people WHERE task_id = NEW.id AND person_id = pid AND position = 0) THEN
    RETURN NEW;
  END IF;
  DELETE FROM task_people WHERE task_id = NEW.id AND person_id = pid;
  UPDATE task_people SET position = position + 1 WHERE task_id = NEW.id;
  INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES (NEW.id, pid, NEW.organization_id, 0);
  RETURN NEW;
END $$;
CREATE TRIGGER tasks_owner_person AFTER INSERT OR UPDATE OF owner_key_id ON tasks
  FOR EACH ROW EXECUTE FUNCTION tasks_owner_person();

-- A person who revokes the key their tasks name still owns them: the
-- mirror moves to another of their keys. With none left (they were
-- removed), the task is nobody's, as it always was for a revoked owner.
CREATE FUNCTION api_keys_revoked() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE tasks SET owner_key_id = (
    SELECT k.id FROM api_keys k WHERE k.person_id = NEW.person_id AND k.revoked_at IS NULL
    ORDER BY k.last_used_at DESC NULLS LAST, k.created_at DESC LIMIT 1)
  WHERE owner_key_id = NEW.id
    AND EXISTS (SELECT 1 FROM api_keys k WHERE k.person_id = NEW.person_id AND k.revoked_at IS NULL);
  RETURN NEW;
END $$;
CREATE TRIGGER api_keys_revoked AFTER UPDATE OF revoked_at ON api_keys
  FOR EACH ROW WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL AND NEW.person_id IS NOT NULL)
  EXECUTE FUNCTION api_keys_revoked();

-- The notifier tells the owner on any browser they signed in with,
-- whichever of their keys that was.
GRANT SELECT (person_id) ON api_keys TO dude_sweeper;
