-- 096_tier_effort.sql — reasoning effort belongs to the model tier, not the role.
--
-- A tier carries how hard its model thinks and the extra settings its agent
-- is requested with, so "Thinker (High)" and "Thinker (Medium)" are two tiers
-- on one model. The orchestrator turns them into OpenCode model options
-- (phases.openCodeConfig): a new model setting is a tier edit.
--
--   effort   none | low | medium | high | max; NULL is the model's default.
--   options  extra OpenCode model options, a JSON object merged over what the
--            effort makes (the tier's win). At most 4 KB serialised.
--   headers  extra request headers, {name: value}, names in RFC 9110 token
--            syntax, values one line. At most 4 KB serialised.
--
-- modelTierInputSchema (packages/domain) holds the same bounds.

-- Whether a JSON value is request headers: an object of token-named string
-- values with no line breaks. A function because a CHECK takes no subquery.
CREATE FUNCTION model_tier_headers_valid(h jsonb) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
  SELECT jsonb_typeof(h) = 'object' AND NOT EXISTS (
    SELECT 1 FROM jsonb_each(h) e
    WHERE e.key !~ '^[!#$%&''*+.^_`|~0-9A-Za-z-]+$'
       OR jsonb_typeof(e.value) <> 'string'
       OR (e.value #>> '{}') ~ '[\r\n]')
$$;

ALTER TABLE model_tiers
  ADD COLUMN effort text CHECK (effort IN ('none', 'low', 'medium', 'high', 'max')),
  ADD COLUMN options jsonb CHECK (jsonb_typeof(options) = 'object' AND octet_length(options::text) <= 4096),
  ADD COLUMN headers jsonb CHECK (model_tier_headers_valid(headers) AND octet_length(headers::text) <= 4096);

CREATE OR REPLACE FUNCTION model_tier(t model_tiers) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('id', t.id, 'name', t.name, 'description', t.description, 'model', t.model,
    'effort', t.effort, 'options', t.options, 'headers', t.headers,
    'position', t.position, 'updatedAt', t.updated_at,
    'updatedBy', (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = t.updated_by))
$$;

-- New organisations: Thinker thinks hard, Coder moderately, Fast at the
-- model's default. Tiers there now keep NULL: nothing they run changes.
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
  INSERT INTO model_tiers (id, organization_id, name, description, model, effort, position) VALUES
    (ids->>'Thinker', org, 'Thinker', 'Reads, plans, judges and tidies. Slow and thorough.', thinker_model, 'high', 0),
    (ids->>'Coder', org, 'Coder', 'Writes and fixes code for hours at a time.', coder_model, 'medium', 1),
    (ids->>'Fast', org, 'Fast', 'Small, mechanical jobs where speed beats depth.', NULL, NULL, 2);
  SELECT default_agent_models INTO models FROM organizations WHERE id = org;
  FOREACH role IN ARRAY ARRAY['conductor', 'brainstorm', 'investigator', 'reviewer', 'simplifier', 'qa_browser', 'implementer'] LOOP
    models := jsonb_set(models, ARRAY[role], COALESCE(models->role, '{}'::jsonb)
      || jsonb_build_object('tier', ids->>(CASE role WHEN 'implementer' THEN 'Coder' ELSE 'Thinker' END)));
  END LOOP;
  UPDATE organizations SET default_agent_models = models WHERE id = org;
  RETURN ids;
END $$;
REVOKE ALL ON FUNCTION seed_model_tiers_for(text, text, text) FROM PUBLIC;

-- No role names an effort any more. A role left with nothing is dropped, as
-- a Reset leaves it.
UPDATE organizations SET default_agent_models = (
  SELECT COALESCE(jsonb_object_agg(key, value - 'effort'), '{}'::jsonb)
  FROM jsonb_each(default_agent_models) WHERE value - 'effort' <> '{}'::jsonb)
WHERE EXISTS (SELECT 1 FROM jsonb_each(default_agent_models) r WHERE r.value ? 'effort');

UPDATE projects SET agent_models = (
  SELECT COALESCE(jsonb_object_agg(key, value - 'effort'), '{}'::jsonb)
  FROM jsonb_each(agent_models) WHERE value - 'effort' <> '{}'::jsonb)
WHERE EXISTS (SELECT 1 FROM jsonb_each(agent_models) r WHERE r.value ? 'effort');
