-- Cloudflare Access sign-in reads the caller's people by verified email on
-- every request. Under people's forced row-level security, citext's `=` is
-- not leakproof, so as dude_app that read filtered every person of the
-- organization instead of using people_email_lookup_idx (058).
--
-- Runs as the migrating owner, which is exempt from row-level security, so
-- the index applies. It is confined explicitly to the caller's tenant, the
-- transaction's app.organization_id (NULL when unset, so nothing matches),
-- and returns active and removed people alike: Access refuses removed ones.
CREATE FUNCTION people_by_email(p_email citext)
RETURNS TABLE (id text, removed boolean, name text, role text, email text, photo_url text, photo_key text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT p.id, p.removed_at IS NOT NULL, p.name, p.role::text, p.email::text, p.photo_url, p.photo_key
  FROM people p
  WHERE p.organization_id = NULLIF(current_setting('app.organization_id', true), '')
    AND p.email = p_email
$$;
REVOKE ALL ON FUNCTION people_by_email(citext) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION people_by_email(citext) TO dude_app;
