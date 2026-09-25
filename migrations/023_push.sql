-- Browser notifications (Web Push, docs/design/notifications.md): each
-- browser that opted in, and what has been sent.

-- A browser's push subscription: where its push service takes messages
-- for it, and the keys a message is encrypted to. Per organization, and
-- per API key (the person, until there are users).
CREATE TABLE push_subscriptions (
  endpoint        text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  api_key_id      text REFERENCES api_keys(id) ON DELETE CASCADE,
  p256dh          text NOT NULL,
  auth            text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_sent_at    timestamptz
);
CREATE INDEX push_subscriptions_org_idx ON push_subscriptions (organization_id);

ALTER TABLE push_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE push_subscriptions FORCE ROW LEVEL SECURITY;
CREATE POLICY push_subscriptions_isolation ON push_subscriptions
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
GRANT SELECT, INSERT, UPDATE, DELETE ON push_subscriptions TO dude_app;
-- The notifier sends across organizations.
GRANT SELECT, UPDATE, DELETE ON push_subscriptions TO dude_sweeper;

-- The factory's own: its VAPID key pair (made once when none is
-- configured), and the last event the notifier has looked at. One row.
CREATE TABLE push_config (
  id            boolean PRIMARY KEY DEFAULT true CHECK (id),
  vapid_public  text NOT NULL,
  vapid_private text NOT NULL,
  -- Events up to here have been considered for notifying. Starts at the
  -- present: nothing already asked is announced on the first run.
  after_cursor  bigint NOT NULL DEFAULT 0
);
GRANT SELECT, INSERT, UPDATE ON push_config TO dude_sweeper;
-- The backend hands the public key to browsers.
GRANT SELECT (vapid_public) ON push_config TO dude_app;
-- The notifier names the work item an ask is on (its key).
GRANT SELECT ON work_items TO dude_sweeper;
