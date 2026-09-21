-- 004_workflow_lookup.sql — tenant-blind workflow run lookup.
--
-- Same bootstrap problem as API keys (003): callers such as webhook handlers
-- and signal senders hold a workflow run id but not the organization it
-- belongs to, and workflow_runs is tenant-scoped.
--
-- One narrow SECURITY DEFINER function resolves id -> organization_id and
-- nothing else. It cannot list runs or read their state.

CREATE OR REPLACE FUNCTION workflow_run_organization(p_workflow_run_id text)
RETURNS TABLE (organization_id text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
  SELECT w.organization_id FROM workflow_runs w WHERE w.id = p_workflow_run_id
$$;

REVOKE ALL ON FUNCTION workflow_run_organization(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION workflow_run_organization(text) TO dude_app;
