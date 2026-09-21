-- 003_api_key_lookup.sql — tenant-blind API key lookup.
--
-- Authentication is a chicken-and-egg problem under RLS: the organization is
-- not known until the key is resolved, but api_keys is tenant-scoped, so a
-- normal read as dude_app returns nothing.
--
-- Rather than exempting api_keys from RLS (which would expose every key to any
-- tenant-scoped query), expose exactly one narrow, SECURITY DEFINER function.
-- It returns a single row matched on the full SHA-256 hash and leaks nothing
-- else: no listing, no prefix search, no enumeration.

CREATE OR REPLACE FUNCTION lookup_api_key(p_key_hash text)
RETURNS TABLE (
  id              text,
  organization_id text,
  name            text,
  kind            text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
-- Pin the search path so the definer's rights cannot be redirected to an
-- attacker-controlled schema.
SET search_path = public, pg_temp
AS $$
  SELECT k.id, k.organization_id, k.name, k.kind
  FROM api_keys k
  WHERE k.key_hash = p_key_hash
    AND k.revoked_at IS NULL
  LIMIT 1
$$;

REVOKE ALL ON FUNCTION lookup_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lookup_api_key(text) TO dude_app;

-- Usage tracking has the same bootstrap problem: the caller is authenticated
-- but no tenant context is set yet. Scoped to one key by id.
CREATE OR REPLACE FUNCTION touch_api_key(p_id text)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
  UPDATE api_keys SET last_used_at = now() WHERE id = p_id
$$;

REVOKE ALL ON FUNCTION touch_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION touch_api_key(text) TO dude_app;
