-- GitHub, deeper: what a pull request's page and dude's decisions need to
-- know about it beyond "checks" and "review", and what makes webhooks
-- something a person can set up and trust.

-- ---------------------------------------------------------------------------
-- Pull requests: the state GitHub shows, not only its rollup
-- ---------------------------------------------------------------------------

-- `mergeable_state` is dude's reading of GitHub's mergeable fields and the
-- branch's distance from its base, in the words a person uses: clean,
-- behind (merges cleanly, but main has moved), conflicting, or unknown
-- (GitHub has not worked it out yet — it does so lazily, after a read).
-- `checks_json` is every check by name ({name, status, conclusion, url,
-- durationMs}), `reviews_json` each reviewer's latest verdict ({login,
-- state, submittedAt}), `unresolved_threads` the review threads nobody has
-- resolved: the rollups in `checks` and `review` stay, because the workflow
-- decides on them, and these are what a person reads.
ALTER TABLE pull_requests
  ADD COLUMN mergeable_state text NOT NULL DEFAULT 'unknown'
    CHECK (mergeable_state IN ('clean', 'behind', 'conflicting', 'unknown')),
  ADD COLUMN behind_by integer NOT NULL DEFAULT 0,
  ADD COLUMN checks_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN reviews_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN unresolved_threads integer NOT NULL DEFAULT 0;

-- ---------------------------------------------------------------------------
-- GitHub settings, and webhook health
-- ---------------------------------------------------------------------------

-- How dude behaves on GitHub, for the organization: who may wake a fixer,
-- how pull requests open and merge, what happens when main moves ahead.
-- Stored beside the credential they apply to; absent keys take the
-- defaults the orchestrator applies (forge.DefaultSettings).
ALTER TABLE forge_credentials ADD COLUMN settings jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Rotating the webhook secret must not drop deliveries GitHub already
-- signed with the old one while the hooks are being re-registered: the old
-- secret is kept, and accepted, for a day after the rotation.
ALTER TABLE forge_credentials
  -- Where GitHub reaches this dude: what hooks are registered with, as the
  -- person who registered them reached it.
  ADD COLUMN public_url text,
  ADD COLUMN previous_webhook_secret text,
  ADD COLUMN webhook_rotated_at timestamptz,
  -- Whether webhooks arrive at all is invisible without these: a delivery
  -- that fails its signature never reaches webhook_deliveries.
  ADD COLUMN webhook_last_delivery_at timestamptz,
  ADD COLUMN webhook_last_failure_at timestamptz,
  ADD COLUMN webhook_last_failure text,
  ADD COLUMN webhook_failures_today integer NOT NULL DEFAULT 0,
  ADD COLUMN webhook_failures_day date;

-- When dude registered each repository's hook, or why it could not.
ALTER TABLE repositories
  ADD COLUMN webhook_registered_at timestamptz,
  ADD COLUMN webhook_error text;

-- The secrets a delivery may be signed with: the current one, and the one
-- before it for a day after a rotation. Replaces webhook_secret_for (014)
-- for the same caller — a delivery, before any tenant scope exists.
CREATE FUNCTION webhook_secrets_for(p_organization_id text) RETURNS text[]
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT array_remove(ARRAY[webhook_secret,
    CASE WHEN webhook_rotated_at > now() - interval '1 day' THEN previous_webhook_secret END], NULL)
  FROM forge_credentials
  WHERE organization_id = p_organization_id AND forge = 'github' LIMIT 1
$$;

REVOKE ALL ON FUNCTION webhook_secrets_for(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_secrets_for(text) TO dude_app;

-- A delivery arrived: signed (ok) or not (the reason). Also before a tenant
-- scope exists, and for an unsigned delivery there never is one; so it
-- writes these columns and nothing else.
CREATE FUNCTION note_webhook_delivery(p_organization_id text, p_failure text) RETURNS void
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  UPDATE forge_credentials SET webhook_last_delivery_at = now()
  WHERE organization_id = p_organization_id AND forge = 'github' AND p_failure IS NULL;
  UPDATE forge_credentials SET webhook_last_failure_at = now(), webhook_last_failure = p_failure,
    webhook_failures_today = CASE WHEN webhook_failures_day = current_date THEN webhook_failures_today + 1 ELSE 1 END,
    webhook_failures_day = current_date
  WHERE organization_id = p_organization_id AND forge = 'github' AND p_failure IS NOT NULL;
$$;

REVOKE ALL ON FUNCTION note_webhook_delivery(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION note_webhook_delivery(text, text) TO dude_app;

-- The pull request sync reads the organization's GitHub settings.
GRANT SELECT ON forge_credentials TO dude_sweeper;

-- ---------------------------------------------------------------------------
-- Who may wake a fixer
-- ---------------------------------------------------------------------------

-- What GitHub says a login may do in a repository (the collaborator
-- permission endpoint), or whether it is a member of the repository's
-- organization, cached: every comment on a busy pull request would
-- otherwise ask GitHub again. `kind` says which question was asked;
-- `value` is the permission (admin, maintain, write, triage, read, none) or
-- 'member' / 'none'.
CREATE TABLE forge_permissions (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind            text NOT NULL CHECK (kind IN ('collaborator', 'member')),
  -- owner/repo for a collaborator; the owner for a member.
  scope           text NOT NULL,
  login           text NOT NULL,
  value           text NOT NULL,
  checked_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (organization_id, kind, scope, login)
);

ALTER TABLE forge_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE forge_permissions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON forge_permissions
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON forge_permissions TO dude_app;
