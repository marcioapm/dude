-- 090_lux_name.sql — a repository's name in a lux spec, as SQL reads it.
--
-- lux takes a spec's repository names only as ^[a-z0-9][a-z0-9_-]{0,31}$.
-- A session's agent checks each linked repository out under the name
-- <project key>-<repository name>, which an uppercase key ("BILL") or a
-- long name breaks. lux_name is the orchestrator's lux.SpecName: a name lux
-- takes stays as it is; any other is lowercased, every other character
-- mapped to '-', leading '-' and '_' dropped, cut to 23 characters and
-- suffixed with '-' and the first 8 hex digits of the SHA-256 of the name
-- as given. The two must agree: queries over runs.lux_repositories match
-- what the spec named (orchestrator/internal/lux/names_test.go runs both).

CREATE FUNCTION lux_name(p_name text) RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
  SELECT CASE
    WHEN p_name ~ '^[a-z0-9][a-z0-9_-]{0,31}$' THEN p_name
    ELSE concat_ws('-',
      NULLIF(left(ltrim(regexp_replace(translate(p_name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'),
        '[^a-z0-9_-]', '-', 'g'), '-_'), 23), ''),
      left(encode(sha256(convert_to(p_name, 'UTF8')), 'hex'), 8))
  END
$$;
