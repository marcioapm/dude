-- The conductor: an agent on a task that people talk to in its Chat.
--
-- The `orchestrator` agent role becomes `conductor`, with its own settings
-- and a small machine. A conductor is a Run of its task with no phase
-- (role conductor), at most one live per task, started by a person's first
-- message in Chat and briefed by dude (runs.prompt). Between turns it stays
-- warm a while, then dude parks it (dude_pause 'conductor'); a person's next
-- message resumes it, and that resume is timed (run_resumes.cause).

ALTER TYPE agent_role RENAME VALUE 'orchestrator' TO 'conductor';

-- Settings keyed by role. A key that is already `conductor` cannot exist
-- (the schema refused it), so the old key's value moves as it is.
UPDATE organizations
SET default_agent_models = (default_agent_models - 'orchestrator')
  || jsonb_build_object('conductor', default_agent_models->'orchestrator')
WHERE default_agent_models ? 'orchestrator';

UPDATE projects
SET agent_models = (agent_models - 'orchestrator') || jsonb_build_object('conductor', agent_models->'orchestrator')
WHERE agent_models ? 'orchestrator';

-- The ledger names a session's role (session.started, subagent.started,
-- run.created): what it calls an orchestrator is the same role.
UPDATE events SET payload = jsonb_set(payload, '{role}', '"conductor"')
WHERE payload->>'role' = 'orchestrator';

-- 069's notes on what the tier upgrade changed name a role the Models page
-- shows; it is the conductor now.
UPDATE model_tier_upgrade_notes SET role = 'conductor' WHERE role = 'orchestrator';

-- What dude briefs a conductor with: its first prompt, written when it is
-- created and kept, since a resume rebuilds the rest of its spec.
ALTER TABLE runs ADD COLUMN prompt text;

-- One live conductor per task: two people writing at once reach the same one.
CREATE UNIQUE INDEX runs_live_conductor_idx ON runs (task_id)
  WHERE role = 'conductor' AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused');

-- Messages no agent has read or been told it will not: the syncer looks
-- here for those an ended conductor still holds, to hand to the next one.
-- Unlike directives_pending_idx it leaves out failed ones, which are kept.
CREATE INDEX directives_unsettled_idx ON directives (run_id) WHERE delivered_at IS NULL AND failed_at IS NULL;

-- conductor: parked after its warm period; a person's message resumes it.
ALTER TABLE runs DROP CONSTRAINT runs_dude_pause_check;
ALTER TABLE runs ADD CONSTRAINT runs_dude_pause_check
  CHECK (dude_pause IN ('repository', 'person', 'idle', 'unused', 'conductor'));

ALTER TABLE run_resumes DROP CONSTRAINT run_resumes_cause_check;
ALTER TABLE run_resumes ADD CONSTRAINT run_resumes_cause_check
  CHECK (cause IN ('answer', 'repository', 'person', 'idle', 'conductor'));

-- The conductor's instructions are editable like any role's.
ALTER TABLE prompt_versions DROP CONSTRAINT prompt_versions_role_check;
ALTER TABLE prompt_versions ADD CONSTRAINT prompt_versions_role_check
  CHECK (role IN ('investigator', 'implementer', 'reviewer', 'fixer', 'simplifier', 'qa_browser', 'conductor'));

-- Small: the conductor's machine. It reads, talks and runs git; it never
-- builds. Every organisation has it (one already named Small is reused),
-- and it is the conductor's size unless the organisation names another.
-- Standard stays the default for everything else.
CREATE FUNCTION seed_conductor_size(org text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
  small text;
BEGIN
  SELECT id INTO small FROM machine_sizes WHERE organization_id = org AND lower(name) = 'small'
    ORDER BY id LIMIT 1;
  IF small IS NULL THEN
    small := 'msz_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24);
    INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default)
    VALUES (small, org, 'Small', 0.5, 1024, 10, NULL, false);
  END IF;
  UPDATE organizations
  SET default_agent_models = jsonb_set(default_agent_models, '{conductor}',
    COALESCE(default_agent_models->'conductor', '{}'::jsonb) || jsonb_build_object('machineSize', small))
  WHERE id = org AND NOT COALESCE(default_agent_models->'conductor' ? 'machineSize', false);
END $$;
-- Any organisation's: the trigger's and this migration's alone to call.
REVOKE ALL ON FUNCTION seed_conductor_size(text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION seed_machine_size() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
BEGIN
  INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default)
  VALUES ('msz_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24), NEW.id, 'Standard', 2, 8192, 20, NULL, true);
  PERFORM seed_conductor_size(NEW.id);
  RETURN NEW;
END $$;

SELECT seed_conductor_size(id) FROM organizations;

-- The conductor reads and judges: it runs on the Thinker tier, in a new
-- organization (the seed below) and in every one there now that gave it none.
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
  FOREACH role IN ARRAY ARRAY['conductor', 'investigator', 'reviewer', 'simplifier', 'qa_browser', 'implementer'] LOOP
    models := jsonb_set(models, ARRAY[role], COALESCE(models->role, '{}'::jsonb)
      || jsonb_build_object('tier', ids->>(CASE role WHEN 'implementer' THEN 'Coder' ELSE 'Thinker' END)));
  END LOOP;
  UPDATE organizations SET default_agent_models = models WHERE id = org;
  RETURN ids;
END $$;
REVOKE ALL ON FUNCTION seed_model_tiers_for(text, text, text) FROM PUBLIC;

UPDATE organizations o
SET default_agent_models = jsonb_set(o.default_agent_models, '{conductor}',
  COALESCE(o.default_agent_models->'conductor', '{}'::jsonb) || jsonb_build_object('tier', t.id))
FROM (SELECT DISTINCT ON (organization_id) organization_id, id FROM model_tiers
      WHERE name = 'Thinker' ORDER BY organization_id, position, id) t
WHERE t.organization_id = o.id AND NOT COALESCE(o.default_agent_models->'conductor' ? 'tier', false);
