-- Project secrets retain preview-only access until their recipients are changed.
-- The existing table and runs keep their forced tenant RLS policies.
CREATE FUNCTION secret_recipients_valid(names text[]) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
  SELECT cardinality(names) > 0
    AND array_ndims(names) = 1
    AND array_position(names, NULL) IS NULL
    AND names <@ ARRAY['previews', 'tester', 'implementer', 'fixer', 'simplifier', 'reviewer']::text[]
    AND cardinality(names) = (SELECT count(DISTINCT name) FROM unnest(names) name)
$$;

ALTER TABLE project_secrets ADD COLUMN recipients text[] NOT NULL DEFAULT '{previews}'
  CHECK (secret_recipients_valid(recipients));

-- Names only: current values are supplied again on resume, never returned with a Run.
ALTER TABLE runs ADD COLUMN project_secret_names text[] NOT NULL DEFAULT '{}';
