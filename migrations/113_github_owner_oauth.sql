-- OAuth secrets follow forge_credentials: tenant-scoped, unavailable to the sweeper.
ALTER TABLE forge_credentials
  ADD COLUMN oauth_client_id text,
  ADD COLUMN oauth_client_secret text,
  ADD COLUMN oauth_version bigint NOT NULL DEFAULT 1;

CREATE FUNCTION invalidate_github_oauth() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF (NEW.auth, NEW.app_id, NEW.oauth_client_id, NEW.oauth_client_secret)
     IS DISTINCT FROM (OLD.auth, OLD.app_id, OLD.oauth_client_id, OLD.oauth_client_secret) THEN
    NEW.oauth_version := OLD.oauth_version + 1;
    DELETE FROM github_user_authorizations WHERE credential_id = OLD.id;
    DELETE FROM github_oauth_transactions WHERE credential_id = OLD.id;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TABLE github_user_authorizations (
  organization_id text NOT NULL REFERENCES organizations(id),
  person_id text NOT NULL REFERENCES people(id),
  credential_id text NOT NULL REFERENCES forge_credentials(id) ON DELETE CASCADE,
  credential_version bigint NOT NULL,
  github_user_id bigint NOT NULL,
  github_login text NOT NULL,
  access_token text NOT NULL,
  refresh_token text NOT NULL,
  expires_at timestamptz NOT NULL,
  refresh_expires_at timestamptz NOT NULL,
  refresh_claim text,
  refresh_claim_until timestamptz,
  reconnect_required boolean NOT NULL DEFAULT false,
  PRIMARY KEY (organization_id, person_id, credential_id)
);
CREATE TABLE github_oauth_transactions (
  state_hash text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id),
  person_id text NOT NULL REFERENCES people(id),
  credential_id text NOT NULL REFERENCES forge_credentials(id) ON DELETE CASCADE,
  credential_version bigint NOT NULL,
  verifier text NOT NULL,
  expires_at timestamptz NOT NULL DEFAULT now() + interval '10 minutes'
);
CREATE TABLE pr_publications (
  organization_id text NOT NULL REFERENCES organizations(id),
  task_id text NOT NULL REFERENCES tasks(id),
  branch text NOT NULL,
  owner_person_id text REFERENCES people(id),
  PRIMARY KEY (organization_id, task_id, branch)
);

ALTER TABLE github_user_authorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE github_user_authorizations FORCE ROW LEVEL SECURITY;
CREATE POLICY github_user_authorizations_isolation ON github_user_authorizations
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
ALTER TABLE github_oauth_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE github_oauth_transactions FORCE ROW LEVEL SECURITY;
CREATE POLICY github_oauth_transactions_isolation ON github_oauth_transactions
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
ALTER TABLE pr_publications ENABLE ROW LEVEL SECURITY;
ALTER TABLE pr_publications FORCE ROW LEVEL SECURITY;
CREATE POLICY pr_publications_isolation ON pr_publications
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
GRANT SELECT, INSERT, UPDATE, DELETE ON github_user_authorizations, github_oauth_transactions, pr_publications TO dude_app;
CREATE TRIGGER forge_oauth_replacement BEFORE UPDATE ON forge_credentials
  FOR EACH ROW EXECUTE FUNCTION invalidate_github_oauth();
