-- A key's lookup says what its person may do, so the request that just
-- authenticated knows whether they administer the organisation without
-- asking again (api/access.ts). Read on every request: a role changed or
-- a person removed holds from their next one.
DROP FUNCTION lookup_api_key(text);
CREATE FUNCTION lookup_api_key(p_key_hash text)
RETURNS TABLE (id text, organization_id text, name text, kind text, person_id text, role text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT k.id, k.organization_id, k.name, k.kind, k.person_id,
         CASE WHEN p.removed_at IS NULL THEN p.role::text END
  FROM api_keys k LEFT JOIN people p ON p.id = k.person_id
  WHERE k.key_hash = p_key_hash AND k.revoked_at IS NULL
  LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lookup_api_key(text) TO dude_app;
