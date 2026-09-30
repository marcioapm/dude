-- Cloudflare Access matches a verified email to any person with it in the
-- organization, removed ones included (they are refused, not recreated), so
-- the active-only unique index from 035 cannot serve that lookup.
--
-- people has forced row-level security, and the planner uses a clause as an
-- index condition under it only when its operator is leakproof; citext's `=`
-- is not. Queries as dude_app therefore still filter email over the
-- organization's rows; RLS-exempt readers use this index directly.
CREATE INDEX people_email_lookup_idx ON people (organization_id, email) WHERE email IS NOT NULL;
