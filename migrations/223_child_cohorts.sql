-- Durable cohorts use the child lifecycle/outbox; membership is sealed at commit.
CREATE TABLE child_cohorts (
 id text PRIMARY KEY,
 organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 parent_run_id text NOT NULL REFERENCES runs(id),
 creation_key text NOT NULL,
 directive_id text REFERENCES directives(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (parent_run_id, creation_key)
);
CREATE TABLE child_cohort_members (
 organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 cohort_id text NOT NULL REFERENCES child_cohorts(id) ON DELETE CASCADE,
 slot integer NOT NULL CHECK (slot BETWEEN 0 AND 7),
 child_run_id text NOT NULL UNIQUE REFERENCES runs(id),
 generation integer NOT NULL CHECK (generation > 0),
 PRIMARY KEY (cohort_id, slot)
);
ALTER TABLE child_cohorts ENABLE ROW LEVEL SECURITY;
ALTER TABLE child_cohorts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON child_cohorts
 USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
ALTER TABLE child_cohort_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE child_cohort_members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON child_cohort_members
 USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON child_cohorts, child_cohort_members TO dude_app;
