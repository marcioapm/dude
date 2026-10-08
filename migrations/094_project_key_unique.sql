-- 094_project_key_unique.sql — one project per key in an organisation.
--
-- A task's key is its project's key and a number (BILL-12), and people,
-- agents and a session's checkouts (repos/<KEY>/<name>) name a project by
-- its key alone. Keys were the slug's first four letters, so billing-api and
-- billing-worker were both BILL. Keys compare ignoring case, as every lookup
-- by key does (upper(key_prefix)).
--
-- An existing pair stops the migration, naming the organisation, both
-- projects and the key: set another key_prefix on one of each, then migrate
-- again.

DO $$
DECLARE
  clash text;
BEGIN
  SELECT string_agg(format('organization %L (%s): %s', o.name, c.organization_id, c.names), '; '
           ORDER BY c.organization_id, c.names) INTO clash
  FROM (SELECT organization_id,
          string_agg(format('%L (%s)', name, id), ' and ' ORDER BY name, id) || ' both have the key ' || upper(key_prefix) AS names
        FROM projects GROUP BY organization_id, upper(key_prefix) HAVING count(*) > 1) c
  JOIN organizations o ON o.id = c.organization_id;
  IF clash IS NOT NULL THEN
    RAISE EXCEPTION 'projects would share a key: %; give one of each another key_prefix, then migrate again', clash
      USING ERRCODE = '23505';
  END IF;
END
$$;

CREATE UNIQUE INDEX projects_key_idx ON projects (organization_id, upper(key_prefix));
