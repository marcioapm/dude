-- 008_sweeper_role.sql — a role for cross-tenant background work.
--
-- Some work is inherently cross-tenant: the workflow poller claims the oldest
-- runnable workflow across all organizations, the outbox dispatcher sends
-- pending side effects, and the reapers reclaim expired run leases and lost
-- workers. None of them can name a tenant, because their whole job is to find
-- the tenants that need attention.
--
-- `withOrg` cannot express that, and the alternative — enumerating every
-- organization and polling each — turns one indexed query into N and makes
-- fairness impossible.
--
-- So: one role that may bypass row-level security, entered with SET ROLE for
-- the duration of a sweep and reset immediately after. The grant is narrow and
-- visible in the catalogue, which is a much better place for this authority
-- than a comment asking callers to be careful.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dude_sweeper') THEN
    -- NOLOGIN: this role is only ever reached via SET ROLE from dude_app.
    -- It is not a second connection identity and has no password.
    CREATE ROLE dude_sweeper NOLOGIN BYPASSRLS;
  END IF;
END $$;

-- Only the tables a sweeper legitimately touches.
GRANT USAGE ON SCHEMA public TO dude_sweeper;
GRANT SELECT, UPDATE ON workflow_runs TO dude_sweeper;
GRANT SELECT, UPDATE, DELETE ON workflow_signals TO dude_sweeper;
GRANT SELECT, UPDATE ON workflow_outbox TO dude_sweeper;
GRANT SELECT, UPDATE ON runs TO dude_sweeper;
GRANT SELECT, UPDATE ON workers TO dude_sweeper;

-- Sweepers record what they did. Append-only applies to them too: they may
-- INSERT into the ledger, never rewrite it.
GRANT SELECT, INSERT ON events TO dude_sweeper;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dude_sweeper;

-- Deliberately NOT granted: projects, work_items, sessions, artifacts,
-- questions, api_keys, cost_samples. A sweeper that needs tenant data should
-- carry the row's own organization_id into a normal withOrg transaction
-- instead of reading across tenants.

-- Let the application role assume it.
GRANT dude_sweeper TO dude_app;
