-- Browser ownership is a person; the originating key is optional attribution.
ALTER TABLE push_subscriptions ADD COLUMN person_id text;
UPDATE push_subscriptions s SET person_id = k.person_id
FROM api_keys k WHERE k.id = s.api_key_id AND k.organization_id = s.organization_id;
-- Old anonymous subscriptions cannot be assigned to a verified member.
DELETE FROM push_subscriptions WHERE person_id IS NULL;
ALTER TABLE push_subscriptions ALTER COLUMN person_id SET NOT NULL;

CREATE UNIQUE INDEX people_push_identity_idx ON people (organization_id, id);
CREATE UNIQUE INDEX api_keys_push_identity_idx ON api_keys (organization_id, person_id, id);
ALTER TABLE push_subscriptions DROP CONSTRAINT push_subscriptions_api_key_id_fkey;
ALTER TABLE push_subscriptions ADD CONSTRAINT push_subscriptions_person_fkey
  FOREIGN KEY (organization_id, person_id) REFERENCES people (organization_id, id) ON DELETE CASCADE;
ALTER TABLE push_subscriptions ADD CONSTRAINT push_subscriptions_source_key_fkey
  FOREIGN KEY (organization_id, person_id, api_key_id)
  REFERENCES api_keys (organization_id, person_id, id) ON DELETE SET NULL (api_key_id);
CREATE INDEX push_subscriptions_person_idx ON push_subscriptions (person_id);

DROP FUNCTION claim_push_subscription(text, text, text, text, text);
-- This is the existing endpoint-only cross-tenant transfer, not a tenant reader.
CREATE FUNCTION claim_push_subscription(p_endpoint text, p_org text, p_person text,
  p_api_key text, p_p256dh text, p_auth text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
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
  ON CONFLICT (endpoint) DO UPDATE SET organization_id = EXCLUDED.organization_id,
    person_id = EXCLUDED.person_id, api_key_id = EXCLUDED.api_key_id,
    p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth;
END $$;
REVOKE ALL ON FUNCTION claim_push_subscription(text, text, text, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_push_subscription(text, text, text, text, text, text) TO dude_app;
GRANT SELECT ON people, task_people TO dude_sweeper;
