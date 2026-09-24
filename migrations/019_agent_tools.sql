-- Tools for agents: dude's MCP server (docs/design/agent-tools.md).
--
-- Each phase Run gets a bearer token when it is submitted, stored only as a
-- hash, which the agent's MCP client sends. The token names the Run; the
-- Run names the organization, project, work item and role — all a tool call
-- may reach. It stops working when the Run ends.

ALTER TABLE runs ADD COLUMN mcp_token_hash text UNIQUE;

-- Finding a Run by its token happens before the organization is known, so
-- it goes through a function that sees across tenants and returns only the
-- one Run the token names.
CREATE FUNCTION lookup_run_by_mcp_token(p_hash text)
RETURNS TABLE (run_id text, organization_id text, project_id text, work_item_id text, role text, status text)
LANGUAGE sql STABLE SECURITY DEFINER
-- Pinned, so the definer's rights cannot be redirected to another schema.
SET search_path = public, pg_temp AS $$
  SELECT id, organization_id, project_id, work_item_id, role::text, status::text
  FROM runs WHERE mcp_token_hash = p_hash
$$;

REVOKE ALL ON FUNCTION lookup_run_by_mcp_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lookup_run_by_mcp_token(text) TO dude_app;

-- What an agent made is marked as the agent's: shown so, and never started
-- on its own — a person decides.
ALTER TABLE work_items ADD COLUMN created_by_run_id text REFERENCES runs(id) ON DELETE SET NULL;
ALTER TABLE epics ADD COLUMN created_by_run_id text REFERENCES runs(id) ON DELETE SET NULL;
