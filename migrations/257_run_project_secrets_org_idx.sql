-- dude:no-transaction
-- The secret-copy backstop (phases.ForgetSecretCopies) runs in each
-- organization on every tick and weighs the Runs that have a copy there:
-- since copies are kept for up to 30 days (256), that is every retained
-- copy, which only a scan of the whole table found (129's key leads with
-- run_id). Concurrently, a file of its own, IF NOT EXISTS, as 254.
CREATE INDEX CONCURRENTLY IF NOT EXISTS run_project_secrets_org_idx ON run_project_secrets (organization_id, run_id);
