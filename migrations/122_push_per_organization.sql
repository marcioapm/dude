-- A browser is subscribed per organisation: one PushSubscription endpoint
-- may be kept by each organisation its person turned notifications on in.
-- Rows stay as they are; their endpoints are already unique, so the wider
-- key holds for them.
ALTER TABLE push_subscriptions DROP CONSTRAINT push_subscriptions_pkey;
ALTER TABLE push_subscriptions ADD PRIMARY KEY (endpoint, organization_id);

-- Registers (or refreshes the keys of) this browser in the caller's
-- organisation only. It no longer moves a row between organisations, so it
-- runs as the caller, under push_subscriptions' row-level security.
DROP FUNCTION claim_push_subscription(text, text, text, text, text, text);
CREATE FUNCTION claim_push_subscription(p_endpoint text, p_org text, p_person text,
  p_api_key text, p_p256dh text, p_auth text)
RETURNS void LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
BEGIN
  IF p_org IS DISTINCT FROM current_setting('app.organization_id', true)
     OR NOT EXISTS (SELECT 1 FROM people WHERE id = p_person
       AND organization_id = p_org AND removed_at IS NULL) THEN
    RAISE EXCEPTION 'invalid push person' USING ERRCODE = '42501';
  END IF;
  IF p_api_key IS NOT NULL AND NOT EXISTS (SELECT 1 FROM api_keys
      WHERE id = p_api_key AND organization_id = p_org AND person_id = p_person
        AND revoked_at IS NULL) THEN
    RAISE EXCEPTION 'invalid push source key' USING ERRCODE = '42501';
  END IF;
  INSERT INTO push_subscriptions (endpoint, organization_id, person_id, api_key_id, p256dh, auth)
  VALUES (p_endpoint, p_org, p_person, p_api_key, p_p256dh, p_auth)
  ON CONFLICT (endpoint, organization_id) DO UPDATE SET person_id = EXCLUDED.person_id,
    api_key_id = EXCLUDED.api_key_id, p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth;
END $$;
REVOKE ALL ON FUNCTION claim_push_subscription(text, text, text, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_push_subscription(text, text, text, text, text, text) TO dude_app;
