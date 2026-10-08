-- 091_repository_lux_name_unique.sql — one checkout name per project.
--
-- A task's Run checks its project's repositories out under lux_name(name)
-- (migration 090), at repos/<lux name>. lux_name keeps a name lux already
-- takes and rewrites any other with a hash suffix, so a rewritten name can
-- equal another repository's unchanged one: "Web" is "web-29751047", and so
-- is a repository named "web-29751047". Both in one Run, lux refuses it as a
-- duplicate; one added later is taken as already checked out. A Run holds
-- one project's repositories, so the name must be unique per project.
--
-- An existing pair stops the migration, naming both: rename one, then
-- migrate again.

DO $$
DECLARE
  clash text;
BEGIN
  SELECT string_agg(format('project %s: %s', project_id, names), '; ' ORDER BY project_id, names) INTO clash
  FROM (SELECT project_id, lux_name(name) AS checkout,
          string_agg(format('%L', name), ' and ' ORDER BY name) || ' are both checked out as ' || lux_name(name) AS names
        FROM repositories GROUP BY project_id, lux_name(name) HAVING count(*) > 1) c;
  IF clash IS NOT NULL THEN
    RAISE EXCEPTION 'repositories would share a checkout: %; rename one of each, then migrate again', clash
      USING ERRCODE = '23505';
  END IF;
END
$$;

CREATE UNIQUE INDEX repositories_lux_name_idx ON repositories (project_id, lux_name(name));
