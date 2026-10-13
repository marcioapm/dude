-- Accounts are global identities; people remain stable tenant memberships.
CREATE TABLE accounts (
  id text PRIMARY KEY,
  email citext NOT NULL UNIQUE,
  name text NOT NULL,
  photo_url text CHECK (photo_url IS NULL OR photo_url LIKE 'https://%'),
  photo_key text,
  photo_token text,
  created_at timestamptz NOT NULL DEFAULT now(),
  signed_in_at timestamptz
);
GRANT SELECT, INSERT, UPDATE ON accounts TO dude_app;
ALTER TABLE people ADD COLUMN account_id text REFERENCES accounts(id),
  ADD COLUMN welcomed_at timestamptz;
CREATE UNIQUE INDEX people_account_active_idx ON people (organization_id, account_id)
  WHERE removed_at IS NULL;
CREATE INDEX people_account_idx ON people (account_id);

INSERT INTO accounts (id, email, name, photo_url, photo_key, photo_token, created_at)
SELECT 'acc_' || md5(email::text), email, name, photo_url, photo_key, photo_token, created_at
FROM (
  SELECT DISTINCT ON (lower(btrim(email::text))) lower(btrim(email::text))::citext AS email,
    name, photo_url, photo_key, photo_token, created_at
  FROM people WHERE email IS NOT NULL
  ORDER BY lower(btrim(email::text)), (removed_at IS NULL) DESC,
    CASE WHEN removed_at IS NULL THEN last_seen_at END DESC NULLS LAST, created_at DESC, id
) chosen;
UPDATE people p SET account_id = a.id,
  name = CASE WHEN p.removed_at IS NULL THEN a.name ELSE p.name END,
  email = CASE WHEN p.removed_at IS NULL THEN a.email ELSE p.email END,
  photo_url = CASE WHEN p.removed_at IS NULL THEN a.photo_url ELSE p.photo_url END,
  photo_key = CASE WHEN p.removed_at IS NULL THEN a.photo_key ELSE p.photo_key END,
  photo_token = CASE WHEN p.removed_at IS NULL THEN a.photo_token ELSE p.photo_token END
FROM accounts a
WHERE lower(btrim(p.email::text))::citext = a.email;

CREATE FUNCTION copy_account_profile() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  UPDATE people SET name = NEW.name, email = NEW.email, photo_url = NEW.photo_url,
    photo_key = NEW.photo_key, photo_token = NEW.photo_token
  WHERE account_id = NEW.id AND removed_at IS NULL;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION copy_account_profile() FROM PUBLIC;
CREATE TRIGGER accounts_profile AFTER UPDATE OF name, email, photo_url, photo_key, photo_token
  ON accounts FOR EACH ROW
  WHEN ((OLD.name, OLD.email, OLD.photo_url, OLD.photo_key, OLD.photo_token)
        IS DISTINCT FROM (NEW.name, NEW.email, NEW.photo_url, NEW.photo_key, NEW.photo_token))
  EXECUTE FUNCTION copy_account_profile();

CREATE TABLE invitations (
  id text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  code text NOT NULL UNIQUE,
  email citext NOT NULL,
  name text,
  role text NOT NULL CHECK (role IN ('admin', 'member')),
  invited_by text REFERENCES people(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now() + interval '14 days',
  renewed_at timestamptz,
  renewed_by text REFERENCES people(id),
  accepted_at timestamptz,
  accepted_person_id text REFERENCES people(id),
  revoked_at timestamptz,
  revoked_by text REFERENCES people(id)
);
CREATE UNIQUE INDEX invitations_live_email_idx ON invitations (organization_id, email)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX invitations_email_idx ON invitations (email);
ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
CREATE POLICY invitations_isolation ON invitations
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
GRANT SELECT, INSERT, UPDATE ON invitations TO dude_app;

CREATE FUNCTION account_memberships(p_account text)
RETURNS TABLE (organization_id text, organization_name text, organization_slug text,
  person_id text, name text, role text, welcomed_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT o.id, o.name, o.slug, p.id, p.name, p.role, p.welcomed_at
  FROM people p JOIN organizations o ON o.id = p.organization_id
  WHERE p.account_id = p_account AND p.removed_at IS NULL ORDER BY o.name, o.id
$$;
REVOKE ALL ON FUNCTION account_memberships(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION account_memberships(text) TO dude_app;

CREATE FUNCTION invitation_details(i invitations) RETURNS jsonb
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT jsonb_build_object('id', i.id, 'code', i.code, 'email', i.email, 'name', i.name,
    'organization', jsonb_build_object('id', o.id, 'name', o.name, 'slug', o.slug),
    'role', i.role, 'inviter', COALESCE(p.name, 'the operator'),
    'createdAt', i.created_at, 'expiresAt', i.expires_at)
  FROM organizations o LEFT JOIN people p ON p.id = i.invited_by WHERE o.id = i.organization_id
$$;
REVOKE ALL ON FUNCTION invitation_details(invitations) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invitation_details(invitations) TO dude_app;

CREATE FUNCTION invitations_for_email(p_email citext) RETURNS SETOF jsonb
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT invitation_details(i) FROM invitations i WHERE i.email = p_email
    AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
  ORDER BY i.created_at, i.id
$$;
REVOKE ALL ON FUNCTION invitations_for_email(citext) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invitations_for_email(citext) TO dude_app;

-- Codes reveal only unusable/mismatch until the verified email matches.
CREATE FUNCTION invitation_for_code(p_code text, p_email citext) RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE i invitations; member_id text;
BEGIN
  SELECT * INTO i FROM invitations WHERE code = p_code;
  IF NOT FOUND OR i.revoked_at IS NOT NULL OR (i.accepted_at IS NULL AND i.expires_at <= now()) THEN
    RETURN NULL;
  END IF;
  SELECT p.id INTO member_id FROM people p JOIN accounts a ON a.id = p.account_id
    WHERE a.email = p_email AND p.organization_id = i.organization_id AND p.removed_at IS NULL;
  IF i.accepted_at IS NOT NULL AND (i.email <> p_email OR member_id IS NULL) THEN RETURN NULL; END IF;
  IF i.email <> p_email THEN RETURN jsonb_build_object('state', 'invitation_for_another_email'); END IF;
  RETURN invitation_details(i) || jsonb_build_object('state', CASE WHEN member_id IS NULL THEN 'pending' ELSE 'member' END,
    'personId', member_id);
END $$;
REVOKE ALL ON FUNCTION invitation_for_code(text, citext) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invitation_for_code(text, citext) TO dude_app;
