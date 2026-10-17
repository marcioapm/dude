-- dude:no-transaction
-- A tier's id with its organization, unique: the key model_calls and
-- organizations.judgement_tier_id (141) name a tier by, so neither can name
-- another organisation's. 141 attaches it as a constraint (UNIQUE USING
-- INDEX). Built concurrently, as 139 is; IF NOT EXISTS makes a retry after
-- a crash before it was recorded a no-op.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS model_tiers_id_organization_id_key ON model_tiers (id, organization_id);
