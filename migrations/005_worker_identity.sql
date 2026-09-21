-- 005_worker_identity.sql — stable worker identity across restarts.
--
-- A runner that restarts must reclaim its existing worker row rather than
-- create a new one. Otherwise every restart leaks a row, and the node loses
-- the repository-cache affinity that makes Session startup fast (plan §61).
--
-- Workers may be org-scoped or shared (organization_id NULL), so this needs
-- two partial indexes rather than one constraint: NULL is not equal to NULL,
-- which would let duplicate shared workers through.

CREATE UNIQUE INDEX workers_org_name_key
  ON workers (organization_id, name)
  WHERE organization_id IS NOT NULL;

CREATE UNIQUE INDEX workers_shared_name_key
  ON workers (name)
  WHERE organization_id IS NULL;
