-- 066_model_tiers.sql — agents pick a model tier, never a model.
--
-- A tier is the organization's, changed by its admins: a name, what it is
-- for, and the one model name dude requests from the LLM proxy for it,
-- exactly as the proxy names it (NULL: not set yet, and a Run on it fails
-- saying so). Every agent role names a tier in its settings (`tier` in
-- organizations.default_agent_models and projects.agent_models, field by
-- field like its effort); no role names a model any more. Editing a tier's
-- model moves every agent on it from its next session.
CREATE TABLE model_tiers (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name            text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 24),
  description     text NOT NULL DEFAULT '' CHECK (length(description) <= 80),
  -- The proxy's name for the model: no whitespace and no '/', except the
  -- scripted agent's test models (orchestrator/internal/fakeagent).
  model           text CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask')
                              OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]')),
  position        integer NOT NULL DEFAULT 0,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      text REFERENCES people(id) ON DELETE SET NULL
);

-- A name is unique in its organization, whatever its case.
CREATE UNIQUE INDEX model_tiers_name_idx ON model_tiers (organization_id, lower(name));

ALTER TABLE model_tiers ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_tiers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON model_tiers
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON model_tiers TO dude_app;

-- A tier as the API shows it, for both processes.
CREATE FUNCTION model_tier(t model_tiers) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('id', t.id, 'name', t.name, 'description', t.description, 'model', t.model,
    'position', t.position, 'updatedAt', t.updated_at,
    'updatedBy', (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = t.updated_by))
$$;

-- What the upgrade below changed, for the organization's admins to check:
-- one row per organization role or project override that named a model.
-- The Models page shows them until an admin dismisses them.
CREATE TABLE model_tier_upgrade_notes (
  id              bigserial PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- NULL: the organization's role.
  project_id      text REFERENCES projects(id) ON DELETE CASCADE,
  role            text NOT NULL,
  old_model       text NOT NULL,
  tier_id         text REFERENCES model_tiers(id) ON DELETE SET NULL,
  -- The tier's name then, so the note reads the same after a rename or removal.
  tier_name       text NOT NULL,
  -- The tier was made by the upgrade for this model.
  new_tier        boolean NOT NULL DEFAULT false,
  -- What it requests now is not what it named before.
  model_changed   boolean NOT NULL,
  dismissed_at    timestamptz
);
CREATE INDEX model_tier_upgrade_notes_org_idx ON model_tier_upgrade_notes (organization_id) WHERE dismissed_at IS NULL;
ALTER TABLE model_tier_upgrade_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_tier_upgrade_notes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON model_tier_upgrade_notes
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, UPDATE ON model_tier_upgrade_notes TO dude_app;

-- Every organization starts with three tiers, none naming a model yet, and
-- its roles on them: Thinker for those that read and judge, Coder for the
-- implementer (the fixer follows it), Fast for none yet. Returns the
-- tiers' ids by name. Any organization's: the trigger's alone to call.
CREATE FUNCTION seed_model_tiers_for(org text, thinker_model text, coder_model text) RETURNS jsonb
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
  FOREACH role IN ARRAY ARRAY['investigator', 'reviewer', 'simplifier', 'qa_browser', 'implementer'] LOOP
    models := jsonb_set(models, ARRAY[role], COALESCE(models->role, '{}'::jsonb)
      || jsonb_build_object('tier', ids->>(CASE role WHEN 'implementer' THEN 'Coder' ELSE 'Thinker' END)));
  END LOOP;
  IF models ? 'orchestrator' THEN
    models := jsonb_set(models, '{orchestrator}', models->'orchestrator' || jsonb_build_object('tier', ids->>'Thinker'));
  END IF;
  UPDATE organizations SET default_agent_models = models WHERE id = org;
  RETURN ids;
END $$;
REVOKE ALL ON FUNCTION seed_model_tiers_for(text, text, text) FROM PUBLIC;

CREATE FUNCTION seed_model_tiers() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
BEGIN
  PERFORM seed_model_tiers_for(NEW.id, NULL, NULL);
  RETURN NEW;
END $$;

-- ---------------------------------------------------------------------------
-- The upgrade: organizations there now, whose roles name models.
-- ---------------------------------------------------------------------------

-- A role's stored model as a tier's: the image provider's prefix taken off
-- (llm-anthropic/claude-opus-5-5 is claude-opus-5-5); NULL for one no tier
-- could hold.
CREATE FUNCTION pg_temp.tier_model(raw text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE
    WHEN raw IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask') THEN raw
    WHEN m <> '' AND length(m) <= 200 AND m !~ '[[:space:]/]' THEN m
  END
  FROM (SELECT regexp_replace(raw, '^(llm-anthropic|llm-openai)/', '') AS m) s
$$;

DO $$
DECLARE
  o record;
  p record;
  r record;
  ids jsonb;
  thinker_roles text[] := ARRAY['investigator', 'reviewer', 'simplifier', 'qa_browser', 'orchestrator'];
  coder_roles text[] := ARRAY['implementer', 'fixer'];
  thinker_model text;
  coder_model text;
  wanted text;
  tier record;
  made boolean;
  tier_name text;
  n integer;
  models jsonb;
BEGIN
  FOR o IN SELECT id, default_agent_models AS m FROM organizations ORDER BY id LOOP
    -- The model most of a tier's roles already name; a tie goes to the
    -- first role in its list.
    SELECT model INTO thinker_model FROM (
      SELECT pg_temp.tier_model(o.m->x.role->>'model') AS model, x.ord
      FROM unnest(thinker_roles) WITH ORDINALITY x(role, ord)) s
    WHERE model IS NOT NULL GROUP BY model ORDER BY count(*) DESC, min(ord) LIMIT 1;
    SELECT model INTO coder_model FROM (
      SELECT pg_temp.tier_model(o.m->x.role->>'model') AS model, x.ord
      FROM unnest(coder_roles) WITH ORDINALITY x(role, ord)) s
    WHERE model IS NOT NULL GROUP BY model ORDER BY count(*) DESC, min(ord) LIMIT 1;

    -- The trigger is not there yet: the seed is called here, once.
    ids := seed_model_tiers_for(o.id, thinker_model, coder_model);

    -- The fixer follows the implementer: one that named a model has no tier of its own.
    FOR r IN SELECT key AS role, value->>'model' AS model FROM jsonb_each(o.m) WHERE value ? 'model' LOOP
      tier_name := CASE WHEN r.role = ANY (coder_roles) THEN 'Coder' ELSE 'Thinker' END;
      wanted := pg_temp.tier_model(r.model);
      INSERT INTO model_tier_upgrade_notes (organization_id, project_id, role, old_model, tier_id, tier_name, model_changed)
      VALUES (o.id, NULL, r.role, r.model, ids->>tier_name, tier_name,
        wanted IS NULL OR wanted IS DISTINCT FROM CASE tier_name WHEN 'Coder' THEN coder_model ELSE thinker_model END);
    END LOOP;

    -- A project's override keeps its model: the tier that already asks for
    -- it, else one made for it, once per model.
    FOR p IN SELECT id, agent_models AS m FROM projects WHERE organization_id = o.id ORDER BY id LOOP
      models := p.m;
      FOR r IN SELECT key AS role, value->>'model' AS model FROM jsonb_each(p.m) WHERE value ? 'model' LOOP
        wanted := pg_temp.tier_model(r.model);
        made := false;
        tier := NULL;
        IF wanted IS NULL THEN
          -- Nothing a tier can hold: the override goes, and the role
          -- follows the organization's tier.
          tier_name := CASE WHEN r.role = ANY (coder_roles) THEN 'Coder' ELSE 'Thinker' END;
          SELECT id, name, model INTO tier FROM model_tiers WHERE id = ids->>tier_name;
          models := jsonb_set(models, ARRAY[r.role], (models->r.role) - 'model');
        ELSE
          SELECT id, name, model INTO tier FROM model_tiers
          WHERE organization_id = o.id AND model = wanted ORDER BY position LIMIT 1;
          IF tier.id IS NULL THEN
            -- Named after its model, cut to a name's length and numbered if taken.
            tier_name := left(wanted, 24);
            n := 1;
            WHILE EXISTS (SELECT 1 FROM model_tiers WHERE organization_id = o.id AND lower(name) = lower(tier_name)) LOOP
              n := n + 1;
              tier_name := left(wanted, 24 - length(n::text) - 1) || ' ' || n;
            END LOOP;
            INSERT INTO model_tiers (id, organization_id, name, description, model, position)
            VALUES ('mtr_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24), o.id, tier_name,
              'Made in the upgrade, for a project that named this model.', wanted,
              (SELECT max(position) + 1 FROM model_tiers WHERE organization_id = o.id))
            RETURNING id, name, model INTO tier;
          END IF;
          made := NOT (ids ? tier.name AND ids->>tier.name = tier.id);
          models := jsonb_set(models, ARRAY[r.role], ((models->r.role) - 'model') || jsonb_build_object('tier', tier.id));
        END IF;
        INSERT INTO model_tier_upgrade_notes (organization_id, project_id, role, old_model, tier_id, tier_name, new_tier, model_changed)
        VALUES (o.id, p.id, r.role, r.model, tier.id, tier.name, made, wanted IS NULL OR wanted IS DISTINCT FROM tier.model);
      END LOOP;
      -- A role left with nothing is dropped, as a Reset leaves it.
      SELECT COALESCE(jsonb_object_agg(key, value), '{}'::jsonb) INTO models FROM jsonb_each(models) WHERE value <> '{}'::jsonb;
      IF models IS DISTINCT FROM p.m THEN
        UPDATE projects SET agent_models = models WHERE id = p.id;
      END IF;
    END LOOP;

    -- No role names a model any more.
    UPDATE organizations SET default_agent_models = (
      SELECT COALESCE(jsonb_object_agg(key, value - 'model'), '{}'::jsonb)
      FROM jsonb_each(default_agent_models) WHERE value - 'model' <> '{}'::jsonb)
    WHERE id = o.id;
  END LOOP;
END $$;

CREATE TRIGGER organizations_seed_model_tiers AFTER INSERT ON organizations
  FOR EACH ROW EXECUTE FUNCTION seed_model_tiers();

-- What each Run requested: runs.model stays the model, and model_tier is
-- the tier's name when it was submitted (NULL for a Run from before tiers).
ALTER TABLE runs ADD COLUMN model_tier text;
