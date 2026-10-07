-- 086_sessions.sql — brainstorm sessions: a conversation with an agent
-- (role brainstorm) that belongs to an organisation and its members, not
-- to a task. It reads the projects linked to it and proposes work, which a
-- member files with a click, as themselves.
--
-- The older `sessions` table (an agent's sessions inside a Run, plan §39)
-- becomes `agent_sessions`: `sessions` is what people call these now. Its
-- rows, routes and foreign keys stay as they were.

ALTER TABLE sessions RENAME TO agent_sessions;
ALTER INDEX sessions_pkey RENAME TO agent_sessions_pkey;
ALTER INDEX sessions_run_idx RENAME TO agent_sessions_run_idx;
ALTER INDEX sessions_parent_idx RENAME TO agent_sessions_parent_idx;
ALTER INDEX sessions_external_idx RENAME TO agent_sessions_external_idx;

CREATE TABLE sessions (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  title           text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
  created_by      text REFERENCES people(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_org_idx ON sessions (organization_id, created_at DESC);

-- Who is in a session, and what each may do: owner (exactly one, accepted
-- from the start), chat (write to the agent, file work) or read. An
-- invitation is a row not yet accepted; declining deletes it. A handover
-- to someone not in the session is their invitation with becomes_owner:
-- accepting it makes them the owner, and the owner before them keeps
-- handover_keep (chat or read), or leaves.
CREATE TABLE session_people (
  session_id      text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  person_id       text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  role            text NOT NULL CHECK (role IN ('owner', 'chat', 'read')),
  invited_by      text REFERENCES people(id) ON DELETE SET NULL,
  invited_at      timestamptz NOT NULL DEFAULT now(),
  accepted_at     timestamptz,
  becomes_owner   boolean NOT NULL DEFAULT false,
  handover_keep   text CHECK (handover_keep IN ('chat', 'read', 'leave')),
  -- When the person last had the session open (the web's heartbeat).
  open_at         timestamptz,
  PRIMARY KEY (session_id, person_id),
  CHECK (role <> 'owner' OR accepted_at IS NOT NULL),
  CHECK (becomes_owner = (handover_keep IS NOT NULL)),
  CHECK (NOT becomes_owner OR accepted_at IS NULL)
);
CREATE UNIQUE INDEX session_people_owner_idx ON session_people (session_id) WHERE role = 'owner';
-- At most one handover waiting per session.
CREATE UNIQUE INDEX session_people_handover_idx ON session_people (session_id) WHERE becomes_owner;
CREATE INDEX session_people_person_idx ON session_people (person_id);

-- Exactly one owner, checked at commit: a handover swaps the role within
-- one transaction.
CREATE FUNCTION session_people_one_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  sid text := COALESCE(NEW.session_id, OLD.session_id);
  owners int;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM sessions WHERE id = sid) THEN
    RETURN NULL;
  END IF;
  SELECT count(*) INTO owners FROM session_people WHERE session_id = sid AND role = 'owner';
  IF owners <> 1 THEN
    RAISE EXCEPTION 'session % has % owners; it must have exactly one', sid, owners
      USING ERRCODE = 'check_violation', CONSTRAINT = 'session_people_one_owner';
  END IF;
  RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER session_people_one_owner
  AFTER INSERT OR UPDATE OR DELETE ON session_people
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION session_people_one_owner();

-- The projects a session reads, and which of their repositories its agent
-- has checked out (0..all of each project's).
CREATE TABLE session_projects (
  session_id      text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  linked_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, project_id)
);
CREATE TABLE session_repositories (
  session_id      text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  repository_id   text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  linked_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, repository_id)
);

-- What the agent proposed (its propose tool): the card's items, in order,
-- each {kind: epic|task|edit|comment, ...}. A newer proposal replaces the
-- card; filed items stay recorded.
CREATE TABLE session_proposals (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  session_id      text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  run_id          text REFERENCES runs(id) ON DELETE SET NULL,
  items           jsonb NOT NULL CHECK (jsonb_typeof(items) = 'array'),
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX session_proposals_session_idx ON session_proposals (session_id, created_at DESC);

-- Which items were filed, by whom, as what. Kept on the session's side
-- only: the work filed carries nothing of the session.
CREATE TABLE session_filings (
  proposal_id     text NOT NULL REFERENCES session_proposals(id) ON DELETE CASCADE,
  item            int NOT NULL CHECK (item >= 0),
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  session_id      text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  filed_by        text REFERENCES people(id) ON DELETE SET NULL,
  filed_at        timestamptz NOT NULL DEFAULT now(),
  -- The task's key (BL-61), or an epic's title.
  key             text NOT NULL,
  task_id         text REFERENCES tasks(id) ON DELETE SET NULL,
  epic_id         text REFERENCES epics(id) ON DELETE SET NULL,
  PRIMARY KEY (proposal_id, item)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['sessions', 'session_people', 'session_projects', 'session_repositories',
                           'session_proposals', 'session_filings'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (organization_id = current_organization_id())'
      ' WITH CHECK (organization_id = current_organization_id())', t);
  END LOOP;
END $$;
-- The push loop names a question's audience across organisations.
GRANT SELECT ON sessions, session_people TO dude_sweeper;

-- The role a person has in a session, accepted; NULL for anyone else —
-- an invitee who has not accepted included. Every read and write of a
-- session checks it: there is no admin override.
CREATE FUNCTION session_role(p_session text, p_person text) RETURNS text LANGUAGE sql STABLE AS $$
  SELECT role FROM session_people
  WHERE session_id = p_session AND person_id = p_person AND accepted_at IS NOT NULL
$$;

-- ---------------------------------------------------------------------------
-- A session's Run: no task, no project.
-- ---------------------------------------------------------------------------

ALTER TABLE runs ADD COLUMN session_id text REFERENCES sessions(id) ON DELETE CASCADE;
ALTER TABLE runs ALTER COLUMN task_id DROP NOT NULL;
ALTER TABLE runs ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE runs ADD CONSTRAINT runs_task_or_session
  CHECK ((task_id IS NULL) <> (session_id IS NULL) AND (task_id IS NULL OR project_id IS NOT NULL));
CREATE INDEX runs_session_idx ON runs (session_id, created_at) WHERE session_id IS NOT NULL;
-- One live brainstorm per session: two people writing at once reach the same one.
CREATE UNIQUE INDEX runs_live_brainstorm_idx ON runs (session_id)
  WHERE role = 'brainstorm' AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused');

-- session: a brainstorm parked after its warm period; a member's message
-- resumes it.
ALTER TABLE runs DROP CONSTRAINT runs_dude_pause_check;
ALTER TABLE runs ADD CONSTRAINT runs_dude_pause_check
  CHECK (dude_pause IN ('repository', 'person', 'idle', 'unused', 'conductor', 'session'));
ALTER TABLE run_resumes DROP CONSTRAINT run_resumes_cause_check;
ALTER TABLE run_resumes ADD CONSTRAINT run_resumes_cause_check
  CHECK (cause IN ('answer', 'repository', 'person', 'idle', 'conductor', 'session'));

ALTER TABLE prompt_versions DROP CONSTRAINT prompt_versions_role_check;
ALTER TABLE prompt_versions ADD CONSTRAINT prompt_versions_role_check
  CHECK (role IN ('investigator', 'implementer', 'reviewer', 'fixer', 'simplifier', 'qa_browser', 'conductor', 'brainstorm'));

-- An agent's token names its Run's session too: the tools of a session's
-- Run are scoped by it.
DROP FUNCTION lookup_run_by_mcp_token(text);
CREATE FUNCTION lookup_run_by_mcp_token(p_hash text)
RETURNS TABLE (run_id text, organization_id text, project_id text, task_id text, session_id text, role text, status text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp AS $$
  SELECT id, organization_id, project_id, task_id, session_id, role::text, status::text
  FROM runs WHERE mcp_token_hash = p_hash
$$;
REVOKE ALL ON FUNCTION lookup_run_by_mcp_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lookup_run_by_mcp_token(text) TO dude_app;

-- Every event on a session's Run carries the session, whoever writes it,
-- so the ledger's readers can keep it to the session's members.
CREATE FUNCTION events_session_of_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.session_id IS NULL AND NEW.run_id IS NOT NULL THEN
    NEW.session_id := (SELECT session_id FROM runs WHERE id = NEW.run_id);
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER events_session_of_run BEFORE INSERT ON events
  FOR EACH ROW EXECUTE FUNCTION events_session_of_run();

-- A question an agent puts to one member (ask_person with to): only that
-- person answers it.
ALTER TABLE questions ADD COLUMN to_person text REFERENCES people(id) ON DELETE SET NULL;

-- ---------------------------------------------------------------------------
-- The brainstorm's settings: Thinker, on Small, in every organisation.
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION seed_conductor_size(org text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
  small text;
  role text;
BEGIN
  SELECT id INTO small FROM machine_sizes WHERE organization_id = org AND lower(name) = 'small'
    ORDER BY id LIMIT 1;
  IF small IS NULL THEN
    small := 'msz_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24);
    INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default)
    VALUES (small, org, 'Small', 0.5, 1024, 10, NULL, false);
  END IF;
  FOREACH role IN ARRAY ARRAY['conductor', 'brainstorm'] LOOP
    UPDATE organizations
    SET default_agent_models = jsonb_set(default_agent_models, ARRAY[role],
      COALESCE(default_agent_models->role, '{}'::jsonb) || jsonb_build_object('machineSize', small))
    WHERE id = org AND NOT COALESCE(default_agent_models->role ? 'machineSize', false);
  END LOOP;
END $$;
REVOKE ALL ON FUNCTION seed_conductor_size(text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION seed_model_tiers_for(org text, thinker_model text, coder_model text) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
  ids jsonb := jsonb_build_object(
    'Thinker', 'mtr_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24),
    'Coder', 'mtr_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24),
    'Fast', 'mtr_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24));
  models jsonb;
  role text;
BEGIN
  INSERT INTO model_tiers (id, organization_id, name, description, model, position) VALUES
    (ids->>'Thinker', org, 'Thinker', 'Reads, plans, judges and tidies. Slow and thorough.', thinker_model, 0),
    (ids->>'Coder', org, 'Coder', 'Writes and fixes code for hours at a time.', coder_model, 1),
    (ids->>'Fast', org, 'Fast', 'Small, mechanical jobs where speed beats depth.', NULL, 2);
  SELECT default_agent_models INTO models FROM organizations WHERE id = org;
  FOREACH role IN ARRAY ARRAY['conductor', 'brainstorm', 'investigator', 'reviewer', 'simplifier', 'qa_browser', 'implementer'] LOOP
    models := jsonb_set(models, ARRAY[role], COALESCE(models->role, '{}'::jsonb)
      || jsonb_build_object('tier', ids->>(CASE role WHEN 'implementer' THEN 'Coder' ELSE 'Thinker' END)));
  END LOOP;
  UPDATE organizations SET default_agent_models = models WHERE id = org;
  RETURN ids;
END $$;
REVOKE ALL ON FUNCTION seed_model_tiers_for(text, text, text) FROM PUBLIC;

SELECT seed_conductor_size(id) FROM organizations;

UPDATE organizations o
SET default_agent_models = jsonb_set(o.default_agent_models, '{brainstorm}',
  COALESCE(o.default_agent_models->'brainstorm', '{}'::jsonb) || jsonb_build_object('tier', t.id))
FROM (SELECT DISTINCT ON (organization_id) organization_id, id FROM model_tiers
      WHERE name = 'Thinker' ORDER BY organization_id, position, id) t
WHERE t.organization_id = o.id AND NOT COALESCE(o.default_agent_models->'brainstorm' ? 'tier', false);
