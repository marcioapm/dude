-- 002_app_role.sql — least-privilege application role.
--
-- RLS is silently bypassed by superusers and by any role with BYPASSRLS, so
-- tenant isolation is only real if the control plane connects as a role that
-- has neither. Migrations keep running as the owner; runtime traffic does not.
--
-- The password is a bootstrap value. Deployments override it by rotating the
-- role's password out of band; it never appears in application config except
-- through DATABASE_URL.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dude_app') THEN
    CREATE ROLE dude_app LOGIN PASSWORD 'dude_app';
  END IF;
END $$;

-- Explicitly ensure the app role can never sidestep row-level security.
-- Only when it holds one of these: since PostgreSQL 16, clearing SUPERUSER or
-- CREATEDB needs a role that has it, so an owner that is not a superuser could
-- not run this unconditionally, even on the role created just above.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dude_app'
             AND (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole)) THEN
    ALTER ROLE dude_app NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO dude_app;

-- DML only: the app never changes schema. DDL stays with the migration role.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dude_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dude_app;

-- Same grants for tables added by later migrations.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dude_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO dude_app;

-- The event ledger is append-only (plan §4.2): revoke the verbs that would
-- let a compromised app path rewrite history. Retention deletes run as owner.
REVOKE UPDATE, DELETE ON events FROM dude_app;

-- schema_migrations is migration-runner state; the app has no business in it.
REVOKE ALL ON schema_migrations FROM dude_app;
