-- 096_agent_network.sql — what an agent's Run may reach, set in dude.
--
-- An organisation lists the hosts (addresses, CIDR ranges, lux wildcards
-- *.example.com, or * for anywhere) every agent Run of it may reach; a
-- project adds its own to them ('add'), or runs on its own alone ('only'),
-- for code that must stay tighter than the rest. The operator's
-- agent.egress (DUDE_AGENT_EGRESS) stays a floor under both, and the model's
-- host and dude's tools are always reachable. The API validates entries as
-- lux takes them, and the orchestrator refuses to start on an operator's
-- entry lux would not take.
ALTER TABLE organizations ADD COLUMN agent_egress text[] NOT NULL DEFAULT '{}';
ALTER TABLE projects ADD COLUMN agent_egress text[] NOT NULL DEFAULT '{}',
  ADD COLUMN agent_egress_mode text NOT NULL DEFAULT 'add' CHECK (agent_egress_mode IN ('add', 'only'));

-- The network a Run was submitted with, as lux's spec says it
-- ({unrestricted} or {egress: [{host} | {cidr}]}): lux cannot change a live
-- Run's rules, so a later change of the lists is not what this Run had.
ALTER TABLE runs ADD COLUMN network jsonb;

-- Names a project's Run's agent looked up and lux refused (its dns event
-- with allowed false), one row per Run and name: what a project's Network
-- page lists as refused recently, and in how many Runs, without scanning
-- events. lux reports each distinct lookup once per Run. A session's Run
-- has no project and no page to list them: its refusals are said on the
-- Run alone.
CREATE TABLE agent_egress_refusals (
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  name            text NOT NULL CHECK (name <> '' AND length(name) <= 253),
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text NOT NULL,
  -- The agent's role, as its settings name it (implementer, fixer, …).
  role            text NOT NULL,
  refused_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, name),
  FOREIGN KEY (project_id, organization_id) REFERENCES projects (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX agent_egress_refusals_project_idx ON agent_egress_refusals (project_id, refused_at);

ALTER TABLE agent_egress_refusals ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_egress_refusals FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_egress_refusals
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT ON agent_egress_refusals TO dude_app;

-- The scripted agent's fake/lookup (its implementer looks names up, which
-- lux allows or refuses by the Run's network) is a test model a tier may
-- request.
ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/command',
                   'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
