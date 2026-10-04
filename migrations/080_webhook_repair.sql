-- 080_webhook_repair.sql — webhooks registered in the background, and
-- repaired a few at a time.

-- Where an organization's hooks deliver (public_url and the delivery
-- path), recorded when a person asks dude to register them: what the
-- reconciler's repair registers a missing hook with.
ALTER TABLE forge_credentials ADD COLUMN webhook_url text;

-- The URL each repository's hook was registered to, and when dude last
-- tried. A hook registered to the same URL with no error is not asked
-- about again; one GitHub refused waits before the repair tries again.
ALTER TABLE repositories
  ADD COLUMN webhook_url text,
  ADD COLUMN webhook_attempted_at timestamptz;

-- Hooks registered before this: to public_url, on the control plane's
-- delivery path (/v1/webhooks/github/<organization>).
UPDATE forge_credentials SET webhook_url = rtrim(public_url, '/') || '/v1/webhooks/github/' || organization_id
  WHERE public_url IS NOT NULL;
UPDATE repositories r SET webhook_url = c.webhook_url
  FROM forge_credentials c
  WHERE c.organization_id = r.organization_id AND c.forge = 'github'
    AND r.webhook_registered_at IS NOT NULL AND r.webhook_error IS NULL AND r.webhook_id IS NOT NULL;

-- The repair finds organizations to mend across tenants: where their hooks
-- deliver, never a secret (051).
GRANT SELECT (organization_id, forge, webhook_url) ON forge_credentials TO dude_sweeper;
