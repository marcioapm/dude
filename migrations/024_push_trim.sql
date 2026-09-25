-- Web Push, trimmed (023): what nothing reads goes, and the notifier gets
-- an index of its own.

-- Nothing read when a subscription was last sent to; writing it cost a
-- transaction per push.
ALTER TABLE push_subscriptions DROP COLUMN last_sent_at;
REVOKE UPDATE ON push_subscriptions FROM dude_sweeper;
-- The backend asks the orchestrator for the public key; it never reads it.
REVOKE SELECT (vapid_public) ON push_config FROM dude_app;

-- The notifier reads asks past its cursor every few seconds: straight to
-- them, not through every event since the last one.
CREATE INDEX events_asks_idx ON events (cursor) WHERE event_type IN ('question.asked', 'repository.requested');

-- The asks already sent, above the watermark (push_config.after_cursor).
-- Cursors are handed out at insert and seen at commit, so an ask can
-- appear behind one already read; the notifier looks back over recent
-- asks and skips those here, and moves the watermark only past asks old
-- enough that no transaction can still be committing them.
CREATE TABLE push_sent (cursor bigint PRIMARY KEY);
GRANT SELECT, INSERT, DELETE ON push_sent TO dude_sweeper;

-- A browser subscribes for the organization it is signed in to: one it
-- subscribed for before (another organization, same endpoint) moves over.
-- Across organizations, so as its owner, and only for the endpoint given.
CREATE FUNCTION claim_push_subscription(p_endpoint text, p_org text, p_api_key text, p_p256dh text, p_auth text)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
  INSERT INTO push_subscriptions (endpoint, organization_id, api_key_id, p256dh, auth)
  VALUES (p_endpoint, p_org, p_api_key, p_p256dh, p_auth)
  ON CONFLICT (endpoint) DO UPDATE SET organization_id = EXCLUDED.organization_id,
    api_key_id = EXCLUDED.api_key_id, p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth;
$$;
REVOKE ALL ON FUNCTION claim_push_subscription(text, text, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_push_subscription(text, text, text, text, text) TO dude_app;
